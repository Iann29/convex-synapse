// Package proxy mounts a reverse-proxy handler at /d/{name}/* on the Synapse
// HTTP server. This lets a single host port (8080) act as a front door to
// every provisioned Convex backend, removing the need to expose a separate
// host port per deployment.
//
// Trade-off: the Convex backend's own CONVEX_CLOUD_ORIGIN env var still points
// at the per-container host-port mapping today, so absolute URLs the backend
// returns (file storage signed URLs, redirects to itself) will reference the
// direct port and not /d/{name}. For 95% of API calls — function invocations,
// queries, mutations — the proxy is fully transparent.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Resolver looks up where a deployment lives so the proxy can forward to it.
// In compose mode, addresses are docker-DNS names like "convex-foo:3210";
// when synapse runs on the host, addresses are "127.0.0.1:<hostPort>".
type Resolver struct {
	DB *pgxpool.Pool
	// UseNetworkDNS chooses between the two address shapes — same flag that
	// the provisioner uses for healthchecks. true inside compose, false on host.
	UseNetworkDNS bool
	// CacheTTL is how long a name→address binding stays in memory before we
	// re-read the DB. Short enough to catch deletes, long enough that a
	// chatty client doesn't hammer the DB on every request.
	CacheTTL time.Duration

	mu    sync.RWMutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	addr      string
	expiresAt time.Time
}

// Resolve returns the address ("host:port") for a deployment, or "" + error if
// the deployment is missing / deleted / not running.
func (r *Resolver) Resolve(ctx context.Context, name string) (string, error) {
	r.mu.RLock()
	if e, ok := r.cache[name]; ok && time.Now().Before(e.expiresAt) {
		addr := e.addr
		r.mu.RUnlock()
		return addr, nil
	}
	r.mu.RUnlock()

	var hostPort *int
	var status string
	err := r.DB.QueryRow(ctx, `
		SELECT host_port, status FROM deployments WHERE name = $1
	`, name).Scan(&hostPort, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("deployment %q not found", name)
	}
	if err != nil {
		return "", err
	}
	if status != "running" {
		return "", fmt.Errorf("deployment %q is %s, not running", name, status)
	}

	var addr string
	if r.UseNetworkDNS {
		addr = "convex-" + name + ":3210"
	} else {
		if hostPort == nil {
			return "", fmt.Errorf("deployment %q has no host port", name)
		}
		addr = fmt.Sprintf("127.0.0.1:%d", *hostPort)
	}

	ttl := r.CacheTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = make(map[string]cacheEntry)
	}
	r.cache[name] = cacheEntry{addr: addr, expiresAt: time.Now().Add(ttl)}
	r.mu.Unlock()
	return addr, nil
}

// Invalidate drops a single name from the cache. Call this after a delete /
// state change so the next request re-reads from the DB.
func (r *Resolver) Invalidate(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cache, name)
}

// Handler returns an http.Handler mounted at /d/. It expects URLs of the
// form /d/{name}/{rest...} and forwards to http://{address}/{rest...}.
//
// Errors flow back as JSON: 404 for missing deployments, 502 for upstream
// failures. The proxy is a passthrough — no auth check; deployments enforce
// their own admin-key auth.
func Handler(resolver *Resolver, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}

	// Single ReverseProxy instance, parameterised at request time via Director.
	rp := &httputil.ReverseProxy{
		Director: func(_ *http.Request) {
			// We rewrite the URL in-place inside ServeHTTP below — Director
			// is left empty to avoid being called twice.
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Warn("proxy upstream error", "path", r.URL.Path, "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"code":    "upstream_error",
				"message": "Deployment is unreachable",
			})
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip "/d/" prefix and split off the deployment name.
		raw := strings.TrimPrefix(r.URL.Path, "/d/")
		if raw == r.URL.Path {
			http.NotFound(w, r)
			return
		}
		slash := strings.IndexByte(raw, '/')
		var name, rest string
		if slash < 0 {
			name = raw
			rest = "/"
		} else {
			name = raw[:slash]
			rest = raw[slash:]
		}
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"code":    "bad_request",
				"message": "/d/{deploymentName}/... required",
			})
			return
		}

		addr, err := resolver.Resolve(r.Context(), name)
		if err != nil {
			logger.Info("proxy resolve miss", "name", name, "err", err)
			writeJSON(w, http.StatusNotFound, map[string]string{
				"code":    "deployment_not_found",
				"message": err.Error(),
			})
			return
		}

		target, _ := url.Parse("http://" + addr)
		// Set the request's URL to the upstream's host, preserving the rest path
		// + raw query.
		r2 := r.Clone(r.Context())
		r2.URL.Scheme = target.Scheme
		r2.URL.Host = target.Host
		r2.URL.Path = rest
		r2.Host = target.Host
		// Hide that this came through a proxy — the backend doesn't care, and
		// emitting X-Forwarded-* triggers some Convex-side address-rewriting.
		r2.Header.Del("X-Forwarded-For")
		r2.Header.Del("X-Forwarded-Host")
		r2.Header.Del("X-Forwarded-Proto")

		rp.ServeHTTP(w, r2)
	})
}

func writeJSON(w http.ResponseWriter, status int, body map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Tiny manual encode — no JSON dependency cycle here, and the body shape
	// is fixed. Keep this file zero-import beyond stdlib + pgx.
	first := true
	_, _ = w.Write([]byte("{"))
	for k, v := range body {
		if !first {
			_, _ = w.Write([]byte(","))
		}
		first = false
		_, _ = fmt.Fprintf(w, `"%s":"%s"`, escapeJSON(k), escapeJSON(v))
	}
	_, _ = w.Write([]byte("}"))
}

// escapeJSON handles the small set of chars that can show up in our error
// messages. Anything weirder than this and we should switch to encoding/json.
func escapeJSON(s string) string {
	if !strings.ContainsAny(s, `"\`+"\n\r\t") {
		return s
	}
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	)
	return r.Replace(s)
}
