package synapsetest

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Iann29/synapse/internal/proxy"
)

// TestProxy_HA_Failover wires two upstream servers as replica 0 and
// replica 1, kills replica 0, and confirms the proxy quietly retries
// against replica 1.
func TestProxy_HA_Failover(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "HA Proxy")
	project := createProject(t, h, owner.AccessToken, team.Slug, "App")

	var (
		r0Hits atomic.Int32
		r1Hits atomic.Int32
	)
	r0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r0Hits.Add(1)
		_, _ = w.Write([]byte("r0:" + r.URL.Path))
	}))
	r1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r1Hits.Add(1)
		_, _ = w.Write([]byte("r1:" + r.URL.Path))
	}))
	t.Cleanup(r0.Close)
	t.Cleanup(r1.Close)

	// Seed a deployment + a second replica row. SeedDeployment writes
	// replica_index=0 with port=upstream.r0; we add replica_index=1
	// pointing at r1 by hand.
	depID := h.SeedDeployment(project.ID, "ha-prx-2200", "prod", "running",
		false, owner.ID, mustPort(t, r0.URL), "k")
	if _, err := h.DB.Exec(h.rootCtx,
		`UPDATE deployments SET ha_enabled = true, replica_count = 2 WHERE id = $1`,
		depID,
	); err != nil {
		t.Fatalf("flip ha: %v", err)
	}
	if _, err := h.DB.Exec(h.rootCtx, `
		INSERT INTO deployment_replicas (deployment_id, replica_index, container_id, host_port, status)
		VALUES ($1, 1, 'fake-container-ha-prx-2200-1', $2, 'running')
	`, depID, mustPort(t, r1.URL)); err != nil {
		t.Fatalf("insert replica 1: %v", err)
	}

	resolver := &proxy.Resolver{DB: h.DB, UseNetworkDNS: false, CacheTTL: time.Second}
	srv := httptest.NewServer(proxy.Handler(resolver, nil, ""))
	t.Cleanup(srv.Close)

	// Sanity: with both replicas alive the proxy lands on replica 0
	// (last_seen_active_at NULLs sort last; ties resolved by
	// replica_index ASC). Body must come back from r0.
	body := getBody(t, srv.URL+"/d/ha-prx-2200/api/anything")
	if !strings.HasPrefix(body, "r0:") {
		t.Errorf("first hit: got %q want r0:* (replicas alive)", body)
	}

	// Kill replica 0; close the server to simulate "container gone".
	r0.Close()
	resolver.Invalidate("ha-prx-2200")

	// Same request; proxy should silently fail-over to r1 and the
	// caller should see r1's body.
	body = getBody(t, srv.URL+"/d/ha-prx-2200/api/anything")
	if !strings.HasPrefix(body, "r1:") {
		t.Errorf("after killing r0: got %q want r1:* (failover)", body)
	}
	if r1Hits.Load() < 1 {
		t.Error("expected r1 to receive the failover hit")
	}
}

// TestProxy_HA_NoReplicas exercises the 503 path — when every replica
// is gone, the caller gets a no_replicas error instead of a 502 spam
// trying dead containers.
func TestProxy_HA_NoReplicas(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Empty HA")
	project := createProject(t, h, owner.AccessToken, team.Slug, "App")

	depID := h.SeedDeployment(project.ID, "no-rep-3300", "prod", "running",
		false, owner.ID, 4001, "k")
	if _, err := h.DB.Exec(h.rootCtx,
		`UPDATE deployments SET ha_enabled = true, replica_count = 2 WHERE id = $1`,
		depID); err != nil {
		t.Fatalf("flip ha: %v", err)
	}
	// Drop the auto-seeded replica row so this deployment has zero
	// running replicas. Backfill ran for the SeedDeployment so we
	// delete instead of update.
	if _, err := h.DB.Exec(h.rootCtx,
		`UPDATE deployment_replicas SET status = 'failed' WHERE deployment_id = $1`,
		depID); err != nil {
		t.Fatalf("flip replica status: %v", err)
	}

	resolver := &proxy.Resolver{DB: h.DB, UseNetworkDNS: false, CacheTTL: time.Second}
	srv := httptest.NewServer(proxy.Handler(resolver, nil, ""))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/d/no-rep-3300/api/anything")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: got %d want 503", resp.StatusCode)
	}
}

// TestResolver_HA_OrdersByLastSeen confirms the picker prefers the
// replica with the most-recent last_seen_active_at, falling back to
// ascending replica_index for ties.
func TestResolver_HA_OrdersByLastSeen(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Order")
	project := createProject(t, h, owner.AccessToken, team.Slug, "App")
	depID := h.SeedDeployment(project.ID, "ord-rep-4400", "prod", "running",
		false, owner.ID, 7000, "k")
	if _, err := h.DB.Exec(h.rootCtx,
		`UPDATE deployments SET ha_enabled = true, replica_count = 2 WHERE id = $1`,
		depID); err != nil {
		t.Fatalf("flip ha: %v", err)
	}
	if _, err := h.DB.Exec(h.rootCtx, `
		INSERT INTO deployment_replicas (deployment_id, replica_index, container_id, host_port, status)
		VALUES ($1, 1, 'c1', 7001, 'running')
	`, depID); err != nil {
		t.Fatalf("insert replica 1: %v", err)
	}

	resolver := &proxy.Resolver{DB: h.DB, UseNetworkDNS: false, CacheTTL: 10 * time.Millisecond}

	addrs, err := resolver.ResolveAll(context.Background(), "ord-rep-4400")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(addrs) != 2 {
		t.Fatalf("got %d addrs, want 2", len(addrs))
	}
	// Ties on last_seen_active_at (both NULL) → replica_index ASC →
	// replica 0 (port 7000) first.
	if addrs[0] != "127.0.0.1:7000" {
		t.Errorf("with both NULL last_seen: got %s, want :7000 first", addrs[0])
	}

	// Mark replica 1 as recently active. Wait for cache to expire,
	// re-resolve, replica 1 should now lead.
	if _, err := h.DB.Exec(h.rootCtx, `
		UPDATE deployment_replicas
		   SET last_seen_active_at = now()
		 WHERE deployment_id = $1 AND replica_index = 1
	`, depID); err != nil {
		t.Fatalf("set last_seen: %v", err)
	}
	resolver.Invalidate("ord-rep-4400")

	addrs, err = resolver.ResolveAll(context.Background(), "ord-rep-4400")
	if err != nil {
		t.Fatalf("re-resolve: %v", err)
	}
	if addrs[0] != "127.0.0.1:7001" {
		t.Errorf("after marking r1 active: got %s, want :7001 first", addrs[0])
	}
}

// ---------- helpers ----------

func mustPort(t *testing.T, urlStr string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(urlStr, "http://"))
	if err != nil {
		t.Fatalf("split %q: %v", urlStr, err)
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("atoi: %v", err)
	}
	return p
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}
