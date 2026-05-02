package synapsetest

import (
	"net/http"
	"testing"
	"time"
)

type projectResp struct {
	ID         string    `json:"id"`
	TeamID     string    `json:"teamId"`
	TeamSlug   string    `json:"teamSlug,omitempty"`
	Name       string    `json:"name"`
	Slug       string    `json:"slug"`
	IsDemo     bool      `json:"isDemo"`
	CreateTime time.Time `json:"createTime"`
}

type createProjectResp struct {
	ProjectID   string      `json:"projectId"`
	ProjectSlug string      `json:"projectSlug"`
	Project     projectResp `json:"project"`
}

// createProject is a small helper used across tests in this file.
func createProject(t *testing.T, h *Harness, bearer, teamSlug, name string) projectResp {
	t.Helper()
	var got createProjectResp
	h.DoJSON(http.MethodPost, "/v1/teams/"+teamSlug+"/create_project", bearer,
		map[string]string{"projectName": name}, http.StatusCreated, &got)
	return got.Project
}

func TestProjects_CreateAndList(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Proj Co")

	proj := createProject(t, h, owner.AccessToken, team.Slug, "Web App")
	if proj.Name != "Web App" {
		t.Errorf("project name mismatch: got %q", proj.Name)
	}
	if proj.TeamID != team.ID {
		t.Errorf("team id mismatch: got %s want %s", proj.TeamID, team.ID)
	}

	var listed []projectResp
	h.DoJSON(http.MethodGet, "/v1/teams/"+team.Slug+"/list_projects",
		owner.AccessToken, nil, http.StatusOK, &listed)
	if len(listed) != 1 || listed[0].ID != proj.ID {
		t.Errorf("expected 1 project in list, got %+v", listed)
	}
}

func TestProjects_GetByID(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Get Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Project A")

	var got projectResp
	h.DoJSON(http.MethodGet, "/v1/projects/"+proj.ID, owner.AccessToken,
		nil, http.StatusOK, &got)
	if got.ID != proj.ID || got.Name != "Project A" {
		t.Errorf("get project mismatch: %+v", got)
	}
}

func TestProjects_GetUnknown(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	env := h.AssertStatus(http.MethodGet,
		"/v1/projects/00000000-0000-0000-0000-000000000000", owner.AccessToken,
		nil, http.StatusNotFound)
	if env.Code != "project_not_found" {
		t.Errorf("expected project_not_found, got %q", env.Code)
	}
}

func TestProjects_NonMember403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	stranger := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Closed Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Internal")

	env := h.AssertStatus(http.MethodGet, "/v1/projects/"+proj.ID, stranger.AccessToken,
		nil, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("got code %q want forbidden", env.Code)
	}
}

func TestProjects_UpdateAdminOnly(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	member := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Update Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Old Name")

	// Seed `member` as a non-admin team_member.
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, member.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	// Member: forbidden
	env := h.AssertStatus(http.MethodPut, "/v1/projects/"+proj.ID, member.AccessToken,
		map[string]any{"name": "New Name"}, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("got code %q want forbidden", env.Code)
	}

	// Admin: succeeds
	var updated projectResp
	h.DoJSON(http.MethodPut, "/v1/projects/"+proj.ID, owner.AccessToken,
		map[string]any{"name": "New Name"}, http.StatusOK, &updated)
	if updated.Name != "New Name" {
		t.Errorf("expected updated name 'New Name', got %q", updated.Name)
	}
}

func TestProjects_UpdateMissingName(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Validate Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Whatever")

	env := h.AssertStatus(http.MethodPut, "/v1/projects/"+proj.ID, owner.AccessToken,
		map[string]any{"name": "  "}, http.StatusBadRequest)
	if env.Code != "missing_name" {
		t.Errorf("got code %q want missing_name", env.Code)
	}
}

func TestProjects_UpdateSlugOnly(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Slug Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Original")

	var updated projectResp
	h.DoJSON(http.MethodPut, "/v1/projects/"+proj.ID, owner.AccessToken,
		map[string]any{"slug": "renamed-slug"}, http.StatusOK, &updated)
	if updated.Slug != "renamed-slug" {
		t.Errorf("slug=%q want renamed-slug", updated.Slug)
	}
	if updated.Name != "Original" {
		t.Errorf("name unexpectedly changed: %q", updated.Name)
	}
}

func TestProjects_UpdateNameAndSlug(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Both Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Old")

	var updated projectResp
	h.DoJSON(http.MethodPut, "/v1/projects/"+proj.ID, owner.AccessToken,
		map[string]any{"name": "New Name", "slug": "new-slug"},
		http.StatusOK, &updated)
	if updated.Name != "New Name" || updated.Slug != "new-slug" {
		t.Errorf("got name=%q slug=%q", updated.Name, updated.Slug)
	}
}

func TestProjects_UpdateInvalidSlug(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Bad Slug Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "X")

	env := h.AssertStatus(http.MethodPut, "/v1/projects/"+proj.ID, owner.AccessToken,
		map[string]any{"slug": "Has Spaces"}, http.StatusBadRequest)
	if env.Code != "invalid_slug" {
		t.Errorf("code=%q want invalid_slug", env.Code)
	}
}

func TestProjects_UpdateSlugConflict(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Conf Co")
	first := createProject(t, h, owner.AccessToken, team.Slug, "First")
	second := createProject(t, h, owner.AccessToken, team.Slug, "Second")

	env := h.AssertStatus(http.MethodPut, "/v1/projects/"+second.ID, owner.AccessToken,
		map[string]any{"slug": first.Slug}, http.StatusConflict)
	if env.Code != "slug_taken" {
		t.Errorf("code=%q want slug_taken", env.Code)
	}
}

func TestProjects_UpdateEmptyBodyNoOp(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "NoOp Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Same")

	var got projectResp
	h.DoJSON(http.MethodPut, "/v1/projects/"+proj.ID, owner.AccessToken,
		map[string]any{}, http.StatusOK, &got)
	if got.Name != "Same" || got.Slug != proj.Slug {
		t.Errorf("expected unchanged project, got %+v", got)
	}
}

func TestProjects_DeleteCascadesEnvVars(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Del Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Doomed")

	// Set an env var.
	type envVarChange struct {
		Op              string   `json:"op"`
		Name            string   `json:"name"`
		Value           string   `json:"value,omitempty"`
		DeploymentTypes []string `json:"deploymentTypes,omitempty"`
	}
	type updateEnvVarsReq struct {
		Changes []envVarChange `json:"changes"`
	}
	h.DoJSON(http.MethodPost,
		"/v1/projects/"+proj.ID+"/update_default_environment_variables",
		owner.AccessToken,
		updateEnvVarsReq{Changes: []envVarChange{{Op: "set", Name: "API_KEY", Value: "sekret"}}},
		http.StatusOK, &map[string]any{})

	// Confirm row exists pre-delete.
	var pre int
	if err := h.DB.QueryRow(h.rootCtx,
		`SELECT count(*) FROM project_env_vars WHERE project_id = $1`, proj.ID).Scan(&pre); err != nil {
		t.Fatalf("count env vars: %v", err)
	}
	if pre != 1 {
		t.Fatalf("expected 1 env var pre-delete, got %d", pre)
	}

	// Delete project.
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/delete", owner.AccessToken,
		nil, http.StatusOK, &map[string]string{})

	// Cascade should have wiped the env vars.
	var post int
	if err := h.DB.QueryRow(h.rootCtx,
		`SELECT count(*) FROM project_env_vars WHERE project_id = $1`, proj.ID).Scan(&post); err != nil {
		t.Fatalf("count env vars post-delete: %v", err)
	}
	if post != 0 {
		t.Errorf("expected env vars to cascade-delete, got %d", post)
	}

	// And the project itself is gone.
	var projects int
	if err := h.DB.QueryRow(h.rootCtx,
		`SELECT count(*) FROM projects WHERE id = $1`, proj.ID).Scan(&projects); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if projects != 0 {
		t.Errorf("expected project row to be removed, got %d", projects)
	}
}

func TestProjects_DeleteAdminOnly(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	member := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Delete Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Survive")

	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, member.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+proj.ID+"/delete",
		member.AccessToken, nil, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("got code %q want forbidden", env.Code)
	}
}

func TestProjects_EnvVarsRoundTrip(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Env Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "EnvProj")

	type envVarChange struct {
		Op              string   `json:"op"`
		Name            string   `json:"name"`
		Value           string   `json:"value,omitempty"`
		DeploymentTypes []string `json:"deploymentTypes,omitempty"`
	}
	type updateEnvVarsReq struct {
		Changes []envVarChange `json:"changes"`
	}
	type envConfig struct {
		Name            string   `json:"name"`
		Value           string   `json:"value"`
		DeploymentTypes []string `json:"deploymentTypes"`
	}
	type listResp struct {
		Configs []envConfig `json:"configs"`
	}
	type updateResp struct {
		Applied int `json:"applied"`
	}

	// Empty list to start.
	var initial listResp
	h.DoJSON(http.MethodGet,
		"/v1/projects/"+proj.ID+"/list_default_environment_variables",
		owner.AccessToken, nil, http.StatusOK, &initial)
	if len(initial.Configs) != 0 {
		t.Errorf("expected empty initial env vars, got %+v", initial.Configs)
	}

	// Set two vars in one batch.
	var setResp updateResp
	h.DoJSON(http.MethodPost,
		"/v1/projects/"+proj.ID+"/update_default_environment_variables",
		owner.AccessToken,
		updateEnvVarsReq{Changes: []envVarChange{
			{Op: "set", Name: "FOO", Value: "1"},
			{Op: "set", Name: "BAR", Value: "two", DeploymentTypes: []string{"prod"}},
		}},
		http.StatusOK, &setResp)
	if setResp.Applied != 2 {
		t.Errorf("expected applied=2, got %d", setResp.Applied)
	}

	// List confirms both.
	var listed listResp
	h.DoJSON(http.MethodGet,
		"/v1/projects/"+proj.ID+"/list_default_environment_variables",
		owner.AccessToken, nil, http.StatusOK, &listed)
	if len(listed.Configs) != 2 {
		t.Fatalf("expected 2 env vars after set, got %+v", listed.Configs)
	}
	byName := map[string]envConfig{}
	for _, c := range listed.Configs {
		byName[c.Name] = c
	}
	if byName["FOO"].Value != "1" {
		t.Errorf("FOO mismatch: %+v", byName["FOO"])
	}
	if byName["BAR"].Value != "two" {
		t.Errorf("BAR mismatch: %+v", byName["BAR"])
	}
	if len(byName["BAR"].DeploymentTypes) != 1 || byName["BAR"].DeploymentTypes[0] != "prod" {
		t.Errorf("BAR deployment types: got %v", byName["BAR"].DeploymentTypes)
	}
	// FOO should default to all three deployment types.
	if len(byName["FOO"].DeploymentTypes) != 3 {
		t.Errorf("FOO should have 3 default deployment types, got %v", byName["FOO"].DeploymentTypes)
	}

	// Delete BAR.
	h.DoJSON(http.MethodPost,
		"/v1/projects/"+proj.ID+"/update_default_environment_variables",
		owner.AccessToken,
		updateEnvVarsReq{Changes: []envVarChange{{Op: "delete", Name: "BAR"}}},
		http.StatusOK, &updateResp{})

	var afterDelete listResp
	h.DoJSON(http.MethodGet,
		"/v1/projects/"+proj.ID+"/list_default_environment_variables",
		owner.AccessToken, nil, http.StatusOK, &afterDelete)
	if len(afterDelete.Configs) != 1 || afterDelete.Configs[0].Name != "FOO" {
		t.Errorf("expected only FOO after delete, got %+v", afterDelete.Configs)
	}
}

func TestProjects_Transfer_HappyPath(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	src := createTeam(t, h, owner.AccessToken, "Source Co")
	dst := createTeam(t, h, owner.AccessToken, "Dest Co")
	proj := createProject(t, h, owner.AccessToken, src.Slug, "Movable")

	// Transfer succeeds; status 204 + empty body.
	resp := h.Do(http.MethodPost, "/v1/projects/"+proj.ID+"/transfer", owner.AccessToken,
		map[string]string{"destinationTeamId": dst.ID})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d want 204", resp.StatusCode)
	}
	resp.Body.Close()

	// GET reflects the new team_id.
	var got projectResp
	h.DoJSON(http.MethodGet, "/v1/projects/"+proj.ID, owner.AccessToken, nil, http.StatusOK, &got)
	if got.TeamID != dst.ID {
		t.Errorf("team id mismatch: got %s want %s", got.TeamID, dst.ID)
	}
}

func TestProjects_Transfer_SameTeamNoOp(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Solo Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Stays")

	resp := h.Do(http.MethodPost, "/v1/projects/"+proj.ID+"/transfer", owner.AccessToken,
		map[string]string{"destinationTeamId": team.ID})
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("self-transfer status=%d want 204", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestProjects_Transfer_NonAdminSource403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	mover := h.RegisterRandomUser()
	src := createTeam(t, h, owner.AccessToken, "Source")
	dst := createTeam(t, h, owner.AccessToken, "Dest")
	proj := createProject(t, h, owner.AccessToken, src.Slug, "Forbidden")

	// Add mover as plain member of both teams (not admin of source).
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member'), ($3, $2, 'admin')`,
		src.ID, mover.ID, dst.ID); err != nil {
		t.Fatalf("seed members: %v", err)
	}

	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+proj.ID+"/transfer",
		mover.AccessToken, map[string]string{"destinationTeamId": dst.ID},
		http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("got code %q want forbidden", env.Code)
	}
}

func TestProjects_Transfer_NonAdminDest403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	mover := h.RegisterRandomUser()
	src := createTeam(t, h, owner.AccessToken, "OurSource")
	dst := createTeam(t, h, owner.AccessToken, "OurDest")
	proj := createProject(t, h, owner.AccessToken, src.Slug, "Almost")

	// Admin of source, plain member of dest.
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'admin'), ($3, $2, 'member')`,
		src.ID, mover.ID, dst.ID); err != nil {
		t.Fatalf("seed members: %v", err)
	}

	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+proj.ID+"/transfer",
		mover.AccessToken, map[string]string{"destinationTeamId": dst.ID},
		http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("got code %q want forbidden", env.Code)
	}
}

func TestProjects_Transfer_DestTeamNotFound(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Lonely Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Orphan")

	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+proj.ID+"/transfer",
		owner.AccessToken,
		map[string]string{"destinationTeamId": "00000000-0000-0000-0000-000000000000"},
		http.StatusNotFound)
	if env.Code != "team_not_found" {
		t.Errorf("got code %q want team_not_found", env.Code)
	}
}

func TestProjects_Transfer_StrangerToDestTeam403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	stranger := h.RegisterRandomUser()
	src := createTeam(t, h, owner.AccessToken, "Mine")
	dst := createTeam(t, h, stranger.AccessToken, "TheirsAlone")
	proj := createProject(t, h, owner.AccessToken, src.Slug, "Trying")

	// owner is admin of src, totally outside dst.
	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+proj.ID+"/transfer",
		owner.AccessToken, map[string]string{"destinationTeamId": dst.ID},
		http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("got code %q want forbidden", env.Code)
	}
}

func TestProjects_Transfer_SlugCollision409(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	src := createTeam(t, h, owner.AccessToken, "Source X")
	dst := createTeam(t, h, owner.AccessToken, "Dest X")

	// Same name → same slug in both teams. UNIQUE(team_id, slug) bites the
	// transfer.
	moving := createProject(t, h, owner.AccessToken, src.Slug, "Same Name")
	_ = createProject(t, h, owner.AccessToken, dst.Slug, "Same Name")

	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+moving.ID+"/transfer",
		owner.AccessToken, map[string]string{"destinationTeamId": dst.ID},
		http.StatusConflict)
	if env.Code != "slug_taken" {
		t.Errorf("got code %q want slug_taken", env.Code)
	}
}

func TestProjects_EnvVars_MembersAllowed_ViewersBlocked(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	member := h.RegisterRandomUser()
	viewer := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "EnvRBAC Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")

	// Both extras start as plain team members (no project override).
	if _, err := h.DB.Exec(h.rootCtx, `
		INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member'), ($1, $3, 'member')
	`, team.ID, member.ID, viewer.ID); err != nil {
		t.Fatalf("seed members: %v", err)
	}
	// Downgrade `viewer` via a project_members override — this is the
	// v1.0+ RBAC promise: a team member can be locked down to read-only
	// on one specific project.
	if _, err := h.DB.Exec(h.rootCtx, `
		INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'viewer')
	`, proj.ID, viewer.ID); err != nil {
		t.Fatalf("seed viewer override: %v", err)
	}

	type envVarChange struct {
		Op    string `json:"op"`
		Name  string `json:"name"`
		Value string `json:"value,omitempty"`
	}
	type updateEnvVarsReq struct {
		Changes []envVarChange `json:"changes"`
	}
	type listResp struct {
		Configs []any `json:"configs"`
	}

	// Member can write — was admin-only before v1.0 RBAC, now any
	// editor (admin or member) is fine.
	h.DoJSON(http.MethodPost,
		"/v1/projects/"+proj.ID+"/update_default_environment_variables",
		member.AccessToken,
		updateEnvVarsReq{Changes: []envVarChange{{Op: "set", Name: "X", Value: "y"}}},
		http.StatusOK, &map[string]any{})

	// Viewer is blocked from writes…
	env := h.AssertStatus(http.MethodPost,
		"/v1/projects/"+proj.ID+"/update_default_environment_variables",
		viewer.AccessToken,
		updateEnvVarsReq{Changes: []envVarChange{{Op: "set", Name: "X", Value: "y"}}},
		http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("got code %q want forbidden", env.Code)
	}

	// …but reads stay open for everyone with project access.
	h.DoJSON(http.MethodGet,
		"/v1/projects/"+proj.ID+"/list_default_environment_variables",
		viewer.AccessToken, nil, http.StatusOK, &listResp{})
	h.DoJSON(http.MethodGet,
		"/v1/projects/"+proj.ID+"/list_default_environment_variables",
		member.AccessToken, nil, http.StatusOK, &listResp{})
}
