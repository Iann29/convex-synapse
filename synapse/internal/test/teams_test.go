package synapsetest

import (
	"net/http"
	"testing"
	"time"
)

type teamResp struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Slug          string    `json:"slug"`
	Creator       string    `json:"creator"`
	DefaultRegion string    `json:"defaultRegion"`
	Suspended     bool      `json:"suspended"`
	CreateTime    time.Time `json:"createTime"`
}

type teamMemberResp struct {
	TeamID     string    `json:"teamId"`
	ID         string    `json:"id"` // user id
	Role       string    `json:"role"`
	Email      string    `json:"email,omitempty"`
	Name       string    `json:"name,omitempty"`
	CreateTime time.Time `json:"createTime"`
}

// createTeam is a small helper used across tests in this file.
func createTeam(t *testing.T, h *Harness, bearer, name string) teamResp {
	t.Helper()
	var got teamResp
	h.DoJSON(http.MethodPost, "/v1/teams/create_team", bearer,
		map[string]string{"name": name}, http.StatusCreated, &got)
	return got
}

func TestTeams_CreateAndListMine(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()

	team := createTeam(t, h, u.AccessToken, "Acme Co")
	if team.Name != "Acme Co" {
		t.Errorf("team name mismatch: got %q", team.Name)
	}
	if team.Slug == "" {
		t.Errorf("expected non-empty slug")
	}
	if team.DefaultRegion != "self-hosted" {
		t.Errorf("expected default region self-hosted, got %q", team.DefaultRegion)
	}
	if team.Creator != u.ID {
		t.Errorf("expected creator %s, got %s", u.ID, team.Creator)
	}

	var teams []teamResp
	h.DoJSON(http.MethodGet, "/v1/teams/", u.AccessToken, nil, http.StatusOK, &teams)
	if len(teams) != 1 || teams[0].ID != team.ID {
		t.Errorf("expected exactly one team in list, got %+v", teams)
	}
}

func TestTeams_CreateMissingName(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	env := h.AssertStatus(http.MethodPost, "/v1/teams/create_team", u.AccessToken,
		map[string]string{"name": "  "}, http.StatusBadRequest)
	if env.Code != "missing_name" {
		t.Errorf("expected code missing_name, got %q", env.Code)
	}
}

func TestTeams_GetBySlug(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	team := createTeam(t, h, u.AccessToken, "Lookup Test")

	var got teamResp
	h.DoJSON(http.MethodGet, "/v1/teams/"+team.Slug, u.AccessToken, nil, http.StatusOK, &got)
	if got.ID != team.ID {
		t.Errorf("get by slug: got id %s want %s", got.ID, team.ID)
	}

	// Same lookup, but by UUID.
	var byID teamResp
	h.DoJSON(http.MethodGet, "/v1/teams/"+team.ID, u.AccessToken, nil, http.StatusOK, &byID)
	if byID.Slug != team.Slug {
		t.Errorf("get by id: got slug %s want %s", byID.Slug, team.Slug)
	}
}

func TestTeams_GetUnknown(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	env := h.AssertStatus(http.MethodGet, "/v1/teams/no-such-team", u.AccessToken,
		nil, http.StatusNotFound)
	if env.Code != "team_not_found" {
		t.Errorf("expected code team_not_found, got %q", env.Code)
	}
}

func TestTeams_ListMembersIncludesCreatorAsAdmin(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	team := createTeam(t, h, u.AccessToken, "Member Test")

	var members []teamMemberResp
	h.DoJSON(http.MethodGet, "/v1/teams/"+team.Slug+"/list_members", u.AccessToken,
		nil, http.StatusOK, &members)

	if len(members) != 1 {
		t.Fatalf("expected 1 member, got %d (%+v)", len(members), members)
	}
	if members[0].ID != u.ID {
		t.Errorf("member id mismatch: got %s want %s", members[0].ID, u.ID)
	}
	if members[0].Role != "admin" {
		t.Errorf("creator should be admin, got role %q", members[0].Role)
	}
	if members[0].Email != u.Email {
		t.Errorf("expected member email %s, got %s", u.Email, members[0].Email)
	}
}

func TestTeams_NonMemberGet403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	stranger := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Private Co")

	env := h.AssertStatus(http.MethodGet, "/v1/teams/"+team.Slug, stranger.AccessToken,
		nil, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("expected code forbidden, got %q", env.Code)
	}
}

func TestTeams_NonMemberCannotListMembers(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	stranger := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Other Co")

	h.AssertStatus(http.MethodGet, "/v1/teams/"+team.Slug+"/list_members",
		stranger.AccessToken, nil, http.StatusForbidden)
}

func TestTeams_InviteRequiresAdmin(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	other := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Invite Co")

	// Add `other` as a plain member directly (no public "promote-to-member"
	// path that doesn't go through invites; bypass the invite flow here).
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, other.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	// Member can read the team
	var seen teamResp
	h.DoJSON(http.MethodGet, "/v1/teams/"+team.Slug, other.AccessToken, nil, http.StatusOK, &seen)
	if seen.ID != team.ID {
		t.Errorf("member should see team, got %+v", seen)
	}

	// But cannot invite
	env := h.AssertStatus(http.MethodPost, "/v1/teams/"+team.Slug+"/invite_team_member",
		other.AccessToken, map[string]string{"email": "victim@example.test", "role": "member"},
		http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("expected code forbidden, got %q", env.Code)
	}
}

func TestTeams_InviteAdminHappyPath(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Inviter Co")

	type inviteResp struct {
		InviteID    string `json:"inviteId"`
		Email       string `json:"email"`
		Role        string `json:"role"`
		InviteToken string `json:"inviteToken"`
	}
	var got inviteResp
	h.DoJSON(http.MethodPost, "/v1/teams/"+team.Slug+"/invite_team_member",
		owner.AccessToken,
		map[string]string{"email": "joiner@example.test", "role": "member"},
		http.StatusOK, &got)
	if got.Email != "joiner@example.test" {
		t.Errorf("invite email: got %q", got.Email)
	}
	if got.InviteToken == "" {
		t.Errorf("expected invite token to be returned")
	}
	if got.Role != "member" {
		t.Errorf("expected role member, got %q", got.Role)
	}
}

func TestTeams_InviteValidation(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Validate Co")

	cases := []struct {
		name     string
		body     map[string]string
		wantCode string
	}{
		{"missing email", map[string]string{"email": "", "role": "member"}, "invalid_email"},
		{"malformed email", map[string]string{"email": "not-email", "role": "member"}, "invalid_email"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := h.AssertStatus(http.MethodPost, "/v1/teams/"+team.Slug+"/invite_team_member",
				owner.AccessToken, tc.body, http.StatusBadRequest)
			if env.Code != tc.wantCode {
				t.Errorf("got code %q want %q", env.Code, tc.wantCode)
			}
		})
	}
}

// ---------- update_team ----------

func TestTeams_Update_NameAndSlug(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "First Co")

	var got teamResp
	h.DoJSON(http.MethodPost, "/v1/teams/"+team.Slug, owner.AccessToken,
		map[string]any{"name": "Renamed Co", "slug": "renamed-co"},
		http.StatusOK, &got)
	if got.Name != "Renamed Co" || got.Slug != "renamed-co" {
		t.Errorf("got name=%q slug=%q", got.Name, got.Slug)
	}

	// New slug routes correctly.
	var fetched teamResp
	h.DoJSON(http.MethodGet, "/v1/teams/renamed-co", owner.AccessToken, nil,
		http.StatusOK, &fetched)
	if fetched.ID != team.ID {
		t.Errorf("re-fetch by new slug failed")
	}
}

func TestTeams_Update_DefaultRegion(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Region Co")

	region := "eu-west-1"
	var got teamResp
	h.DoJSON(http.MethodPost, "/v1/teams/"+team.Slug, owner.AccessToken,
		map[string]any{"defaultRegion": region}, http.StatusOK, &got)
	if got.DefaultRegion != region {
		t.Errorf("region=%q want %q", got.DefaultRegion, region)
	}
}

func TestTeams_Update_NonAdmin403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	other := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Locked Co")

	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, other.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	env := h.AssertStatus(http.MethodPost, "/v1/teams/"+team.Slug, other.AccessToken,
		map[string]any{"name": "Hijacked"}, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("code=%q want forbidden", env.Code)
	}
}

func TestTeams_Update_SlugConflict(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	first := createTeam(t, h, owner.AccessToken, "First Co")
	second := createTeam(t, h, owner.AccessToken, "Second Co")

	env := h.AssertStatus(http.MethodPost, "/v1/teams/"+second.Slug, owner.AccessToken,
		map[string]any{"slug": first.Slug}, http.StatusConflict)
	if env.Code != "slug_taken" {
		t.Errorf("code=%q want slug_taken", env.Code)
	}
}

func TestTeams_Update_InvalidSlug(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Bad Slug Co")
	env := h.AssertStatus(http.MethodPost, "/v1/teams/"+team.Slug, owner.AccessToken,
		map[string]any{"slug": "Has Spaces"}, http.StatusBadRequest)
	if env.Code != "invalid_slug" {
		t.Errorf("code=%q want invalid_slug", env.Code)
	}
}

// ---------- delete_team ----------

func TestTeams_Delete_HappyPath(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Doomed Co")

	h.DoJSON(http.MethodPost, "/v1/teams/"+team.Slug+"/delete", owner.AccessToken,
		nil, http.StatusOK, &map[string]string{})

	// Subsequent get → 404.
	env := h.AssertStatus(http.MethodGet, "/v1/teams/"+team.Slug, owner.AccessToken,
		nil, http.StatusNotFound)
	if env.Code != "team_not_found" {
		t.Errorf("code=%q want team_not_found", env.Code)
	}
}

func TestTeams_Delete_NonAdmin403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	other := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Defended Co")
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, other.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	env := h.AssertStatus(http.MethodPost, "/v1/teams/"+team.Slug+"/delete",
		other.AccessToken, nil, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("code=%q want forbidden", env.Code)
	}
}

func TestTeams_Delete_RefusesWithDeployments(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Loaded Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Has Stuff")
	// Seed a fake deployment row directly so we don't drag the docker
	// provisioner into the test.
	h.SeedDeployment(proj.ID, "", "dev", "running", true, owner.ID, 4001, "")

	env := h.AssertStatus(http.MethodPost, "/v1/teams/"+team.Slug+"/delete",
		owner.AccessToken, nil, http.StatusConflict)
	if env.Code != "team_has_deployments" {
		t.Errorf("code=%q want team_has_deployments", env.Code)
	}
}

func TestTeams_Delete_AllowsAfterDeploymentsCleared(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Cleanup Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Cleanable")
	h.SeedDeployment(proj.ID, "", "dev", "deleted", true, owner.ID, 4002, "")

	// 'deleted' status is excluded from the count, so this succeeds.
	h.DoJSON(http.MethodPost, "/v1/teams/"+team.Slug+"/delete", owner.AccessToken,
		nil, http.StatusOK, &map[string]string{})
}

// ---------- update_member_role ----------

func TestTeams_UpdateMemberRole_Promote(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	other := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Promote Co")
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, other.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	h.DoJSON(http.MethodPost, "/v1/teams/"+team.Slug+"/update_member_role",
		owner.AccessToken,
		map[string]string{"memberId": other.ID, "role": "admin"},
		http.StatusOK, &map[string]string{})

	var role string
	if err := h.DB.QueryRow(h.rootCtx,
		`SELECT role FROM team_members WHERE team_id = $1 AND user_id = $2`,
		team.ID, other.ID).Scan(&role); err != nil {
		t.Fatalf("read role: %v", err)
	}
	if role != "admin" {
		t.Errorf("role=%q want admin", role)
	}
}

func TestTeams_UpdateMemberRole_DeveloperAlias(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	other := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Alias Co")
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'admin')`,
		team.ID, other.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	// Cloud uses 'developer'; we map → member.
	h.DoJSON(http.MethodPost, "/v1/teams/"+team.Slug+"/update_member_role",
		owner.AccessToken,
		map[string]string{"memberId": other.ID, "role": "developer"},
		http.StatusOK, &map[string]string{})

	var role string
	if err := h.DB.QueryRow(h.rootCtx,
		`SELECT role FROM team_members WHERE team_id = $1 AND user_id = $2`,
		team.ID, other.ID).Scan(&role); err != nil {
		t.Fatalf("read role: %v", err)
	}
	if role != "member" {
		t.Errorf("developer should map to member, got %q", role)
	}
}

func TestTeams_UpdateMemberRole_LastAdmin409(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Solo Admin Co")

	env := h.AssertStatus(http.MethodPost, "/v1/teams/"+team.Slug+"/update_member_role",
		owner.AccessToken,
		map[string]string{"memberId": owner.ID, "role": "developer"},
		http.StatusConflict)
	if env.Code != "last_admin" {
		t.Errorf("code=%q want last_admin", env.Code)
	}
}

func TestTeams_UpdateMemberRole_NonAdmin403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	other := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Restrict Co")
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, other.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	env := h.AssertStatus(http.MethodPost, "/v1/teams/"+team.Slug+"/update_member_role",
		other.AccessToken,
		map[string]string{"memberId": owner.ID, "role": "developer"},
		http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("code=%q want forbidden", env.Code)
	}
}

func TestTeams_UpdateMemberRole_UnknownMember404(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Strangers Co")

	env := h.AssertStatus(http.MethodPost, "/v1/teams/"+team.Slug+"/update_member_role",
		owner.AccessToken,
		map[string]string{
			"memberId": "00000000-0000-0000-0000-000000000000",
			"role":     "admin",
		},
		http.StatusNotFound)
	if env.Code != "member_not_found" {
		t.Errorf("code=%q want member_not_found", env.Code)
	}
}

func TestTeams_UpdateMemberRole_InvalidRole(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	other := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Bad Role Co")
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, other.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	env := h.AssertStatus(http.MethodPost, "/v1/teams/"+team.Slug+"/update_member_role",
		owner.AccessToken,
		map[string]string{"memberId": other.ID, "role": "owner"},
		http.StatusBadRequest)
	if env.Code != "invalid_role" {
		t.Errorf("code=%q want invalid_role", env.Code)
	}
}

// ---------- remove_member ----------

func TestTeams_RemoveMember_AdminRemovesOther(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	other := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Boot Co")
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, other.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	h.DoJSON(http.MethodPost, "/v1/teams/"+team.Slug+"/remove_member", owner.AccessToken,
		map[string]string{"memberId": other.ID}, http.StatusOK, &map[string]string{})

	var count int
	if err := h.DB.QueryRow(h.rootCtx,
		`SELECT COUNT(*) FROM team_members WHERE team_id = $1 AND user_id = $2`,
		team.ID, other.ID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected member removed, count=%d", count)
	}
}

func TestTeams_RemoveMember_SelfRemoval(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	other := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Quitter Co")
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, other.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	// Self-removal allowed even though caller is non-admin.
	h.DoJSON(http.MethodPost, "/v1/teams/"+team.Slug+"/remove_member", other.AccessToken,
		map[string]string{"memberId": other.ID}, http.StatusOK, &map[string]string{})
}

func TestTeams_RemoveMember_NonAdminTryingOther403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	other := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Strict Co")
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, other.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	env := h.AssertStatus(http.MethodPost, "/v1/teams/"+team.Slug+"/remove_member",
		other.AccessToken,
		map[string]string{"memberId": owner.ID}, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("code=%q want forbidden", env.Code)
	}
}

func TestTeams_RemoveMember_LastAdmin409(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Solo Admin Co")
	env := h.AssertStatus(http.MethodPost, "/v1/teams/"+team.Slug+"/remove_member",
		owner.AccessToken,
		map[string]string{"memberId": owner.ID}, http.StatusConflict)
	if env.Code != "last_admin" {
		t.Errorf("code=%q want last_admin", env.Code)
	}
}

func TestTeams_SlugCollisionAddsSuffix(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	first := createTeam(t, h, u.AccessToken, "Collision")
	second := createTeam(t, h, u.AccessToken, "Collision")

	if first.Slug == second.Slug {
		t.Errorf("expected slug suffix on second team, got %s == %s", first.Slug, second.Slug)
	}
}
