package middleware

import (
	"net/http"
	"strings"
)

// CORS allows the dashboard (typically on a different host:port from Synapse)
// to call the API from a browser. Defaults to fully-permissive for v0 since
// Synapse is meant to run on the operator's own infrastructure — every
// request is already authenticated by JWT or opaque token, and the browser's
// same-origin enforcement isn't doing useful work for us.
//
// `origins` is a comma-separated list of allowed origins, or "*" for any.
// On preflight (OPTIONS) we short-circuit with a 204.
func CORS(origins string) func(http.Handler) http.Handler {
	allowAll := strings.TrimSpace(origins) == "*" || strings.TrimSpace(origins) == ""
	allowed := map[string]bool{}
	if !allowAll {
		for _, o := range strings.Split(origins, ",") {
			o = strings.TrimSpace(o)
			if o != "" {
				allowed[o] = true
			}
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && (allowAll || allowed[origin]) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Requested-With")
				w.Header().Set("Access-Control-Max-Age", "600")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
