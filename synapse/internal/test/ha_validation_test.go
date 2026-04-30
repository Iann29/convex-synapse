package synapsetest

import (
	"net/http"
	"testing"
)

// TestHA_DisabledRefusesHATrue: when SYNAPSE_HA_ENABLED is unset (the
// harness default), `ha:true` in create_deployment should return
// 400 ha_disabled. Existing single-replica behavior is unchanged.
func TestHA_DisabledRefusesHATrue(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "HA Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")

	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+proj.ID+"/create_deployment",
		owner.AccessToken,
		map[string]any{"type": "dev", "ha": true},
		http.StatusBadRequest)
	if env.Code != "ha_disabled" {
		t.Errorf("expected code ha_disabled, got %q", env.Code)
	}
}

// TestHA_HAFalseStillWorksUnchanged: when ha is omitted (or explicitly
// false), the legacy create-deployment flow runs unchanged — should
// return 201 with status=provisioning regardless of HA cluster config.
func TestHA_HAFalseStillWorksUnchanged(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "HA Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")

	var got deploymentJSON
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/create_deployment",
		owner.AccessToken,
		map[string]any{"type": "dev"}, // no `ha` field
		http.StatusCreated, &got)
	if got.Status != "provisioning" {
		t.Errorf("legacy create: status=%q want provisioning", got.Status)
	}
	if got.Name == "" {
		t.Error("legacy create: empty name")
	}
}
