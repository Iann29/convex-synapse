package proxy

import (
	"log/slog"
	"net/http"
	"strings"
)

// DashboardHostHandler dispatches a request whose Host is bound to a
// role='dashboard' custom domain to the right upstream based on the
// request path.
//
// Pre-v1.6.11 the proxy sent the entire request to the bare Convex
// Dashboard container (DashboardAddr). That made `dashboard.<your>.com`
// render the upstream image's login form asking for a deployment URL
// + admin key, completely bypassing the Synapse Next.js shell
// (/login, /embed/<name>, the postMessage handshake that auto-logs
// the iframed dashboard, etc.). The role's name promised "dashboard"
// but the implementation delivered "raw image". Custom domains are
// supposed to surface the SAME Synapse experience the operator gets
// when they click "Open dashboard" on their main install URL — auto-
// login, breadcrumb, deployment picker, all of it.
//
// Path dispatch (host = custom dashboard domain, deployment = the row
// bound to this host):
//
//	"" or "/"                              -> 302 to /embed/<deployment>
//	/v1/* /d/* /health /metrics            -> pass to APIHandler (the chi router)
//	/__convex/*                            -> reverse-proxy to ConvexAddr, prefix stripped
//	anything else (/login /embed /teams …) -> reverse-proxy to ShellAddr
//
// APIHandler is the chi router that already owns /v1/*, /d/*, /health,
// /__convex/*. We pass it in so this package doesn't have to import
// internal/api (which would be an import cycle).
//
// The struct is intentionally request-scoped: callers construct one
// per request (the bound DeploymentName changes per host) and call
// ServeHTTP once. ConvexAddr / ShellAddr / APIHandler are stable
// across all requests so they're fine to share.
type DashboardHostHandler struct {
	APIHandler     http.Handler
	ConvexAddr     string
	ShellAddr      string
	DeploymentName string
	Logger         *slog.Logger
}

// ServeHTTP performs the path dispatch documented above. Errors that
// indicate operator misconfiguration (ConvexAddr / ShellAddr empty)
// surface as 503 so Caddy doesn't cache the route as healthy and the
// operator notices in their logs.
func (h *DashboardHostHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Logger == nil {
		h.Logger = slog.Default()
	}
	path := r.URL.Path

	// Root path: bounce straight to the bound deployment's embed
	// page. Operators don't have to remember /embed/<name>;
	// dashboard.<custom>.com IS the deployment's data UI.
	if path == "" || path == "/" {
		// 302 (Found) not 301 (Moved Permanently): we want browsers
		// to re-hit the redirect target on each visit so a deployment
		// rename eventually flows through. The /embed/<name> path
		// itself is stable for a given deployment name; if the name
		// changes the custom-domain row is the source of truth and
		// the next ResolveDomain returns the new name.
		http.Redirect(w, r, "/embed/"+h.DeploymentName, http.StatusFound)
		return
	}

	// chi-owned paths: hand back to the API router unchanged. The
	// chi router has its own /__convex/* mount so we hit it via the
	// same APIHandler — single source of truth for the upstream.
	if path == "/health" ||
		path == "/metrics" ||
		strings.HasPrefix(path, "/v1/") ||
		strings.HasPrefix(path, "/v1") && (len(path) == 3 || path[3] == '?') ||
		strings.HasPrefix(path, "/d/") ||
		strings.HasPrefix(path, "/__convex/") ||
		path == "/__convex" {
		h.APIHandler.ServeHTTP(w, r)
		return
	}

	// Everything else (Next.js shell): /login, /register, /setup,
	// /teams/*, /embed/*, /admin/*, /_next/*, /favicon.ico, etc.
	if h.ShellAddr == "" {
		// No shell wired: best we can do is keep the API responsive
		// and 503 the chrome. Production wiring always sets this; an
		// empty value indicates a misconfigured cmd/server/main.go,
		// which is operator-fixable.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code":    "dashboard_shell_not_configured",
			"message": "Synapse dashboard shell upstream is not wired (DashboardShellAddr is empty)",
		})
		return
	}
	proxyOnce(w, r, h.ShellAddr, path, h.Logger, h.DeploymentName)
}
