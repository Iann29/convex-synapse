package api

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Iann29/synapse/internal/audit"
	"github.com/Iann29/synapse/internal/auth"
	synapsedb "github.com/Iann29/synapse/internal/db"
	"github.com/Iann29/synapse/internal/models"
)

// DomainsHandler exposes per-deployment custom-domain CRUD under
// /v1/deployments/{name}/domains. The proxy/TLS layer (PR #5) reads
// rows where status='active' to make routing + on-demand TLS gating
// decisions; this handler only persists + DNS-verifies.
//
// Auth flow piggy-backs on DeploymentsHandler.loadDeploymentForRequest
// — Deployments resolves /{name} → deployment + project + team + role
// in one round-trip, and we reuse the canEdit gate for writes.
type DomainsHandler struct {
	DB          *pgxpool.Pool
	Deployments *DeploymentsHandler

	// PublicIP is the IPv4 operators publish in their DNS for custom
	// domains. Empty disables DNS preflight — POST + verify still
	// succeed but rows stay status='pending' with last_dns_error set
	// to a "PublicIP not configured" hint so the operator knows why.
	// Set via RouterDeps.PublicIP (env: SYNAPSE_PUBLIC_IP).
	PublicIP string

	// Cache is the proxy's per-host custom-domain resolver. When
	// non-nil, add/delete/status-flip drops the host from the cache
	// so the next request re-reads from the DB. Nil = the operator
	// is running synapse without the proxy (no need to invalidate).
	Cache DomainCacheInvalidator

	// Logger annotates the rebuild-CORS-and-restart log lines. nil
	// falls back to slog.Default().
	Logger *slog.Logger

	// resolver is overridable in tests so the DNS path doesn't reach
	// out to the real internet from the integration suite. nil =
	// use net.DefaultResolver.
	resolver *net.Resolver
}

// MountInDeploymentRoutes registers the /domains sub-routes on a
// chi router that already routes /v1/deployments/{name}. Called from
// DeploymentsHandler.Routes() so the endpoint shows up at
// /v1/deployments/{name}/domains[...].
func (h *DomainsHandler) MountInDeploymentRoutes(r chi.Router) {
	r.Get("/domains", h.listDomains)
	r.Post("/domains", h.createDomain)
	r.Delete("/domains/{domainID}", h.deleteDomain)
	r.Post("/domains/{domainID}/verify", h.verifyDomain)
}

// hostnameRegex is a sane DNS label sanity check — at least one dot
// (so single-label hosts like "localhost" are rejected), each label
// starts/ends with [a-z0-9] and may carry hyphens in the middle.
//
// Intentionally narrow vs RFC1123 (no IDN, no uppercase): we lowercase
// + trim before validating and Citext makes storage case-insensitive,
// so this is "what we accept on the wire". An IDN-aware caller is
// expected to punycode-encode before posting.
var hostnameRegex = regexp.MustCompile(
	`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`,
)

// publicIPNotConfiguredHint is the last_dns_error stored on rows when
// SYNAPSE_PUBLIC_IP is empty. Surfaces actionable guidance to the
// operator without the dashboard needing to know the env var name.
const publicIPNotConfiguredHint = "SYNAPSE_PUBLIC_IP not configured; set it on the Synapse host to enable DNS verification"

// dnsLookupTimeout caps the synchronous DNS preflight inside POST /
// verify. 5s is generous for a working resolver and short enough that
// a stuck create doesn't hold a request slot (router timeout is 30s).
const dnsLookupTimeout = 5 * time.Second

// ---------- helpers ----------

// validateDomain trims, lowercases, and asserts the hostname is
// well-formed. Returns the canonical form on success, or a structured
// error code/message pair the caller hands directly to writeError.
func validateDomain(raw string) (string, string, string) {
	d := strings.ToLower(strings.TrimSpace(raw))
	if d == "" {
		return "", "missing_domain", "domain is required"
	}
	if len(d) > 253 {
		return "", "invalid_domain",
			"domain must be 253 characters or fewer"
	}
	// Reject obvious junk: schemes, ports, paths, whitespace.
	if strings.ContainsAny(d, " \t\n/:?#@") {
		return "", "invalid_domain",
			"domain must be a bare hostname (no scheme/port/path)"
	}
	if !hostnameRegex.MatchString(d) {
		return "", "invalid_domain",
			"domain must be a valid DNS hostname (e.g. api.example.com)"
	}
	return d, "", ""
}

// validateRole checks that role is one of the supported values.
func validateRole(raw string) (string, string, string) {
	switch raw {
	case models.DomainRoleAPI, models.DomainRoleDashboard:
		return raw, "", ""
	default:
		return "", "invalid_role",
			`role must be "api" or "dashboard"`
	}
}

// verifyDomainDNS resolves `domain` and reports whether any returned
// IPv4 matches `expectedIP`. Returns (status, errMsg) — empty errMsg
// only on a clean active match.
//
// `expectedIP` empty short-circuits to ('pending', publicIPNotConfiguredHint)
// so callers don't have to special-case the unconfigured cluster.
func (h *DomainsHandler) verifyDomainDNS(ctx context.Context, domain, expectedIP string) (string, string) {
	if expectedIP == "" {
		return models.DomainStatusPending, publicIPNotConfiguredHint
	}
	lookupCtx, cancel := context.WithTimeout(ctx, dnsLookupTimeout)
	defer cancel()

	resolver := h.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ips, err := resolver.LookupIP(lookupCtx, "ip4", domain)
	if err != nil {
		return models.DomainStatusFailed, "lookup failed: " + err.Error()
	}
	if len(ips) == 0 {
		return models.DomainStatusFailed, "no A records returned"
	}
	got := make([]string, 0, len(ips))
	for _, ip := range ips {
		s := ip.String()
		if s == expectedIP {
			return models.DomainStatusActive, ""
		}
		got = append(got, s)
	}
	return models.DomainStatusFailed,
		"expected " + expectedIP + ", got " + strings.Join(got, ", ")
}

// domainResponse is the shape returned by POST /domains, POST
// /domains/{id}/verify and (implicitly) DELETE — it embeds the
// DeploymentDomain model and adds an optional `deploymentRestartTriggered`
// hint. The hint surfaces when the handler had to recreate the
// deployment's container to refresh CORS_ALLOWED_ORIGINS — a ~15s
// downtime event that the dashboard wants to flag to the operator.
//
// Anonymous-embed so existing clients that key off the legacy field
// shape (`id`, `deploymentId`, etc.) keep working — only the new
// boolean is additive.
type domainResponse struct {
	models.DeploymentDomain
	DeploymentRestartTriggered bool `json:"deploymentRestartTriggered,omitempty"`
}

// scanDomain is the row-shape for SELECTs on deployment_domains. Used
// by both list + verify paths so the column list stays in one place.
const domainSelectCols = `id, deployment_id, domain, role, status,
	dns_verified_at, last_dns_error, created_at, updated_at`

func scanDomainRow(row pgx.Row) (models.DeploymentDomain, error) {
	var d models.DeploymentDomain
	var verifiedAt *time.Time
	var lastErr *string
	if err := row.Scan(
		&d.ID, &d.DeploymentID, &d.Domain, &d.Role, &d.Status,
		&verifiedAt, &lastErr, &d.CreatedAt, &d.UpdatedAt,
	); err != nil {
		return models.DeploymentDomain{}, err
	}
	d.DNSVerifiedAt = verifiedAt
	if lastErr != nil {
		d.LastDNSError = *lastErr
	}
	return d, nil
}

// ---------- GET /v1/deployments/{name}/domains ----------

func (h *DomainsHandler) listDomains(w http.ResponseWriter, r *http.Request) {
	d, _, _, _, ok := h.Deployments.loadDeploymentForRequest(w, r)
	if !ok {
		return
	}

	rows, err := h.DB.Query(r.Context(), `
		SELECT `+domainSelectCols+`
		FROM deployment_domains
		WHERE deployment_id = $1
		ORDER BY created_at ASC
	`, d.ID)
	if err != nil {
		logErr("list domains", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"Failed to list domains")
		return
	}
	defer rows.Close()

	out := make([]models.DeploymentDomain, 0, 4)
	for rows.Next() {
		row, err := scanDomainRow(rows)
		if err != nil {
			logErr("scan domain", err)
			writeError(w, http.StatusInternalServerError, "internal",
				"Failed to read domains")
			return
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": out})
}

// ---------- POST /v1/deployments/{name}/domains ----------

type createDomainReq struct {
	Domain string `json:"domain"`
	Role   string `json:"role"`
}

func (h *DomainsHandler) createDomain(w http.ResponseWriter, r *http.Request) {
	d, _, t, role, ok := h.Deployments.loadDeploymentForRequest(w, r)
	if !ok {
		return
	}
	if !canEditProject(role) {
		writeError(w, http.StatusForbidden, "forbidden",
			"Viewers cannot manage domains; ask a project admin or member")
		return
	}

	var req createDomainReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	domainCanonical, code, msg := validateDomain(req.Domain)
	if code != "" {
		writeError(w, http.StatusBadRequest, code, msg)
		return
	}
	roleVal, code, msg := validateRole(req.Role)
	if code != "" {
		writeError(w, http.StatusBadRequest, code, msg)
		return
	}

	// Insert as 'pending' first so the row exists even if the DNS
	// preflight panics or the request is cancelled mid-flight.
	var id string
	var createdAt, updatedAt time.Time
	err := h.DB.QueryRow(r.Context(), `
		INSERT INTO deployment_domains (deployment_id, domain, role, status)
		VALUES ($1, $2, $3, 'pending')
		RETURNING id, created_at, updated_at
	`, d.ID, domainCanonical, roleVal).Scan(&id, &createdAt, &updatedAt)
	if err != nil {
		if synapsedb.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "domain_already_registered",
				"That domain is already registered (possibly on another deployment)")
			return
		}
		logErr("insert domain", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"Failed to register domain")
		return
	}

	// Synchronous DNS preflight. 5s timeout via verifyDomainDNS so a
	// slow resolver doesn't keep the request open past the router
	// timeout (30s). The status update is best-effort: a transient
	// DB error here leaves the row at 'pending' and the operator can
	// re-run /verify.
	status, errMsg := h.verifyDomainDNS(r.Context(), domainCanonical, h.PublicIP)
	updated, updateErr := h.applyVerification(r.Context(), id, status, errMsg)
	if updateErr != nil {
		logErr("update domain status after dns preflight", updateErr)
		// Fall through to a synthetic row so the caller sees the
		// inserted state. The next /verify will reconcile.
		updated = models.DeploymentDomain{
			ID:           id,
			DeploymentID: d.ID,
			Domain:       domainCanonical,
			Role:         roleVal,
			Status:       models.DomainStatusPending,
			LastDNSError: errMsg,
			CreatedAt:    createdAt,
			UpdatedAt:    updatedAt,
		}
	}

	// Audit. team scope so the dashboard's "team activity" view picks
	// it up alongside other team-scoped events.
	uid, _ := auth.UserID(r.Context())
	teamID := ""
	if t != nil {
		teamID = t.ID
	}
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     teamID,
		ActorID:    uid,
		Action:     audit.ActionAddDomain,
		TargetType: audit.TargetDomain,
		TargetID:   id,
		Metadata: map[string]any{
			"deploymentId":   d.ID,
			"deploymentName": d.Name,
			"domain":         domainCanonical,
			"role":           roleVal,
		},
	})

	// Drop any stale cache entry the proxy might have for this host
	// — landed here as 'pending' but if the operator's previously
	// deleted+re-added the same domain the resolver could have a
	// cached miss + ErrNoReplicas response baked in for the TTL.
	if h.Cache != nil {
		h.Cache.InvalidateDomain(domainCanonical)
	}

	// Restart the deployment's container ONLY when the row landed
	// 'active' — that's the only state where the proxy will route
	// browser traffic at it, so a stale CORS_ALLOWED_ORIGINS becomes
	// a real problem. Pending / failed rows stay invisible to the
	// proxy until /verify flips them.
	restartTriggered := false
	if updated.Status == models.DomainStatusActive {
		restartTriggered = h.Deployments.rebuildCORSAndRestart(
			r.Context(), d.ID, d.Name, h.Logger)
	}

	writeJSON(w, http.StatusCreated, domainResponse{
		DeploymentDomain:           updated,
		DeploymentRestartTriggered: restartTriggered,
	})
}

// applyVerification updates the row's status + verifiedAt + lastErr
// columns and returns the resulting DeploymentDomain. Used by both
// the create-handler post-insert sync and the explicit /verify
// endpoint so the update SQL stays in one place.
func (h *DomainsHandler) applyVerification(ctx context.Context, id, status, errMsg string) (models.DeploymentDomain, error) {
	// dns_verified_at is set on success and cleared on non-success so
	// the column always reflects "the time of the most recent
	// successful match" (NULL = never matched). last_dns_error mirrors
	// the same shape on the failure side.
	var verifiedAt any
	if status == models.DomainStatusActive {
		verifiedAt = time.Now().UTC()
	} else {
		verifiedAt = nil
	}
	var lastErr any
	if errMsg != "" {
		lastErr = errMsg
	} else {
		lastErr = nil
	}
	row := h.DB.QueryRow(ctx, `
		UPDATE deployment_domains
		SET status = $2,
		    dns_verified_at = $3,
		    last_dns_error = $4,
		    updated_at = now()
		WHERE id = $1
		RETURNING `+domainSelectCols, id, status, verifiedAt, lastErr)
	return scanDomainRow(row)
}

// ---------- DELETE /v1/deployments/{name}/domains/{domainID} ----------

func (h *DomainsHandler) deleteDomain(w http.ResponseWriter, r *http.Request) {
	d, _, t, role, ok := h.Deployments.loadDeploymentForRequest(w, r)
	if !ok {
		return
	}
	if !canEditProject(role) {
		writeError(w, http.StatusForbidden, "forbidden",
			"Viewers cannot manage domains; ask a project admin or member")
		return
	}
	domainID := chi.URLParam(r, "domainID")
	if domainID == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "domain id is required")
		return
	}

	// Single round-trip DELETE … RETURNING tells us 404 vs success
	// AND gives us the values we need for the audit row in one shot.
	// The deployment_id guard rejects cross-deployment deletes (the
	// loadDeploymentForRequest check is defense-in-depth on top).
	// We also pull `status` so we know whether to recreate the
	// container — only 'active' rows were live in the proxy, so
	// removing a 'pending' / 'failed' row needs no restart.
	var (
		domain string
		status string
	)
	err := h.DB.QueryRow(r.Context(), `
		DELETE FROM deployment_domains
		WHERE id = $1 AND deployment_id = $2
		RETURNING domain, status
	`, domainID, d.ID).Scan(&domain, &status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "domain_not_found",
				"Domain not found or belongs to a different deployment")
			return
		}
		logErr("delete domain", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"Failed to delete domain")
		return
	}

	uid, _ := auth.UserID(r.Context())
	teamID := ""
	if t != nil {
		teamID = t.ID
	}
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     teamID,
		ActorID:    uid,
		Action:     audit.ActionRemoveDomain,
		TargetType: audit.TargetDomain,
		TargetID:   domainID,
		Metadata: map[string]any{
			"deploymentId":   d.ID,
			"deploymentName": d.Name,
			"domain":         domain,
		},
	})

	// Drop the proxy cache entry so the next request immediately
	// 404s instead of routing to a deployment that no longer answers
	// for this domain. Safe to call regardless of prior status — the
	// cache only ever holds 'active' rows.
	if h.Cache != nil {
		h.Cache.InvalidateDomain(domain)
	}

	// Restart the container if the deleted row was 'active' — that's
	// the only state where the deployment's CORS list referenced this
	// domain. Pending / failed rows never made it into the live env.
	if status == models.DomainStatusActive {
		_ = h.Deployments.rebuildCORSAndRestart(
			r.Context(), d.ID, d.Name, h.Logger)
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------- POST /v1/deployments/{name}/domains/{domainID}/verify ----------

func (h *DomainsHandler) verifyDomain(w http.ResponseWriter, r *http.Request) {
	d, _, t, role, ok := h.Deployments.loadDeploymentForRequest(w, r)
	if !ok {
		return
	}
	if !canEditProject(role) {
		writeError(w, http.StatusForbidden, "forbidden",
			"Viewers cannot verify domains; ask a project admin or member")
		return
	}
	domainID := chi.URLParam(r, "domainID")
	if domainID == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "domain id is required")
		return
	}

	var (
		domainName  string
		priorStatus string
	)
	err := h.DB.QueryRow(r.Context(), `
		SELECT domain, status FROM deployment_domains
		WHERE id = $1 AND deployment_id = $2
	`, domainID, d.ID).Scan(&domainName, &priorStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "domain_not_found",
				"Domain not found or belongs to a different deployment")
			return
		}
		logErr("load domain for verify", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"Failed to load domain")
		return
	}

	status, errMsg := h.verifyDomainDNS(r.Context(), domainName, h.PublicIP)
	updated, updateErr := h.applyVerification(r.Context(), domainID, status, errMsg)
	if updateErr != nil {
		logErr("update domain status during verify", updateErr)
		writeError(w, http.StatusInternalServerError, "internal",
			"Failed to update domain status")
		return
	}

	uid, _ := auth.UserID(r.Context())
	teamID := ""
	if t != nil {
		teamID = t.ID
	}
	meta := map[string]any{
		"deploymentId":   d.ID,
		"deploymentName": d.Name,
		"domain":         domainName,
		"status":         status,
	}
	if errMsg != "" {
		meta["error"] = errMsg
	}
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     teamID,
		ActorID:    uid,
		Action:     audit.ActionVerifyDomain,
		TargetType: audit.TargetDomain,
		TargetID:   domainID,
		Metadata:   meta,
	})

	// Always invalidate the proxy cache — verify can flip in either
	// direction (active→failed or pending→active) and cached entries
	// for either side are now wrong.
	if h.Cache != nil {
		h.Cache.InvalidateDomain(domainName)
	}

	// Restart only on a pending/failed → active transition: that's
	// when the deployment's CORS list newly needs to acknowledge the
	// domain. active → failed should also drop the host from the
	// allow-list, but a stale "https://gone.example.com" in CORS is
	// harmless (browsers still won't load it without TLS), so we
	// skip the restart on the down-flip to keep the operator's fault
	// blast radius small. Operator can DELETE the row to force a
	// rebuild if they really want it cleared.
	restartTriggered := false
	if priorStatus != models.DomainStatusActive && updated.Status == models.DomainStatusActive {
		restartTriggered = h.Deployments.rebuildCORSAndRestart(
			r.Context(), d.ID, d.Name, h.Logger)
	}

	writeJSON(w, http.StatusOK, domainResponse{
		DeploymentDomain:           updated,
		DeploymentRestartTriggered: restartTriggered,
	})
}
