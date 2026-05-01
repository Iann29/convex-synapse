package synapsetest

import (
	"net/http"
	"testing"
)

// TestURLRewrite_GetDeployment: with PublicURL + ProxyEnabled set, the
// GET /v1/deployments/{name} response must rewrite the loopback URL
// the provisioner stored into "<PublicURL>/d/<name>". Without this,
// the dashboard renders "http://127.0.0.1:<port>" — a URL the
// operator's browser cannot reach. PR #10 added the rewrite helper
// but only wired it into /auth and /cli_credentials; this test pins
// the contract for the remaining GET handlers.
func TestURLRewrite_GetDeployment(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		PublicURL:    "https://synapse.example.com",
		ProxyEnabled: true,
	})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Rewrite Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Rewrite Proj")
	h.SeedDeployment(proj.ID, "happy-cat-1234", "dev", "running", true, owner.ID, 3211, "")

	var got deploymentResp
	h.DoJSON(http.MethodGet, "/v1/deployments/happy-cat-1234", owner.AccessToken,
		nil, http.StatusOK, &got)

	want := "https://synapse.example.com/d/happy-cat-1234"
	if got.DeploymentURL != want {
		t.Errorf("getDeployment URL: got %q want %q", got.DeploymentURL, want)
	}
}

// TestURLRewrite_GetDeployment_NoPublicURL: with PublicURL empty (the
// default), the response keeps the loopback URL — preserves backwards
// compatibility for local dev and existing v0.4-era setups.
func TestURLRewrite_GetDeployment_NoPublicURL(t *testing.T) {
	h := Setup(t) // PublicURL=""
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Legacy Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Legacy Proj")
	h.SeedDeployment(proj.ID, "legacy-bee-9999", "dev", "running", true, owner.ID, 3299, "")

	var got deploymentResp
	h.DoJSON(http.MethodGet, "/v1/deployments/legacy-bee-9999", owner.AccessToken,
		nil, http.StatusOK, &got)

	want := "http://127.0.0.1:3299"
	if got.DeploymentURL != want {
		t.Errorf("legacy URL preserved: got %q want %q", got.DeploymentURL, want)
	}
}

// TestURLRewrite_ProjectListDeployments: every row in the
// project-scoped list must have its URL rewritten. The dashboard's
// project page reads from this endpoint; without the rewrite the
// row-card link goes to localhost.
func TestURLRewrite_ProjectListDeployments(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		PublicURL:    "https://synapse.example.com",
		ProxyEnabled: true,
	})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "List Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "List Proj")
	h.SeedDeployment(proj.ID, "alpha-fox-1111", "dev", "running", true, owner.ID, 3211, "")
	h.SeedDeployment(proj.ID, "beta-fox-2222", "prod", "running", false, owner.ID, 3212, "")

	var deps []deploymentResp
	h.DoJSON(http.MethodGet,
		"/v1/projects/"+proj.ID+"/list_deployments", owner.AccessToken,
		nil, http.StatusOK, &deps)

	if len(deps) != 2 {
		t.Fatalf("got %d deployments, want 2", len(deps))
	}
	wantByName := map[string]string{
		"alpha-fox-1111": "https://synapse.example.com/d/alpha-fox-1111",
		"beta-fox-2222":  "https://synapse.example.com/d/beta-fox-2222",
	}
	for _, d := range deps {
		want, ok := wantByName[d.Name]
		if !ok {
			t.Errorf("unexpected deployment %q", d.Name)
			continue
		}
		if d.DeploymentURL != want {
			t.Errorf("project list URL[%s]: got %q want %q", d.Name, d.DeploymentURL, want)
		}
	}
}

// TestURLRewrite_TeamListDeployments: same assertion as the project
// list, but the team-scoped endpoint (used by the team-home page).
// Both code paths share the rewrite via h.Deployments.publicDeploymentURL.
func TestURLRewrite_TeamListDeployments(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		PublicURL:    "https://synapse.example.com",
		ProxyEnabled: true,
	})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Team-List Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Team-List Proj")
	h.SeedDeployment(proj.ID, "lucky-tiger-7777", "dev", "running", true, owner.ID, 3299, "")

	var deps []deploymentResp
	h.DoJSON(http.MethodGet,
		"/v1/teams/"+team.Slug+"/list_deployments", owner.AccessToken,
		nil, http.StatusOK, &deps)

	if len(deps) != 1 {
		t.Fatalf("got %d deployments, want 1", len(deps))
	}
	want := "https://synapse.example.com/d/lucky-tiger-7777"
	if deps[0].DeploymentURL != want {
		t.Errorf("team list URL: got %q want %q", deps[0].DeploymentURL, want)
	}
}

// TestURLRewrite_HostPortMode: PublicURL set + ProxyEnabled=false ->
// "<PublicURL>:<host_port>". Used when the operator chose to expose
// each Convex backend's port directly through their reverse proxy /
// load balancer instead of mounting Synapse's `/d/<name>/*` proxy.
func TestURLRewrite_HostPortMode(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		PublicURL:    "https://synapse.example.com",
		ProxyEnabled: false,
	})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Host-Port Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Host-Port Proj")
	h.SeedDeployment(proj.ID, "shy-rabbit-3333", "dev", "running", true, owner.ID, 3299, "")

	var got deploymentResp
	h.DoJSON(http.MethodGet, "/v1/deployments/shy-rabbit-3333", owner.AccessToken,
		nil, http.StatusOK, &got)

	want := "https://synapse.example.com:3299"
	if got.DeploymentURL != want {
		t.Errorf("host-port mode URL: got %q want %q", got.DeploymentURL, want)
	}
}

// TestURLRewrite_BaseDomain: BaseDomain set wins over PublicURL +
// ProxyEnabled — deployment URLs become "https://<name>.<BaseDomain>".
// This is the v1.0 custom-domains shape; with wildcard DNS + Caddy
// on-demand TLS configured, Convex clients see a per-deployment
// hostname instead of "<host>/d/<name>".
func TestURLRewrite_BaseDomain(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		PublicURL:    "https://synapse.example.com",
		ProxyEnabled: true,
		BaseDomain:   "synapse.example.com",
	})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Subdomain Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Subdomain Proj")
	h.SeedDeployment(proj.ID, "bold-fox-1234", "dev", "running", true, owner.ID, 3242, "")

	var got deploymentResp
	h.DoJSON(http.MethodGet, "/v1/deployments/bold-fox-1234", owner.AccessToken,
		nil, http.StatusOK, &got)

	want := "https://bold-fox-1234.synapse.example.com"
	if got.DeploymentURL != want {
		t.Errorf("BaseDomain URL: got %q want %q", got.DeploymentURL, want)
	}
}

// TestURLRewrite_BaseDomain_NoOpForEmptyBase: BaseDomain unset keeps
// the v0.6 path-based shape — pins the decision tree against future
// refactors of publicDeploymentURL accidentally swapping branches.
func TestURLRewrite_BaseDomain_NoOpForEmptyBase(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		PublicURL:    "https://synapse.example.com",
		ProxyEnabled: true,
		// BaseDomain intentionally empty
	})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Path Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Path Proj")
	h.SeedDeployment(proj.ID, "calm-fox-2222", "dev", "running", true, owner.ID, 3299, "")

	var got deploymentResp
	h.DoJSON(http.MethodGet, "/v1/deployments/calm-fox-2222", owner.AccessToken,
		nil, http.StatusOK, &got)

	want := "https://synapse.example.com/d/calm-fox-2222"
	if got.DeploymentURL != want {
		t.Errorf("BaseDomain empty falls back to path: got %q want %q", got.DeploymentURL, want)
	}
}
