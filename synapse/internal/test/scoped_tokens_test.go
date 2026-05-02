package synapsetest

import (
	"net/http"
	"testing"
	"time"
)

type scopedTokenView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope"`
	ScopeID    string     `json:"scopeId,omitempty"`
	CreateTime time.Time  `json:"createTime"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

type scopedTokenCreateResp struct {
	Token       string          `json:"token"`
	AccessToken scopedTokenView `json:"accessToken"`
}

type scopedTokenListResp struct {
	Items      []scopedTokenView `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

func TestScopedTokens_TeamCreateAndList(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Tok Co")

	var created scopedTokenCreateResp
	h.DoJSON(http.MethodPost, "/v1/teams/"+team.Slug+"/access_tokens",
		owner.AccessToken,
		map[string]any{"name": "ci-deploy"},
		http.StatusCreated, &created)
	if created.AccessToken.Scope != "team" {
		t.Errorf("scope=%q want team", created.AccessToken.Scope)
	}
	if created.AccessToken.ScopeID != team.ID {
		t.Errorf("scopeId=%q want %s", created.AccessToken.ScopeID, team.ID)
	}
	if created.Token == "" {
		t.Errorf("plaintext token missing")
	}

	var listed scopedTokenListResp
	h.DoJSON(http.MethodGet, "/v1/teams/"+team.Slug+"/access_tokens",
		owner.AccessToken, nil, http.StatusOK, &listed)
	if len(listed.Items) != 1 || listed.Items[0].ID != created.AccessToken.ID {
		t.Errorf("list mismatch: %+v", listed.Items)
	}
}

func TestScopedTokens_TeamCreateNonAdmin403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	other := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Locked Token Co")
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, other.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	env := h.AssertStatus(http.MethodPost, "/v1/teams/"+team.Slug+"/access_tokens",
		other.AccessToken,
		map[string]any{"name": "ci"}, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("code=%q want forbidden", env.Code)
	}
}

func TestScopedTokens_ProjectCreate(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "PrjTok Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Prj")

	var created scopedTokenCreateResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/access_tokens",
		owner.AccessToken, map[string]any{"name": "preview"},
		http.StatusCreated, &created)
	if created.AccessToken.Scope != "project" || created.AccessToken.ScopeID != proj.ID {
		t.Errorf("project token: %+v", created.AccessToken)
	}
}

func TestScopedTokens_AppCreate(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "AppTok Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")

	var created scopedTokenCreateResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/app_access_tokens",
		owner.AccessToken, map[string]any{"name": "preview-deploy-key"},
		http.StatusCreated, &created)
	if created.AccessToken.Scope != "app" {
		t.Errorf("app token scope=%q", created.AccessToken.Scope)
	}

	// Listing via the app endpoint shows it; listing via the regular
	// project endpoint does NOT (separate categories).
	var appList scopedTokenListResp
	h.DoJSON(http.MethodGet, "/v1/projects/"+proj.ID+"/app_access_tokens",
		owner.AccessToken, nil, http.StatusOK, &appList)
	if len(appList.Items) != 1 {
		t.Errorf("app list: %+v", appList.Items)
	}
	var prjList scopedTokenListResp
	h.DoJSON(http.MethodGet, "/v1/projects/"+proj.ID+"/access_tokens",
		owner.AccessToken, nil, http.StatusOK, &prjList)
	if len(prjList.Items) != 0 {
		t.Errorf("regular list should be empty when only app tokens exist, got %+v", prjList.Items)
	}
}

func TestScopedTokens_DeploymentCreate(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "DepTok Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Dep")
	depID := h.SeedDeployment(proj.ID, "deploy-tok", "dev", "running", true, owner.ID, 4321, "")

	var created scopedTokenCreateResp
	h.DoJSON(http.MethodPost, "/v1/deployments/deploy-tok/access_tokens",
		owner.AccessToken, map[string]any{"name": "dep-tok"},
		http.StatusCreated, &created)
	if created.AccessToken.Scope != "deployment" || created.AccessToken.ScopeID != depID {
		t.Errorf("deployment token: %+v want scope=deployment scopeId=%s", created.AccessToken, depID)
	}
}

// ---------- enforcement ----------

func TestScopedTokens_TeamScope_BlocksOtherTeam(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	teamA := createTeam(t, h, owner.AccessToken, "Team A")
	teamB := createTeam(t, h, owner.AccessToken, "Team B")

	var tokenA scopedTokenCreateResp
	h.DoJSON(http.MethodPost, "/v1/teams/"+teamA.Slug+"/access_tokens",
		owner.AccessToken, map[string]any{"name": "scoped-a"},
		http.StatusCreated, &tokenA)

	// Token for team A → 200 on team A endpoint.
	h.DoJSON(http.MethodGet, "/v1/teams/"+teamA.Slug, tokenA.Token,
		nil, http.StatusOK, &teamResp{})

	// Token for team A → 403 on team B endpoint.
	env := h.AssertStatus(http.MethodGet, "/v1/teams/"+teamB.Slug,
		tokenA.Token, nil, http.StatusForbidden)
	if env.Code != "forbidden_token_scope" {
		t.Errorf("code=%q want forbidden_token_scope", env.Code)
	}
}

func TestScopedTokens_TeamScope_AllowsChildProject(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Parent")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Child")

	var tok scopedTokenCreateResp
	h.DoJSON(http.MethodPost, "/v1/teams/"+team.Slug+"/access_tokens",
		owner.AccessToken, map[string]any{"name": "all-projects"},
		http.StatusCreated, &tok)

	// Team-scoped token reaches the child project.
	h.DoJSON(http.MethodGet, "/v1/projects/"+proj.ID, tok.Token,
		nil, http.StatusOK, &projectResp{})
}

func TestScopedTokens_ProjectScope_BlocksOtherProject(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Co")
	pA := createProject(t, h, owner.AccessToken, team.Slug, "ProjA")
	pB := createProject(t, h, owner.AccessToken, team.Slug, "ProjB")

	var tok scopedTokenCreateResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+pA.ID+"/access_tokens",
		owner.AccessToken, map[string]any{"name": "only-a"},
		http.StatusCreated, &tok)

	h.DoJSON(http.MethodGet, "/v1/projects/"+pA.ID, tok.Token,
		nil, http.StatusOK, &projectResp{})

	env := h.AssertStatus(http.MethodGet, "/v1/projects/"+pB.ID,
		tok.Token, nil, http.StatusForbidden)
	if env.Code != "forbidden_token_scope" {
		t.Errorf("code=%q", env.Code)
	}
}

func TestScopedTokens_ProjectScope_BlocksTeam(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "PrjOnly Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "PrjOnly")

	var tok scopedTokenCreateResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/access_tokens",
		owner.AccessToken, map[string]any{"name": "narrow"},
		http.StatusCreated, &tok)

	env := h.AssertStatus(http.MethodGet, "/v1/teams/"+team.Slug,
		tok.Token, nil, http.StatusForbidden)
	if env.Code != "forbidden_token_scope" {
		t.Errorf("code=%q", env.Code)
	}
}

func TestScopedTokens_DeploymentScope_OnlyExact(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Dep Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Dep Prj")
	_ = h.SeedDeployment(proj.ID, "alpha", "dev", "running", true, owner.ID, 4501, "")
	_ = h.SeedDeployment(proj.ID, "beta", "dev", "running", false, owner.ID, 4502, "")

	var tok scopedTokenCreateResp
	h.DoJSON(http.MethodPost, "/v1/deployments/alpha/access_tokens",
		owner.AccessToken, map[string]any{"name": "alpha-only"},
		http.StatusCreated, &tok)

	// alpha → 200; beta → 403.
	h.DoJSON(http.MethodGet, "/v1/deployments/alpha", tok.Token,
		nil, http.StatusOK, &map[string]any{})

	env := h.AssertStatus(http.MethodGet, "/v1/deployments/beta",
		tok.Token, nil, http.StatusForbidden)
	if env.Code != "forbidden_token_scope" {
		t.Errorf("code=%q", env.Code)
	}
}

func TestScopedTokens_AppScope_BehavesLikeProject(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "App Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "AppProj")
	_ = h.SeedDeployment(proj.ID, "stage", "preview", "running", false, owner.ID, 4601, "")

	var tok scopedTokenCreateResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/app_access_tokens",
		owner.AccessToken, map[string]any{"name": "ci"},
		http.StatusCreated, &tok)

	// App token can hit deployment under same project.
	h.DoJSON(http.MethodGet, "/v1/deployments/stage", tok.Token,
		nil, http.StatusOK, &map[string]any{})
	// But not the parent team endpoint.
	env := h.AssertStatus(http.MethodGet, "/v1/teams/"+team.Slug,
		tok.Token, nil, http.StatusForbidden)
	if env.Code != "forbidden_token_scope" {
		t.Errorf("code=%q", env.Code)
	}
}

// User-scoped tokens (legacy /v1/create_personal_access_token) keep
// working everywhere — sanity check the regression surface.
func TestScopedTokens_UserScope_Unrestricted(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "U Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "U Prj")

	var tok scopedTokenCreateResp
	h.DoJSON(http.MethodPost, "/v1/create_personal_access_token",
		owner.AccessToken, map[string]any{"name": "session"},
		http.StatusCreated, &tok)

	h.DoJSON(http.MethodGet, "/v1/teams/"+team.Slug, tok.Token, nil,
		http.StatusOK, &teamResp{})
	h.DoJSON(http.MethodGet, "/v1/projects/"+proj.ID, tok.Token, nil,
		http.StatusOK, &projectResp{})
}
