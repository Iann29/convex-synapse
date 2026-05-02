package synapsetest

import (
	"net/http"
	"testing"
	"time"
)

// project_rbac_test.go — coverage for v1.0+ project-level RBAC
// (migration 000008). Verifies override semantics (project_members
// row beats team_members), the role hierarchy on existing handlers
// (admin / member / viewer), and the four new endpoints under
// /v1/projects/{id}/ for managing project members.

type projectMemberAPI struct {
	ID         string    `json:"id"`
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	Role       string    `json:"role"`
	Source     string    `json:"source"`
	CreateTime time.Time `json:"createTime"`
}

// seedTeamMember inserts a plain (no-override) team_members row for
// the given user under the given team slug. Skips the invite UI
// dance — we're testing RBAC, not invite flows.
func seedTeamMember(t *testing.T, h *Harness, teamID, userID, role string) {
	t.Helper()
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, $3)`,
		teamID, userID, role); err != nil {
		t.Fatalf("seed team_member: %v", err)
	}
}

func TestRBAC_OverrideBeatsTeamRole(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	contractor := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Override Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Production")

	// contractor is team admin globally, but project_members downgrades
	// them to viewer just for this project.
	seedTeamMember(t, h, team.ID, contractor.ID, "admin")
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'viewer')`,
		proj.ID, contractor.ID); err != nil {
		t.Fatalf("seed viewer override: %v", err)
	}

	// Contractor can read the project (any role can).
	h.DoJSON(http.MethodGet, "/v1/projects/"+proj.ID, contractor.AccessToken,
		nil, http.StatusOK, &map[string]any{})

	// But viewer can't update the project even though they're team
	// admin globally.
	env := h.AssertStatus(http.MethodPut, "/v1/projects/"+proj.ID,
		contractor.AccessToken, map[string]any{"name": "Hijack"},
		http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("code=%q want forbidden", env.Code)
	}
}

func TestRBAC_TeamRoleFallback(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	teammate := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Fallback Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")

	// teammate is team admin, no project override.
	seedTeamMember(t, h, team.ID, teammate.ID, "admin")

	// Should be able to admin the project via team-role fallback.
	h.DoJSON(http.MethodPut, "/v1/projects/"+proj.ID,
		teammate.AccessToken, map[string]any{"name": "Renamed"},
		http.StatusOK, &map[string]any{})
}

func TestRBAC_ViewerCannotCreateDeployment(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	viewer := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Viewer Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Locked")

	seedTeamMember(t, h, team.ID, viewer.ID, "member")
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'viewer')`,
		proj.ID, viewer.ID); err != nil {
		t.Fatalf("override: %v", err)
	}

	env := h.AssertStatus(http.MethodPost,
		"/v1/projects/"+proj.ID+"/create_deployment",
		viewer.AccessToken, map[string]any{"type": "dev"},
		http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("code=%q want forbidden", env.Code)
	}
}

func TestRBAC_MemberCanCreateDeployment(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	dev := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "MemberCreate Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "OK")

	seedTeamMember(t, h, team.ID, dev.ID, "member")

	// member-level user can create a deployment via canEditProject.
	h.DoJSON(http.MethodPost,
		"/v1/projects/"+proj.ID+"/create_deployment",
		dev.AccessToken, map[string]any{"type": "dev"},
		http.StatusCreated, &map[string]any{})
}

func TestRBAC_MemberCannotDeleteDeployment(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	dev := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "MemberDel Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "OK")
	depID := h.SeedDeployment(proj.ID, "block-me", "dev", "running", false, owner.ID, 5901, "")
	_ = depID

	seedTeamMember(t, h, team.ID, dev.ID, "member")

	env := h.AssertStatus(http.MethodPost,
		"/v1/deployments/block-me/delete",
		dev.AccessToken, nil, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("code=%q want forbidden", env.Code)
	}
}

// ---------- /list_members ----------

func TestRBAC_ListMembers_MergesOverrideAndTeam(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	teammate := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "List Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	seedTeamMember(t, h, team.ID, teammate.ID, "member")
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'viewer')`,
		proj.ID, teammate.ID); err != nil {
		t.Fatalf("override: %v", err)
	}

	var got []projectMemberAPI
	h.DoJSON(http.MethodGet, "/v1/projects/"+proj.ID+"/list_members",
		owner.AccessToken, nil, http.StatusOK, &got)
	if len(got) != 2 {
		t.Fatalf("want 2 members got %d: %+v", len(got), got)
	}
	byID := map[string]projectMemberAPI{}
	for _, m := range got {
		byID[m.ID] = m
	}
	if byID[owner.ID].Role != "admin" || byID[owner.ID].Source != "team" {
		t.Errorf("owner: got role=%q source=%q want admin/team", byID[owner.ID].Role, byID[owner.ID].Source)
	}
	if byID[teammate.ID].Role != "viewer" || byID[teammate.ID].Source != "project" {
		t.Errorf("teammate: got role=%q source=%q want viewer/project", byID[teammate.ID].Role, byID[teammate.ID].Source)
	}
}

// ---------- /add_member ----------

func TestRBAC_AddMember_HappyPath(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	teammate := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Add Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	seedTeamMember(t, h, team.ID, teammate.ID, "member")

	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/add_member",
		owner.AccessToken,
		map[string]string{"userId": teammate.ID, "role": "viewer"},
		http.StatusOK, &map[string]string{})

	// Verify via /list_members.
	var got []projectMemberAPI
	h.DoJSON(http.MethodGet, "/v1/projects/"+proj.ID+"/list_members",
		owner.AccessToken, nil, http.StatusOK, &got)
	for _, m := range got {
		if m.ID == teammate.ID {
			if m.Role != "viewer" || m.Source != "project" {
				t.Errorf("override didn't apply: %+v", m)
			}
		}
	}
}

func TestRBAC_AddMember_NotTeamMember(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	stranger := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Strict Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	// Note: stranger is NOT a team_member.

	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+proj.ID+"/add_member",
		owner.AccessToken,
		map[string]string{"userId": stranger.ID, "role": "viewer"},
		http.StatusBadRequest)
	if env.Code != "not_team_member" {
		t.Errorf("code=%q want not_team_member", env.Code)
	}
}

func TestRBAC_AddMember_InvalidRole(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	teammate := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "InvRole Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	seedTeamMember(t, h, team.ID, teammate.ID, "member")

	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+proj.ID+"/add_member",
		owner.AccessToken,
		map[string]string{"userId": teammate.ID, "role": "owner"},
		http.StatusBadRequest)
	if env.Code != "invalid_role" {
		t.Errorf("code=%q want invalid_role", env.Code)
	}
}

func TestRBAC_AddMember_RequiresAdmin(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	caller := h.RegisterRandomUser()
	target := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Admin Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")

	// caller is team-member; target is team-member.
	seedTeamMember(t, h, team.ID, caller.ID, "member")
	seedTeamMember(t, h, team.ID, target.ID, "member")

	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+proj.ID+"/add_member",
		caller.AccessToken,
		map[string]string{"userId": target.ID, "role": "viewer"},
		http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("code=%q want forbidden", env.Code)
	}
}

// ---------- /update_member_role ----------

func TestRBAC_UpdateMemberRole_Upsert(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	teammate := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Upsert Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	seedTeamMember(t, h, team.ID, teammate.ID, "member")

	// First update creates the override; second updates it.
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/update_member_role",
		owner.AccessToken,
		map[string]string{"memberId": teammate.ID, "role": "viewer"},
		http.StatusOK, &map[string]string{})

	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/update_member_role",
		owner.AccessToken,
		map[string]string{"memberId": teammate.ID, "role": "admin"},
		http.StatusOK, &map[string]string{})

	var role string
	if err := h.DB.QueryRow(h.rootCtx,
		`SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2`,
		proj.ID, teammate.ID).Scan(&role); err != nil {
		t.Fatalf("read role: %v", err)
	}
	if role != "admin" {
		t.Errorf("role=%q want admin", role)
	}
}

// ---------- /remove_member ----------

func TestRBAC_RemoveMember_FallsBackToTeamRole(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	teammate := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Remove Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	seedTeamMember(t, h, team.ID, teammate.ID, "admin")
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'viewer')`,
		proj.ID, teammate.ID); err != nil {
		t.Fatalf("override: %v", err)
	}

	// While viewer override is in place, teammate can't update the
	// project even though they're team admin.
	env := h.AssertStatus(http.MethodPut, "/v1/projects/"+proj.ID,
		teammate.AccessToken, map[string]any{"name": "Try"},
		http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("pre-remove: code=%q want forbidden", env.Code)
	}

	// Owner removes the override.
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/remove_member",
		owner.AccessToken,
		map[string]string{"memberId": teammate.ID},
		http.StatusOK, &map[string]string{})

	// Now teammate's team-admin role kicks back in via fallback.
	h.DoJSON(http.MethodPut, "/v1/projects/"+proj.ID,
		teammate.AccessToken, map[string]any{"name": "Renamed"},
		http.StatusOK, &map[string]any{})
}

func TestRBAC_RemoveMember_NoOverride404(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	teammate := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "NoOverride Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	seedTeamMember(t, h, team.ID, teammate.ID, "member")

	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+proj.ID+"/remove_member",
		owner.AccessToken,
		map[string]string{"memberId": teammate.ID},
		http.StatusNotFound)
	if env.Code != "no_override" {
		t.Errorf("code=%q want no_override", env.Code)
	}
}

func TestRBAC_RemoveMember_SelfRemoval(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	teammate := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Self Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	seedTeamMember(t, h, team.ID, teammate.ID, "member")
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'viewer')`,
		proj.ID, teammate.ID); err != nil {
		t.Fatalf("override: %v", err)
	}

	// teammate (viewer) removes their own override — allowed even
	// though they don't have admin.
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/remove_member",
		teammate.AccessToken,
		map[string]string{"memberId": teammate.ID},
		http.StatusOK, &map[string]string{})
}
