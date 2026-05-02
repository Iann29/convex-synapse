package synapsetest

import (
	"net/http"
	"testing"
)

// Cuts catalogued in not_supported.go; the test pins the operator-facing
// 404 + structured code so a regression where a stub leaks 401 (auth not
// reached) or a 500 (unparametersied path crashes the matcher) shows up
// loud.
//
// One case per family — the matcher's behaviour is uniform; no need to
// blast it with every leaf.
func TestNotSupported_404Matrix(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()

	cases := []struct {
		name   string
		method string
		path   string
	}{
		// Whole-prefix families.
		{"discord", http.MethodGet, "/v1/discord/accounts"},
		{"workos", http.MethodGet, "/v1/workos/disconnect_workos_team"},
		{"vercel", http.MethodGet, "/v1/vercel/potential_teams"},
		{"profile_emails", http.MethodPost, "/v1/profile_emails/list"},
		{"cloud_backups", http.MethodPost, "/v1/cloud_backups/some-id"},
		// Exact paths.
		{"validate_referral_code", http.MethodPost, "/v1/validate_referral_code"},
		// Parameterised — billing.
		{"orb_cancel", http.MethodPost, "/v1/teams/abc/cancel_orb_subscription"},
		{"list_invoices", http.MethodGet, "/v1/teams/abc/list_invoices"},
		// Parameterised — SSO/WorkOS team.
		{"enable_sso", http.MethodPost, "/v1/teams/abc/enable_sso"},
		{"workos_integration", http.MethodGet, "/v1/teams/abc/workos_integration"},
		// Parameterised — OAuth apps.
		{"oauth_apps_register", http.MethodPost, "/v1/teams/abc/oauth_apps/register"},
		// Parameterised — usage.
		{"usage_query", http.MethodGet, "/v1/teams/abc/usage/query"},
		// Parameterised — cloud backups.
		{"periodic_backup_disable", http.MethodPost, "/v1/deployments/abc/disable_periodic_backup"},
		// Parameterised — WorkOS deployment.
		{"deployment_workos", http.MethodGet, "/v1/deployments/abc/workos_environment"},
		// Parameterised — WorkOS project.
		{"project_workos", http.MethodGet, "/v1/projects/abc/workos_environments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := h.AssertStatus(tc.method, tc.path, u.AccessToken, nil,
				http.StatusNotFound)
			if env.Code != "not_supported_in_self_hosted" {
				t.Errorf("code=%q want not_supported_in_self_hosted", env.Code)
			}
		})
	}
}

// Real endpoints must still work. Pick a representative live path per
// family that the middleware MUST NOT short-circuit.
func TestNotSupported_RealPathsStillReachable(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Live Co")

	// Real team endpoint near a cut sibling (audit_log near workos_team_health).
	h.DoJSON(http.MethodGet, "/v1/teams/"+team.Slug+"/audit_log",
		owner.AccessToken, nil, http.StatusOK, &map[string]any{})

	// Public install_status mounted under /v1 — middleware shouldn't
	// false-positive on it.
	h.DoJSON(http.MethodGet, "/v1/install_status", "", nil,
		http.StatusOK, &map[string]any{})
}

// Pre-auth probes should still get 404 (not 401) so external tooling
// figures out the missing feature without needing to authenticate.
func TestNotSupported_RunsBeforeAuth(t *testing.T) {
	h := Setup(t)

	env := h.AssertStatus(http.MethodGet, "/v1/discord/login_url",
		"", nil, http.StatusNotFound)
	if env.Code != "not_supported_in_self_hosted" {
		t.Errorf("code=%q want not_supported_in_self_hosted", env.Code)
	}
}
