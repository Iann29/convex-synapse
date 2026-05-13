package api

import (
	"context"
	"database/sql"
	"encoding/json"
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
	synapsedns "github.com/Iann29/synapse/internal/dns"
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

	// Crypto decrypts dns_credentials.token_encrypted at the moment
	// auto_configure runs. Optional: nil disables /auto_configure
	// (returns 503 dns_auto_configure_unavailable) so an operator
	// running without SYNAPSE_STORAGE_KEY just sees the manual flow.
	Crypto SecretEnvelope

	// CloudflareFactory mirrors DNSCredentialsHandler.CloudflareFactory
	// — same factory, both handlers, so test wiring sets it once on the
	// router deps and both surfaces hit the stubbed Cloudflare API.
	CloudflareFactory func(token string) *synapsedns.CloudflareClient

	// Resolver is overridable in tests so the DNS path doesn't reach
	// out to the real internet from the integration suite. nil =
	// use synapsedns.ExternalResolver (1.1.1.1, 8.8.8.8 fallback)
	// instead of net.DefaultResolver — the OS resolver inside our
	// distroless container can't be relied on (see resolver.go).
	//
	// Interface-typed (not *net.Resolver) so tests can pass a tiny
	// in-memory stub instead of stubbing a full *net.Resolver dial
	// path. Production wiring leaves this nil; router_test +
	// deployment_domains_test inject a fake when they need to
	// exercise propagation / mismatch scenarios deterministically.
	Resolver LookupIPResolver
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
	// Auto-configure (v1.5+, migration 000015): mint the A record on
	// the operator's behalf using a stored DNS-provider credential.
	// Same project-admin gate as add/delete/verify; behaviour gates
	// on PublicIP being set (we need an IP to point the record at).
	r.Post("/domains/{domainID}/auto_configure", h.autoConfigureDomain)
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
// IPv4 matches `expectedIP`. Returns (status, errMsg).
//
// Status mapping:
//
//   - active: a returned IPv4 exactly matches expectedIP. errMsg "".
//   - pending: the lookup couldn't see the record yet (NXDOMAIN /
//     no-such-host / SERVFAIL / timeout / "no A records returned").
//     This is a TRANSIENT failure — DNS propagation hasn't reached the
//     resolver, OR the operator just minted the record and the world
//     hasn't caught up. The dns.Verifier loop keeps retrying every
//     15s; we mirror its semantics in the sync path so an impatient
//     "Verify" click during the propagation window doesn't flip the
//     row to a permanent-looking failed state. errMsg carries the
//     lookup error so the dashboard can show "still propagating".
//   - failed: the resolver returned IPv4s but NONE matched expectedIP.
//     This is DETERMINISTIC — propagation completed and is pointing
//     somewhere else (proxied through Cloudflare orange-cloud,
//     pointing at a different host, etc.). The operator must act.
//     errMsg has the "expected X, got Y" hint.
//
// `expectedIP` empty short-circuits to ('pending', publicIPNotConfiguredHint)
// so callers don't have to special-case the unconfigured cluster.
//
// The resolver is ExternalResolver (1.1.1.1 with 8.8.8.8 fallback)
// when the test seam isn't set — see internal/dns/resolver.go for
// why we don't trust net.DefaultResolver inside the distroless
// container.
func (h *DomainsHandler) verifyDomainDNS(ctx context.Context, domain, expectedIP string) (string, string) {
	if expectedIP == "" {
		return models.DomainStatusPending, publicIPNotConfiguredHint
	}
	lookupCtx, cancel := context.WithTimeout(ctx, dnsLookupTimeout)
	defer cancel()

	resolver := h.dnsResolver()
	ips, err := resolver.LookupIP(lookupCtx, "ip4", domain)
	if err != nil {
		// Lookup error → still propagating. Verifier loop retries
		// until MaxAge ages the row out (5min after the last write).
		return models.DomainStatusPending, "still propagating: " + err.Error()
	}
	if len(ips) == 0 {
		// Resolver returned cleanly with zero records. Same shape as
		// NXDOMAIN semantically — record isn't visible yet.
		return models.DomainStatusPending, "still propagating: no A records returned"
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

// dnsResolver returns the test-injected resolver if set, otherwise
// the production ExternalResolver. Centralised so the two call sites
// (verifyDomainDNS + any future preflight) can't drift on the
// default.
func (h *DomainsHandler) dnsResolver() LookupIPResolver {
	if h.Resolver != nil {
		return h.Resolver
	}
	return synapsedns.ExternalResolver()
}

// LookupIPResolver is the minimal subset of *net.Resolver
// verifyDomainDNS depends on. Lets dnsResolver return either an
// injected test stub or the ExternalResolver helper behind one
// interface.
type LookupIPResolver interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
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

	// AutoDNS* fields are populated only on POST /domains when the
	// handler tried to mint the A record inline via a stored DNS
	// credential. Attempted=false (zero value) means we didn't try
	// — preconditions missed (no Crypto, no PublicIP, no credential
	// covers the zone). Attempted=true with Success=false carries a
	// human-readable Reason; the operator can hit
	// /domains/{id}/auto_configure to retry. Other endpoints (verify,
	// delete) leave all of these zero so omitempty hides them.
	AutoDNSAttempted    bool   `json:"autoDnsAttempted,omitempty"`
	AutoDNSSuccess      bool   `json:"autoDnsSuccess,omitempty"`
	AutoDNSReason       string `json:"autoDnsReason,omitempty"`
	AutoDNSCredentialID string `json:"autoDnsCredentialId,omitempty"`
	AutoDNSZone         string `json:"autoDnsZone,omitempty"`
}

// scanDomain is the row-shape for SELECTs on deployment_domains. Used
// by both list + verify paths so the column list stays in one place.
// auto_configured + dns_credential_id were added in migration 000015
// for the Cloudflare auto-configure flow; they're nullable so older
// rows scan as (false, nil).
const domainSelectCols = `id, deployment_id, domain, role, status,
	dns_verified_at, last_dns_error, auto_configured, dns_credential_id,
	created_at, updated_at`

func scanDomainRow(row pgx.Row) (models.DeploymentDomain, error) {
	var d models.DeploymentDomain
	var verifiedAt *time.Time
	var lastErr *string
	var credID *string
	if err := row.Scan(
		&d.ID, &d.DeploymentID, &d.Domain, &d.Role, &d.Status,
		&verifiedAt, &lastErr, &d.AutoConfigured, &credID,
		&d.CreatedAt, &d.UpdatedAt,
	); err != nil {
		return models.DeploymentDomain{}, err
	}
	d.DNSVerifiedAt = verifiedAt
	if lastErr != nil {
		d.LastDNSError = *lastErr
	}
	d.DNSCredentialID = credID
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

	// v1.5+: when the row landed non-active AND we have everything we
	// need to mint an A record on the operator's behalf (Crypto +
	// PublicIP + a credential covering the zone), do it inline. This
	// turns the ergonomically-broken "row sits at pending forever
	// because operator forgot the manual A record" into the happy
	// path. Best-effort — silent skip on any precondition miss; the
	// existing /domains/{id}/auto_configure endpoint stays the manual
	// fallback for re-tries.
	autoOutcome := h.tryAutoConfigureOnCreate(r.Context(), id, domainCanonical, d.ProjectID)
	if autoOutcome != nil && autoOutcome.success {
		// Re-read the row so the response carries the post-update
		// auto_configured + dns_credential_id columns. Status went
		// back to 'pending' so the verifier loop picks up the freshly
		// minted record once DNS propagates.
		if reread, err := h.loadDomainByID(r.Context(), id); err == nil {
			updated = reread
		}
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

	resp := domainResponse{
		DeploymentDomain:           updated,
		DeploymentRestartTriggered: restartTriggered,
	}
	if autoOutcome != nil {
		resp.AutoDNSAttempted = true
		resp.AutoDNSSuccess = autoOutcome.success
		resp.AutoDNSReason = autoOutcome.reason
		resp.AutoDNSCredentialID = autoOutcome.credentialID
		resp.AutoDNSZone = autoOutcome.zone
	}

	writeJSON(w, http.StatusCreated, resp)
}

// autoConfigureOnCreateOutcome is the inline-auto-configure trace
// stamped onto the createDomain response. It exists so the dashboard
// can render a green "✓ A record created via Cloudflare credential
// 'Personal CF'" line on first-add success — no second round-trip
// to /auto_configure needed.
//
// success=false with attempted=true means we tried but Cloudflare
// rejected (token bad, network blip, etc); the row is still pending
// + the operator can hit /auto_configure manually to retry. nil
// outcome means we didn't try (preconditions missed: no Crypto, no
// PublicIP, or no credential covers the zone).
type autoConfigureOnCreateOutcome struct {
	success      bool
	credentialID string
	zone         string
	reason       string
}

// tryAutoConfigureOnCreate is the inline-best-effort auto-configure
// invoked at the end of createDomain. Returns nil when there is
// genuinely nothing the operator could do without further setup
// (no Crypto, no PublicIP, or no credential covers the zone — the
// operator already knows their setup doesn't carry one). Returns
// an outcome with attempted=true otherwise: success=true on a clean
// Cloudflare upsert, or success=false + reason populated when we
// HAD a credential candidate but couldn't pick one (ambiguity) or
// the upsert failed.
//
// The "ambiguity" case — multiple project credentials cover the
// same zone — was previously a silent skip, leaving the operator
// guessing why auto-configure didn't fire on a freshly added domain.
// v1.6.5 surfaces it inline so the dashboard can render "Multiple
// credentials match — pick one via Auto-configure".
//
// Decrypt failures and Cloudflare API errors are also surfaced via
// outcome.reason so the dashboard's POST /domains response carries
// enough detail to show a clear next step.
func (h *DomainsHandler) tryAutoConfigureOnCreate(ctx context.Context, domainID, domainName, projectID string) *autoConfigureOnCreateOutcome {
	if h.Crypto == nil || h.PublicIP == "" {
		return nil
	}

	cred, code, msg, _ := h.resolveCredentialForDomain(ctx, "", domainName, projectID)
	switch code {
	case "":
		// Happy path — fall through to the upsert.
	case "no_credential_for_zone":
		// Genuine "nothing to do" — operator never wired a credential
		// for this zone. Silent skip preserves the v1.6.4 UX (the
		// row's existing status='pending' + DNS hint is enough).
		return nil
	default:
		// "credential_required" or any future ambiguity code: surface
		// inline so the dashboard can show the operator they need to
		// pick a specific credentialId via /auto_configure.
		return &autoConfigureOnCreateOutcome{reason: msg}
	}

	zoneName, recordName, ok := longestMatchingZone(domainName, cred.zones)
	if !ok {
		return nil
	}

	out := &autoConfigureOnCreateOutcome{credentialID: cred.id, zone: zoneName}

	plaintextToken, err := h.Crypto.DecryptString(cred.tokenEncrypted)
	if err != nil {
		// Decrypt failures mean SYNAPSE_STORAGE_KEY rotated or the
		// row is corrupt. We surface this loudly because both call
		// for operator action; the row stays pending so the manual
		// retry path doesn't hide the problem.
		out.reason = "could not decrypt the stored DNS credential token"
		logErr("auto-configure on create: decrypt token", err)
		return out
	}

	client := h.cloudflareClient(plaintextToken)
	if err := client.UpsertARecord(ctx, zoneName, recordName, h.PublicIP); err != nil {
		out.reason = "cloudflare upsert failed: " + err.Error()
		h.recordCredentialError(ctx, cred.id, err.Error())
		return out
	}

	// Mark auto_configured + reset to pending so the dns.Verifier
	// picks the row up once DNS propagates. last_dns_error cleared
	// because the previous synchronous preflight wrote a stale
	// "lookup failed" before the record existed.
	if _, err := h.DB.Exec(ctx, `
		UPDATE deployment_domains
		   SET auto_configured = true,
		       dns_credential_id = $2,
		       status = 'pending',
		       dns_verified_at = NULL,
		       last_dns_error = NULL,
		       updated_at = now()
		 WHERE id = $1
	`, domainID, cred.id); err != nil {
		out.reason = "cloudflare ok but failed to persist auto_configured flag"
		logErr("auto-configure on create: update row", err)
		return out
	}

	out.success = true
	return out
}

// loadDomainByID reads a single deployment_domains row. Used by
// createDomain to refresh the row after a successful inline auto-
// configure flipped auto_configured + dns_credential_id.
func (h *DomainsHandler) loadDomainByID(ctx context.Context, id string) (models.DeploymentDomain, error) {
	row := h.DB.QueryRow(ctx, `
		SELECT `+domainSelectCols+`
		  FROM deployment_domains
		 WHERE id = $1
	`, id)
	return scanDomainRow(row)
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
	// removing a 'pending' / 'failed' row needs no restart. The
	// auto_configured + dns_credential_id columns inform the post-
	// delete Cloudflare cleanup: if Synapse minted the A record on
	// the operator's behalf, we tear it down on delete (best-effort)
	// so their Cloudflare zone stays clean.
	var (
		domain          string
		status          string
		autoConfigured  bool
		dnsCredentialID *string
	)
	err := h.DB.QueryRow(r.Context(), `
		DELETE FROM deployment_domains
		WHERE id = $1 AND deployment_id = $2
		RETURNING domain, status, auto_configured, dns_credential_id
	`, domainID, d.ID).Scan(&domain, &status, &autoConfigured, &dnsCredentialID)
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

	// Best-effort Cloudflare cleanup: if we minted the A record, tear
	// it down. We log on error and continue — the deployment_domains
	// row is already gone, so a stale Cloudflare record is the lesser
	// evil vs leaving the operator's domain row half-deleted.
	if autoConfigured && dnsCredentialID != nil {
		h.cleanupAutoConfiguredRecord(r.Context(), *dnsCredentialID, domain)
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

// ---------- POST /v1/deployments/{name}/domains/{domainID}/auto_configure ----------

type autoConfigureDomainReq struct {
	// Optional. When omitted, the handler picks the unique credential
	// whose zone list covers the domain's apex. Required when more
	// than one credential matches (the dashboard offers a picker).
	CredentialID string `json:"credentialId,omitempty"`
}

func (h *DomainsHandler) autoConfigureDomain(w http.ResponseWriter, r *http.Request) {
	d, _, t, role, ok := h.Deployments.loadDeploymentForRequest(w, r)
	if !ok {
		return
	}
	if !canEditProject(role) {
		writeError(w, http.StatusForbidden, "forbidden",
			"Viewers cannot manage domains; ask a project admin or member")
		return
	}
	if h.Crypto == nil {
		writeError(w, http.StatusServiceUnavailable, "dns_auto_configure_unavailable",
			"DNS auto-configure requires SYNAPSE_STORAGE_KEY to be set on the Synapse host")
		return
	}
	if h.PublicIP == "" {
		writeError(w, http.StatusServiceUnavailable, "public_ip_not_configured",
			"SYNAPSE_PUBLIC_IP is not set; can't point an A record at this host")
		return
	}

	domainID := chi.URLParam(r, "domainID")
	if domainID == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "domain id is required")
		return
	}

	var req autoConfigureDomainReq
	if r.ContentLength > 0 {
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
	}

	// Load the row we're configuring.
	var domainName string
	err := h.DB.QueryRow(r.Context(), `
		SELECT domain FROM deployment_domains
		WHERE id = $1 AND deployment_id = $2
	`, domainID, d.ID).Scan(&domainName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "domain_not_found",
				"Domain not found or belongs to a different deployment")
			return
		}
		logErr("load domain for auto-configure", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"Failed to load domain")
		return
	}

	// Resolve the credential. With CredentialID set we look up that
	// row directly; otherwise we ask the DB to find the unique
	// credential whose zones cover this domain's apex.
	cred, code, msg, status := h.resolveCredentialForDomain(r.Context(), req.CredentialID, domainName, d.ProjectID)
	if code != "" {
		writeError(w, status, code, msg)
		return
	}

	// Decrypt the token. Failure here is a 500 — it means
	// SYNAPSE_STORAGE_KEY rotated without re-encrypting the rows, or
	// the column was tampered with. Either way the operator should
	// hear about it loudly.
	plaintextToken, err := h.Crypto.DecryptString(cred.tokenEncrypted)
	if err != nil {
		logErr("decrypt cloudflare token", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"Failed to decrypt DNS credential token")
		return
	}

	zoneName, recordName, ok := longestMatchingZone(domainName, cred.zones)
	if !ok {
		// We already filtered on this in resolveCredentialForDomain
		// when no credential_id was supplied; explicit credential_id
		// can still hit this when the picked credential doesn't
		// actually cover the domain.
		writeError(w, http.StatusBadRequest, "no_credential_for_zone",
			"The selected credential's zones don't cover "+domainName)
		return
	}

	client := h.cloudflareClient(plaintextToken)
	if err := client.UpsertARecord(r.Context(), zoneName, recordName, h.PublicIP); err != nil {
		// Token revoked or wrong scopes → 400 + persist last_error
		// onto the credential row so the dashboard surfaces it
		// without another round-trip.
		if errors.Is(err, synapsedns.ErrUnauthorized) {
			h.recordCredentialError(r.Context(), cred.id, err.Error())
			writeError(w, http.StatusBadRequest, "token_invalid_or_revoked",
				"Cloudflare rejected the stored token; rotate the credential")
			return
		}
		// Any other error is upstream / network — 502 keeps the
		// dashboard's error banner short and re-tryable.
		h.recordCredentialError(r.Context(), cred.id, err.Error())
		writeError(w, http.StatusBadGateway, "cloudflare_api_error",
			"Cloudflare API error: "+err.Error())
		return
	}

	// Mark the row auto-configured AND reset status back to 'pending'
	// so the verification loop (internal/dns.Verifier) picks it up.
	// The createDomain handler may have stamped 'failed' synchronously
	// when the operator first added the row (DNS hadn't propagated
	// yet) — that's a stale verdict the moment we mint the A record.
	// Clear last_dns_error too so the dashboard renders "auto-
	// configured, awaiting propagation" instead of the old failure.
	row := h.DB.QueryRow(r.Context(), `
		UPDATE deployment_domains
		SET auto_configured = true,
		    dns_credential_id = $2,
		    status = 'pending',
		    dns_verified_at = NULL,
		    last_dns_error = NULL,
		    updated_at = now()
		WHERE id = $1
		RETURNING `+domainSelectCols, domainID, cred.id)
	updated, err := scanDomainRow(row)
	if err != nil {
		logErr("update domain after auto-configure", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"A record created but failed to update the domain row")
		return
	}

	// Mark the credential successful — bumps last_used_at, clears
	// any stale last_error from a previous failed attempt.
	_, _ = h.DB.Exec(r.Context(), `
		UPDATE dns_credentials
		SET last_used_at = now(), last_error = NULL
		WHERE id = $1
	`, cred.id)

	uid, _ := auth.UserID(r.Context())
	teamID := ""
	if t != nil {
		teamID = t.ID
	}
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     teamID,
		ActorID:    uid,
		Action:     audit.ActionAutoConfigureDomain,
		TargetType: audit.TargetDomain,
		TargetID:   domainID,
		Metadata: map[string]any{
			"deploymentId":   d.ID,
			"deploymentName": d.Name,
			"domain":         domainName,
			"credentialId":   cred.id,
			"zone":           zoneName,
		},
	})

	writeJSON(w, http.StatusOK, domainResponse{DeploymentDomain: updated})
}

// dnsCredentialForAuto is the subset of dns_credentials a single
// auto-configure call needs: id, zones, encrypted token. We don't
// pull last_used_at / last_error / created_by because they're
// observability fields the handler only writes back to.
type dnsCredentialForAuto struct {
	id             string
	zones          []models.ZoneInfo
	tokenEncrypted []byte
}

// resolveCredentialForDomain implements the credential-selection rules
// for the auto-configure flow. v1.6.4+ added project-scoped credentials,
// which take precedence over instance-wide ones via a two-tier lookup:
//
//   - credential_id provided → fetch that row, but only if the row is
//     visible to this project (its own row, or an instance-wide row).
//     A row belonging to *another* project surfaces as 404 — UUIDs are
//     unguessable but the check makes scope leaks impossible by
//     construction.
//   - credential_id empty → first try project-scoped rows; if any cover
//     the zone, those are the candidate set. Only when zero project rows
//     match do we fall back to instance-wide rows. Within whichever tier
//     wins:
//       0 matches  → 400 no_credential_for_zone
//       1 match    → use it
//       2+ matches → 400 credential_required (pass credentialId)
//
// projectID being "" means "no project context" (legacy path / instance
// admin) and skips the project tier entirely.
//
// Returns either a populated dnsCredentialForAuto OR (code, msg, status)
// for writeError.
func (h *DomainsHandler) resolveCredentialForDomain(ctx context.Context, credentialID, domainName, projectID string) (dnsCredentialForAuto, string, string, int) {
	if credentialID != "" {
		// Scope guard: the row must belong to this project OR be
		// instance-wide. The IS NOT DISTINCT FROM trick lets us pass
		// "" for the legacy / admin path and still match NULL.
		var c dnsCredentialForAuto
		var zonesRaw []byte
		var pid sql.NullString
		if projectID != "" {
			pid = sql.NullString{String: projectID, Valid: true}
		}
		err := h.DB.QueryRow(ctx, `
			SELECT id, zones, token_encrypted FROM dns_credentials
			WHERE id = $1
			  AND (project_id IS NULL OR project_id = $2)
		`, credentialID, pid).Scan(&c.id, &zonesRaw, &c.tokenEncrypted)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return dnsCredentialForAuto{}, "credential_not_found",
					"DNS credential not found", http.StatusNotFound
			}
			logErr("load credential", err)
			return dnsCredentialForAuto{}, "internal",
				"Failed to load DNS credential", http.StatusInternalServerError
		}
		if len(zonesRaw) > 0 {
			_ = json.Unmarshal(zonesRaw, &c.zones)
		}
		return c, "", "", 0
	}

	// Auto-pick with two-tier hierarchy: project-scoped wins, fall
	// through to instance-wide. We do one DB read returning both tiers
	// + a flag, then split in-memory. Avoids the chatty "query
	// project, count, maybe query global" round-trip pattern.
	rows, err := h.DB.Query(ctx, `
		SELECT id, zones, token_encrypted, (project_id IS NOT NULL) AS is_project_scoped
		  FROM dns_credentials
		 WHERE project_id IS NULL OR project_id = $1
	`, sql.NullString{String: projectID, Valid: projectID != ""})
	if err != nil {
		logErr("list credentials for auto-pick", err)
		return dnsCredentialForAuto{}, "internal",
			"Failed to list DNS credentials", http.StatusInternalServerError
	}
	defer rows.Close()

	var projectMatches, globalMatches []dnsCredentialForAuto
	for rows.Next() {
		var c dnsCredentialForAuto
		var zonesRaw []byte
		var isProjectScoped bool
		if err := rows.Scan(&c.id, &zonesRaw, &c.tokenEncrypted, &isProjectScoped); err != nil {
			logErr("scan credential", err)
			return dnsCredentialForAuto{}, "internal",
				"Failed to read DNS credentials", http.StatusInternalServerError
		}
		if len(zonesRaw) > 0 {
			_ = json.Unmarshal(zonesRaw, &c.zones)
		}
		if _, _, ok := longestMatchingZone(domainName, c.zones); !ok {
			continue
		}
		if isProjectScoped {
			projectMatches = append(projectMatches, c)
		} else {
			globalMatches = append(globalMatches, c)
		}
	}

	// Hierarchy: project tier wins if it has any matches. Only when
	// the project tier is empty do we look at globals. This is the
	// agency model — "credentials for client X live in client X's
	// project" — without sacrificing the v1.5 single-cred install.
	candidates := projectMatches
	if len(candidates) == 0 {
		candidates = globalMatches
	}
	switch len(candidates) {
	case 0:
		return dnsCredentialForAuto{}, "no_credential_for_zone",
			"No saved DNS credential covers the apex of " + domainName, http.StatusBadRequest
	case 1:
		return candidates[0], "", "", 0
	default:
		return dnsCredentialForAuto{}, "credential_required",
			"Multiple DNS credentials cover this domain; pass credentialId to disambiguate",
			http.StatusBadRequest
	}
}

// longestMatchingZone walks `zones` looking for the entry whose Name
// is the longest suffix of `domain`. Returns the zone name (e.g.
// "fechasul.com.br") and the relative record name (e.g. "api" for
// "api.fechasul.com.br"; "@" for the apex).
//
// Returns ok=false when nothing matches. Comparison is case-
// insensitive — Cloudflare normalises both sides.
func longestMatchingZone(domain string, zones []models.ZoneInfo) (zoneName, recordName string, ok bool) {
	d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	bestZone := ""
	for _, z := range zones {
		zn := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(z.Name), "."))
		if zn == "" {
			continue
		}
		if d == zn {
			if len(zn) > len(bestZone) {
				bestZone = zn
			}
			continue
		}
		if strings.HasSuffix(d, "."+zn) {
			if len(zn) > len(bestZone) {
				bestZone = zn
			}
		}
	}
	if bestZone == "" {
		return "", "", false
	}
	if d == bestZone {
		// Apex record. libdns/cloudflare accepts "@" and translates
		// it to the bare zone name on the wire.
		return bestZone, "@", true
	}
	rec := strings.TrimSuffix(d, "."+bestZone)
	return bestZone, rec, true
}

// recordCredentialError stamps last_error on the credential row so
// the dashboard can show "Cloudflare rejected this token" without
// another API round-trip. Best-effort — the auto_configure response
// is more important than this hint.
func (h *DomainsHandler) recordCredentialError(ctx context.Context, credID, msg string) {
	if credID == "" || msg == "" {
		return
	}
	if len(msg) > 1024 {
		msg = msg[:1024]
	}
	if _, err := h.DB.Exec(ctx, `
		UPDATE dns_credentials
		SET last_error = $2, last_used_at = now()
		WHERE id = $1
	`, credID, msg); err != nil {
		// Logged but not surfaced — the parent handler already
		// returned a structured error to the user.
		logErr("record credential error", err)
	}
}

// cloudflareClient builds (or reuses, via injected factory) a
// CloudflareClient for the given plaintext token.
func (h *DomainsHandler) cloudflareClient(token string) *synapsedns.CloudflareClient {
	if h.CloudflareFactory != nil {
		return h.CloudflareFactory(token)
	}
	return &synapsedns.CloudflareClient{Token: token}
}

// cleanupAutoConfiguredRecord deletes the A record we minted on the
// operator's Cloudflare account when the deployment_domains row is
// removed. Best-effort: any failure is logged + swallowed because
// the deployment_domains row is already gone (returning 5xx now would
// confuse the operator into thinking the delete didn't take).
func (h *DomainsHandler) cleanupAutoConfiguredRecord(ctx context.Context, credID, domain string) {
	if h.Crypto == nil {
		// Auto_configured rows shouldn't exist without Crypto, but
		// defense-in-depth: if SYNAPSE_STORAGE_KEY rotated out we
		// silently skip the cleanup.
		return
	}
	logger := h.Logger
	if logger == nil {
		logger = slog.Default()
	}
	var (
		zonesRaw       []byte
		tokenEncrypted []byte
	)
	err := h.DB.QueryRow(ctx, `
		SELECT zones, token_encrypted FROM dns_credentials WHERE id = $1
	`, credID).Scan(&zonesRaw, &tokenEncrypted)
	if err != nil {
		logger.Warn("dns cleanup: load credential",
			"credentialId", credID, "domain", domain, "err", err)
		return
	}
	var zones []models.ZoneInfo
	if len(zonesRaw) > 0 {
		_ = json.Unmarshal(zonesRaw, &zones)
	}
	zoneName, recordName, ok := longestMatchingZone(domain, zones)
	if !ok {
		logger.Warn("dns cleanup: no matching zone",
			"credentialId", credID, "domain", domain)
		return
	}
	plaintext, err := h.Crypto.DecryptString(tokenEncrypted)
	if err != nil {
		logger.Warn("dns cleanup: decrypt token",
			"credentialId", credID, "err", err)
		return
	}
	client := h.cloudflareClient(plaintext)
	if err := client.DeleteARecord(ctx, zoneName, recordName); err != nil {
		logger.Warn("dns cleanup: delete A record",
			"credentialId", credID, "domain", domain, "err", err)
	}
}
