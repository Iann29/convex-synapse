package synapsetest

import (
	"net/http"
	"testing"
	"time"
)

// TestUpgradeToHA_RefusedWhenHADisabled: the endpoint should refuse
// `ha_disabled` when the cluster has SYNAPSE_HA_ENABLED unset, even if
// the deployment is otherwise valid for upgrade.
func TestUpgradeToHA_RefusedWhenHADisabled(t *testing.T) {
	h := Setup(t) // HA disabled
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Up Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")
	h.SeedDeployment(proj.ID, "up-cat-2200", "prod", "running",
		false, owner.ID, 4500, "k")

	env := h.AssertStatus(http.MethodPost, "/v1/deployments/up-cat-2200/upgrade_to_ha",
		owner.AccessToken, map[string]any{}, http.StatusBadRequest)
	if env.Code != "ha_disabled" {
		t.Errorf("expected ha_disabled, got %q", env.Code)
	}
}

// TestUpgradeToHA_RefusedOnAdopted: adopted deployments aren't managed
// by Synapse — Synapse can't migrate the underlying container. Refuse
// loudly instead of attempting a corrupting partial migration.
func TestUpgradeToHA_RefusedOnAdopted(t *testing.T) {
	h := SetupHA(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Adopted Up Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")
	id := h.SeedDeployment(proj.ID, "adopt-up-3300", "prod", "running",
		false, owner.ID, 4501, "k")
	if _, err := h.DB.Exec(h.rootCtx,
		`UPDATE deployments SET adopted = true WHERE id = $1`, id); err != nil {
		t.Fatalf("flip adopted: %v", err)
	}

	env := h.AssertStatus(http.MethodPost, "/v1/deployments/adopt-up-3300/upgrade_to_ha",
		owner.AccessToken, map[string]any{}, http.StatusBadRequest)
	if env.Code != "cannot_upgrade_adopted" {
		t.Errorf("expected cannot_upgrade_adopted, got %q", env.Code)
	}
}

// TestUpgradeToHA_RefusedOnAlreadyHA: 409 already_ha — no-op rather
// than re-running the upgrade machinery.
func TestUpgradeToHA_RefusedOnAlreadyHA(t *testing.T) {
	h := SetupHA(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "AlreadyHA Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")
	id := h.SeedDeployment(proj.ID, "ha-up-4400", "prod", "running",
		false, owner.ID, 4502, "k")
	if _, err := h.DB.Exec(h.rootCtx,
		`UPDATE deployments SET ha_enabled = true, replica_count = 2 WHERE id = $1`,
		id); err != nil {
		t.Fatalf("flip ha_enabled: %v", err)
	}

	env := h.AssertStatus(http.MethodPost, "/v1/deployments/ha-up-4400/upgrade_to_ha",
		owner.AccessToken, map[string]any{}, http.StatusConflict)
	if env.Code != "already_ha" {
		t.Errorf("expected already_ha, got %q", env.Code)
	}
}

// TestUpgradeToHA_RefusedWhenNotRunning: an upgrade in 'provisioning' /
// 'failed' / 'stopped' state would race with the worker. Operator must
// resolve first.
func TestUpgradeToHA_RefusedWhenNotRunning(t *testing.T) {
	h := SetupHA(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "NotRunning Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")
	h.SeedDeployment(proj.ID, "stale-up-5500", "prod", "failed",
		false, owner.ID, 4503, "k")

	env := h.AssertStatus(http.MethodPost, "/v1/deployments/stale-up-5500/upgrade_to_ha",
		owner.AccessToken, map[string]any{}, http.StatusConflict)
	if env.Code != "deployment_not_running" {
		t.Errorf("expected deployment_not_running, got %q", env.Code)
	}
}

// TestUpgradeToHA_HappyPath_QueuesAndMigrates: with everything set up
// correctly the endpoint queues an upgrade job and the worker provisions two
// HA replicas, runs the snapshot migrator, swaps replica rows, and stops the
// old SQLite container without removing its volume.
func TestUpgradeToHA_HappyPath_QueuesAndMigrates(t *testing.T) {
	h := SetupHA(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Happy Up Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")
	depID := h.SeedDeployment(proj.ID, "happy-up-6600", "prod", "running",
		false, owner.ID, 4504, "k")
	if _, err := h.DB.Exec(h.rootCtx, `
		INSERT INTO project_env_vars (project_id, name, value, deployment_types)
		VALUES ($1, 'UPGRADE_TOKEN', 'kept', ARRAY['prod'])
	`, proj.ID); err != nil {
		t.Fatalf("seed env var: %v", err)
	}
	if _, err := h.DB.Exec(h.rootCtx, `
		INSERT INTO deployment_domains (deployment_id, domain, role, status)
		VALUES ($1, 'api.example.test', 'api', 'active')
	`, depID); err != nil {
		t.Fatalf("seed active domain: %v", err)
	}

	var queued struct {
		DeploymentName string `json:"deploymentName"`
		Status         string `json:"status"`
		JobID          int64  `json:"jobId"`
	}
	h.DoJSON(http.MethodPost, "/v1/deployments/happy-up-6600/upgrade_to_ha",
		owner.AccessToken, map[string]any{}, http.StatusAccepted, &queued)
	if queued.DeploymentName != "happy-up-6600" || queued.Status != "queued" || queued.JobID == 0 {
		t.Fatalf("bad queued response: %+v", queued)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		var haEnabled bool
		var replicaCount int
		if err := h.DB.QueryRow(h.rootCtx, `
			SELECT status, ha_enabled, replica_count
			  FROM deployments
			 WHERE name = 'happy-up-6600'
		`).Scan(&status, &haEnabled, &replicaCount); err != nil {
			t.Fatalf("load deployment: %v", err)
		}
		if status == "running" && haEnabled && replicaCount == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	var status string
	var haEnabled bool
	var replicaCount int
	if err := h.DB.QueryRow(h.rootCtx, `
		SELECT status, ha_enabled, replica_count
		  FROM deployments
		 WHERE name = 'happy-up-6600'
	`).Scan(&status, &haEnabled, &replicaCount); err != nil {
		t.Fatalf("load final deployment: %v", err)
	}
	if status != "running" || !haEnabled || replicaCount != 2 {
		t.Fatalf("deployment not upgraded: status=%s ha=%v replicas=%d", status, haEnabled, replicaCount)
	}

	provisioned := h.Docker.ProvisionedSpecs()
	var haSpecs int
	for _, spec := range provisioned {
		if spec.Name == "happy-up-6600" && spec.HAReplica && spec.Storage != nil {
			if spec.EnvVars["UPGRADE_TOKEN"] != "kept" {
				t.Errorf("project env var not preserved on HA replica %d: %+v", spec.ReplicaIndex, spec.EnvVars)
			}
			if spec.EnvVars["CORS_ALLOWED_ORIGINS"] != "https://api.example.test" {
				t.Errorf("CORS origins not preserved on HA replica %d: %+v", spec.ReplicaIndex, spec.EnvVars)
			}
			haSpecs++
		}
	}
	if haSpecs != 2 {
		t.Fatalf("expected 2 HA provision specs, got %d in %+v", haSpecs, provisioned)
	}

	migrations := h.Docker.MigrationSpecs()
	if len(migrations) != 1 {
		t.Fatalf("expected 1 snapshot migration, got %d", len(migrations))
	}
	if migrations[0].SourceURL != "http://convex-happy-up-6600:3210" {
		t.Errorf("source URL: got %q", migrations[0].SourceURL)
	}
	if len(migrations[0].TargetURLs) != 2 ||
		migrations[0].TargetURLs[0] != "http://convex-happy-up-6600-0:3210" ||
		migrations[0].TargetURLs[1] != "http://convex-happy-up-6600-1:3210" {
		t.Errorf("target URLs: %+v", migrations[0].TargetURLs)
	}

	stopped := h.Docker.StoppedNames()
	if len(stopped) != 1 || stopped[0] != "happy-up-6600" {
		t.Fatalf("expected old replica stop, got %+v", stopped)
	}

	var oldRows int
	if err := h.DB.QueryRow(h.rootCtx, `
		SELECT count(*) FROM deployment_replicas r
		  JOIN deployments d ON d.id = r.deployment_id
		 WHERE d.name = 'happy-up-6600'
		   AND r.replica_index = -1
		   AND r.status = 'stopped'
	`).Scan(&oldRows); err != nil {
		t.Fatalf("count old replica rows: %v", err)
	}
	if oldRows != 1 {
		t.Errorf("expected stopped old replica row, got %d", oldRows)
	}

	var runningReplicas int
	if err := h.DB.QueryRow(h.rootCtx, `
		SELECT count(*) FROM deployment_replicas r
		  JOIN deployments d ON d.id = r.deployment_id
		 WHERE d.name = 'happy-up-6600'
		   AND r.replica_index IN (0, 1)
		   AND r.status = 'running'
	`).Scan(&runningReplicas); err != nil {
		t.Fatalf("count running replicas: %v", err)
	}
	if runningReplicas != 2 {
		t.Errorf("expected 2 running HA replicas, got %d", runningReplicas)
	}

	var jobStatus string
	if err := h.DB.QueryRow(h.rootCtx, `
		SELECT status FROM provisioning_jobs WHERE id = $1
	`, queued.JobID).Scan(&jobStatus); err != nil {
		t.Fatalf("load job: %v", err)
	}
	if jobStatus != "done" {
		t.Errorf("job status: got %q want done", jobStatus)
	}

	var nEvents int
	if err := h.DB.QueryRow(h.rootCtx, `
		SELECT count(*) FROM audit_events
		 WHERE action = 'upgradeToHA'
	`).Scan(&nEvents); err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	if nEvents != 1 {
		t.Errorf("expected 1 upgradeToHA audit event, got %d", nEvents)
	}
}

func TestUpgradeToHA_RefusesDuplicateActiveJob(t *testing.T) {
	h := SetupHA(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Duplicate Up Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")
	depID := h.SeedDeployment(proj.ID, "dup-up-6601", "prod", "running",
		false, owner.ID, 4506, "k")
	if _, err := h.DB.Exec(h.rootCtx, `
		INSERT INTO provisioning_jobs (deployment_id, kind, status)
		VALUES ($1, 'upgrade_to_ha', 'pending')
	`, depID); err != nil {
		t.Fatalf("seed active job: %v", err)
	}

	env := h.AssertStatus(http.MethodPost, "/v1/deployments/dup-up-6601/upgrade_to_ha",
		owner.AccessToken, map[string]any{}, http.StatusConflict)
	if env.Code != "upgrade_already_in_progress" {
		t.Errorf("expected upgrade_already_in_progress, got %q", env.Code)
	}
}

// TestUpgradeToHA_NonAdminForbidden: regular team members can't trigger
// upgrades — only admins.
func TestUpgradeToHA_NonAdminForbidden(t *testing.T) {
	h := SetupHA(t)
	admin := h.RegisterRandomUser()
	member := h.RegisterRandomUser()
	team := createTeam(t, h, admin.AccessToken, "Member Up Co")
	proj := createProject(t, h, admin.AccessToken, team.Slug, "App")
	h.SeedDeployment(proj.ID, "perm-up-7700", "prod", "running",
		false, admin.ID, 4505, "k")
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, member.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	env := h.AssertStatus(http.MethodPost, "/v1/deployments/perm-up-7700/upgrade_to_ha",
		member.AccessToken, map[string]any{}, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("expected forbidden, got %q", env.Code)
	}
}
