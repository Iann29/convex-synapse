// Package api — dashboard_proxy.go
//
// Bridges the upstream `dashboard-self-hosted` (the Convex Dashboard
// image we iframe at /embed/<name>) and Synapse's deployment catalogue.
// The upstream has a built-in protocol — `?a=<api-url>` query param +
// fetch returning {deployments: [{name, url, adminKey}]} — that we
// already inherit via `npm-packages/dashboard-self-hosted/src/components/
// DeploymentList.tsx`. Lighting it up means standing up that endpoint.
//
// Auth: the upstream's fetch carries no credentials. We can't rely on
// cookies because the iframe runs at a different origin (the dashboard
// proxy host) than the Synapse API. The shape we adopt is a
// short-lived token in the URL itself — the Synapse dashboard mints
// a project-scoped PAT (TTL 15min, name "[dashboard-session-…]")
// before loading the iframe and stitches it into the `?a=` URL:
//
//   ?a=https%3A%2F%2Fsynapse.example.com%2Fv1%2Finternal%2Flist_deployments_for_dashboard%3Ftoken%3Dsyn_xxx
//
// That token grants exactly enough to list this project's deployments;
// shells that snoop the URL get a 15-min window of read-only access
// to one project's URLs/admin keys. The dashboard fork deletes the
// token on unmount as a defensive cleanup; even if it leaks, the TTL
// caps the blast radius.
package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Iann29/synapse/internal/auth"
	"github.com/Iann29/synapse/internal/models"
)

// DashboardProxyHandler exposes the cross-origin endpoints the upstream
// Convex Dashboard hits while running inside the /embed/<name> iframe.
//
// Deployments is non-optional — every URL we emit is rewritten via
// publicDeploymentURL so remote browsers see a reachable address (same
// rule the rest of the API follows). Without it the upstream's fetch
// would receive http://127.0.0.1:<port> URLs that nobody outside the
// VPS can hit.
type DashboardProxyHandler struct {
	DB          *pgxpool.Pool
	Deployments *DeploymentsHandler
}

// upstreamDeployment matches the shape `dashboard-self-hosted/src/components/
// DeploymentList.tsx` expects in its `Deployment` type — name + url +
// adminKey, nothing more. Don't add fields without confirming the
// upstream renders them; new keys are silently dropped today but the
// upstream may tighten the type later.
type upstreamDeployment struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	AdminKey string `json:"adminKey"`
}

type listDeploymentsForDashboardResp struct {
	Deployments []upstreamDeployment `json:"deployments"`
}

// listDeploymentsForDashboard answers `GET /v1/internal/list_deployments_for_dashboard?token=syn_xxx`.
//
// The token is validated by:
//   1. Hash lookup against access_tokens.token_hash.
//   2. Scope must be "project" (or "app" — both bind to the same project).
//   3. expires_at must be in the future (or NULL — we accept long-lived
//      tokens too, even though the dashboard always issues TTL'd ones).
//
// Returns deployments under the bound project, with public URLs +
// admin keys ready for the upstream dashboard to talk to the
// containers directly.
func (h *DashboardProxyHandler) listDeploymentsForDashboard(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing_token", "token query parameter is required")
		return
	}

	hash := auth.HashToken(token)
	var scope string
	var scopeID *string
	err := h.DB.QueryRow(r.Context(), `
		UPDATE access_tokens
		   SET last_used_at = now()
		 WHERE token_hash = $1
		   AND (expires_at IS NULL OR expires_at > now())
		 RETURNING scope, scope_id
	`, hash).Scan(&scope, &scopeID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "invalid_token", "Token is not valid or has expired")
		return
	}
	if err != nil {
		logErr("dashboard token lookup", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to validate token")
		return
	}
	// Only project / app scopes can list deployments. Wider scopes
	// (user, team) over-grant; narrower (deployment) under-fits the
	// "list every deployment in this project" contract.
	if scope != models.TokenScopeProject && scope != models.TokenScopeApp {
		writeError(w, http.StatusForbidden, "wrong_scope",
			"Dashboard tokens must be scoped to a project")
		return
	}
	if scopeID == nil || *scopeID == "" {
		writeError(w, http.StatusForbidden, "wrong_scope",
			"Dashboard tokens must carry a project id")
		return
	}

	rows, err := h.DB.Query(r.Context(), `
		SELECT id, project_id, name, deployment_type, status,
		       deployment_url, host_port, admin_key,
		       is_default, reference, creator_user_id, created_at, adopted
		  FROM deployments
		 WHERE project_id = $1
		   AND status <> 'deleted'
		 ORDER BY created_at ASC, id ASC
	`, *scopeID)
	if err != nil {
		logErr("list project deployments for dashboard", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list deployments")
		return
	}
	defer rows.Close()

	out := make([]upstreamDeployment, 0)
	for rows.Next() {
		var d models.Deployment
		var url, ref, creator *string
		var hostPort *int
		if err := rows.Scan(
			&d.ID, &d.ProjectID, &d.Name, &d.DeploymentType, &d.Status,
			&url, &hostPort, &d.AdminKey,
			&d.IsDefault, &ref, &creator, &d.CreatedAt, &d.Adopted,
		); err != nil {
			logErr("scan dashboard deployment", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to scan deployments")
			return
		}
		if url != nil {
			d.DeploymentURL = *url
		}
		if hostPort != nil {
			d.HostPort = *hostPort
		}
		if ref != nil {
			d.Reference = *ref
		}
		if creator != nil {
			d.CreatorUserID = *creator
		}
		// Provisioning rows have no admin key yet (or one that isn't
		// usable) — skip them. The dashboard would fail to authenticate
		// anyway and the picker is more honest if we hide them until
		// they're really up.
		if d.Status == models.DeploymentStatusProvisioning || d.AdminKey == "" {
			continue
		}
		publicURL := d.DeploymentURL
		if h.Deployments != nil {
			publicURL = h.Deployments.publicDeploymentURL(&d)
		}
		out = append(out, upstreamDeployment{
			Name:     d.Name,
			URL:      publicURL,
			AdminKey: d.AdminKey,
		})
	}
	if err := rows.Err(); err != nil {
		logErr("iterate dashboard deployments", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to read deployments")
		return
	}

	// CORS: this endpoint is fetched cross-origin from the iframe at the
	// dashboard host. The middleware-level CORS handler covers most
	// origins via SYNAPSE_ALLOWED_ORIGINS; setting the header here is
	// belt-and-suspenders for the case where the operator forgets to
	// list the dashboard host. The token-in-URL gate is the real
	// security boundary, not the CORS allowlist — CORS only protects
	// cookies/credentials, which we don't carry.
	if origin := r.Header.Get("Origin"); origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	writeJSON(w, http.StatusOK, listDeploymentsForDashboardResp{Deployments: out})
}

