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

func TestTeams_SlugCollisionAddsSuffix(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	first := createTeam(t, h, u.AccessToken, "Collision")
	second := createTeam(t, h, u.AccessToken, "Collision")

	if first.Slug == second.Slug {
		t.Errorf("expected slug suffix on second team, got %s == %s", first.Slug, second.Slug)
	}
}
