package synapsetest

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Iann29/synapse/internal/api"
)

// stubBackendProbe is the minimal api.BackendProbe used by these tests
// — records call count and either returns a canned version or an error.
type stubBackendProbe struct {
	calls   int32
	version string
	err     error
}

func (s *stubBackendProbe) Probe(_ context.Context, _ string, _ int, _ bool) (string, error) {
	atomic.AddInt32(&s.calls, 1)
	return s.version, s.err
}

type backendVersionResp struct {
	Version      string     `json:"version,omitempty"`
	LastDeployAt *time.Time `json:"lastDeployAt,omitempty"`
	FetchedAt    string     `json:"fetchedAt"`
	FromCache    bool       `json:"fromCache"`
	Error        string     `json:"error,omitempty"`
}

// TestBackendVersion_ProbesAndReturnsVersion: happy path. The handler
// hands the probe a name + replica index, the probe returns a version,
// the response carries it back to the caller.
func TestBackendVersion_ProbesAndReturnsVersion(t *testing.T) {
	probe := &stubBackendProbe{version: "0.5.0"}
	h := SetupWithOpts(t, SetupOpts{BackendProbe: probe})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Version Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "VersionProj")
	h.SeedDeployment(proj.ID, "neat-mole-1234", "dev", "running", true, owner.ID, 3211, "")

	var got backendVersionResp
	h.DoJSON(http.MethodGet, "/v1/deployments/neat-mole-1234/backend_version",
		owner.AccessToken, nil, http.StatusOK, &got)

	if got.Version != "0.5.0" {
		t.Errorf("version: got %q want %q", got.Version, "0.5.0")
	}
	if got.Error != "" {
		t.Errorf("expected no error, got %q", got.Error)
	}
	if got.FromCache {
		t.Errorf("first call should not be cache hit")
	}
	if atomic.LoadInt32(&probe.calls) != 1 {
		t.Errorf("expected 1 probe call, got %d", probe.calls)
	}
}

// TestBackendVersion_CachesProbeResult: the second call within the TTL
// window must reuse the first probe — confirmed by call count staying
// at 1 and fromCache=true on the response.
func TestBackendVersion_CachesProbeResult(t *testing.T) {
	probe := &stubBackendProbe{version: "0.5.1"}
	h := SetupWithOpts(t, SetupOpts{BackendProbe: probe})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Cache Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "CacheProj")
	h.SeedDeployment(proj.ID, "cool-otter-2222", "dev", "running", true, owner.ID, 3212, "")

	var first backendVersionResp
	h.DoJSON(http.MethodGet, "/v1/deployments/cool-otter-2222/backend_version",
		owner.AccessToken, nil, http.StatusOK, &first)
	if first.FromCache {
		t.Errorf("first call should not be cache hit")
	}

	var second backendVersionResp
	h.DoJSON(http.MethodGet, "/v1/deployments/cool-otter-2222/backend_version",
		owner.AccessToken, nil, http.StatusOK, &second)
	if !second.FromCache {
		t.Errorf("second call should be cache hit")
	}
	if second.Version != "0.5.1" {
		t.Errorf("cached version: got %q want %q", second.Version, "0.5.1")
	}
	if atomic.LoadInt32(&probe.calls) != 1 {
		t.Errorf("expected 1 probe call across both requests, got %d", probe.calls)
	}
}

// TestBackendVersion_ProbeErrorSurfacesGracefully: a probe failure
// must produce a 200 with the error in the body, not a 5xx. The
// dashboard depends on this — version-banner failures shouldn't break
// the page.
func TestBackendVersion_ProbeErrorSurfacesGracefully(t *testing.T) {
	probe := &stubBackendProbe{err: errors.New("dial tcp: connection refused")}
	h := SetupWithOpts(t, SetupOpts{BackendProbe: probe})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Err Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "ErrProj")
	h.SeedDeployment(proj.ID, "sad-fish-3333", "dev", "running", true, owner.ID, 3213, "")

	var got backendVersionResp
	h.DoJSON(http.MethodGet, "/v1/deployments/sad-fish-3333/backend_version",
		owner.AccessToken, nil, http.StatusOK, &got)

	if got.Error == "" {
		t.Errorf("expected probe error to surface, got empty Error field")
	}
	if got.Version != "" {
		t.Errorf("version should be empty on probe failure, got %q", got.Version)
	}
}

// TestBackendVersion_AdoptedDeploymentSkipsProbe: adopted backends
// aren't reachable via Docker DNS — the handler must short-circuit and
// return a special-cased "adopted_deployment" error string instead of
// emitting a probe call that would always fail.
func TestBackendVersion_AdoptedDeploymentSkipsProbe(t *testing.T) {
	probe := &stubBackendProbe{version: "should not be called"}
	h := SetupWithOpts(t, SetupOpts{BackendProbe: probe})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Adopt Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "AdoptProj")
	h.SeedDeployment(proj.ID, "ext-eagle-4444", "dev", "running", true, owner.ID, 3214, "")
	if _, err := h.DB.Exec(h.rootCtx,
		`UPDATE deployments SET adopted = true WHERE name = $1`,
		"ext-eagle-4444"); err != nil {
		t.Fatalf("flip adopted: %v", err)
	}

	var got backendVersionResp
	h.DoJSON(http.MethodGet, "/v1/deployments/ext-eagle-4444/backend_version",
		owner.AccessToken, nil, http.StatusOK, &got)

	if got.Error != "adopted_deployment" {
		t.Errorf("error: got %q want %q", got.Error, "adopted_deployment")
	}
	if got.Version != "" {
		t.Errorf("version should be empty for adopted deployment, got %q", got.Version)
	}
	if atomic.LoadInt32(&probe.calls) != 0 {
		t.Errorf("expected 0 probe calls for adopted deployment, got %d", probe.calls)
	}
}

// TestBackendVersion_RequiresAccess: non-members can't read another
// project's backend version. Same access-control surface as
// /v1/deployments/{name}.
func TestBackendVersion_RequiresAccess(t *testing.T) {
	probe := &stubBackendProbe{version: "0.5.0"}
	h := SetupWithOpts(t, SetupOpts{BackendProbe: probe})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Owner Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "OwnerProj")
	h.SeedDeployment(proj.ID, "private-ant-5555", "dev", "running", true, owner.ID, 3215, "")

	stranger := h.RegisterRandomUser()
	env := h.AssertStatus(http.MethodGet, "/v1/deployments/private-ant-5555/backend_version",
		stranger.AccessToken, nil, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("got code %q want forbidden", env.Code)
	}
}

// Quiet the unused-API-package import warning if the file gets pared
// down later; keeps the tooling happy without an explicit blank ident.
var _ api.BackendProbe = (*stubBackendProbe)(nil)
