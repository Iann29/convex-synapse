package synapsetest

import (
	"net/http"
	"testing"
)

// TestDeployments_CreateAsterRegistersWithoutContainer is the load-bearing
// test for the kind=aster path: the row exists with the right shape, the
// FakeDocker provisioner was *not* called, no host port was allocated, and
// no provisioning_jobs row was enqueued. This is the contract that lets
// us model an Aster deployment in Synapse before the Aster image ships.
func TestDeployments_CreateAsterRegistersWithoutContainer(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Aster Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "AsterProj")

	var got deploymentResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/create_deployment",
		owner.AccessToken,
		map[string]any{"type": "dev", "kind": "aster"},
		http.StatusCreated, &got)

	if got.Kind != "aster" {
		t.Errorf("kind: got %q want aster", got.Kind)
	}
	if got.Status != "running" {
		t.Errorf("status: got %q want running (aster is not provisioned)", got.Status)
	}
	if got.DeploymentURL != "" {
		t.Errorf("deploymentUrl: got %q want empty (no container behind kind=aster)", got.DeploymentURL)
	}
	if got.Name == "" {
		t.Fatal("expected a generated name")
	}

	// FakeDocker MUST NOT have been called for kind=aster.
	if n := len(h.Docker.Provisioned); n != 0 {
		t.Errorf("FakeDocker.Provisioned: got %d calls, want 0; specs=%+v",
			n, h.Docker.Provisioned)
	}

	// DB row shape: kind='aster', container_id NULL, host_port NULL,
	// admin_key empty.
	var (
		kind        string
		status      string
		containerID *string
		hostPort    *int
		adminKey    string
	)
	err := h.DB.QueryRow(h.rootCtx,
		`SELECT kind, status, container_id, host_port, admin_key
		   FROM deployments WHERE id = $1`, got.ID).
		Scan(&kind, &status, &containerID, &hostPort, &adminKey)
	if err != nil {
		t.Fatalf("query deployment: %v", err)
	}
	if kind != "aster" {
		t.Errorf("DB kind: got %q want aster", kind)
	}
	if status != "running" {
		t.Errorf("DB status: got %q want running", status)
	}
	if containerID != nil {
		t.Errorf("DB container_id: got %v want NULL", *containerID)
	}
	if hostPort != nil {
		t.Errorf("DB host_port: got %v want NULL", *hostPort)
	}
	if adminKey != "" {
		t.Errorf("DB admin_key: got %q want empty (aster has no admin key)", adminKey)
	}

	// No provisioning_jobs row should have been enqueued.
	var jobCount int
	err = h.DB.QueryRow(h.rootCtx,
		`SELECT count(*) FROM provisioning_jobs WHERE deployment_id = $1`, got.ID).
		Scan(&jobCount)
	if err != nil {
		t.Fatalf("count provisioning_jobs: %v", err)
	}
	if jobCount != 0 {
		t.Errorf("provisioning_jobs: got %d, want 0 for kind=aster", jobCount)
	}

	// Synthetic replica row preserves the "every deployment has a
	// replica" invariant. host_port and container_id are NULL.
	var (
		replicaCount  int
		replicaStatus string
		replicaHost   *int
	)
	err = h.DB.QueryRow(h.rootCtx,
		`SELECT count(*) FROM deployment_replicas WHERE deployment_id = $1`, got.ID).
		Scan(&replicaCount)
	if err != nil {
		t.Fatalf("count replicas: %v", err)
	}
	if replicaCount != 1 {
		t.Fatalf("expected exactly 1 replica row for aster, got %d", replicaCount)
	}
	err = h.DB.QueryRow(h.rootCtx,
		`SELECT status, host_port FROM deployment_replicas WHERE deployment_id = $1`, got.ID).
		Scan(&replicaStatus, &replicaHost)
	if err != nil {
		t.Fatalf("read replica: %v", err)
	}
	if replicaStatus != "running" {
		t.Errorf("replica status: got %q want running", replicaStatus)
	}
	if replicaHost != nil {
		t.Errorf("replica host_port: got %v want NULL", *replicaHost)
	}
}

// TestDeployments_CreateDefaultKindIsConvex confirms that an old client
// not yet aware of `kind` keeps getting Convex deployments.
func TestDeployments_CreateDefaultKindIsConvex(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Default Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "DefaultProj")

	var got deploymentResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/create_deployment",
		owner.AccessToken,
		map[string]string{"type": "dev"}, // no kind
		http.StatusCreated, &got)

	if got.Kind != "convex" {
		t.Errorf("kind: got %q want convex (default)", got.Kind)
	}
	if got.Status != "provisioning" {
		t.Errorf("status: got %q want provisioning (convex provisions a container)", got.Status)
	}
}

// TestDeployments_CreateRejectsInvalidKind keeps the API contract honest.
func TestDeployments_CreateRejectsInvalidKind(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Invalid Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "InvalidProj")

	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+proj.ID+"/create_deployment",
		owner.AccessToken,
		map[string]any{"type": "dev", "kind": "lambda"},
		http.StatusBadRequest)
	if env.Code != "invalid_kind" {
		t.Errorf("code: got %q want invalid_kind", env.Code)
	}
}

// TestDeployments_CreateRejectsAsterPlusHA — HA semantics are tied to the
// Convex backend (Postgres + S3 + dual replicas). Aster's horizontal story
// is its own thing; refusing the combination keeps the contract honest.
func TestDeployments_CreateRejectsAsterPlusHA(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Combo Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "ComboProj")

	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+proj.ID+"/create_deployment",
		owner.AccessToken,
		map[string]any{"type": "dev", "kind": "aster", "ha": true},
		http.StatusBadRequest)
	if env.Code != "invalid_combination" {
		t.Errorf("code: got %q want invalid_combination", env.Code)
	}
}

// TestDeployments_ListIncludesKind verifies both list endpoints return
// kind so the dashboard can render an "Aster" badge without a second
// round-trip.
func TestDeployments_ListIncludesKind(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Mix Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "MixProj")

	// Convex (default).
	var convexDep deploymentResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/create_deployment",
		owner.AccessToken,
		map[string]string{"type": "dev"},
		http.StatusCreated, &convexDep)

	// Aster.
	var asterDep deploymentResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/create_deployment",
		owner.AccessToken,
		map[string]any{"type": "prod", "kind": "aster"},
		http.StatusCreated, &asterDep)

	// Project list.
	var projList []deploymentResp
	h.DoJSON(http.MethodGet, "/v1/projects/"+proj.ID+"/list_deployments",
		owner.AccessToken, nil, http.StatusOK, &projList)
	gotKinds := map[string]string{}
	for _, d := range projList {
		gotKinds[d.Name] = d.Kind
	}
	if gotKinds[convexDep.Name] != "convex" {
		t.Errorf("project list[%s].kind: got %q want convex", convexDep.Name, gotKinds[convexDep.Name])
	}
	if gotKinds[asterDep.Name] != "aster" {
		t.Errorf("project list[%s].kind: got %q want aster", asterDep.Name, gotKinds[asterDep.Name])
	}

	// Team list.
	var teamList []deploymentResp
	h.DoJSON(http.MethodGet, "/v1/teams/"+team.Slug+"/list_deployments",
		owner.AccessToken, nil, http.StatusOK, &teamList)
	gotKinds = map[string]string{}
	for _, d := range teamList {
		gotKinds[d.Name] = d.Kind
	}
	if gotKinds[convexDep.Name] != "convex" {
		t.Errorf("team list[%s].kind: got %q want convex", convexDep.Name, gotKinds[convexDep.Name])
	}
	if gotKinds[asterDep.Name] != "aster" {
		t.Errorf("team list[%s].kind: got %q want aster", asterDep.Name, gotKinds[asterDep.Name])
	}
}

// TestDeployments_GetReturnsKind confirms the single-row endpoint also
// surfaces kind. Same guarantee as the list — dashboards/CLIs branch UI
// without an extra round-trip.
func TestDeployments_GetReturnsKind(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Get Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "GetProj")

	var asterDep deploymentResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/create_deployment",
		owner.AccessToken,
		map[string]any{"type": "dev", "kind": "aster"},
		http.StatusCreated, &asterDep)

	var got deploymentResp
	h.DoJSON(http.MethodGet, "/v1/deployments/"+asterDep.Name,
		owner.AccessToken, nil, http.StatusOK, &got)
	if got.Kind != "aster" {
		t.Errorf("kind: got %q want aster", got.Kind)
	}
}
