package synapsetest

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Iann29/synapse/internal/health"
)

// TestHealthWorker_HA_OneReplicaDown covers the aggregate-status logic:
// when one of two replicas is dead but the other is still running, the
// deployment-level status stays "running". Only when ALL replicas drop
// off does the deployment get demoted.
func TestHealthWorker_HA_OneReplicaDown(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "HA Health Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")

	// Seed an HA deployment manually — we don't have the create flow
	// wired yet (chunk 4 lands the worker; chunk 5+ wires the request
	// path). The seed inserts replica index 0; we add index 1 + flip
	// ha_enabled by hand.
	depID := h.SeedDeployment(proj.ID, "ha-cat-2200", "prod", "running",
		false, owner.ID, 5210, "k")
	if _, err := h.DB.Exec(h.rootCtx,
		`UPDATE deployments SET ha_enabled = true, replica_count = 2 WHERE id = $1`,
		depID,
	); err != nil {
		t.Fatalf("flip ha_enabled: %v", err)
	}
	if _, err := h.DB.Exec(h.rootCtx, `
		INSERT INTO deployment_replicas (deployment_id, replica_index, container_id, host_port, status)
		VALUES ($1, 1, 'fake-container-ha-cat-2200-1', 5211, 'running')
	`, depID); err != nil {
		t.Fatalf("insert replica 1: %v", err)
	}

	// Replica 0 reports running, replica 1 reports gone.
	var calls atomic.Int32
	h.Docker.StatusReplicaFn = func(_ context.Context, name string, idx int) (string, error) {
		calls.Add(1)
		if idx == 1 {
			return "", nil // gone
		}
		return "running", nil
	}

	w := &health.Worker{
		DB:     h.DB,
		Docker: h.Docker,
		Config: health.Config{Interval: time.Hour, StatusTimeout: 2 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	w.Run(ctx)

	// Replica 1 should now be "stopped".
	var r1Status string
	if err := h.DB.QueryRow(h.rootCtx,
		`SELECT status FROM deployment_replicas WHERE deployment_id = $1 AND replica_index = 1`,
		depID,
	).Scan(&r1Status); err != nil {
		t.Fatalf("read replica 1 status: %v", err)
	}
	if r1Status != "stopped" {
		t.Errorf("replica 1: got %q want stopped", r1Status)
	}

	// Deployment-level status should still be "running" — replica 0 holds
	// up the aggregate.
	var depStatus string
	if err := h.DB.QueryRow(h.rootCtx,
		`SELECT status FROM deployments WHERE id = $1`, depID,
	).Scan(&depStatus); err != nil {
		t.Fatalf("read deployment status: %v", err)
	}
	if depStatus != "running" {
		t.Errorf("deployment status: got %q want running (1/2 replicas alive)", depStatus)
	}
}

// TestHealthWorker_HA_AllReplicasDown: when both replicas are gone the
// deployment-level status should reflect that. Worst-wins on mixed
// failures (failed > stopped), but here both are simply gone (stopped).
func TestHealthWorker_HA_AllReplicasDown(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "HA Down Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")

	depID := h.SeedDeployment(proj.ID, "ha-fox-4400", "prod", "running",
		false, owner.ID, 5300, "k")
	if _, err := h.DB.Exec(h.rootCtx,
		`UPDATE deployments SET ha_enabled = true, replica_count = 2 WHERE id = $1`,
		depID,
	); err != nil {
		t.Fatalf("flip ha_enabled: %v", err)
	}
	if _, err := h.DB.Exec(h.rootCtx, `
		INSERT INTO deployment_replicas (deployment_id, replica_index, container_id, host_port, status)
		VALUES ($1, 1, 'fake-container-ha-fox-4400-1', 5301, 'running')
	`, depID); err != nil {
		t.Fatalf("insert replica 1: %v", err)
	}

	// Both replicas vanish from Docker.
	h.Docker.StatusReplicaFn = func(_ context.Context, _ string, _ int) (string, error) {
		return "", nil
	}

	w := &health.Worker{
		DB:     h.DB,
		Docker: h.Docker,
		Config: health.Config{Interval: time.Hour, StatusTimeout: 2 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	w.Run(ctx)

	var depStatus string
	if err := h.DB.QueryRow(h.rootCtx,
		`SELECT status FROM deployments WHERE id = $1`, depID,
	).Scan(&depStatus); err != nil {
		t.Fatalf("read deployment status: %v", err)
	}
	if depStatus != "stopped" {
		t.Errorf("deployment status with both replicas gone: got %q want stopped", depStatus)
	}
}
