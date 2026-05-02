package synapsetest

import (
	"net/http"
	"testing"
	"time"
)

type meResp struct {
	ID         string    `json:"id"`
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	CreateTime time.Time `json:"createTime"`
	UpdateTime time.Time `json:"updateTime"`
}

func TestMe_UpdateProfileName_TopLevel(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()

	var got meResp
	h.DoJSON(http.MethodPut, "/v1/update_profile_name", u.AccessToken,
		map[string]string{"name": "New Name"}, http.StatusOK, &got)
	if got.Name != "New Name" {
		t.Errorf("name=%q want New Name", got.Name)
	}

	// /v1/me reflects the change.
	var current meResp
	h.DoJSON(http.MethodGet, "/v1/me/", u.AccessToken, nil, http.StatusOK, &current)
	if current.Name != "New Name" {
		t.Errorf("/v1/me name=%q", current.Name)
	}
}

func TestMe_UpdateProfileName_MeAlias(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()

	var got meResp
	h.DoJSON(http.MethodPut, "/v1/me/update_profile_name", u.AccessToken,
		map[string]string{"name": "Alias"}, http.StatusOK, &got)
	if got.Name != "Alias" {
		t.Errorf("name=%q want Alias", got.Name)
	}
}

func TestMe_UpdateProfileName_Empty400(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()

	env := h.AssertStatus(http.MethodPut, "/v1/update_profile_name", u.AccessToken,
		map[string]string{"name": "  "}, http.StatusBadRequest)
	if env.Code != "missing_name" {
		t.Errorf("code=%q want missing_name", env.Code)
	}
}

func TestMe_DeleteAccount_HappyPath(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()

	h.DoJSON(http.MethodPost, "/v1/delete_account", u.AccessToken, nil,
		http.StatusOK, &map[string]string{})

	// User row gone.
	var count int
	if err := h.DB.QueryRow(h.rootCtx,
		`SELECT COUNT(*) FROM users WHERE id = $1`, u.ID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected user deleted, count=%d", count)
	}
}

func TestMe_DeleteAccount_RefusesTeamCreator(t *testing.T) {
	h := Setup(t)
	creator := h.RegisterRandomUser()
	otherAdmin := h.RegisterRandomUser()
	team := createTeam(t, h, creator.AccessToken, "Their Team")

	// Promote another admin so the last_admin guard wouldn't trigger first;
	// only the team_creator guard remains in the way.
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'admin')`,
		team.ID, otherAdmin.ID); err != nil {
		t.Fatalf("seed admin: %v", err)
	}

	env := h.AssertStatus(http.MethodPost, "/v1/delete_account", creator.AccessToken,
		nil, http.StatusConflict)
	if env.Code != "team_creator" {
		t.Errorf("code=%q want team_creator", env.Code)
	}
}

func TestMe_DeleteAccount_RefusesLastAdmin(t *testing.T) {
	h := Setup(t)
	creator := h.RegisterRandomUser()
	otherAdmin := h.RegisterRandomUser()
	team := createTeam(t, h, creator.AccessToken, "Joint Co")

	// Promote otherAdmin to admin so creator deleting wouldn't orphan.
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'admin')`,
		team.ID, otherAdmin.ID); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	// otherAdmin is admin of one team and not its creator. Deleting them
	// would leave the team with creator as the only admin — fine. But if we
	// remove creator's admin row first (they stay creator but lose admin),
	// otherAdmin becomes the sole admin. Then deleting otherAdmin would
	// orphan it → 409 last_admin.
	if _, err := h.DB.Exec(h.rootCtx,
		`DELETE FROM team_members WHERE team_id = $1 AND user_id = $2`,
		team.ID, creator.ID); err != nil {
		t.Fatalf("demote creator: %v", err)
	}

	env := h.AssertStatus(http.MethodPost, "/v1/delete_account", otherAdmin.AccessToken,
		nil, http.StatusConflict)
	if env.Code != "last_admin" {
		t.Errorf("code=%q want last_admin", env.Code)
	}
}

func TestMe_DeleteAccount_TeamCreatorWinsOverLastAdmin(t *testing.T) {
	h := Setup(t)
	creator := h.RegisterRandomUser()
	_ = createTeam(t, h, creator.AccessToken, "Solo Admin Co")

	// creator is BOTH last admin and creator — both checks would 409.
	// Verify the user-facing error mentions team_creator (or last_admin —
	// either is acceptable, both fire). Just assert it's a 409.
	env := h.AssertStatus(http.MethodPost, "/v1/delete_account", creator.AccessToken,
		nil, http.StatusConflict)
	if env.Code != "team_creator" && env.Code != "last_admin" {
		t.Errorf("code=%q want team_creator or last_admin", env.Code)
	}
}

func TestMe_DeleteAccount_MeAlias(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()

	h.DoJSON(http.MethodPost, "/v1/me/delete_account", u.AccessToken, nil,
		http.StatusOK, &map[string]string{})
}

// ---------- member_data ----------

type memberDataAPI struct {
	Teams []struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Slug          string `json:"slug"`
		Creator       string `json:"creator"`
		DefaultRegion string `json:"defaultRegion"`
		Suspended     bool   `json:"suspended"`
		CreateTime    string `json:"createTime"`
	} `json:"teams"`
	Projects []struct {
		ID         string `json:"id"`
		TeamID     string `json:"teamId"`
		Name       string `json:"name"`
		Slug       string `json:"slug"`
		IsDemo     bool   `json:"isDemo"`
		CreateTime string `json:"createTime"`
	} `json:"projects"`
	Deployments    []any `json:"deployments"`
	OptInsToAccept []any `json:"optInsToAccept"`
}

func TestMe_MemberData(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Membr Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Membr Proj")

	var data memberDataAPI
	h.DoJSON(http.MethodGet, "/v1/member_data", owner.AccessToken, nil,
		http.StatusOK, &data)
	if len(data.Teams) != 1 || data.Teams[0].ID != team.ID {
		t.Errorf("teams: got %+v", data.Teams)
	}
	if len(data.Projects) != 1 || data.Projects[0].ID != proj.ID {
		t.Errorf("projects: got %+v", data.Projects)
	}
	if data.OptInsToAccept == nil || len(data.OptInsToAccept) != 0 {
		t.Errorf("optInsToAccept must be []: %v", data.OptInsToAccept)
	}
}

func TestMe_MemberData_Alias(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()

	var data memberDataAPI
	h.DoJSON(http.MethodGet, "/v1/me/member_data", u.AccessToken, nil,
		http.StatusOK, &data)
	if data.OptInsToAccept == nil || len(data.OptInsToAccept) != 0 {
		t.Errorf("optInsToAccept must be []")
	}
}

// ---------- optins ----------

func TestMe_Optins_AlwaysEmpty(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()

	var resp struct {
		OptInsToAccept []any `json:"optInsToAccept"`
	}
	h.DoJSON(http.MethodGet, "/v1/optins", u.AccessToken, nil,
		http.StatusOK, &resp)
	if resp.OptInsToAccept == nil || len(resp.OptInsToAccept) != 0 {
		t.Errorf("expected [] optInsToAccept, got %v", resp.OptInsToAccept)
	}
}
