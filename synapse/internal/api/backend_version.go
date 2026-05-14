package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	dockerprov "github.com/Iann29/synapse/internal/docker"
)

// BackendProbe reports the version string a deployment's Convex backend
// container advertises via GET /version. Pulled behind an interface so
// tests can swap in a deterministic fake without exec'ing into a real
// container.
//
// The default implementation hits the container by Docker-DNS name
// (`convex-{deployment}:3210`) over the synapse network — same path
// the proxy resolver uses, so this works in compose without any extra
// wiring. HA deployments probe replica 0; sibling replicas advertise
// the same version because they run from one image.
type BackendProbe interface {
	Probe(ctx context.Context, deploymentName string, replicaIndex int, ha bool) (string, error)
}

// HTTPBackendProbe is the production BackendProbe: HTTP GET against
// the Convex backend's /version endpoint. Used when api.RouterDeps
// leaves BackendProbe nil.
type HTTPBackendProbe struct {
	Client *http.Client
}

// NewHTTPBackendProbe returns a probe with a sane default client
// timeout. Callers can override the client (e.g. tests using
// httptest.NewServer) by constructing the struct literal directly.
func NewHTTPBackendProbe() *HTTPBackendProbe {
	return &HTTPBackendProbe{Client: &http.Client{Timeout: 3 * time.Second}}
}

func (p *HTTPBackendProbe) Probe(ctx context.Context, name string, replicaIndex int, ha bool) (string, error) {
	if p.Client == nil {
		p.Client = &http.Client{Timeout: 3 * time.Second}
	}
	url := "http://" + dockerprov.ContainerName(name, replicaIndex, ha) + ":3210/version"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("backend /version returned %s", resp.Status)
	}
	// Convex backend currently emits either bare-string body
	// ("0.5.0\n") or a {"version":"..."} JSON envelope depending on
	// the upstream build. Try JSON first; fall back to trimmed body.
	var payload struct {
		Version string `json:"version"`
	}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&payload); err == nil && payload.Version != "" {
		return payload.Version, nil
	}
	// Older / pre-precompiled builds return the raw version on the
	// first non-empty line. Reuse the body we may have partially
	// consumed by re-fetching — cheaper than buffering a tee'd reader.
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp2, err := p.Client.Do(req2)
	if err != nil {
		return "", err
	}
	defer resp2.Body.Close()
	buf := make([]byte, 256)
	n, _ := resp2.Body.Read(buf)
	raw := strings.TrimSpace(string(buf[:n]))
	if raw == "" {
		return "", errors.New("backend /version returned empty body")
	}
	return raw, nil
}

// backendVersionResp is the shape returned by the per-deployment
// version probe. Mirrors the structure of /v1/admin/version_check so
// the dashboard can render the per-deployment banner with the same
// component.
type backendVersionResp struct {
	// Version is the raw string the backend reports. Empty when the
	// probe failed; check Error for the reason.
	Version string `json:"version,omitempty"`
	// LastDeployAt is when this deployment's container was last
	// (re)created — surfaces "your backend has been running for N
	// days" without operators having to docker-inspect anything.
	LastDeployAt *time.Time `json:"lastDeployAt,omitempty"`
	// FetchedAt is when the probe was run. Cache-aware UIs can use
	// this with FromCache to render "checked 30s ago" labels.
	FetchedAt string `json:"fetchedAt"`
	FromCache bool   `json:"fromCache"`
	// Error is a short reason when the probe failed (container down,
	// DNS resolution failed, timed out). Dashboard renders a muted
	// "—" instead of the version pill when this is set.
	Error string `json:"error,omitempty"`
}

// backendVersionCacheTTL bounds how often the dashboard re-probes a
// deployment. 60 seconds is enough that the operator can refresh the
// page to see a brand-new version after a docker recreate, but rare
// enough that an over-eager dashboard tab doesn't hammer the backend.
const backendVersionCacheTTL = 60 * time.Second

type backendVersionCacheEntry struct {
	version  string
	probedAt time.Time
	err      error
}

// backendVersionCache is a process-wide LRU-ish map of deployment ID
// to last probe result. The cache is small (one entry per deployment
// the operator has clicked into) so we don't bother evicting.
type backendVersionCache struct {
	mu    sync.Mutex
	cache map[string]backendVersionCacheEntry
}

func (c *backendVersionCache) get(deploymentID string) (backendVersionCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache == nil {
		return backendVersionCacheEntry{}, false
	}
	e, ok := c.cache[deploymentID]
	if !ok {
		return backendVersionCacheEntry{}, false
	}
	if time.Since(e.probedAt) > backendVersionCacheTTL {
		delete(c.cache, deploymentID)
		return backendVersionCacheEntry{}, false
	}
	return e, true
}

func (c *backendVersionCache) put(deploymentID string, e backendVersionCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache == nil {
		c.cache = make(map[string]backendVersionCacheEntry)
	}
	c.cache[deploymentID] = e
}

// getBackendVersion handles GET /v1/deployments/{name}/backend_version.
// Reads through the per-deployment probe cache; on miss runs the probe
// and stores the result for backendVersionCacheTTL. Always returns 200
// — probe failures surface in the response body's Error field so the
// dashboard can render a graceful empty state instead of a broken
// "couldn't load" banner.
func (h *DeploymentsHandler) getBackendVersion(w http.ResponseWriter, r *http.Request) {
	d, _, _, _, ok := h.loadDeploymentForRequest(w, r)
	if !ok {
		return
	}

	resp := backendVersionResp{
		LastDeployAt: d.LastDeployAt,
		FetchedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	// Adopted deployments aren't reachable via Docker DNS — we have no
	// container to probe. Return the row metadata and skip the probe.
	if d.Adopted {
		resp.Error = "adopted_deployment"
		writeJSON(w, http.StatusOK, resp)
		return
	}

	if cached, ok := h.versionCache.get(d.ID); ok {
		resp.Version = cached.version
		resp.FromCache = true
		resp.FetchedAt = cached.probedAt.UTC().Format(time.RFC3339)
		if cached.err != nil {
			resp.Error = trimProbeError(cached.err)
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	probe := h.BackendProbe
	if probe == nil {
		probe = NewHTTPBackendProbe()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	version, err := probe.Probe(ctx, d.Name, 0, d.HAEnabled)
	now := time.Now()
	h.versionCache.put(d.ID, backendVersionCacheEntry{
		version:  version,
		probedAt: now,
		err:      err,
	})
	resp.Version = version
	resp.FetchedAt = now.UTC().Format(time.RFC3339)
	if err != nil {
		resp.Error = trimProbeError(err)
		slog.Default().Debug("backend_version probe failed",
			"deployment_id", d.ID, "name", d.Name, "err", err)
	}
	writeJSON(w, http.StatusOK, resp)
}

// trimProbeError keeps the dashboard's "couldn't reach backend" string
// short and machine-readable. Full error text lands in the server log
// at debug level for operator triage.
func trimProbeError(err error) string {
	s := err.Error()
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
