package synapsetest

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// hostDomainResp mirrors the GET /v1/admin/host_domain shape so the
// strict decoder catches any drift between handler + test fixture.
type hostDomainResp struct {
	Mode         string `json:"mode"`
	Domain       string `json:"domain,omitempty"`
	BaseDomain   string `json:"baseDomain,omitempty"`
	PublicURL    string `json:"publicUrl,omitempty"`
	PublicIP     string `json:"publicIp,omitempty"`
	AcmeEmail    string `json:"acmeEmail,omitempty"`
	FallbackURLs struct {
		Dashboard string `json:"dashboard,omitempty"`
		API       string `json:"api,omitempty"`
	} `json:"fallbackUrls"`
}

type hostDomainPostResp struct {
	JobID     string `json:"jobId"`
	StatusURL string `json:"statusUrl"`
	State     string `json:"state"`
}

type hostDomainStatusResp struct {
	ID         string     `json:"id"`
	State      string     `json:"state"`
	Log        string     `json:"log"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

func TestHostDomain_Get_PlainModeNoConfig(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{})
	owner := makeAdminUser(t, h)

	var got hostDomainResp
	h.DoJSON(http.MethodGet, "/v1/admin/host_domain",
		owner.AccessToken, nil, http.StatusOK, &got)

	if got.Mode != "plain" {
		t.Errorf("mode: got %q want plain", got.Mode)
	}
	if got.Domain != "" || got.BaseDomain != "" || got.PublicURL != "" {
		t.Errorf("expected empty domain fields, got %+v", got)
	}
}

func TestHostDomain_Get_TLSMode(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		PublicURL:  "https://synapse.example.com",
		BaseDomain: "deployments.example.com",
	})
	owner := makeAdminUser(t, h)

	var got hostDomainResp
	h.DoJSON(http.MethodGet, "/v1/admin/host_domain",
		owner.AccessToken, nil, http.StatusOK, &got)

	if got.Mode != "tls_with_wildcard" {
		t.Errorf("mode: got %q want tls_with_wildcard", got.Mode)
	}
	if got.PublicURL != "https://synapse.example.com" {
		t.Errorf("publicUrl: got %q", got.PublicURL)
	}
	if got.BaseDomain != "deployments.example.com" {
		t.Errorf("baseDomain: got %q", got.BaseDomain)
	}
	// SYNAPSE_DOMAIN env not set — handler should derive Domain from
	// PublicURL host so dashboard pre-fill works on existing installs.
	if got.Domain != "synapse.example.com" {
		t.Errorf("domain (derived from publicUrl): got %q want synapse.example.com", got.Domain)
	}
}

func TestHostDomain_Get_FallbackURLsWhenPublicIP(t *testing.T) {
	// PublicIP threaded through SetupOpts isn't an existing field; the
	// admin handler reads the same value from RouterDeps.PublicIP. Use
	// the env-var path via SetupOpts instead by setting BaseDomain
	// alone — the test is really about the FallbackURLs branch firing
	// when PublicIP is non-empty. To set PublicIP we go directly via
	// the harness's underlying SetupOpts: extend the helper to surface
	// it. (Done below.)
	h := SetupWithOpts(t, SetupOpts{
		PublicIP: "203.0.113.42",
	})
	owner := makeAdminUser(t, h)

	var got hostDomainResp
	h.DoJSON(http.MethodGet, "/v1/admin/host_domain",
		owner.AccessToken, nil, http.StatusOK, &got)

	if got.PublicIP != "203.0.113.42" {
		t.Errorf("publicIp: got %q", got.PublicIP)
	}
	if got.FallbackURLs.Dashboard != "http://203.0.113.42:6790" {
		t.Errorf("fallback dashboard: got %q", got.FallbackURLs.Dashboard)
	}
	if got.FallbackURLs.API != "http://203.0.113.42:8080" {
		t.Errorf("fallback api: got %q", got.FallbackURLs.API)
	}
}

func TestHostDomain_Post_EmptyBody_400(t *testing.T) {
	url, tok := stubUpdater(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("daemon should never be hit on a malformed body")
		w.WriteHeader(http.StatusInternalServerError)
	})
	h := SetupWithOpts(t, SetupOpts{UpdaterURL: url, UpdaterToken: tok})
	owner := makeAdminUser(t, h)

	env := h.AssertStatus(http.MethodPost, "/v1/admin/host_domain",
		owner.AccessToken, map[string]any{}, http.StatusBadRequest)
	if env.Code != "bad_request" {
		t.Errorf("code: got %q want bad_request", env.Code)
	}
}

func TestHostDomain_Post_BothDomainAndPlainHttp_400(t *testing.T) {
	url, tok := stubUpdater(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("daemon should never be hit on contradictory flags")
		w.WriteHeader(http.StatusInternalServerError)
	})
	h := SetupWithOpts(t, SetupOpts{UpdaterURL: url, UpdaterToken: tok})
	owner := makeAdminUser(t, h)

	env := h.AssertStatus(http.MethodPost, "/v1/admin/host_domain",
		owner.AccessToken,
		map[string]any{"domain": "example.com", "plainHttp": true},
		http.StatusBadRequest)
	if env.Code != "bad_flags" {
		t.Errorf("code: got %q want bad_flags", env.Code)
	}
}

func TestHostDomain_Post_InvalidDomain_400(t *testing.T) {
	url, tok := stubUpdater(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("daemon should never be hit on a malformed domain")
		w.WriteHeader(http.StatusInternalServerError)
	})
	h := SetupWithOpts(t, SetupOpts{UpdaterURL: url, UpdaterToken: tok})
	owner := makeAdminUser(t, h)

	env := h.AssertStatus(http.MethodPost, "/v1/admin/host_domain",
		owner.AccessToken,
		map[string]any{"domain": "not a hostname"},
		http.StatusBadRequest)
	if env.Code != "invalid_domain" {
		t.Errorf("code: got %q want invalid_domain", env.Code)
	}
}

func TestHostDomain_Post_InvalidAcmeEmail_400(t *testing.T) {
	url, tok := stubUpdater(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("daemon should never be hit on a malformed email")
		w.WriteHeader(http.StatusInternalServerError)
	})
	h := SetupWithOpts(t, SetupOpts{UpdaterURL: url, UpdaterToken: tok})
	owner := makeAdminUser(t, h)

	env := h.AssertStatus(http.MethodPost, "/v1/admin/host_domain",
		owner.AccessToken,
		map[string]any{
			"domain":    "example.com",
			"acmeEmail": "not-an-email",
		},
		http.StatusBadRequest)
	if env.Code != "invalid_acme_email" {
		t.Errorf("code: got %q want invalid_acme_email", env.Code)
	}
}

func TestHostDomain_Post_ValidDomain_NoPublicIP_202(t *testing.T) {
	var hits int64
	var seenBody []byte
	url, tok := stubUpdater(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		if r.Method != http.MethodPost || r.URL.Path != "/reconfigure_host_domain" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		buf, _ := io.ReadAll(r.Body)
		seenBody = buf
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"started":true,"jobId":"placeholder"}`))
	})
	h := SetupWithOpts(t, SetupOpts{UpdaterURL: url, UpdaterToken: tok})
	owner := makeAdminUser(t, h)

	var got hostDomainPostResp
	h.DoJSON(http.MethodPost, "/v1/admin/host_domain",
		owner.AccessToken,
		map[string]any{
			"domain":    "newdomain.example",
			"acmeEmail": "ops@example.com",
		},
		http.StatusAccepted, &got)

	if got.JobID == "" {
		t.Fatalf("expected jobId, got %+v", got)
	}
	if got.StatusURL != "/v1/admin/host_domain/status/"+got.JobID {
		t.Errorf("statusUrl: got %q", got.StatusURL)
	}
	if got.State != "queued" {
		t.Errorf("state: got %q want queued", got.State)
	}
	if atomic.LoadInt64(&hits) != 1 {
		t.Errorf("daemon hits: got %d want 1", atomic.LoadInt64(&hits))
	}

	// Verify the daemon got the validated payload + jobId.
	var dispatched map[string]any
	if err := json.Unmarshal(seenBody, &dispatched); err != nil {
		t.Fatalf("unmarshal daemon body: %v", err)
	}
	if dispatched["jobId"] != got.JobID {
		t.Errorf("daemon jobId: got %v want %s", dispatched["jobId"], got.JobID)
	}
	if dispatched["domain"] != "newdomain.example" {
		t.Errorf("daemon domain: got %v", dispatched["domain"])
	}
	if dispatched["acmeEmail"] != "ops@example.com" {
		t.Errorf("daemon acmeEmail: got %v", dispatched["acmeEmail"])
	}

	// admin_jobs row exists with kind = reconfigure_host_domain.
	var kind, state string
	if err := h.DB.QueryRow(h.rootCtx, `
		SELECT kind, state FROM admin_jobs WHERE id = $1
	`, got.JobID).Scan(&kind, &state); err != nil {
		t.Fatalf("load admin_jobs row: %v", err)
	}
	if kind != "reconfigure_host_domain" {
		t.Errorf("kind: got %q", kind)
	}
	// State is queued because the daemon mock returned 202 but we don't
	// actually run setup.sh in tests — the mock doesn't write back.
	if state != "queued" {
		t.Errorf("state: got %q want queued (mock daemon does not write back)", state)
	}

	// Audit row was emitted.
	var count int
	if err := h.DB.QueryRow(h.rootCtx, `
		SELECT count(*) FROM audit_events
		 WHERE actor_id = $1
		   AND action = 'host_domain.change_initiated'
		   AND target_type = 'synapse'
		   AND target_id = $2
	`, owner.ID, got.JobID).Scan(&count); err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 audit row, got %d", count)
	}
}

func TestHostDomain_Post_DNSPreflight_Mismatch_400(t *testing.T) {
	url, tok := stubUpdater(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("daemon should never be hit when DNS preflight fails")
		w.WriteHeader(http.StatusInternalServerError)
	})
	h := SetupWithOpts(t, SetupOpts{
		UpdaterURL:   url,
		UpdaterToken: tok,
		PublicIP:     "203.0.113.10",
		HostDomainResolver: stubResolverFunc(func(host string) ([]string, error) {
			return []string{"198.51.100.99"}, nil
		}),
	})
	owner := makeAdminUser(t, h)

	env := h.AssertStatus(http.MethodPost, "/v1/admin/host_domain",
		owner.AccessToken,
		map[string]any{"domain": "newdomain.example"},
		http.StatusBadRequest)
	if env.Code != "dns_preflight_failed" {
		t.Errorf("code: got %q want dns_preflight_failed", env.Code)
	}
	// Surface both expected and got IPs in the message so the
	// dashboard can render a useful banner.
	if !strings.Contains(env.Message, "203.0.113.10") || !strings.Contains(env.Message, "198.51.100.99") {
		t.Errorf("message should include both ips: got %q", env.Message)
	}

	// No admin_jobs row created on validation failure.
	var count int
	if err := h.DB.QueryRow(h.rootCtx, `SELECT count(*) FROM admin_jobs`).Scan(&count); err != nil {
		t.Fatalf("count admin_jobs: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no admin_jobs rows, got %d", count)
	}
}

func TestHostDomain_Post_DNSPreflight_Match_202(t *testing.T) {
	url, tok := stubUpdater(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"started":true}`))
	})
	h := SetupWithOpts(t, SetupOpts{
		UpdaterURL:   url,
		UpdaterToken: tok,
		PublicIP:     "203.0.113.10",
		HostDomainResolver: stubResolverFunc(func(host string) ([]string, error) {
			return []string{"203.0.113.10"}, nil
		}),
	})
	owner := makeAdminUser(t, h)

	var got hostDomainPostResp
	h.DoJSON(http.MethodPost, "/v1/admin/host_domain",
		owner.AccessToken,
		map[string]any{"domain": "matches.example"},
		http.StatusAccepted, &got)
	if got.JobID == "" {
		t.Errorf("expected jobId on successful preflight")
	}
}

func TestHostDomain_Status_ExistingJob(t *testing.T) {
	url, tok := stubUpdater(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"started":true}`))
	})
	h := SetupWithOpts(t, SetupOpts{UpdaterURL: url, UpdaterToken: tok})
	owner := makeAdminUser(t, h)

	// Seed a row and pretend the daemon advanced it to running.
	var jobID string
	if err := h.DB.QueryRow(h.rootCtx, `
		INSERT INTO admin_jobs (kind, payload, state, log, started_at)
		VALUES ('reconfigure_host_domain', '{"domain":"x.test"}'::jsonb,
		        'running', 'starting setup.sh\nrunning caddy phase\n', now())
		RETURNING id
	`).Scan(&jobID); err != nil {
		t.Fatalf("insert admin_jobs: %v", err)
	}

	var got hostDomainStatusResp
	h.DoJSON(http.MethodGet, "/v1/admin/host_domain/status/"+jobID,
		owner.AccessToken, nil, http.StatusOK, &got)
	if got.ID != jobID {
		t.Errorf("id: got %q want %q", got.ID, jobID)
	}
	if got.State != "running" {
		t.Errorf("state: got %q", got.State)
	}
	if !strings.Contains(got.Log, "starting setup.sh") {
		t.Errorf("log not surfaced: %q", got.Log)
	}
	if got.StartedAt == nil {
		t.Errorf("expected startedAt to be populated for running job")
	}
}

func TestHostDomain_Status_NotFound_404(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{})
	owner := makeAdminUser(t, h)
	env := h.AssertStatus(http.MethodGet,
		"/v1/admin/host_domain/status/00000000-0000-0000-0000-000000000000",
		owner.AccessToken, nil, http.StatusNotFound)
	if env.Code != "job_not_found" {
		t.Errorf("code: got %q want job_not_found", env.Code)
	}
}

func TestHostDomain_Status_InvalidUUID_404(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{})
	owner := makeAdminUser(t, h)
	// "garbage" doesn't parse as a UUID — handler folds the postgres
	// 22P02 error into 404 instead of leaking the DB error.
	env := h.AssertStatus(http.MethodGet, "/v1/admin/host_domain/status/garbage",
		owner.AccessToken, nil, http.StatusNotFound)
	if env.Code != "job_not_found" {
		t.Errorf("code: got %q want job_not_found", env.Code)
	}
}

func TestHostDomain_Post_NotAdmin_403(t *testing.T) {
	url, tok := stubUpdater(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("daemon should NEVER be hit when caller is not admin")
		w.WriteHeader(http.StatusInternalServerError)
	})
	h := SetupWithOpts(t, SetupOpts{UpdaterURL: url, UpdaterToken: tok})
	_ = makeAdminUser(t, h)
	stranger := makeNonAdminUser(t, h)

	h.AssertStatus(http.MethodPost, "/v1/admin/host_domain",
		stranger.AccessToken, map[string]any{"plainHttp": true},
		http.StatusForbidden)
}

func TestHostDomain_Get_NotAdmin_403(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{})
	_ = makeAdminUser(t, h)
	stranger := makeNonAdminUser(t, h)

	h.AssertStatus(http.MethodGet, "/v1/admin/host_domain",
		stranger.AccessToken, nil, http.StatusForbidden)
}

func TestHostDomain_Post_NoUpdater_503(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{})
	owner := makeAdminUser(t, h)
	env := h.AssertStatus(http.MethodPost, "/v1/admin/host_domain",
		owner.AccessToken,
		map[string]any{"plainHttp": true},
		http.StatusServiceUnavailable)
	if env.Code != "updater_unavailable" && env.Code != "updater_unreachable" {
		t.Errorf("expected updater_unavailable or updater_unreachable, got %q", env.Code)
	}
}

func TestHostDomain_Post_DaemonReturns409_PassThrough(t *testing.T) {
	url, tok := stubUpdater(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"reconfigure_in_progress"}`))
	})
	h := SetupWithOpts(t, SetupOpts{UpdaterURL: url, UpdaterToken: tok})
	owner := makeAdminUser(t, h)

	env := h.AssertStatus(http.MethodPost, "/v1/admin/host_domain",
		owner.AccessToken,
		map[string]any{"plainHttp": true},
		http.StatusConflict)
	if env.Code != "reconfigure_in_progress" {
		t.Errorf("code: got %q want reconfigure_in_progress", env.Code)
	}

	// admin_jobs row should be marked failed when the daemon refused.
	var state string
	if err := h.DB.QueryRow(h.rootCtx, `
		SELECT state FROM admin_jobs ORDER BY created_at DESC LIMIT 1
	`).Scan(&state); err != nil {
		t.Fatalf("query admin_jobs: %v", err)
	}
	if state != "failed" {
		t.Errorf("expected the row to be flipped to failed, got %q", state)
	}
}
