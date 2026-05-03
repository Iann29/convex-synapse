package api

import (
	"net/http"
	"path"
	"strings"
)

// NotSupportedMiddleware intercepts requests to OpenAPI endpoints that
// Synapse self-hosted intentionally doesn't implement (Convex Cloud's
// billing, SSO, Discord, Vercel, OAuth apps, cloud backups, etc) and
// returns a structured 404 with `code: not_supported_in_self_hosted`.
//
// Why a middleware rather than per-handler 501 stubs:
//   - These paths belong to entire feature families (Stripe/Orb billing,
//     WorkOS, ...) — adding a stub per path is busywork and easy to drift.
//   - 404 is the right answer for "this URL has no resource here" — 501
//     "Not Implemented" reads as "we plan to ship it" which we don't, so
//     callers (the dashboard, cloud-spec test suites) keep retrying.
//   - The structured `code` lets clients programmatically distinguish
//     "deliberately cut" from "real 404 — try a different name". The
//     message points operators at docs/ARCHITECTURE.md for the why.
//
// The middleware runs BEFORE chi's router so it short-circuits without
// hitting auth — operators investigating a missing feature find out
// quickly even on an unauthenticated probe.
func NotSupportedMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isNotSupportedPath(r.URL.Path) {
			writeError(w, http.StatusNotFound, "not_supported_in_self_hosted",
				"This endpoint exists in Convex Cloud but is intentionally not implemented in Synapse self-hosted. "+
					"See docs/ARCHITECTURE.md \"Out of scope\" for the rationale.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isNotSupportedPath returns true for every cloud-OpenAPI path Synapse
// has intentionally cut. The catalog matches docs/ROADMAP.md
// "Out of scope" + ARCHITECTURE.md "Out of scope" — new entries here
// should also land in those docs so the operator-visible cut list
// stays honest.
//
// Matching is done in three layers:
//   1. exact /v1/<path> matches for one-off cuts (validate_referral_code)
//   2. /v1/<prefix>/* matches for whole feature families (workos, vercel)
//   3. parameterised matches for endpoints under /v1/<resource>/<id>/<verb>
//      where <verb> is one of a fixed set
//
// path.Match has the right semantics for layer 3 — "*" matches a single
// path segment (no slashes), so "/v1/teams/*/cancel_orb_subscription"
// matches "/v1/teams/abc/cancel_orb_subscription" but not
// "/v1/teams/abc/sub/cancel_orb_subscription".
func isNotSupportedPath(urlPath string) bool {
	// Normalise — strip trailing slash so /v1/discord/ and /v1/discord both hit.
	p := strings.TrimRight(urlPath, "/")
	if p == "" {
		return false
	}

	// 1. Exact paths.
	for _, exact := range exactNotSupportedPaths {
		if p == exact {
			return true
		}
	}

	// 2. Whole-prefix families (/v1/discord/*, /v1/workos/*, …).
	for _, prefix := range notSupportedPrefixes {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}

	// 3. Parameterised verbs under /v1/teams/{id}/, /v1/deployments/{name}/,
	//    /v1/projects/{id}/. path.Match handles the wildcard segment.
	for _, pat := range notSupportedPatterns {
		if ok, _ := path.Match(pat, p); ok {
			return true
		}
	}
	return false
}

// exactNotSupportedPaths — single OpenAPI paths that get a structured
// 404 even though they don't fall into a broader family.
var exactNotSupportedPaths = []string{
	"/v1/validate_referral_code",
}

// notSupportedPrefixes — the entire path family at this prefix is cut.
// "/v1/discord" matches "/v1/discord", "/v1/discord/", and
// "/v1/discord/anything/here". Lighter than listing every leaf.
var notSupportedPrefixes = []string{
	"/v1/cloud_backups",
	"/v1/discord",
	"/v1/profile_emails",
	"/v1/vercel",
	"/v1/workos",
}

// notSupportedPatterns — paths whose middle segment is a parameter
// (team id, deployment name, project id). One entry per cut endpoint;
// path.Match's "*" matches a single segment. Order doesn't matter.
//
// Grouped by family so the diff is easy to audit:
//   - billing (Orb / Stripe)
//   - SSO / WorkOS
//   - OAuth apps
//   - usage / spending
//   - referrals
//   - cloud backups
//   - periodic backups
//   - workos team health
var notSupportedPatterns = []string{
	// Billing — Orb / Stripe. Whole feature: cut.
	"/v1/teams/*/apply_referral_code",
	"/v1/teams/*/cancel_orb_subscription",
	"/v1/teams/*/change_subscription_plan",
	"/v1/teams/*/create_setup_intent",
	"/v1/teams/*/create_subscription",
	"/v1/teams/*/get_current_spend",
	"/v1/teams/*/get_discounted_plan/*",
	"/v1/teams/*/get_discounted_plan/*/*",
	"/v1/teams/*/get_entitlements",
	"/v1/teams/*/get_orb_subscription",
	"/v1/teams/*/get_spending_limits",
	"/v1/teams/*/has_failed_payment",
	"/v1/teams/*/list_active_plans",
	"/v1/teams/*/list_invoices",
	"/v1/teams/*/referral_state",
	"/v1/teams/*/set_spending_limit",
	"/v1/teams/*/unschedule_cancel_orb_subscription",
	"/v1/teams/*/update_billing_address",
	"/v1/teams/*/update_billing_contact",
	"/v1/teams/*/update_payment_method",

	// SSO via WorkOS. OIDC is on the roadmap (v1.0+); WorkOS-specific
	// routes are not.
	"/v1/teams/*/disable_sso",
	"/v1/teams/*/enable_sso",
	"/v1/teams/*/generate_sso_configuration_link",
	"/v1/teams/*/get_sso",
	"/v1/teams/*/update_sso",
	"/v1/teams/*/workos_integration",
	"/v1/teams/*/workos_invitation_eligible_emails",
	"/v1/teams/*/workos_team_health",

	// OAuth apps — Convex Cloud's own product offering. Out of scope.
	"/v1/teams/*/oauth_apps",
	"/v1/teams/*/oauth_apps/check",
	"/v1/teams/*/oauth_apps/register",
	"/v1/teams/*/oauth_apps/*/delete",
	"/v1/teams/*/oauth_apps/*/regenerate_secret",
	"/v1/teams/*/oauth_apps/*/update",

	// Usage / spending — Cloud-only metering.
	"/v1/teams/*/usage/current_billing_period",
	"/v1/teams/*/usage/query",
	"/v1/teams/*/usage/team_usage_state",
	"/v1/teams/*/usage/get_token_info",

	// Cloud-managed backups — Synapse's --backup is the equivalent.
	"/v1/deployments/*/configure_periodic_backup",
	"/v1/deployments/*/disable_periodic_backup",
	"/v1/deployments/*/get_periodic_backup_config",
	"/v1/deployments/*/list_cloud_backups",
	"/v1/deployments/*/request_cloud_backup",
	"/v1/deployments/*/restore_from_cloud_backup",

	// WorkOS-flavoured deployment / project routes.
	"/v1/deployments/*/has_associated_workos_team",
	"/v1/deployments/*/workos_environment",
	"/v1/deployments/*/workos_environment_health",
	"/v1/projects/*/workos_environments",
	"/v1/projects/*/workos_environments/*",
}
