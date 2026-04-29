package synapsetest

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Iann29/synapse/internal/proxy"
)

// Spin up a local HTTP server, treat it as the "convex backend" the proxy
// forwards to, seed a deployment row pointing at it, and round-trip a
// request through proxy.Handler.
func TestProxy_ForwardsToResolvedAddress(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Proxied")
	project := createProject(t, h, owner.AccessToken, team.Slug, "App")

	// Upstream that records what it received.
	var hitPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("upstream-ok:" + r.URL.Path))
	}))
	t.Cleanup(upstream.Close)

	// Extract the port the test server picked.
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	if err != nil {
		t.Fatalf("split upstream URL: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port: %v", err)
	}

	// Seed a deployment row pointing at the upstream port.
	h.SeedDeployment(project.ID, "px-cat-1234", "dev", "running", false, owner.ID, port, "k")

	resolver := &proxy.Resolver{DB: h.DB, UseNetworkDNS: false, CacheTTL: time.Second}
	srv := httptest.NewServer(proxy.Handler(resolver, nil))
	t.Cleanup(srv.Close)

	// GET /d/px-cat-1234/api/check_admin_key → upstream sees /api/check_admin_key
	resp, err := http.Get(srv.URL + "/d/px-cat-1234/api/check_admin_key")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		t.Errorf("status: got %d, want 200; body=%s", resp.StatusCode, string(body))
	}
	if hitPath != "/api/check_admin_key" {
		t.Errorf("upstream saw %q; want /api/check_admin_key", hitPath)
	}
	if !strings.HasPrefix(string(body), "upstream-ok:") {
		t.Errorf("body: got %q; want prefix upstream-ok:", string(body))
	}
}

func TestProxy_404OnMissingDeployment(t *testing.T) {
	h := Setup(t)
	resolver := &proxy.Resolver{DB: h.DB, UseNetworkDNS: false, CacheTTL: time.Second}
	srv := httptest.NewServer(proxy.Handler(resolver, nil))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/d/does-not-exist/anything")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", resp.StatusCode)
	}
}

// A "deleted" or "failed" deployment row must not be reachable through the proxy.
func TestProxy_404ForNonRunningDeployment(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Px2")
	project := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(project.ID, "down-fox-1234", "dev", "failed", false, owner.ID, 9999, "k")

	resolver := &proxy.Resolver{DB: h.DB, UseNetworkDNS: false, CacheTTL: time.Second}
	srv := httptest.NewServer(proxy.Handler(resolver, nil))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/d/down-fox-1234/api/check_admin_key")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", resp.StatusCode)
	}
}

// The Resolver caches name → address until TTL expires.
func TestProxy_ResolverCaches(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Cache")
	project := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(project.ID, "cached-owl-1234", "dev", "running", false, owner.ID, 4242, "k")

	resolver := &proxy.Resolver{DB: h.DB, UseNetworkDNS: false, CacheTTL: time.Hour}

	got1, err := resolver.Resolve(context.Background(), "cached-owl-1234")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got1 != "127.0.0.1:4242" {
		t.Errorf("got %q; want 127.0.0.1:4242", got1)
	}

	// Mutate the row directly. With cache active, Resolve should still return the OLD address.
	if _, err := h.DB.Exec(context.Background(),
		`UPDATE deployments SET host_port = 5252 WHERE name = 'cached-owl-1234'`); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := resolver.Resolve(context.Background(), "cached-owl-1234")
	if got2 != "127.0.0.1:4242" {
		t.Errorf("cache miss: got %q; want stale 127.0.0.1:4242", got2)
	}

	// Invalidate → next resolve hits DB and sees the new port.
	resolver.Invalidate("cached-owl-1234")
	got3, _ := resolver.Resolve(context.Background(), "cached-owl-1234")
	if got3 != "127.0.0.1:5252" {
		t.Errorf("post-invalidate: got %q; want 127.0.0.1:5252", got3)
	}
}
