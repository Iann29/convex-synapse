package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Iann29/synapse/internal/auth"
)

// Authenticator returns chi middleware that requires a valid bearer token.
// Two token formats are accepted:
//
//   - JWT (HS256, issued by /v1/auth/login): used by the dashboard
//   - Opaque "syn_*" tokens: used by CLI/CI; verified by SHA-256 hash lookup
//
// On success, the request context is enriched with the authenticated user
// via auth.WithUser. On failure, the handler responds 401 and aborts.
func Authenticator(jwt *auth.JWTIssuer, db *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := bearerFromHeader(r)
			if tok == "" {
				unauthorized(w, "missing_authorization", "Authorization header required")
				return
			}

			ctx, ok := authenticate(r.Context(), tok, jwt, db)
			if !ok {
				unauthorized(w, "invalid_token", "Token is not valid")
				return
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func bearerFromHeader(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[len("Bearer "):])
}

func authenticate(ctx context.Context, token string, jwt *auth.JWTIssuer, db *pgxpool.Pool) (context.Context, bool) {
	// Opaque token: prefix lookup hits a unique index. We RETURN scope
	// + scope_id alongside the user identity so scope-sensitive handlers
	// can gate access without a second query. v1.0+ scope-aware auth.
	if strings.HasPrefix(token, auth.TokenPrefix) {
		hash := auth.HashToken(token)
		var userID, email, scope string
		var scopeID *string
		err := db.QueryRow(ctx, `
			UPDATE access_tokens t
			   SET last_used_at = now()
			  FROM users u
			 WHERE t.token_hash = $1
			   AND u.id = t.user_id
			   AND (t.expires_at IS NULL OR t.expires_at > now())
			 RETURNING u.id, u.email, t.scope, t.scope_id
		`, hash).Scan(&userID, &email, &scope, &scopeID)
		if err != nil {
			return ctx, false
		}
		_ = pgx.Rows(nil) // keep import in case of future query helpers
		sid := ""
		if scopeID != nil {
			sid = *scopeID
		}
		return auth.WithPrincipal(ctx, userID, email, scope, sid), true
	}

	// JWT path. JWTs are always user-scoped (the dashboard refresh flow
	// doesn't know about team/project/deployment) — pass through WithUser
	// which is equivalent to WithPrincipal(scope="user", scopeID="").
	claims, err := jwt.Verify(token)
	if err != nil {
		return ctx, false
	}
	if claims.Kind != "access" {
		return ctx, false
	}
	return auth.WithUser(ctx, claims.UserID, claims.Email), true
}

func unauthorized(w http.ResponseWriter, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"code":"` + code + `","message":"` + msg + `"}`))
}
