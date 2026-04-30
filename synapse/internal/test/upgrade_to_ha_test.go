package synapsetest

import (
	"net/http"
	"testing"
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

// TestUpgradeToHA_HappyPath_ReturnsNotYetImplemented: with everything
// set up correctly the endpoint refuses with ha_upgrade_not_yet_implemented.
// This is the only test that will *flip* once the export/import worker
// lands — at that point it should expect 202 + a job row.
func TestUpgradeToHA_HappyPath_ReturnsNotYetImplemented(t *testing.T) {
	h := SetupHA(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Happy Up Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")
	h.SeedDeployment(proj.ID, "happy-up-6600", "prod", "running",
		false, owner.ID, 4504, "k")

	env := h.AssertStatus(http.MethodPost, "/v1/deployments/happy-up-6600/upgrade_to_ha",
		owner.AccessToken, map[string]any{}, http.StatusNotImplemented)
	if env.Code != "ha_upgrade_not_yet_implemented" {
		t.Errorf("expected ha_upgrade_not_yet_implemented, got %q", env.Code)
	}

	// Even rejected, the audit log records the attempt — operators
	// debugging "why won't my upgrade work" need to see the timestamps.
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
