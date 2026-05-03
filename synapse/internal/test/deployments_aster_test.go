package synapsetest

import (
	"net/http"
	"testing"
	"time"
)

// TestDeployments_CreateAsterEnqueuesProvisioning is the contract test
// for the kind=aster path: the row is created in 'provisioning' state,
// no host port is allocated, no admin key is generated, and a
// provisioning_job is enqueued so the worker spawns the brokerd
// container with spec.Kind="aster".
func TestDeployments_CreateAsterEnqueuesProvisioning(t *testing.T) {
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
	if got.Status != "provisioning" {
		t.Errorf("status: got %q want provisioning (worker hasn't run yet)", got.Status)
	}
	if got.Name == "" {
		t.Fatal("expected a generated name")
	}

	// Wait for the worker to flip to running. FakeDocker.Provision
	// returns immediately with a fake-container id.
	waitForStatus(t, h, got.Name, "running", 5*time.Second)

	// FakeDocker SHOULD have been called exactly once with Kind="aster".
	// host_port is zero (UDS, no TCP listener); InstanceSecret carries
	// the seal-key seed for the brokerd's CapsuleSealKey.
	if n := len(h.Docker.Provisioned); n != 1 {
		t.Fatalf("FakeDocker.Provisioned: got %d calls, want 1", n)
	}
	spec := h.Docker.Provisioned[0]
	if spec.Kind != "aster" {
		t.Errorf("spec.Kind: got %q want aster", spec.Kind)
	}
	if spec.Name != got.Name {
		t.Errorf("spec.Name: got %q want %q", spec.Name, got.Name)
	}
	if spec.HostPort != 0 {
		t.Errorf("spec.HostPort: got %d want 0 (aster has no TCP port)", spec.HostPort)
	}
	if spec.HAReplica {
		t.Errorf("spec.HAReplica: got true want false")
	}
	if spec.Storage != nil {
		t.Errorf("spec.Storage: got non-nil; aster has no Postgres+S3 env")
	}
	if spec.InstanceSecret == "" {
		t.Errorf("spec.InstanceSecret empty; broker needs the seal-key seed")
	}

	// DB row shape after worker pass: kind=aster, status=running,
	// container_id is the fake provisioner output. host_port stays
	// NULL — there's no TCP listener.
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
	if containerID == nil || *containerID == "" {
		t.Errorf("DB container_id: got nil/empty want fake-container-...")
	}
	if hostPort != nil {
		t.Errorf("DB host_port: got %v want NULL (no TCP listener for aster)", *hostPort)
	}
	if adminKey != "" {
		t.Errorf("DB admin_key: got %q want empty (aster has no admin key)", adminKey)
	}

	// Replica row was flipped to 'running' by the worker too.
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
		t.Errorf("status: got %q want provisioning", got.Status)
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
// surfaces kind.
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
	waitForStatus(t, h, asterDep.Name, "running", 5*time.Second)

	var got deploymentResp
	h.DoJSON(http.MethodGet, "/v1/deployments/"+asterDep.Name,
		owner.AccessToken, nil, http.StatusOK, &got)
	if got.Kind != "aster" {
		t.Errorf("kind: got %q want aster", got.Kind)
	}
}

// TestDeployments_DeleteAsterRoutesToDestroyAster — the delete handler
// must dispatch on Kind, not blindly call Destroy. A Convex Destroy on
// an aster row would try to remove a non-existent convex-{name} container
// and would not clean up the aster broker volume.
func TestDeployments_DeleteAsterRoutesToDestroyAster(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Del Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "DelProj")

	var asterDep deploymentResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/create_deployment",
		owner.AccessToken,
		map[string]any{"type": "dev", "kind": "aster"},
		http.StatusCreated, &asterDep)
	waitForStatus(t, h, asterDep.Name, "running", 5*time.Second)

	// Reset destroy logs so the assertion below is unambiguous.
	h.Docker.Destroyed = nil
	h.Docker.DestroyedAster = nil

	h.DoJSON(http.MethodPost, "/v1/deployments/"+asterDep.Name+"/delete",
		owner.AccessToken, nil, http.StatusOK, nil)

	if got := len(h.Docker.Destroyed); got != 0 {
		t.Errorf("Docker.Destroyed (Convex path) called %d times for aster row; want 0", got)
	}
	if got := len(h.Docker.DestroyedAster); got != 1 || h.Docker.DestroyedAster[0] != asterDep.Name {
		t.Errorf("Docker.DestroyedAster = %v, want exactly [%s]", h.Docker.DestroyedAster, asterDep.Name)
	}
}
