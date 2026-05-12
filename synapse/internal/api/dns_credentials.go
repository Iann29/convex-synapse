package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Iann29/synapse/internal/audit"
	"github.com/Iann29/synapse/internal/auth"
	synapsedns "github.com/Iann29/synapse/internal/dns"
	"github.com/Iann29/synapse/internal/models"
)

// DNSCredentialsHandler exposes the instance-level DNS-provider
// credential CRUD under /v1/admin/dns_credentials. The credentials
// stored here let other handlers (notably the per-deployment domain
// auto-configure flow) talk to a DNS provider on the operator's
// behalf.
//
// Auth gate: requireInstanceAdmin — same as the rest of /v1/admin.
// Plaintext tokens never leave the server: GET/list returns metadata
// only; the encrypted column is only decrypted inside the auto-
// configure flow which holds the row briefly to mint the A record.
type DNSCredentialsHandler struct {
	DB     *pgxpool.Pool
	Crypto SecretEnvelope

	// CloudflareFactory returns a synapsedns.CloudflareClient for the
	// given token. Test seam: production wiring leaves this nil and we
	// build a real client; tests inject a closure that points at an
	// httptest.Server pretending to be the Cloudflare API.
	CloudflareFactory func(token string) *synapsedns.CloudflareClient
}

// SecretEnvelope is the subset of *crypto.SecretBox that we use here:
// both encrypt + decrypt, since the auto-configure flow needs the
// plaintext token to talk to the provider. Distinct from
// SecretEncrypter (deployments.go) which only needs the encrypt half.
type SecretEnvelope interface {
	EncryptString(s string) ([]byte, error)
	DecryptString(ciphertext []byte) (string, error)
}

// cloudflareClient builds (or reuses, via injected factory) a
// CloudflareClient for the given plaintext token.
func (h *DNSCredentialsHandler) cloudflareClient(token string) *synapsedns.CloudflareClient {
	if h.CloudflareFactory != nil {
		return h.CloudflareFactory(token)
	}
	return &synapsedns.CloudflareClient{Token: token}
}

// Routes mounts the credential endpoints. Called from router.go
// behind the same requireInstanceAdmin gate as the rest of /v1/admin.
// We mount as siblings of /version_check rather than at the root so
// the dashboard's "host-domain admin" page and "DNS credentials" page
// can share a top-level layout that loads /v1/admin/* atomically.
func (h *DNSCredentialsHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.list)
	r.Post("/cloudflare", h.createCloudflare)
	r.Delete("/{id}", h.delete)
	return r
}

// listResp is the shape returned by GET /dns_credentials. The
// individual rows match models.DNSCredential — token never leaves the
// server; only metadata flows out.
type listDNSCredentialsResp struct {
	Credentials []models.DNSCredential `json:"credentials"`
}

// dnsCredentialSelectCols keeps the SELECT column list for
// scanDNSCredentialRow in one place. project_id was added in
// migration 000016; the column is NULL for instance-wide credentials
// (the v1.5 default) and non-NULL for project-scoped rows added via
// /v1/projects/{id}/dns_credentials.
const dnsCredentialSelectCols = `id, provider, label, project_id, zones, created_by, created_at, last_used_at, last_error`

// /v1/admin/dns_credentials returns only instance-wide credentials
// (project_id IS NULL). Project-scoped rows are exposed via
// /v1/projects/{id}/dns_credentials so each project admin sees only
// their own keys.
func (h *DNSCredentialsHandler) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(r.Context(), `
		SELECT `+dnsCredentialSelectCols+`
		FROM dns_credentials
		WHERE project_id IS NULL
		ORDER BY created_at DESC
	`)
	if err != nil {
		logErr("list dns credentials", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"Failed to list DNS credentials")
		return
	}
	defer rows.Close()

	out := make([]models.DNSCredential, 0, 4)
	for rows.Next() {
		c, err := scanDNSCredentialRow(rows)
		if err != nil {
			logErr("scan dns credential", err)
			writeError(w, http.StatusInternalServerError, "internal",
				"Failed to read DNS credentials")
			return
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, listDNSCredentialsResp{Credentials: out})
}

// scanDNSCredentialRow centralises the row scan so the column list
// stays in one place. zones is decoded from JSONB; project_id is
// nullable (NULL for instance-wide credentials).
func scanDNSCredentialRow(row pgx.Row) (models.DNSCredential, error) {
	var c models.DNSCredential
	var projectID *string
	var createdBy *string
	var lastUsedAt *time.Time
	var lastError *string
	var zonesRaw []byte
	if err := row.Scan(
		&c.ID, &c.Provider, &c.Label, &projectID, &zonesRaw,
		&createdBy, &c.CreatedAt, &lastUsedAt, &lastError,
	); err != nil {
		return models.DNSCredential{}, err
	}
	c.ProjectID = projectID
	c.CreatedBy = createdBy
	c.LastUsedAt = lastUsedAt
	if lastError != nil {
		c.LastError = *lastError
	}
	if len(zonesRaw) > 0 {
		if err := json.Unmarshal(zonesRaw, &c.Zones); err != nil {
			// Defensive: if the column drifted, default to empty
			// rather than 500ing the whole list.
			c.Zones = []models.ZoneInfo{}
		}
	} else {
		c.Zones = []models.ZoneInfo{}
	}
	return c, nil
}

type createCloudflareCredentialReq struct {
	Token string `json:"token"`
	Label string `json:"label"`
}

// dnsCredentialScope captures the scope + audit metadata a single
// credential operation needs. Instance-wide ops pass the zero value
// (all-nil + ActionAdd/RemoveDNSCredential); project-scoped ops fill
// ProjectID + TeamID so the audit row carries proper provenance and
// the team activity feed picks the event up via WHERE team_id = $X.
//
// Pre-v1.6.5 the helpers took ProjectID + Action as positional args
// and never propagated TeamID — leaving project DNS credential events
// invisible in the team audit feed. Surfaced by the v1.6.4 audit pass.
type dnsCredentialScope struct {
	ProjectID *string
	TeamID    *string
	Action    string
}

// createCloudflare is the /v1/admin/dns_credentials/cloudflare path:
// creates an instance-wide credential (project_id NULL). The mirror
// path for project-scoped credentials lives on ProjectsHandler and
// reuses createCloudflareScoped.
func (h *DNSCredentialsHandler) createCloudflare(w http.ResponseWriter, r *http.Request) {
	h.createCloudflareScoped(w, r, dnsCredentialScope{Action: audit.ActionAddDNSCredential})
}

// createCloudflareScoped is the shared verify-token → list-zones →
// encrypt → insert path used by both the instance-wide endpoint
// (scope.ProjectID = nil) and the per-project endpoint
// (scope.ProjectID = the project UUID, scope.TeamID = the project's
// team). Audit action is overridable so the per-project flow can
// emit a distinct ActionAddProjectDNSCredential row, keeping the
// team audit feed honest about which scope changed.
func (h *DNSCredentialsHandler) createCloudflareScoped(w http.ResponseWriter, r *http.Request, scope dnsCredentialScope) {
	projectID := scope.ProjectID
	action := scope.Action
	// Trace each entry so a fresh-install 500 in the field gives the
	// operator a clear server-side breadcrumb to share without needing
	// to attach a debugger.
	slog.Default().Info("dns_credentials: createCloudflare entry",
		"crypto_configured", h.Crypto != nil,
		"factory_configured", h.CloudflareFactory != nil,
		"project_scoped", projectID != nil)

	if h.Crypto == nil {
		// Without a SecretBox we can't safely persist the token —
		// plaintext-at-rest is not an acceptable fallback. Surface
		// the missing config so the operator knows to set
		// SYNAPSE_STORAGE_KEY.
		writeError(w, http.StatusServiceUnavailable, "crypto_not_configured",
			"DNS credentials require SYNAPSE_STORAGE_KEY to be set on the Synapse host")
		return
	}

	var req createCloudflareCredentialReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	token := strings.TrimSpace(req.Token)
	label := strings.TrimSpace(req.Label)
	if token == "" {
		writeError(w, http.StatusBadRequest, "missing_token", "token is required")
		return
	}
	if label == "" {
		writeError(w, http.StatusBadRequest, "missing_label", "label is required")
		return
	}

	client := h.cloudflareClient(token)
	if err := client.VerifyToken(r.Context()); err != nil {
		// Distinguish "Cloudflare said the token is bad" from
		// "Cloudflare unreachable / decode failed". The former is a
		// 400; the latter degrades to 502 so the dashboard can show
		// "try again" instead of "fix your token".
		if errors.Is(err, synapsedns.ErrUnauthorized) {
			writeError(w, http.StatusBadRequest, "invalid_token",
				"Cloudflare rejected this token (revoked or wrong scopes)")
			return
		}
		writeError(w, http.StatusBadGateway, "cloudflare_api_error",
			"Could not verify token with Cloudflare: "+err.Error())
		return
	}

	zones, err := client.ListZones(r.Context())
	if err != nil {
		if errors.Is(err, synapsedns.ErrUnauthorized) {
			writeError(w, http.StatusBadRequest, "invalid_token",
				"Cloudflare rejected this token when listing zones")
			return
		}
		writeError(w, http.StatusBadGateway, "cloudflare_api_error",
			"Could not list zones: "+err.Error())
		return
	}
	zonesJSON, err := json.Marshal(zones)
	if err != nil {
		logErr("marshal zones", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"Failed to encode zones")
		return
	}

	encrypted, err := h.Crypto.EncryptString(token)
	if err != nil {
		logErr("encrypt cloudflare token", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"Failed to encrypt token")
		return
	}

	uid, _ := auth.UserID(r.Context())
	var creator any
	if uid != "" {
		creator = uid
	}

	// projectID is *string; pgx converts a nil pointer to SQL NULL.
	// Pass the pointer directly rather than dereferencing to preserve
	// that distinction.
	var (
		id        string
		createdAt time.Time
	)
	err = h.DB.QueryRow(r.Context(), `
		INSERT INTO dns_credentials (provider, label, project_id, token_encrypted, zones, created_by)
		VALUES ('cloudflare', $1, $2, $3, $4, $5)
		RETURNING id, created_at
	`, label, projectID, encrypted, zonesJSON, creator).Scan(&id, &createdAt)
	if err != nil {
		logErr("insert dns credential", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"Failed to save DNS credential")
		return
	}

	auditMeta := map[string]any{
		"provider":  "cloudflare",
		"label":     label,
		"zoneCount": len(zones),
	}
	if projectID != nil {
		auditMeta["projectId"] = *projectID
	}
	auditOpts := audit.Options{
		ActorID:    uid,
		Action:     action,
		TargetType: audit.TargetDNSCredential,
		TargetID:   id,
		Metadata:   auditMeta,
	}
	// TeamID stamps the row so it surfaces in the team activity feed
	// (which filters WHERE team_id = $X). Instance-wide credentials
	// stay TeamID="" — they don't belong to any team.
	if scope.TeamID != nil {
		auditOpts.TeamID = *scope.TeamID
	}
	_ = audit.Record(r.Context(), h.DB, auditOpts)

	out := models.DNSCredential{
		ID:        id,
		Provider:  "cloudflare",
		Label:     label,
		ProjectID: projectID,
		Zones:     zones,
		CreatedAt: createdAt,
	}
	if uid != "" {
		out.CreatedBy = &uid
	}
	writeJSON(w, http.StatusCreated, out)
}

// delete is the /v1/admin/dns_credentials/{id} path: removes an
// instance-wide credential. Project-scoped credentials are deleted
// via DeleteScoped called from ProjectsHandler so the route-level
// project_id check happens there.
func (h *DNSCredentialsHandler) delete(w http.ResponseWriter, r *http.Request) {
	h.DeleteScoped(w, r, dnsCredentialScope{Action: audit.ActionRemoveDNSCredential})
}

// DeleteScoped runs the in-use check + DELETE flow with optional
// scope constraint. When scope.ProjectID is non-nil the DELETE only
// matches rows owned by that project — otherwise it'd be possible
// for a project admin to remove a credential from another project
// (or instance-wide) by guessing its UUID.
//
// Order of checks matters for honesty: pre-v1.6.5 the in-use check
// (409) ran before the scope guard, so a project-A admin guessing a
// project-B credential UUID could distinguish "exists and is in
// use" (409) from "doesn't exist or isn't in use" (404). UUIDs are
// unguessable in practice (low risk), but we run the scope-bounded
// SELECT first now to remove the leak: the in-use 409 only fires
// for rows the caller is actually allowed to see.
func (h *DNSCredentialsHandler) DeleteScoped(w http.ResponseWriter, r *http.Request, scope dnsCredentialScope) {
	projectID := scope.ProjectID
	action := scope.Action

	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "credential id is required")
		return
	}

	// Scope-bounded existence check FIRST. If the row doesn't exist
	// in this scope, return 404 immediately — without leaking via
	// the 409 in-use check whether some other scope holds it. The
	// IS NOT DISTINCT FROM trick lets nil project_id match SQL NULL.
	var (
		provider string
		label    string
	)
	err := h.DB.QueryRow(r.Context(), `
		SELECT provider, label
		  FROM dns_credentials
		 WHERE id = $1
		   AND project_id IS NOT DISTINCT FROM $2
	`, id, projectID).Scan(&provider, &label)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "credential_not_found",
				"DNS credential not found")
			return
		}
		logErr("load credential for delete", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"Failed to load DNS credential")
		return
	}

	// 409 if any deployment_domains row still references this
	// credential. The row sticks around for the operator to inspect —
	// accidentally orphaning the column would be safe (ON DELETE SET
	// NULL) but it'd silently break the "auto_configured" badge on
	// every domain that used the token.
	var inUse bool
	if err := h.DB.QueryRow(r.Context(), `
		SELECT EXISTS (
			SELECT 1 FROM deployment_domains WHERE dns_credential_id = $1
		)`, id).Scan(&inUse); err != nil {
		logErr("check credential in use", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"Failed to check credential usage")
		return
	}
	if inUse {
		writeError(w, http.StatusConflict, "credential_in_use",
			"This credential is still referenced by one or more deployment domains; remove the auto-configuration on those domains first")
		return
	}

	// Now perform the actual DELETE. Scope is re-applied here as
	// belt-and-suspenders: between the SELECT above and this DELETE
	// the row could (in principle) be moved across scopes by another
	// concurrent operation; the scope-guarded WHERE makes that race
	// safe — worst case the DELETE no-ops with pgx.ErrNoRows and we
	// surface 404, never delete the wrong row.
	if _, err := h.DB.Exec(r.Context(), `
		DELETE FROM dns_credentials
		WHERE id = $1
		  AND project_id IS NOT DISTINCT FROM $2
	`, id, projectID); err != nil {
		logErr("delete dns credential", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"Failed to delete DNS credential")
		return
	}

	uid, _ := auth.UserID(r.Context())
	auditMeta := map[string]any{
		"provider": provider,
		"label":    label,
	}
	if projectID != nil {
		auditMeta["projectId"] = *projectID
	}
	auditOpts := audit.Options{
		ActorID:    uid,
		Action:     action,
		TargetType: audit.TargetDNSCredential,
		TargetID:   id,
		Metadata:   auditMeta,
	}
	if scope.TeamID != nil {
		auditOpts.TeamID = *scope.TeamID
	}
	_ = audit.Record(r.Context(), h.DB, auditOpts)

	w.WriteHeader(http.StatusNoContent)
}

// ListForProject is the project-scoped variant of list. Mounted on
// ProjectsHandler under /v1/projects/{id}/dns_credentials.
func (h *DNSCredentialsHandler) ListForProject(w http.ResponseWriter, r *http.Request, projectID string) {
	rows, err := h.DB.Query(r.Context(), `
		SELECT `+dnsCredentialSelectCols+`
		FROM dns_credentials
		WHERE project_id = $1
		ORDER BY created_at DESC
	`, projectID)
	if err != nil {
		logErr("list project dns credentials", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"Failed to list DNS credentials")
		return
	}
	defer rows.Close()

	out := make([]models.DNSCredential, 0, 4)
	for rows.Next() {
		c, err := scanDNSCredentialRow(rows)
		if err != nil {
			logErr("scan project dns credential", err)
			writeError(w, http.StatusInternalServerError, "internal",
				"Failed to read DNS credentials")
			return
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, listDNSCredentialsResp{Credentials: out})
}

// CreateCloudflareForProject is the project-scoped variant of
// createCloudflare. Mounted on ProjectsHandler. teamID is the
// project's team — stamped on the audit row so the team activity
// feed picks the event up via WHERE team_id = $X.
func (h *DNSCredentialsHandler) CreateCloudflareForProject(w http.ResponseWriter, r *http.Request, projectID, teamID string) {
	h.createCloudflareScoped(w, r, dnsCredentialScope{
		ProjectID: &projectID,
		TeamID:    &teamID,
		Action:    audit.ActionAddProjectDNSCredential,
	})
}
