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

func TestProjects_EnvVarsAdminOnly(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	member := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "EnvAdmin Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")

	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, member.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	type envVarChange struct {
		Op    string `json:"op"`
		Name  string `json:"name"`
		Value string `json:"value,omitempty"`
	}
	type updateEnvVarsReq struct {
		Changes []envVarChange `json:"changes"`
	}
	env := h.AssertStatus(http.MethodPost,
		"/v1/projects/"+proj.ID+"/update_default_environment_variables",
		member.AccessToken,
		updateEnvVarsReq{Changes: []envVarChange{{Op: "set", Name: "X", Value: "y"}}},
		http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("got code %q want forbidden", env.Code)
	}

	// But list_env is OK for plain members.
	type listResp struct {
		Configs []any `json:"configs"`
	}
	h.DoJSON(http.MethodGet,
		"/v1/projects/"+proj.ID+"/list_default_environment_variables",
		member.AccessToken, nil, http.StatusOK, &listResp{})
}
