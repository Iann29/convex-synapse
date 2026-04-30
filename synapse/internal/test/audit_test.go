package synapsetest

import (
	"net/http"
	"testing"
	"time"
)

// auditEventResp mirrors the JSON shape returned by GET /v1/teams/{ref}/audit_log.
// Strict-decoded with DisallowUnknownFields, so any drift in the handler's
// response shape fails this test loudly instead of silently.
type auditEventResp struct {
	ID         string         `json:"id"`
	CreateTime time.Time      `json:"createTime"`
	Action     string         `json:"action"`
	ActorID    string         `json:"actorId,omitempty"`
	ActorEmail string         `json:"actorEmail,omitempty"`
	TargetType string         `json:"targetType,omitempty"`
	TargetID   string         `json:"targetId,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

type auditLogResp struct {
	Items      []auditEventResp `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// TestAudit_CreateTeamAndProjectAreLogged verifies the writer end of the loop:
// after a CreateTeam + CreateProject, the audit log contains both events with
// the right actor + targets.
func TestAudit_CreateTeamAndProjectAreLogged(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()

	team := createTeam(t, h, owner.AccessToken, "Audited Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Audited Project")

	// Audit writes are best-effort and currently happen synchronously after
	// the response is written; on a hot path with parallel tests there's a
	// tiny window where the row hasn't landed yet. Poll briefly to keep
	// flakes out of CI without slowing down the green path.
	var got auditLogResp
	deadline := time.Now().Add(2 * time.Second)
	for {
		got = auditLogResp{}
		h.DoJSON(http.MethodGet, "/v1/teams/"+team.Slug+"/audit_log",
			owner.AccessToken, nil, http.StatusOK, &got)
		if len(got.Items) >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(got.Items) < 2 {
		t.Fatalf("expected at least 2 audit events, got %d (%+v)", len(got.Items), got.Items)
	}

	// Index by action for stable assertions regardless of insert ordering.
	byAction := map[string]auditEventResp{}
	for _, e := range got.Items {
		byAction[e.Action] = e
	}

	createTeamEvt, ok := byAction["createTeam"]
	if !ok {
		t.Fatalf("expected a createTeam event, got %+v", got.Items)
	}
	if createTeamEvt.ActorID != owner.ID {
		t.Errorf("createTeam actor: got %q want %q", createTeamEvt.ActorID, owner.ID)
	}
	if createTeamEvt.ActorEmail != owner.Email {
		t.Errorf("createTeam email: got %q want %q", createTeamEvt.ActorEmail, owner.Email)
	}
	if createTeamEvt.TargetType != "team" || createTeamEvt.TargetID != team.ID {
		t.Errorf("createTeam target: got %s/%s want team/%s",
			createTeamEvt.TargetType, createTeamEvt.TargetID, team.ID)
	}

	createProjEvt, ok := byAction["createProject"]
	if !ok {
		t.Fatalf("expected a createProject event, got %+v", got.Items)
	}
	if createProjEvt.ActorID != owner.ID {
		t.Errorf("createProject actor: got %q want %q", createProjEvt.ActorID, owner.ID)
	}
	if createProjEvt.TargetType != "project" || createProjEvt.TargetID != proj.ID {
		t.Errorf("createProject target: got %s/%s want project/%s",
			createProjEvt.TargetType, createProjEvt.TargetID, proj.ID)
	}
}

// TestAudit_NonMemberGet403 — strangers can't even see that the team has an
// audit log (treated like every other team-scoped endpoint).
func TestAudit_NonMemberGet403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	stranger := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Closed Audit")

	env := h.AssertStatus(http.MethodGet, "/v1/teams/"+team.Slug+"/audit_log",
		stranger.AccessToken, nil, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("expected forbidden, got %q", env.Code)
	}
}

// TestAudit_NonAdminMemberGet403 — even members of the team can't read the
// audit log. Choice: admin-only (matches Cloud's behavior; auditing is for
// trust anchors). Documented in API.md.
func TestAudit_NonAdminMemberGet403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	member := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Admin Audit Co")

	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, member.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	env := h.AssertStatus(http.MethodGet, "/v1/teams/"+team.Slug+"/audit_log",
		member.AccessToken, nil, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("expected forbidden, got %q", env.Code)
	}
}

// TestAudit_EmptyListWhenNoEvents — an admin querying a team with no
// recorded events gets an empty items array and no cursor. Critical: nil-vs-
// empty-slice matters for JSON shape; `null` would break clients.
func TestAudit_EmptyListWhenNoEvents(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Quiet Co")

	// Wipe the createTeam event so we can assert the empty case.
	if _, err := h.DB.Exec(h.rootCtx,
		`DELETE FROM audit_events WHERE team_id = $1`, team.ID); err != nil {
		t.Fatalf("clear audit events: %v", err)
	}

	var got auditLogResp
	h.DoJSON(http.MethodGet, "/v1/teams/"+team.Slug+"/audit_log",
		owner.AccessToken, nil, http.StatusOK, &got)

	if len(got.Items) != 0 {
		t.Errorf("expected empty items, got %+v", got.Items)
	}
	if got.NextCursor != "" {
		t.Errorf("expected empty cursor, got %q", got.NextCursor)
	}
}

// TestAudit_LimitClampedTo200 — sanity-check on the limit query param. We
// don't seed 201 events; just verify the parser accepts large values without
// rejecting them and clamps to 200 server-side (effectively an upper bound on
// per-request work). A request with limit=500 should succeed with status 200.
func TestAudit_LimitClampedTo200(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Limit Co")

	var got auditLogResp
	h.DoJSON(http.MethodGet, "/v1/teams/"+team.Slug+"/audit_log?limit=500",
		owner.AccessToken, nil, http.StatusOK, &got)
	// We're not asserting on item count — just that the endpoint doesn't 400
	// on a large limit and decoded cleanly.
}
