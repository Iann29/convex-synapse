package synapsetest

import (
	"context"
	"net/http"
	"testing"
	"time"

	dockerprov "github.com/Iann29/synapse/internal/docker"
)

// TestConvexOrigin_BakedFromPublicURL: v1.6.15. The provisioner must
// emit CONVEX_CLOUD_ORIGIN / CONVEX_SITE_ORIGIN matching the CLI-
// reachable URL the dashboard hands operators — not the legacy
// "http://127.0.0.1:<port>" form which leaks into function-spec.url
// and breaks CONVEX_SITE_URL inside httpAction handlers.
//
// With PublicURL set + ProxyEnabled=true the worker should still hand
// the CLI-anchored URL ("<host>:<HostPort>") to the spec because the
// official `npx convex` CLI strips paths via new URL("/api/...", base).
func TestConvexOrigin_BakedFromPublicURL(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		PublicURL:    "https://synapse.example.com",
		ProxyEnabled: true,
	})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Origin Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "OriginProj")

	var got deploymentResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/create_deployment",
		owner.AccessToken, map[string]string{"type": "dev"}, http.StatusCreated, &got)
	waitForStatus(t, h, got.Name, "running", 5*time.Second)

	spec := lastProvisionedSpec(t, h, got.Name)
	want := "https://synapse.example.com:" + itoa(spec.HostPort)
	if spec.PublicURL != want {
		t.Errorf("spec.PublicURL: got %q want %q", spec.PublicURL, want)
	}
}

// TestConvexOrigin_BaseDomainWinsOverPublicURL: BaseDomain is the v1.0+
// preferred shape — pretty per-deployment subdomain. The worker should
// produce "https://<name>.<BaseDomain>" so the container's
// CONVEX_CLOUD_ORIGIN agrees with what `synapse convex` puts in
// CONVEX_SELF_HOSTED_URL.
func TestConvexOrigin_BaseDomainWinsOverPublicURL(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		PublicURL:    "https://synapse.example.com",
		ProxyEnabled: true,
		BaseDomain:   "synapse.example.com",
	})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Base-Origin Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "BaseOrigin")

	var got deploymentResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/create_deployment",
		owner.AccessToken, map[string]string{"type": "dev"}, http.StatusCreated, &got)
	waitForStatus(t, h, got.Name, "running", 5*time.Second)

	spec := lastProvisionedSpec(t, h, got.Name)
	want := "https://" + got.Name + ".synapse.example.com"
	if spec.PublicURL != want {
		t.Errorf("spec.PublicURL: got %q want %q", spec.PublicURL, want)
	}
}

// TestConvexOrigin_NoConfigFallsBackToLoopback: with neither PublicURL
// nor BaseDomain wired, the worker must emit "" — the docker layer
// then defaults to "http://127.0.0.1:<port>", preserving pre-v1.6.15
// behaviour for local dev / CI.
func TestConvexOrigin_NoConfigFallsBackToLoopback(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Loopback Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Loopback")

	var got deploymentResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/create_deployment",
		owner.AccessToken, map[string]string{"type": "dev"}, http.StatusCreated, &got)
	waitForStatus(t, h, got.Name, "running", 5*time.Second)

	spec := lastProvisionedSpec(t, h, got.Name)
	if spec.PublicURL != "" {
		t.Errorf("spec.PublicURL: got %q want empty (loopback fallback)", spec.PublicURL)
	}
}

// lastProvisionedSpec polls FakeDocker until it sees a Provision call
// for the named deployment, then returns the recorded spec. Returns
// after the worker has caught up so callers can assert on env shape.
func lastProvisionedSpec(t *testing.T, h *Harness, name string) dockerprov.DeploymentSpec {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range h.Docker.ProvisionedSpecs() {
			if s.Name == name {
				return s
			}
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-context.Background().Done():
		}
	}
	t.Fatalf("Provision was never called for %q", name)
	return dockerprov.DeploymentSpec{}
}
