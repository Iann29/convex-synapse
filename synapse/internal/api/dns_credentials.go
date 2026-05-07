package api

import (
	"encoding/json"
	"errors"
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

func (h *DNSCredentialsHandler) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(r.Context(), `
		SELECT id, provider, label, zones, created_by, created_at, last_used_at, last_error
		FROM dns_credentials
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
// stays in one place. zones is decoded from JSONB.
func scanDNSCredentialRow(row pgx.Row) (models.DNSCredential, error) {
	var c models.DNSCredential
	var createdBy *string
	var lastUsedAt *time.Time
	var lastError *string
	var zonesRaw []byte
	if err := row.Scan(
		&c.ID, &c.Provider, &c.Label, &zonesRaw,
		&createdBy, &c.CreatedAt, &lastUsedAt, &lastError,
	); err != nil {
		return models.DNSCredential{}, err
	}
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

func (h *DNSCredentialsHandler) createCloudflare(w http.ResponseWriter, r *http.Request) {
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

	var (
		id        string
		createdAt time.Time
	)
	err = h.DB.QueryRow(r.Context(), `
		INSERT INTO dns_credentials (provider, label, token_encrypted, zones, created_by)
		VALUES ('cloudflare', $1, $2, $3, $4)
		RETURNING id, created_at
	`, label, encrypted, zonesJSON, creator).Scan(&id, &createdAt)
	if err != nil {
		logErr("insert dns credential", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"Failed to save DNS credential")
		return
	}

	_ = audit.Record(r.Context(), h.DB, audit.Options{
		ActorID:    uid,
		Action:     audit.ActionAddDNSCredential,
		TargetType: audit.TargetDNSCredential,
		TargetID:   id,
		Metadata: map[string]any{
			"provider":  "cloudflare",
			"label":     label,
			"zoneCount": len(zones),
		},
	})

	out := models.DNSCredential{
		ID:        id,
		Provider:  "cloudflare",
		Label:     label,
		Zones:     zones,
		CreatedAt: createdAt,
	}
	if uid != "" {
		out.CreatedBy = &uid
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *DNSCredentialsHandler) delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "credential id is required")
		return
	}

	// 409 if any deployment_domains row still references this
	// credential. We check before the DELETE so the row sticks around
	// for the operator to inspect — accidentally orphaning the column
	// would be safe (ON DELETE SET NULL) but it'd silently break the
	// "auto_configured" badge on every domain that used the token.
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

	var (
		provider string
		label    string
	)
	err := h.DB.QueryRow(r.Context(), `
		DELETE FROM dns_credentials
		WHERE id = $1
		RETURNING provider, label
	`, id).Scan(&provider, &label)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "credential_not_found",
				"DNS credential not found")
			return
		}
		logErr("delete dns credential", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"Failed to delete DNS credential")
		return
	}

	uid, _ := auth.UserID(r.Context())
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		ActorID:    uid,
		Action:     audit.ActionRemoveDNSCredential,
		TargetType: audit.TargetDNSCredential,
		TargetID:   id,
		Metadata: map[string]any{
			"provider": provider,
			"label":    label,
		},
	})

	w.WriteHeader(http.StatusNoContent)
}
