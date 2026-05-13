package api

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// ConvexDashboardProxy serves the open-source Convex Dashboard image
// at a same-origin /__convex/* path on synapse-api. The Synapse Next.js
// `/embed/<name>` page iframes this path so the dashboard's
// postMessage handshake runs on the same origin as the parent — no
// cross-origin TLS gymnastics on :6791, no Mixed Content blocks when
// the parent page is HTTPS and the upstream container is plain HTTP.
//
// Mount point is `/__convex` (chi r.Mount strips the prefix before
// dispatching here, so this handler sees the stripped path as it
// should appear on the upstream).
//
// Field-discovered: synapsepanel.com/embed/<name> stayed black under
// v1.6.x because the iframe src was https://<domain>:6791 and Caddy
// only fronted :443 with TLS — the dashboard image was reachable
// only via plain HTTP on :6791, which the operator's HTTPS page
// can't iframe. Routing through synapse-api at the same origin
// makes the whole TLS/Mixed-Content class of issues disappear.
//
// Upstream is the address of the `convex-dashboard-proxy` Caddy
// sidecar (which itself strips X-Frame-Options + CSP frame-ancestors
// from the bare Convex Dashboard image's responses, so the iframe
// can embed). Production wiring: "synapse-convex-dashboard-proxy:80"
// inside docker compose, "127.0.0.1:6791" on bare-metal hosts.
// Empty disables the route (503) — operator notices and wires it.
type ConvexDashboardProxy struct {
	Upstream string
	Logger   *slog.Logger
}

func (h *ConvexDashboardProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Upstream == "" {
		writeError(w, http.StatusServiceUnavailable, "dashboard_upstream_unset",
			"Convex dashboard upstream not configured (DashboardAddr is empty)")
		return
	}
	target, err := url.Parse("http://" + h.Upstream)
	if err != nil {
		// Misconfig at startup — operator-fixable. We don't want to
		// leak the parse error string; the log line below is enough.
		writeError(w, http.StatusInternalServerError, "internal",
			"Could not parse Convex dashboard upstream address")
		return
	}
	// chi r.Mount strips the /__convex prefix. The request hitting
	// us has r.URL.Path = whatever followed /__convex — e.g. "/" for
	// the root, "/_next/static/foo.js" for an asset, "/team/x/..." for
	// a deep link. If chi strips the trailing slash too (path == "")
	// we coerce back to "/" so the upstream sees a well-formed URI.
	if r.URL.Path == "" {
		r.URL.Path = "/"
	}
	logger := h.Logger
	if logger == nil {
		logger = slog.Default()
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.Warn("convex dashboard proxy: upstream error",
			"path", r.URL.Path, "upstream", h.Upstream, "err", err)
		writeError(w, http.StatusBadGateway, "upstream_error",
			"Convex dashboard upstream is unreachable")
	}
	// NewSingleHostReverseProxy sets a Director that overwrites
	// r.URL.Host + Scheme + Path. We're happy with the default — it
	// joins target.Path with r.URL.Path correctly for the "host root"
	// upstream we have. No body buffering needed; the dashboard is
	// chatty but each request is small.
	rp.ServeHTTP(w, r)
}
