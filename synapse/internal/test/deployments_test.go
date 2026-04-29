package synapsetest

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

var errBoom = errors.New("simulated docker error")

type deploymentResp struct {
	ID             string     `json:"id"`
	ProjectID      string     `json:"projectId"`
	Name           string     `json:"name"`
	DeploymentType string     `json:"deploymentType"`
	Status         string     `json:"status"`
	DeploymentURL  string     `json:"deploymentUrl,omitempty"`
	IsDefault      bool       `json:"isDefault"`
	Reference      string     `json:"reference,omitempty"`
	Creator        string     `json:"creator,omitempty"`
	CreateTime     time.Time  `json:"createTime"`
	LastDeployTime *time.Time `json:"lastDeployTime,omitempty"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
}

type deploymentAuthResp struct {
	DeploymentName string `json:"deploymentName"`
	DeploymentURL  string `json:"deploymentUrl"`
	AdminKey       string `json:"adminKey"`
	DeploymentType string `json:"deploymentType"`
}

func TestDeployments_GetReturnsRow(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Dep Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "DepProj")

	id := h.SeedDeployment(proj.ID, "happy-cat-1234", "dev", "running", true, owner.ID, 3211, "")
	_ = id

	var got deploymentResp
	h.DoJSON(http.MethodGet, "/v1/deployments/happy-cat-1234", owner.AccessToken,
		nil, http.StatusOK, &got)
	if got.Name != "happy-cat-1234" {
		t.Errorf("name mismatch: %s", got.Name)
	}
	if got.Status != "running" {
		t.Errorf("status: got %s want running", got.Status)
	}
	if got.DeploymentURL == "" {
		t.Errorf("expected deployment URL")
	}
	// admin_key MUST NOT be in the response.
	// (This is enforced by `json:"-"` in models.Deployment; if a future
	// refactor breaks that, our DisallowUnknownFields decode will catch it.)
}

func TestDeployments_GetUnknown(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	env := h.AssertStatus(http.MethodGet, "/v1/deployments/no-such-name",
		owner.AccessToken, nil, http.StatusNotFound)
	if env.Code != "deployment_not_found" {
		t.Errorf("got code %q want deployment_not_found", env.Code)
	}
}

func TestDeployments_NonMember403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	stranger := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Closed Dep Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Closed")
	h.SeedDeployment(proj.ID, "secret-fox-9999", "prod", "running", false, owner.ID, 3212, "")

	env := h.AssertStatus(http.MethodGet, "/v1/deployments/secret-fox-9999",
		stranger.AccessToken, nil, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("got code %q want forbidden", env.Code)
	}
}

func TestDeployments_AuthReturnsAdminKey(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Auth Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "AuthProj")
	h.SeedDeployment(proj.ID, "auth-bee-5555", "prod", "running", true, owner.ID, 3213, "secret-key-xyz")

	var got deploymentAuthResp
	h.DoJSON(http.MethodGet, "/v1/deployments/auth-bee-5555/auth", owner.AccessToken,
		nil, http.StatusOK, &got)
	if got.AdminKey != "secret-key-xyz" {
		t.Errorf("admin key mismatch: got %q", got.AdminKey)
	}
	if got.DeploymentName != "auth-bee-5555" {
		t.Errorf("name mismatch: %s", got.DeploymentName)
	}
	if got.DeploymentType != "prod" {
		t.Errorf("type mismatch: %s", got.DeploymentType)
	}
	if got.DeploymentURL == "" {
		t.Errorf("expected deployment URL")
	}
}

func TestDeployments_AuthNonMember403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	stranger := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "AuthStranger Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(proj.ID, "stranger-owl-7777", "dev", "running", false, owner.ID, 3214, "")

	h.AssertStatus(http.MethodGet, "/v1/deployments/stranger-owl-7777/auth",
		stranger.AccessToken, nil, http.StatusForbidden)
}

func TestDeployments_DeleteMarksRowDeletedAndCallsDocker(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Del Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	id := h.SeedDeployment(proj.ID, "del-wolf-3333", "dev", "running", true, owner.ID, 3215, "")

	h.DoJSON(http.MethodPost, "/v1/deployments/del-wolf-3333/delete", owner.AccessToken,
		nil, http.StatusOK, &map[string]string{})

	// Fake docker should have been called with the deployment name.
	if len(h.Docker.Destroyed) != 1 || h.Docker.Destroyed[0] != "del-wolf-3333" {
		t.Errorf("expected fake Destroy([del-wolf-3333]), got %v", h.Docker.Destroyed)
	}

	// Row is now status=deleted with cleared host_port + container_id.
	var status string
	var hostPort *int
	if err := h.DB.QueryRow(h.rootCtx,
		`SELECT status, host_port FROM deployments WHERE id = $1`, id).Scan(&status, &hostPort); err != nil {
		t.Fatalf("read deployment row: %v", err)
	}
	if status != "deleted" {
		t.Errorf("status: got %q want deleted", status)
	}
	if hostPort != nil {
		t.Errorf("expected host_port to be nulled, got %v", *hostPort)
	}
}

func TestDeployments_DeleteAdminOnly(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	member := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Member Del Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(proj.ID, "stay-cat-4444", "dev", "running", true, owner.ID, 3216, "")

	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, member.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	env := h.AssertStatus(http.MethodPost, "/v1/deployments/stay-cat-4444/delete",
		member.AccessToken, nil, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("got code %q want forbidden", env.Code)
	}
	// Fake docker should NOT have been called.
	if len(h.Docker.Destroyed) != 0 {
		t.Errorf("expected no destroys, got %v", h.Docker.Destroyed)
	}
}

func TestDeployments_ListExcludesDeleted(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "List Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")

	// Two live deployments + one deleted.
	h.SeedDeployment(proj.ID, "live-fox-1111", "dev", "running", true, owner.ID, 3217, "")
	h.SeedDeployment(proj.ID, "live-fox-2222", "prod", "running", false, owner.ID, 3218, "")
	h.SeedDeployment(proj.ID, "dead-fox-3333", "dev", "deleted", false, owner.ID, 0, "")

	var fromTeam []deploymentResp
	h.DoJSON(http.MethodGet, "/v1/teams/"+team.Slug+"/list_deployments",
		owner.AccessToken, nil, http.StatusOK, &fromTeam)
	if len(fromTeam) != 2 {
		t.Errorf("team list_deployments: expected 2 live, got %d (%+v)", len(fromTeam), fromTeam)
	}

	var fromProject []deploymentResp
	h.DoJSON(http.MethodGet, "/v1/projects/"+proj.ID+"/list_deployments",
		owner.AccessToken, nil, http.StatusOK, &fromProject)
	if len(fromProject) != 2 {
		t.Errorf("project list_deployments: expected 2 live, got %d", len(fromProject))
	}

	// Deleted one should not be retrievable individually either.
	env := h.AssertStatus(http.MethodGet, "/v1/deployments/dead-fox-3333",
		owner.AccessToken, nil, http.StatusNotFound)
	if env.Code != "deployment_not_found" {
		t.Errorf("got code %q want deployment_not_found", env.Code)
	}
}

func TestDeployments_DeleteIdempotentOnDockerError(t *testing.T) {
	// If the fake docker returns an error, we should NOT mark the row deleted —
	// the operator can retry. Mirrors the prod contract.
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Err Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	id := h.SeedDeployment(proj.ID, "fail-bee-9999", "dev", "running", true, owner.ID, 3219, "")

	h.Docker.DestroyFn = func(_ context.Context, _ string) error {
		return errBoom
	}

	env := h.AssertStatus(http.MethodPost, "/v1/deployments/fail-bee-9999/delete",
		owner.AccessToken, nil, http.StatusInternalServerError)
	if env.Code != "destroy_failed" {
		t.Errorf("got code %q want destroy_failed", env.Code)
	}

	// Row remains running so the operator can retry after fixing docker.
	var status string
	if err := h.DB.QueryRow(h.rootCtx,
		`SELECT status FROM deployments WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if status != "running" {
		t.Errorf("status: got %q want running (retry-able)", status)
	}
}
