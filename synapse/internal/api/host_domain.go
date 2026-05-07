package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/Iann29/synapse/internal/audit"
	"github.com/Iann29/synapse/internal/auth"
)

// hostDomainHandlers groups the /v1/admin/host_domain endpoints. They share
// the AdminHandler's deps (DB, UpdaterSocket) plus the host-config snapshot
// that GET surfaces and POST validates against. Mounted from
// AdminHandler.Routes().
//
// Why a separate file (not admin.go): host_domain has its own
// validation surface, daemon dispatcher, and status flow. Splitting keeps
// admin.go's existing /version_check + /upgrade narrative readable while
// the new feature lands as an additive island.

// hostDomainConfig is the snapshot of the SYNAPSE_* env that determines
// the answer to GET /v1/admin/host_domain. We don't read os.Getenv
// directly inside the handler so tests can fully drive the response
// through the AdminHandler fields without touching process env.
type hostDomainConfig struct {
	BaseDomain string
	PublicURL  string
	PublicIP   string
	// Domain + AcmeEmail come from the bootstrap env (SYNAPSE_DOMAIN /
	// SYNAPSE_ACME_EMAIL). They are not strictly required for the
	// runtime — the api server only needs PublicURL — but the dashboard
	// wants them to pre-fill the form. Empty == not configured.
	Domain    string
	AcmeEmail string
}

// hostDomainConfigFromEnv reads the SYNAPSE_* env once at handler-call
// time. Cheap (a few getenv calls) and avoids stale-config drift if the
// operator changes the file but hasn't restarted the api yet — the api
// only ever returns the config it actually booted with via PublicURL,
// the rest is a "best knowledge of the host's last-known intent" hint.
func hostDomainConfigFromEnv() hostDomainConfig {
	return hostDomainConfig{
		Domain:    strings.TrimSpace(os.Getenv("SYNAPSE_DOMAIN")),
		AcmeEmail: strings.TrimSpace(os.Getenv("SYNAPSE_ACME_EMAIL")),
	}
}

// hostDomainResp is the GET shape. Pointers (well — explicit nullable
// strings via the encoder) keep "field not configured" distinguishable
// from "field is empty". We use plain strings + omitempty because the
// dashboard treats "" the same as null and the omitempty form keeps the
// JSON small.
type hostDomainResp struct {
	Mode       string `json:"mode"`
	Domain     string `json:"domain,omitempty"`
	BaseDomain string `json:"baseDomain,omitempty"`
	PublicURL  string `json:"publicUrl,omitempty"`
	PublicIP   string `json:"publicIp,omitempty"`
	AcmeEmail  string `json:"acmeEmail,omitempty"`
	// Fallback URLs are the loopback / IP-port form — what the operator
	// can hit if their DNS is broken or they want to bypass TLS. Always
	// populated when PublicIP is known so the dashboard can show a
	// recovery hint after a misconfigured change.
	FallbackURLs hostDomainFallbacks `json:"fallbackUrls"`
}

type hostDomainFallbacks struct {
	Dashboard string `json:"dashboard,omitempty"`
	API       string `json:"api,omitempty"`
}

// getHostDomain returns the current configuration. Tier-1 honesty: the
// dashboard pre-fills its form with whatever this returns, so we err on
// the side of "null when unknown" rather than guess.
func (h *AdminHandler) getHostDomain(w http.ResponseWriter, r *http.Request) {
	envCfg := hostDomainConfigFromEnv()

	resp := hostDomainResp{
		Mode:       hostDomainMode(h.PublicURL, h.BaseDomain),
		Domain:     envCfg.Domain,
		BaseDomain: h.BaseDomain,
		PublicURL:  h.PublicURL,
		PublicIP:   h.PublicIP,
		AcmeEmail:  envCfg.AcmeEmail,
	}
	// If SYNAPSE_DOMAIN isn't on the env (e.g. the api process started
	// before --reconfigure landed it), fall back to extracting the
	// hostname from PublicURL. Keeps the dashboard's pre-fill correct
	// for the common case where the operator only set SYNAPSE_PUBLIC_URL.
	if resp.Domain == "" && resp.PublicURL != "" {
		if extracted := extractHostFromURL(resp.PublicURL); extracted != "" && !looksLikeIP(extracted) {
			resp.Domain = extracted
		}
	}
	if resp.PublicIP != "" {
		resp.FallbackURLs = hostDomainFallbacks{
			Dashboard: fmt.Sprintf("http://%s:6790", resp.PublicIP),
			API:       fmt.Sprintf("http://%s:8080", resp.PublicIP),
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// hostDomainMode classifies the current setup so the dashboard knows
// which form variant to show. Plain HTTP looks like "no PublicURL OR
// PublicURL points at an IP" — both end states of --no-tls. We don't
// look at SYNAPSE_DOMAIN here because it's hint-only and could lag the
// running config.
func hostDomainMode(publicURL, baseDomain string) string {
	host := extractHostFromURL(publicURL)
	switch {
	case baseDomain != "" && host != "" && !looksLikeIP(host):
		return "tls_with_wildcard"
	case host != "" && !looksLikeIP(host):
		return "tls"
	default:
		return "plain"
	}
}

// extractHostFromURL returns the bare hostname (no scheme, no port) of
// `u`. Empty input → empty output; we never panic on a malformed URL,
// the GET handler is tolerant by design.
func extractHostFromURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	if i := strings.LastIndex(u, ":"); i >= 0 {
		// Strip a trailing port iff what follows is purely digits.
		// Bracketed IPv6 hosts ("[::1]:8080") are unreachable for
		// our use case (Caddy + dashboard is IPv4-only on Hetzner)
		// so the simple split is safe.
		port := u[i+1:]
		if port != "" && allDigits(port) {
			u = u[:i]
		}
	}
	return strings.TrimSpace(u)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// looksLikeIP says whether `s` parses as an IP. Used by GET to decide
// whether PublicURL already implies "tls" or just "plain on an IP".
func looksLikeIP(s string) bool {
	return net.ParseIP(s) != nil
}

// ---------- POST /v1/admin/host_domain ----------

type hostDomainPostReq struct {
	Domain     *string `json:"domain"`
	BaseDomain *string `json:"baseDomain"`
	PlainHTTP  bool    `json:"plainHttp"`
	AcmeEmail  *string `json:"acmeEmail"`
}

// trim returns the trimmed string pointed to, or "" if nil.
func (r hostDomainPostReq) domain() string {
	if r.Domain == nil {
		return ""
	}
	return strings.TrimSpace(*r.Domain)
}
func (r hostDomainPostReq) baseDomain() string {
	if r.BaseDomain == nil {
		return ""
	}
	return strings.TrimSpace(*r.BaseDomain)
}
func (r hostDomainPostReq) acmeEmail() string {
	if r.AcmeEmail == nil {
		return ""
	}
	return strings.TrimSpace(*r.AcmeEmail)
}

type hostDomainPostResp struct {
	JobID     string `json:"jobId"`
	StatusURL string `json:"statusUrl"`
	State     string `json:"state"`
}

// postHostDomain validates a reconfigure request, persists a row in
// admin_jobs, dispatches the work to the synapse-updater daemon, and
// returns the job id so the dashboard can poll for completion.
//
// Order of operations:
//  1. Reject malformed bodies up front (parse + flag matrix + format
//     validation) — no row, no audit, no daemon call.
//  2. DNS preflight (best-effort). Required only when PublicIP is set;
//     we don't have a way to verify the operator's DNS without it.
//  3. Insert admin_jobs row. We hold the row id locally so the daemon
//     dispatch carries it; the daemon writes back state via psql.
//  4. Call the daemon. On daemon error: flip the row to 'failed' and
//     surface 502/503 to the caller.
//  5. Audit + return 202 with the row id.
func (h *AdminHandler) postHostDomain(w http.ResponseWriter, r *http.Request) {
	if h.UpdaterSocket == "" {
		writeError(w, http.StatusServiceUnavailable, "updater_unavailable",
			"Self-update daemon is not configured on this host. Run setup.sh --reconfigure via SSH.")
		return
	}
	if _, err := os.Stat(h.UpdaterSocket); err != nil {
		writeError(w, http.StatusServiceUnavailable, "updater_unreachable",
			"Self-update daemon socket missing — daemon installed but not running, or this host doesn't have systemd. Run setup.sh --reconfigure via SSH.")
		return
	}

	var req hostDomainPostReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	domain := req.domain()
	baseDomain := req.baseDomain()
	acme := req.acmeEmail()

	// At least one effective field is required. plainHttp=true alone is
	// fine (it means "revert to no-TLS"); domain or baseDomain alone
	// also fine; an empty body is not.
	if domain == "" && baseDomain == "" && !req.PlainHTTP {
		writeError(w, http.StatusBadRequest, "bad_request",
			"At least one of domain, baseDomain, or plainHttp must be provided")
		return
	}
	// Mutually exclusive: plainHttp=true means "remove TLS"; setting a
	// domain in the same call would contradict that intent.
	if req.PlainHTTP && (domain != "" || baseDomain != "") {
		writeError(w, http.StatusBadRequest, "bad_flags",
			"plainHttp=true cannot be combined with domain or baseDomain")
		return
	}

	if domain != "" {
		canonical, code, msg := validateDomain(domain)
		if code != "" {
			writeError(w, http.StatusBadRequest, code, msg)
			return
		}
		domain = canonical
	}
	if baseDomain != "" {
		canonical, code, msg := validateDomain(baseDomain)
		if code != "" {
			writeError(w, http.StatusBadRequest, code, msg)
			return
		}
		baseDomain = canonical
	}
	if acme != "" {
		if _, err := mail.ParseAddress(acme); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_acme_email",
				"acmeEmail must be a valid email address")
			return
		}
	}

	// DNS preflight — only meaningful when the operator has told us
	// what their public IP is. Without PublicIP, we have no anchor to
	// compare resolved A records against, so we skip and let the
	// daemon's own DNS check (inside setup.sh --reconfigure) be the
	// source of truth.
	if domain != "" && h.PublicIP != "" {
		if got, ok := h.dnsLookupA(r.Context(), domain); !ok {
			writeError(w, http.StatusBadRequest, "dns_preflight_failed",
				fmt.Sprintf("domain does not resolve to the configured PublicIP: expected %s, got %s",
					h.PublicIP, strings.Join(got, ", ")))
			return
		}
	}

	uid, _ := auth.UserID(r.Context())

	payload := map[string]any{}
	if domain != "" {
		payload["domain"] = domain
	}
	if baseDomain != "" {
		payload["baseDomain"] = baseDomain
	}
	if req.PlainHTTP {
		payload["plainHttp"] = true
	}
	if acme != "" {
		payload["acmeEmail"] = acme
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Failed to encode payload")
		return
	}

	var jobID string
	if err := h.DB.QueryRow(r.Context(), `
		INSERT INTO admin_jobs (kind, payload, state, created_by)
		VALUES ('reconfigure_host_domain', $1, 'queued', $2)
		RETURNING id
	`, payloadJSON, nullableUUID(uid)).Scan(&jobID); err != nil {
		logErr("insert admin_jobs", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to enqueue reconfigure job")
		return
	}

	// Daemon body carries the validated payload + jobId so the daemon
	// can write back to the same row when it finishes.
	daemonBody, err := json.Marshal(map[string]any{
		"jobId":      jobID,
		"domain":     domain,
		"baseDomain": baseDomain,
		"plainHttp":  req.PlainHTTP,
		"acmeEmail":  acme,
	})
	if err != nil {
		// Should never happen — we just marshalled the same data above.
		// Mark the row failed so the operator can re-issue rather than
		// leaving it queued forever.
		_ = h.markJobFailed(r.Context(), jobID, "encode daemon body: "+err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "Failed to encode daemon request")
		return
	}

	status, daemonResp, err := h.callUpdater(r.Context(), http.MethodPost, "/reconfigure_host_domain", daemonBody)
	if err != nil {
		_ = h.markJobFailed(r.Context(), jobID, "daemon unreachable: "+err.Error())
		writeError(w, http.StatusBadGateway, "updater_unreachable",
			"Could not reach the self-update daemon: "+err.Error())
		return
	}
	if status >= 400 {
		var ue struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(daemonResp, &ue)
		code := ue.Error
		if code == "" {
			code = "updater_error"
		}
		_ = h.markJobFailed(r.Context(), jobID, "daemon returned "+code)
		// 409 from the daemon (e.g. another reconfigure already in
		// flight) should pass through as 409; everything else is a 502
		// because the api thought the request was valid.
		clientStatus := http.StatusBadGateway
		if status == http.StatusConflict {
			clientStatus = http.StatusConflict
		}
		writeError(w, clientStatus, code, hostDomainErrorMessage(code))
		return
	}

	// Audit on the success path only — rejected payloads don't need a
	// row, and we record the sanitised fields (no acmeEmail in
	// metadata; it's a contact, not a secret, but still PII).
	auditMeta := map[string]any{
		"jobId":      jobID,
		"domain":     domain,
		"baseDomain": baseDomain,
		"plainHttp":  req.PlainHTTP,
	}
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		ActorID:    uid,
		Action:     audit.ActionHostDomainChangeInitiated,
		TargetType: audit.TargetSynapse,
		TargetID:   jobID,
		Metadata:   auditMeta,
	})

	writeJSON(w, http.StatusAccepted, hostDomainPostResp{
		JobID:     jobID,
		StatusURL: "/v1/admin/host_domain/status/" + jobID,
		State:     "queued",
	})
}

// hostDomainErrorMessage humanises the codes the daemon emits.
func hostDomainErrorMessage(code string) string {
	switch code {
	case "reconfigure_in_progress":
		return "Another reconfigure is already running"
	case "invalid_payload":
		return "Updater rejected the reconfigure payload"
	case "invalid_json":
		return "Updater rejected the request body"
	default:
		return "Updater error: " + code
	}
}

// markJobFailed flips an admin_jobs row to 'failed' with a short error
// message. Used when the daemon dispatch fails before the daemon could
// take ownership of the row — keeps state machine consistent.
func (h *AdminHandler) markJobFailed(ctx context.Context, jobID, reason string) error {
	_, err := h.DB.Exec(ctx, `
		UPDATE admin_jobs
		   SET state = 'failed',
		       finished_at = now(),
		       error = $2
		 WHERE id = $1
		   AND state IN ('queued', 'running')
	`, jobID, reason)
	if err != nil {
		logErr("mark admin_jobs failed", err)
	}
	return err
}

// HostDomainResolver is the test seam for the host-domain DNS
// preflight. Implementors return the list of IPv4 addresses that
// `host` resolves to, in string form. Production wiring leaves the
// AdminHandler.HostDomainResolver field nil → the handler falls back
// to net.DefaultResolver.
type HostDomainResolver interface {
	LookupIP(host string) ([]string, error)
}

// dnsLookupA resolves `domain` and reports whether any returned A
// record matches h.PublicIP. Returns the list of resolved IPs (string
// form) so the caller can include them in the error message.
//
// Mirrors the behaviour of DomainsHandler.verifyDomainDNS but is
// dedicated here so an admin DNS check doesn't reach into the
// per-deployment domains code path. 5s timeout — same rationale as the
// per-deployment path.
func (h *AdminHandler) dnsLookupA(ctx context.Context, domain string) ([]string, bool) {
	if h.PublicIP == "" {
		return nil, true // no anchor → can't fail
	}
	if h.HostDomainResolver != nil {
		ips, err := h.HostDomainResolver.LookupIP(domain)
		return matchAgainstPublicIP(ips, err, h.PublicIP)
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(lookupCtx, "ip4", domain)
	strs := make([]string, 0, len(ips))
	for _, ip := range ips {
		strs = append(strs, ip.String())
	}
	return matchAgainstPublicIP(strs, err, h.PublicIP)
}

// matchAgainstPublicIP folds (ips, err) into the (got, ok) shape the
// caller hands back to the response writer. Extracted so the real-DNS
// and stub-resolver paths produce identical output.
func matchAgainstPublicIP(ips []string, err error, expected string) ([]string, bool) {
	if err != nil {
		return []string{"<lookup failed: " + err.Error() + ">"}, false
	}
	got := make([]string, 0, len(ips))
	for _, s := range ips {
		if s == expected {
			return nil, true
		}
		got = append(got, s)
	}
	if len(got) == 0 {
		got = append(got, "<no A records>")
	}
	return got, false
}

// nullableUUID converts an empty-string id into nil so the DB stores
// NULL rather than choking on a zero-length text value cast to UUID.
func nullableUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---------- GET /v1/admin/host_domain/status/{jobId} ----------

type hostDomainStatusResp struct {
	ID         string     `json:"id"`
	State      string     `json:"state"`
	Log        string     `json:"log"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// maxStatusLogBytes caps the log payload so a runaway daemon log
// doesn't blow up the JSON response. Tail the last N bytes so the
// dashboard always sees the freshest output.
const maxStatusLogBytes = 64 * 1024

func (h *AdminHandler) getHostDomainStatus(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	if jobID == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "job id is required")
		return
	}

	var (
		state                 string
		logText               string
		errStr                *string
		createdAt             time.Time
		startedAt, finishedAt *time.Time
	)
	err := h.DB.QueryRow(r.Context(), `
		SELECT state, log, error, created_at, started_at, finished_at
		  FROM admin_jobs
		 WHERE id = $1
	`, jobID).Scan(&state, &logText, &errStr, &createdAt, &startedAt, &finishedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "job_not_found",
				"No host-domain job with that id")
			return
		}
		// invalid UUID format produces a non-pgx-ErrNoRows error;
		// fold it into 404 so probes don't leak DB internals.
		if isInvalidUUID(err) {
			writeError(w, http.StatusNotFound, "job_not_found",
				"No host-domain job with that id")
			return
		}
		logErr("load admin_jobs", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to load job")
		return
	}

	// Tail the log to the last N bytes so the response stays bounded.
	if len(logText) > maxStatusLogBytes {
		logText = "...\n" + logText[len(logText)-maxStatusLogBytes:]
	}

	resp := hostDomainStatusResp{
		ID:         jobID,
		State:      state,
		Log:        logText,
		CreatedAt:  createdAt,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}
	if errStr != nil {
		resp.Error = *errStr
	}
	writeJSON(w, http.StatusOK, resp)
}

// isInvalidUUID returns true when the error looks like a Postgres "
// invalid input syntax for type uuid" — the result of querying with a
// random non-UUID string in the path. We bucket those into 404 instead
// of leaking the DB error to the caller.
func isInvalidUUID(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "invalid input syntax for type uuid") ||
		strings.Contains(msg, "invalid UUID") ||
		strings.Contains(msg, "SQLSTATE 22P02")
}
