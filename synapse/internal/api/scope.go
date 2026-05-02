package api

import (
	"context"
	"net/http"

	"github.com/Iann29/synapse/internal/auth"
	"github.com/Iann29/synapse/internal/models"
)

// Scope hierarchy enforcement (v1.0+).
//
// Synapse access tokens carry a scope: user | team | project | deployment | app.
// `user` (or empty) is unrestricted — that's a personal access token, the
// caller can do anything they could do via dashboard JWT. The other scopes
// pin the token to a specific resource:
//
//   team       → token can act inside team T
//                  (list/edit projects, deployments, members of T)
//   project    → token can act inside project P (and team T = P's team)
//                  (CRUD on P's deployments, env vars, …)
//   app        → same access surface as project; just a different label.
//                  Mirrors Convex Cloud's app_access_tokens family —
//                  used by CI/CD as preview deploy keys.
//   deployment → token can act on deployment D only.
//
// Hierarchy rules (a token scoped at scope X can act on a target at scope Y):
//
//   X       Y=team     Y=project     Y=deployment
//   user    yes        yes           yes
//   team    eq         child         child
//   proj/app   no      eq            child
//   deploy  no         no            eq
//
// Helpers below are called from the load*ForRequest helpers AFTER the
// resource is resolved + caller membership is verified. They write a 403
// `forbidden_token_scope` and return false on mismatch; on the unrestricted
// path they return true silently.

// enforceTeamAccess gates an operation that touches the supplied team.
// A team-scoped token must match exactly; a project- or deployment-
// scoped token cannot reach into team-level operations even if the
// project/deployment lives inside this team.
func enforceTeamAccess(w http.ResponseWriter, ctx context.Context, teamID string) bool {
	scope := auth.TokenScope(ctx)
	switch scope {
	case "", models.TokenScopeUser:
		return true
	case models.TokenScopeTeam:
		if auth.TokenScopeID(ctx) == teamID {
			return true
		}
	}
	denyScope(w)
	return false
}

// enforceProjectAccess gates a project-level operation. team-scoped
// tokens win when the project belongs to that team; project- and
// app-scoped tokens win on exact match; deployment-scoped tokens
// can't widen up to project.
func enforceProjectAccess(w http.ResponseWriter, ctx context.Context, projectID, teamID string) bool {
	scope := auth.TokenScope(ctx)
	switch scope {
	case "", models.TokenScopeUser:
		return true
	case models.TokenScopeTeam:
		if auth.TokenScopeID(ctx) == teamID {
			return true
		}
	case models.TokenScopeProject, models.TokenScopeApp:
		if auth.TokenScopeID(ctx) == projectID {
			return true
		}
	}
	denyScope(w)
	return false
}

// enforceDeploymentAccess is the most permissive — every scope above
// deployment can reach down to one of its deployments.
func enforceDeploymentAccess(w http.ResponseWriter, ctx context.Context, deploymentID, projectID, teamID string) bool {
	scope := auth.TokenScope(ctx)
	switch scope {
	case "", models.TokenScopeUser:
		return true
	case models.TokenScopeTeam:
		if auth.TokenScopeID(ctx) == teamID {
			return true
		}
	case models.TokenScopeProject, models.TokenScopeApp:
		if auth.TokenScopeID(ctx) == projectID {
			return true
		}
	case models.TokenScopeDeployment:
		if auth.TokenScopeID(ctx) == deploymentID {
			return true
		}
	}
	denyScope(w)
	return false
}

func denyScope(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, "forbidden_token_scope",
		"This access token is scoped to a different resource")
}
