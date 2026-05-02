package synapsetest

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

// dashboardListResp mirrors the on-the-wire shape that the upstream
// Convex Dashboard's `DeploymentList.tsx` consumes. Decoded with
// DisallowUnknownFields so an upstream tightening of the type
// fails the test loud instead of silently dropping a field.
type dashboardListResp struct {
	Deployments []struct {
		Name     string `json:"name"`
		URL      string `json:"url"`
		AdminKey string `json:"adminKey"`
	} `json:"deployments"`
}

// createScopedTokenForProject hits POST /v1/projects/{id}/access_tokens to
// mint a project-scoped PAT. Mirrors what the dashboard fork does before
// loading the iframe.
func createScopedTokenForProject(t *testing.T, h *Harness, bearer, projectID, name string, expiresAt *time.Time) string {
	t.Helper()
	body := map[string]any{"name": name}
	if expiresAt != nil {
		body["expiresAt"] = expiresAt.Format(time.RFC3339Nano)
	}
	var got struct {
		Token       string `json:"token"`
		AccessToken struct {
			ID         string  `json:"id"`
			Name       string  `json:"name"`
			Scope      string  `json:"scope"`
			ScopeID    string  `json:"scopeId,omitempty"`
			CreateTime string  `json:"createTime"`
			ExpiresAt  *string `json:"expiresAt,omitempty"`
			LastUsedAt *string `json:"lastUsedAt,omitempty"`
		} `json:"accessToken"`
	}
	h.DoJSON(http.MethodPost,
		"/v1/projects/"+projectID+"/access_tokens",
		bearer, body, http.StatusCreated, &got)
	if got.AccessToken.Scope != "project" {
		t.Fatalf("expected scope=project, got %q", got.AccessToken.Scope)
	}
	return got.Token
}

func TestDashboardProxy_HappyPath(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Dash Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Dash Project")
	// Two real deployments — at least one must be marked default to
	// exercise the publicDeploymentURL path with the legacy host_port.
	_ = h.SeedDeployment(proj.ID, "dash-alpha", "dev", "running", true, owner.ID, 4801, "key-alpha")
	_ = h.SeedDeployment(proj.ID, "dash-beta", "prod", "running", false, owner.ID, 4802, "key-beta")

	tok := createScopedTokenForProject(t, h, owner.AccessToken, proj.ID, "[dashboard-session]", nil)

	var got dashboardListResp
	h.DoJSON(http.MethodGet,
		"/v1/internal/list_deployments_for_dashboard?token="+url.QueryEscape(tok),
		"", nil, http.StatusOK, &got)

	if len(got.Deployments) != 2 {
		t.Fatalf("want 2 deployments, got %d: %+v", len(got.Deployments), got.Deployments)
	}
	byName := map[string]string{}
	for _, d := range got.Deployments {
		byName[d.Name] = d.AdminKey
		if d.URL == "" {
			t.Errorf("deployment %s has empty URL", d.Name)
		}
	}
	if byName["dash-alpha"] != "key-alpha" {
		t.Errorf("alpha admin key mismatch: %q", byName["dash-alpha"])
	}
	if byName["dash-beta"] != "key-beta" {
		t.Errorf("beta admin key mismatch: %q", byName["dash-beta"])
	}
}

func TestDashboardProxy_MissingToken401(t *testing.T) {
	h := Setup(t)
	env := h.AssertStatus(http.MethodGet,
		"/v1/internal/list_deployments_for_dashboard",
		"", nil, http.StatusUnauthorized)
	if env.Code != "missing_token" {
		t.Errorf("code=%q want missing_token", env.Code)
	}
}

func TestDashboardProxy_InvalidToken401(t *testing.T) {
	h := Setup(t)
	env := h.AssertStatus(http.MethodGet,
		"/v1/internal/list_deployments_for_dashboard?token=syn_does_not_exist",
		"", nil, http.StatusUnauthorized)
	if env.Code != "invalid_token" {
		t.Errorf("code=%q want invalid_token", env.Code)
	}
}

func TestDashboardProxy_ExpiredToken401(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Expire Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Expire Prj")

	// Mint with expiresAt in the past — wait, the create endpoint
	// rejects past timestamps. Mint with a future expiry, then flip
	// expires_at directly via SQL to simulate an expired token.
	future := time.Now().Add(15 * time.Minute)
	tok := createScopedTokenForProject(t, h, owner.AccessToken, proj.ID, "[dashboard-session]", &future)

	if _, err := h.DB.Exec(h.rootCtx,
		`UPDATE access_tokens SET expires_at = now() - interval '1 minute' WHERE token_hash = encode(sha256($1::bytea), 'hex')`,
		tok); err != nil {
		t.Fatalf("expire token: %v", err)
	}

	env := h.AssertStatus(http.MethodGet,
		"/v1/internal/list_deployments_for_dashboard?token="+url.QueryEscape(tok),
		"", nil, http.StatusUnauthorized)
	if env.Code != "invalid_token" {
		t.Errorf("code=%q want invalid_token", env.Code)
	}
}

func TestDashboardProxy_WrongScope403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Wrong Scope Co")
	_ = createProject(t, h, owner.AccessToken, team.Slug, "Anything")

	// Team-scoped token shouldn't pass the project-only gate.
	var teamTok struct {
		Token       string `json:"token"`
		AccessToken struct {
			ID         string  `json:"id"`
			Name       string  `json:"name"`
			Scope      string  `json:"scope"`
			ScopeID    string  `json:"scopeId,omitempty"`
			CreateTime string  `json:"createTime"`
			ExpiresAt  *string `json:"expiresAt,omitempty"`
			LastUsedAt *string `json:"lastUsedAt,omitempty"`
		} `json:"accessToken"`
	}
	h.DoJSON(http.MethodPost,
		"/v1/teams/"+team.Slug+"/access_tokens",
		owner.AccessToken,
		map[string]any{"name": "team-tok"},
		http.StatusCreated, &teamTok)

	env := h.AssertStatus(http.MethodGet,
		"/v1/internal/list_deployments_for_dashboard?token="+url.QueryEscape(teamTok.Token),
		"", nil, http.StatusForbidden)
	if env.Code != "wrong_scope" {
		t.Errorf("code=%q want wrong_scope", env.Code)
	}
}

func TestDashboardProxy_AppScope_Allowed(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "App Tok Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App Prj")
	_ = h.SeedDeployment(proj.ID, "app-dep", "dev", "running", true, owner.ID, 4901, "key-app")

	var appTok struct {
		Token       string `json:"token"`
		AccessToken struct {
			ID         string  `json:"id"`
			Name       string  `json:"name"`
			Scope      string  `json:"scope"`
			ScopeID    string  `json:"scopeId,omitempty"`
			CreateTime string  `json:"createTime"`
			ExpiresAt  *string `json:"expiresAt,omitempty"`
			LastUsedAt *string `json:"lastUsedAt,omitempty"`
		} `json:"accessToken"`
	}
	h.DoJSON(http.MethodPost,
		"/v1/projects/"+proj.ID+"/app_access_tokens",
		owner.AccessToken,
		map[string]any{"name": "preview-key"},
		http.StatusCreated, &appTok)

	var got dashboardListResp
	h.DoJSON(http.MethodGet,
		"/v1/internal/list_deployments_for_dashboard?token="+url.QueryEscape(appTok.Token),
		"", nil, http.StatusOK, &got)
	if len(got.Deployments) != 1 || got.Deployments[0].Name != "app-dep" {
		t.Errorf("app-scope listing mismatch: %+v", got.Deployments)
	}
}

func TestDashboardProxy_HidesProvisioningDeployments(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Prov Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Prov Prj")
	_ = h.SeedDeployment(proj.ID, "ready-one", "dev", "running", true, owner.ID, 4910, "key-ready")
	_ = h.SeedDeployment(proj.ID, "still-prov", "dev", "provisioning", false, owner.ID, 4911, "key-prov")

	tok := createScopedTokenForProject(t, h, owner.AccessToken, proj.ID, "[dashboard-session]", nil)
	var got dashboardListResp
	h.DoJSON(http.MethodGet,
		"/v1/internal/list_deployments_for_dashboard?token="+url.QueryEscape(tok),
		"", nil, http.StatusOK, &got)
	if len(got.Deployments) != 1 || got.Deployments[0].Name != "ready-one" {
		t.Errorf("provisioning row should be hidden; got %+v", got.Deployments)
	}
}

func TestDashboardProxy_PublicURLRewriteApplied(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		PublicURL:    "https://synapse.example.com",
		ProxyEnabled: true,
	})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Rewrite Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Rewrite Prj")
	_ = h.SeedDeployment(proj.ID, "happy-cat-1234", "dev", "running", true, owner.ID, 4920, "key-cat")

	tok := createScopedTokenForProject(t, h, owner.AccessToken, proj.ID, "[dashboard-session]", nil)
	var got dashboardListResp
	h.DoJSON(http.MethodGet,
		"/v1/internal/list_deployments_for_dashboard?token="+url.QueryEscape(tok),
		"", nil, http.StatusOK, &got)
	if len(got.Deployments) != 1 {
		t.Fatalf("expected 1 deployment, got %+v", got.Deployments)
	}
	want := "https://synapse.example.com/d/happy-cat-1234"
	if got.Deployments[0].URL != want {
		t.Errorf("public URL not rewritten: got %q want %q", got.Deployments[0].URL, want)
	}
}
