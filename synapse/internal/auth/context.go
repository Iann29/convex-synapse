// Package auth is split across files: password hashing, opaque token
// generation, JWT issuance/verification, and request-context plumbing.
package auth

import (
	"context"
	"errors"
)

type ctxKey int

const (
	ctxKeyUserID ctxKey = iota
	ctxKeyEmail
	ctxKeyTokenScope
	ctxKeyTokenScopeID
)

// WithUser attaches authenticated user info to a request context. Used by
// the JWT path (no scope information — JWTs are always user-scoped).
// Equivalent to WithPrincipal(ctx, userID, email, "user", "").
func WithUser(ctx context.Context, userID, email string) context.Context {
	return WithPrincipal(ctx, userID, email, "user", "")
}

// WithPrincipal attaches the full authenticated principal — user identity
// PLUS the token's scope (user/team/project/deployment/app) and the
// scope_id when applicable. Opaque PAT auth uses this so handlers can
// gate scope-sensitive operations.
//
// scope == "user" + scopeID == "" is the unrestricted case; any other
// combination means the token is bound to a specific team/project/
// deployment and handlers must reject mismatches with 403.
func WithPrincipal(ctx context.Context, userID, email, scope, scopeID string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyUserID, userID)
	ctx = context.WithValue(ctx, ctxKeyEmail, email)
	ctx = context.WithValue(ctx, ctxKeyTokenScope, scope)
	ctx = context.WithValue(ctx, ctxKeyTokenScopeID, scopeID)
	return ctx
}

func UserID(ctx context.Context) (string, error) {
	v, _ := ctx.Value(ctxKeyUserID).(string)
	if v == "" {
		return "", errors.New("no authenticated user in context")
	}
	return v, nil
}

func Email(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyEmail).(string)
	return v
}

// TokenScope returns the scope of the access token used to authenticate
// the request, or "" if the request was authenticated via JWT (which is
// implicitly user-scoped). Callers who need to enforce scope should treat
// "" or "user" as "no restriction".
func TokenScope(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyTokenScope).(string)
	return v
}

// TokenScopeID returns the resource id the access token is bound to —
// team_id when scope=team, project_id when scope=project|app,
// deployment_id when scope=deployment. Empty when scope is "" or "user".
func TokenScopeID(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyTokenScopeID).(string)
	return v
}
