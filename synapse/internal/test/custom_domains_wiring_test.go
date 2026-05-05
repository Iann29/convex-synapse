package synapsetest

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Iann29/synapse/internal/proxy"
)

// Tests for the v1.1 custom-domains wiring: proxy host-header dispatch
// for deployment_domains rows, TLS-ask gate covering custom domains,
// and the deployment-restart-on-add/delete/verify flow that refreshes
// CORS_ALLOWED_ORIGINS.
//
// All tests use Setup(t)/SetupWithOpts(t, ...) plus FakeDocker, so they
// run inside the existing harness — no real Docker daemon involved. The
// "restart triggered" assertions inspect FakeDocker.RecreatedSpecs() to
// confirm the handler called Recreate (the CORS rebuild path).

// seedActiveDomain inserts a deployment_domains row directly so tests
// can drive proxy/TLS lookups without going through DNS preflight (the
// harness leaves PublicIP empty, so a happy-path /domains POST always
// lands as 'pending'). Returns the inserted row's id.
func seedActiveDomain(t *testing.T, h *Harness, deploymentID, domain, role string) string {
	t.Helper()
	var id string
	if err := h.DB.QueryRow(context.Background(), `
		INSERT INTO deployment_domains (deployment_id, domain, role, status,
		                                dns_verified_at)
		VALUES ($1, $2, $3, 'active', now())
		RETURNING id
	`, deploymentID, domain, role).Scan(&id); err != nil {
		t.Fatalf("seed active domain: %v", err)
	}
	return id
}

// upstreamFromAddr returns "host:port" extracted from an httptest.URL.
func upstreamHostPort(t *testing.T, srv *httptest.Server) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	return host + ":" + portStr, port
}

// ---------------- TLS gate ----------------

func TestTLSAsk_AcceptsActiveCustomDomain(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{BaseDomain: "synapse.example.com"})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "TLS Custom Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "TLS Custom")
	depID := h.SeedDeployment(proj.ID, "tls-cd-1111", "prod", "running", true,
		owner.ID, 4501, "")
	seedActiveDomain(t, h, depID, "api.fechasul.com.br", "api")

	q := url.Values{"domain": {"api.fechasul.com.br"}}
	resp := h.Do(http.MethodGet, "/v1/internal/tls_ask?"+q.Encode(), "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("tls_ask custom domain: status=%d want 200", resp.StatusCode)
	}
}

func TestTLSAsk_RejectsUnknownCustomDomain(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{BaseDomain: "synapse.example.com"})
	q := url.Values{"domain": {"never-registered.example.com"}}
	resp := h.Do(http.MethodGet, "/v1/internal/tls_ask?"+q.Encode(), "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("tls_ask unknown custom: status=%d want 404", resp.StatusCode)
	}
}

func TestTLSAsk_RejectsPendingCustomDomain(t *testing.T) {
	// status='pending' rows must NOT pass the gate — Caddy should
	// not request a cert for a host that hasn't passed DNS preflight.
	h := SetupWithOpts(t, SetupOpts{BaseDomain: "synapse.example.com"})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Pending TLS")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Pending TLS")
	depID := h.SeedDeployment(proj.ID, "tls-pend-2222", "prod", "running", true,
		owner.ID, 4502, "")
	if _, err := h.DB.Exec(context.Background(), `
		INSERT INTO deployment_domains (deployment_id, domain, role, status)
		VALUES ($1, $2, 'api', 'pending')
	`, depID, "wait.example.com"); err != nil {
		t.Fatalf("seed pending: %v", err)
	}

	q := url.Values{"domain": {"wait.example.com"}}
	resp := h.Do(http.MethodGet, "/v1/internal/tls_ask?"+q.Encode(), "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("tls_ask pending custom: status=%d want 404", resp.StatusCode)
	}
}

func TestTLSAsk_AcceptsCustomDomainWithoutBaseDomain(t *testing.T) {
	// BaseDomain="" — the wildcard branch is fully off, but custom
	// domains still work when registered. Important for installs
	// that DON'T wire the wildcard but want per-deployment domains.
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Custom-only Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Custom-only")
	depID := h.SeedDeployment(proj.ID, "tls-only-3333", "prod", "running", true,
		owner.ID, 4503, "")
	seedActiveDomain(t, h, depID, "edge.example.com", "api")

	q := url.Values{"domain": {"edge.example.com"}}
	resp := h.Do(http.MethodGet, "/v1/internal/tls_ask?"+q.Encode(), "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("tls_ask custom-only: status=%d want 200", resp.StatusCode)
	}
}

// ---------------- Proxy host-header routing ----------------

func TestProxy_RoutesActiveCustomDomain(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Proxy CD")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Proxy CD")

	var hitPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)
	_, port := upstreamHostPort(t, upstream)

	depID := h.SeedDeployment(proj.ID, "px-cd-4444", "prod", "running", true,
		owner.ID, port, "")
	seedActiveDomain(t, h, depID, "api.brand.example", "api")

	resolver := &proxy.Resolver{DB: h.DB, UseNetworkDNS: false, CacheTTL: time.Second}
	srv := httptest.NewServer(proxy.Handler(resolver, nil, ""))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/whatever", nil)
	req.Host = "api.brand.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("custom-domain api route: status=%d", resp.StatusCode)
	}
	if hitPath != "/api/whatever" {
		t.Errorf("custom-domain api: upstream saw %q", hitPath)
	}
}

func TestProxy_RoutesDashboardCustomDomain(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Proxy DashCD")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Proxy DashCD")

	// Dashboard upstream — dedicated server because the proxy will
	// treat role='dashboard' as a fixed Resolver.DashboardAddr.
	var dashHitPath string
	dashUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dashHitPath = r.URL.Path
		_, _ = w.Write([]byte("dash"))
	}))
	t.Cleanup(dashUp.Close)
	dashAddr, _ := upstreamHostPort(t, dashUp)

	// Backend upstream — proves role='api' goes to the deployment,
	// NOT the dashboard, when both are wired.
	apiUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("api"))
	}))
	t.Cleanup(apiUp.Close)
	_, apiPort := upstreamHostPort(t, apiUp)

	depID := h.SeedDeployment(proj.ID, "px-dash-5555", "prod", "running", true,
		owner.ID, apiPort, "")
	seedActiveDomain(t, h, depID, "admin.brand.example", "dashboard")

	resolver := &proxy.Resolver{
		DB:            h.DB,
		UseNetworkDNS: false,
		CacheTTL:      time.Second,
		DashboardAddr: dashAddr,
	}
	srv := httptest.NewServer(proxy.Handler(resolver, nil, ""))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/teams", nil)
	req.Host = "admin.brand.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("dashboard custom-domain: status=%d body=%s", resp.StatusCode, body)
	}
	if string(body) != "dash" {
		t.Errorf("dashboard custom-domain: body=%q want dash (forwarded to wrong upstream?)", body)
	}
	if dashHitPath != "/teams" {
		t.Errorf("dashboard custom-domain: dashUp saw path %q want /teams", dashHitPath)
	}
}

func TestProxy_DashboardRole_503WithoutAddress(t *testing.T) {
	// role='dashboard' configured but Resolver.DashboardAddr empty
	// → 503 dashboard_not_configured. Better than letting a bogus
	// address bleed into the user's request.
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Dash 503")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Dash 503")
	depID := h.SeedDeployment(proj.ID, "dash-503-6666", "prod", "running", true,
		owner.ID, 6603, "")
	seedActiveDomain(t, h, depID, "console.example.com", "dashboard")

	resolver := &proxy.Resolver{DB: h.DB, UseNetworkDNS: false, CacheTTL: time.Second}
	srv := httptest.NewServer(proxy.Handler(resolver, nil, ""))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/anything", nil)
	req.Host = "console.example.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("dashboard 503: status=%d want 503", resp.StatusCode)
	}
}

func TestProxy_404OnInactiveCustomDomain(t *testing.T) {
	// status='pending' / 'failed' rows must NOT route — only 'active'
	// counts. Belt-and-suspenders against the dashboard accidentally
	// flipping a row early.
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Inactive CD")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Inactive CD")
	depID := h.SeedDeployment(proj.ID, "px-inactive-7777", "prod", "running", true,
		owner.ID, 4507, "")
	if _, err := h.DB.Exec(context.Background(), `
		INSERT INTO deployment_domains (deployment_id, domain, role, status)
		VALUES ($1, 'pending.example.com', 'api', 'pending'),
		       ($1, 'failed.example.com',  'api', 'failed')
	`, depID); err != nil {
		t.Fatalf("seed inactive rows: %v", err)
	}

	resolver := &proxy.Resolver{DB: h.DB, UseNetworkDNS: false, CacheTTL: time.Second}
	srv := httptest.NewServer(proxy.Handler(resolver, nil, ""))
	t.Cleanup(srv.Close)

	for _, host := range []string{"pending.example.com", "failed.example.com"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/x", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", host, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("host %s: status=%d want 404 (inactive)", host, resp.StatusCode)
		}
	}
}

func TestProxy_DomainCacheInvalidation(t *testing.T) {
	// Add active row → request routes 200; delete the row directly
	// (bypassing the API so we don't auto-invalidate); request still
	// 200 because the cache holds the binding; Invalidate → next
	// request 404. Mirrors the contract that POST/DELETE on the
	// domains API call InvalidateDomain after the DB write.
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Cache CD")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Cache CD")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)
	_, port := upstreamHostPort(t, upstream)
	depID := h.SeedDeployment(proj.ID, "px-cache-8888", "prod", "running", true,
		owner.ID, port, "")
	seedActiveDomain(t, h, depID, "cached.example.com", "api")

	resolver := &proxy.Resolver{DB: h.DB, UseNetworkDNS: false, CacheTTL: time.Hour}
	srv := httptest.NewServer(proxy.Handler(resolver, nil, ""))
	t.Cleanup(srv.Close)

	doGet := func() int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/x", nil)
		req.Host = "cached.example.com"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if got := doGet(); got != http.StatusOK {
		t.Fatalf("first GET: status=%d want 200", got)
	}
	// Delete the row directly. With the long TTL the cached binding
	// keeps the request flowing until we invalidate.
	if _, err := h.DB.Exec(context.Background(),
		`DELETE FROM deployment_domains WHERE deployment_id = $1`, depID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := doGet(); got != http.StatusOK {
		t.Errorf("post-delete (cached) GET: status=%d want 200", got)
	}
	resolver.InvalidateDomain("cached.example.com")
	if got := doGet(); got != http.StatusNotFound {
		t.Errorf("post-invalidate GET: status=%d want 404", got)
	}
}

// ---------------- Restart-on-domain-change ----------------

// expectRestart polls FakeDocker.RecreatedSpecs() for up to ~1s,
// returning the recorded specs once at least one matches the target
// deployment name. Avoids a race against the audit goroutine that may
// run after the response is written.
func expectRestart(t *testing.T, fd *FakeDocker, deploymentName string) []string {
	t.Helper()
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		var matched []string
		for _, s := range fd.RecreatedSpecs() {
			if s.Name == deploymentName {
				matched = append(matched, s.EnvVars["CORS_ALLOWED_ORIGINS"])
			}
		}
		if len(matched) > 0 {
			return matched
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected at least one Recreate call for %q; got %v",
		deploymentName, fd.RecreatedSpecs())
	return nil
}

func TestDomains_Restart_OnDelete_RestartTriggered(t *testing.T) {
	// Pre-seed two active rows on the same deployment, then delete
	// one through the API. The handler should call Recreate, and
	// the recreated CORS list should include the surviving row's
	// origin but NOT the one that was just deleted.
	h := Setup(t)
	f := newDomainsFixture(t, h, "dom-cors-list-aaaa", 4611)

	keepID := seedActiveDomain(t, h, f.deploymentID, "keep.example.com", "api")
	dropID := seedActiveDomain(t, h, f.deploymentID, "drop.example.com", "api")
	_ = keepID

	h.AssertStatus(http.MethodDelete,
		"/v1/deployments/"+f.deployment+"/domains/"+dropID,
		f.owner.AccessToken, nil, http.StatusNoContent)

	cors := expectRestart(t, h.Docker, f.deployment)
	if len(cors) == 0 {
		t.Fatalf("expected at least one Recreate call")
	}
	last := cors[len(cors)-1]
	if !strings.Contains(last, "https://keep.example.com") {
		t.Errorf("post-delete CORS=%q want to contain https://keep.example.com", last)
	}
	if strings.Contains(last, "drop.example.com") {
		t.Errorf("post-delete CORS=%q must NOT contain drop.example.com", last)
	}
}

func TestDomains_NoRestart_OnVerifyStayingPending(t *testing.T) {
	// Negative case: /verify must NOT trigger a recreate when the
	// row stays 'pending' (DNS still doesn't match). PublicIP unset
	// in the harness → verifyDomainDNS always returns 'pending', so
	// the handler's prior=pending → new=pending branch covers the
	// "no flip, no restart" expectation directly.
	h := Setup(t) // PublicIP unset → /verify always returns 'pending'
	f := newDomainsFixture(t, h, "dom-no-restart-1111", 4602)

	var created domainResp
	h.DoJSON(http.MethodPost, "/v1/deployments/"+f.deployment+"/domains",
		f.owner.AccessToken,
		map[string]any{"domain": "still-pending.example.com", "role": "api"},
		http.StatusCreated, &created)
	if created.Status != "pending" {
		t.Fatalf("expected pending, got %q", created.Status)
	}

	var verified domainResp
	h.DoJSON(http.MethodPost,
		"/v1/deployments/"+f.deployment+"/domains/"+created.ID+"/verify",
		f.owner.AccessToken, nil, http.StatusOK, &verified)

	if verified.Status != "pending" {
		t.Errorf("expected verify to keep pending, got %q", verified.Status)
	}
	if verified.DeploymentRestartTriggered {
		t.Errorf("expected deploymentRestartTriggered=false on no-flip, got true")
	}
	// And FakeDocker.Recreate should NEVER have been called for this
	// deployment.
	for _, s := range h.Docker.RecreatedSpecs() {
		if s.Name == f.deployment {
			t.Errorf("did not expect Recreate for %s on pending verify; got %+v",
				f.deployment, s)
		}
	}
}

func TestDomains_Restart_OnDeleteActive(t *testing.T) {
	h := Setup(t)
	f := newDomainsFixture(t, h, "dom-restart-d-2222", 4603)

	// Seed an 'active' row directly so we have something to delete
	// without depending on DNS preflight.
	id := seedActiveDomain(t, h, f.deploymentID, "del-active.example.com", "api")

	h.AssertStatus(http.MethodDelete,
		"/v1/deployments/"+f.deployment+"/domains/"+id,
		f.owner.AccessToken, nil, http.StatusNoContent)

	corsValues := expectRestart(t, h.Docker, f.deployment)
	// After the delete the live set is empty → CORS env should not
	// be set in the recreated spec.
	for _, v := range corsValues {
		if v != "" {
			t.Errorf("post-delete CORS=%q want empty", v)
		}
	}
}

func TestDomains_NoRestart_OnDeletePending(t *testing.T) {
	// status='pending' rows never made it into the live CORS env, so
	// deleting one must NOT trigger a recreate.
	h := Setup(t)
	f := newDomainsFixture(t, h, "dom-norestart-d-3333", 4604)

	var created domainResp
	h.DoJSON(http.MethodPost, "/v1/deployments/"+f.deployment+"/domains",
		f.owner.AccessToken,
		map[string]any{"domain": "del-pending.example.com", "role": "api"},
		http.StatusCreated, &created)
	if created.Status != "pending" {
		t.Fatalf("expected pending, got %q", created.Status)
	}

	h.AssertStatus(http.MethodDelete,
		"/v1/deployments/"+f.deployment+"/domains/"+created.ID,
		f.owner.AccessToken, nil, http.StatusNoContent)

	// Sanity-give the audit goroutine a moment, then assert we never
	// saw a Recreate.
	time.Sleep(100 * time.Millisecond)
	for _, s := range h.Docker.RecreatedSpecs() {
		if s.Name == f.deployment {
			t.Errorf("did not expect Recreate for delete-pending; got %+v", s)
		}
	}
}

func TestDomains_NoRestart_OnHADeployment(t *testing.T) {
	// HA deployments are deliberately skipped by rebuildCORSAndRestart —
	// per-replica orchestration is out of scope for v1.1. Verify the
	// handler logs + returns deploymentRestartTriggered=false even
	// when an active row's deleted.
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "HA Domain Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "HA Domain")
	depID := h.SeedDeployment(proj.ID, "ha-restart-skip-4444", "prod", "running",
		true, owner.ID, 4605, "")
	// Promote to HA after the fact — SeedDeployment seeds single-replica.
	if _, err := h.DB.Exec(context.Background(),
		`UPDATE deployments SET ha_enabled = TRUE, replica_count = 2 WHERE id = $1`, depID); err != nil {
		t.Fatalf("promote ha: %v", err)
	}
	id := seedActiveDomain(t, h, depID, "ha-skip.example.com", "api")

	h.AssertStatus(http.MethodDelete,
		"/v1/deployments/ha-restart-skip-4444/domains/"+id,
		owner.AccessToken, nil, http.StatusNoContent)

	time.Sleep(100 * time.Millisecond)
	for _, s := range h.Docker.RecreatedSpecs() {
		if s.Name == "ha-restart-skip-4444" {
			t.Errorf("did not expect Recreate for HA deployment; got %+v", s)
		}
	}
}
