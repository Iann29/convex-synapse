package synapsetest

import (
	"net/http"
	"testing"
)

// TestMigration_BackfillsReplicasFromExistingDeployments ensures that
// migration 000004 creates a deployment_replicas row for every existing
// deployment — even ones that were inserted *after* the migration ran
// (the test server applies all migrations at startup, so to exercise the
// backfill INSERT we instead verify that the post-migration code path
// keeps the invariant: every non-deleted deployment has a replica row
// matching its host_port + container_id).
//
// The harness already runs all migrations on each fresh DB. We seed a
// deployment via SeedDeployment (which inserts directly into the
// deployments table without going through any v0.5-aware code) and
// observe that the backfill SELECT still finds it on a re-run.
func TestMigration_BackfillsReplicasFromExistingDeployments(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Migration Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")

	// Seed a deployment as if the operator had created it pre-v0.5
	// (SeedDeployment goes straight to SQL, doesn't write a replica row).
	depID := h.SeedDeployment(proj.ID, "legacy-cat-1234", "dev", "running",
		false, owner.ID, 4242, "legacy-key")

	// Re-run the backfill INSERT to simulate "the migration ran on a DB
	// that already has rows". Idempotent thanks to the NOT EXISTS guard.
	if _, err := h.DB.Exec(h.rootCtx, `
		INSERT INTO deployment_replicas (deployment_id, replica_index, container_id, host_port, status, created_at)
		SELECT id, 0, container_id, host_port,
		       CASE WHEN status = 'deleted' THEN 'stopped' ELSE status END,
		       created_at
		  FROM deployments
		 WHERE NOT EXISTS (
		       SELECT 1 FROM deployment_replicas r WHERE r.deployment_id = deployments.id
		 )
	`); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var (
		count    int
		port     int
		containerID string
		status   string
		idx      int
	)
	err := h.DB.QueryRow(h.rootCtx, `
		SELECT count(*) FROM deployment_replicas WHERE deployment_id = $1
	`, depID).Scan(&count)
	if err != nil {
		t.Fatalf("count replicas: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 replica row for legacy deployment, got %d", count)
	}

	err = h.DB.QueryRow(h.rootCtx, `
		SELECT replica_index, container_id, host_port, status
		  FROM deployment_replicas
		 WHERE deployment_id = $1
	`, depID).Scan(&idx, &containerID, &port, &status)
	if err != nil {
		t.Fatalf("read replica: %v", err)
	}
	if idx != 0 {
		t.Errorf("replica_index: got %d want 0", idx)
	}
	if port != 4242 {
		t.Errorf("host_port: got %d want 4242", port)
	}
	if status != "running" {
		t.Errorf("status: got %q want running", status)
	}
	if containerID == "" {
		t.Error("container_id should have been backfilled, got empty")
	}
}

// TestMigration_DeploymentDefaultsForHA confirms that a freshly-created
// deployment row defaults to the single-replica + ha_disabled values, so
// no existing handler logic needs to special-case "old shape" rows.
func TestMigration_DeploymentDefaultsForHA(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Defaults Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")
	id := h.SeedDeployment(proj.ID, "fresh-cat-1234", "dev", "running",
		false, owner.ID, 4243, "k")

	var haEnabled bool
	var replicaCount int
	err := h.DB.QueryRow(h.rootCtx, `
		SELECT ha_enabled, replica_count FROM deployments WHERE id = $1
	`, id).Scan(&haEnabled, &replicaCount)
	if err != nil {
		t.Fatalf("read deployment: %v", err)
	}
	if haEnabled {
		t.Errorf("ha_enabled default: got true, want false")
	}
	if replicaCount != 1 {
		t.Errorf("replica_count default: got %d, want 1", replicaCount)
	}
}

// TestMigration_HostPortUniqueOnReplicas verifies the new UNIQUE
// constraint on deployment_replicas.host_port — two replicas can't share
// a port even if they belong to different deployments.
func TestMigration_HostPortUniqueOnReplicas(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Unique Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")

	idA := h.SeedDeployment(proj.ID, "first-cat-1234", "dev", "running",
		false, owner.ID, 4244, "k1")
	idB := h.SeedDeployment(proj.ID, "second-cat-5678", "dev", "running",
		false, owner.ID, 4245, "k2")

	if _, err := h.DB.Exec(h.rootCtx, `
		INSERT INTO deployment_replicas (deployment_id, replica_index, host_port, status)
		VALUES ($1, 0, 9999, 'running')
	`, idA); err != nil {
		t.Fatalf("seed replica A: %v", err)
	}
	_, err := h.DB.Exec(h.rootCtx, `
		INSERT INTO deployment_replicas (deployment_id, replica_index, host_port, status)
		VALUES ($1, 0, 9999, 'running')
	`, idB)
	if err == nil {
		t.Fatalf("expected UNIQUE host_port to reject second replica using same port, got nil")
	}
	// Sanity: caller reaching this still has read-access via the API.
	h.DoJSON(http.MethodGet, "/v1/projects/"+proj.ID+"/list_deployments",
		owner.AccessToken, nil, http.StatusOK, nil)
}
