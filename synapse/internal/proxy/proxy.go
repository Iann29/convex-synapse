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
//
// HA awareness: when a deployment has multiple replicas (deployment_replicas
// rows from the v0.5 schema), Resolve returns the full list and the proxy
// handler tries them in preference order — last-seen-active first, then by
// ascending replica_index. A connection error against the first replica
// triggers an automatic retry against the next; the caller never sees the
// dead replica unless every replica is unreachable.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
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

// cacheEntry stores the resolved replica list for a deployment, plus the
// time the entry was minted. Replica order is the picker's preference
// order (best replica first); the proxy retries down the slice on
// connection error.
type cacheEntry struct {
	replicas  []string
	expiresAt time.Time
}

// ErrAsterNotProxied signals that the deployment exists and is reachable,
// but it's a kind=aster row whose runtime doesn't speak HTTP — it speaks
// the Aster IPC protocol over a Unix-domain socket. The proxy maps this
// to a typed 501 so the dashboard can render an "Aster — execution
// path not yet wired" panel instead of pretending the deployment is
// broken. Raw-JS cell invocation already lives on the control API; this
// sentinel remains until the Convex-shaped HTTP frontend lands.
var ErrAsterNotProxied = errors.New("kind=aster deployments do not expose an HTTP proxy yet")

// ErrNoReplicas signals that a deployment has no live replicas. Distinct
// from "deployment not found" so the handler can return 503 (Service
// Unavailable) instead of 404.
var ErrNoReplicas = errors.New("deployment has no running replicas")

// Resolve returns the highest-priority address for a deployment — the
// active replica per the picker. Callers that want the full list (e.g.
// the proxy handler so it can retry on connection failure) should use
// ResolveAll. Resolve is kept around for legacy single-address callers.
func (r *Resolver) Resolve(ctx context.Context, name string) (string, error) {
	addrs, err := r.ResolveAll(ctx, name)
	if err != nil {
		return "", err
	}
	if len(addrs) == 0 {
		return "", ErrNoReplicas
	}
	return addrs[0], nil
}

// ResolveAll returns every running replica's address, ordered by the
// picker's preference: replicas with a recent successful health probe
// first (last_seen_active_at DESC), ties broken by ascending
// replica_index. For single-replica deployments the slice has one entry.
//
// The lookup goes through deployment_replicas (post-v0.5 schema). When
// no replica rows exist (extremely unlikely after the backfill migration
// — see internal/db/migrations/000004), we fall back to the legacy
// deployments.host_port path so a half-rolled-out cluster never hands
// callers a 404.
func (r *Resolver) ResolveAll(ctx context.Context, name string) ([]string, error) {
	r.mu.RLock()
	if e, ok := r.cache[name]; ok && time.Now().Before(e.expiresAt) {
		out := append([]string(nil), e.replicas...)
		r.mu.RUnlock()
		return out, nil
	}
	r.mu.RUnlock()

	// First: does the deployment exist and is it in a routable state?
	var haEnabled bool
	var depStatus, depKind string
	err := r.DB.QueryRow(ctx,
		`SELECT ha_enabled, status, kind FROM deployments WHERE name = $1`, name,
	).Scan(&haEnabled, &depStatus, &depKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("deployment %q not found", name)
	}
	if err != nil {
		return nil, err
	}
	if depStatus != "running" {
		return nil, fmt.Errorf("deployment %q is %s, not running", name, depStatus)
	}
	// kind=aster: row exists and the brokerd container is up, but
	// there's no HTTP runtime to proxy into. Surface that explicitly so
	// the handler can return a 501 with a useful message rather than a
	// confusing 502/timeout.
	if depKind == "aster" {
		return nil, ErrAsterNotProxied
	}

	// Then: ordered list of running replicas. last_seen_active_at DESC
	// puts the most-recently-healthy replica first; ties resolve by
	// ascending replica_index for determinism.
	rows, err := r.DB.Query(ctx, `
		SELECT r.host_port, r.replica_index
		  FROM deployment_replicas r
		  JOIN deployments d ON d.id = r.deployment_id
		 WHERE d.name = $1
		   AND r.status = 'running'
		 ORDER BY r.last_seen_active_at DESC NULLS LAST, r.replica_index ASC
	`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type rrow struct {
		hostPort     *int
		replicaIndex int
	}
	var replicas []rrow
	for rows.Next() {
		var rr rrow
		if err := rows.Scan(&rr.hostPort, &rr.replicaIndex); err != nil {
			return nil, err
		}
		replicas = append(replicas, rr)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	addrs := make([]string, 0, len(replicas))
	if len(replicas) == 0 {
		// No *running* replicas. Distinguish "deployment has no replica
		// rows at all" (legacy / not-yet-migrated → fall back to
		// deployments.host_port) from "rows exist, none are running"
		// (no_replicas → 503). A second cheap query keeps the fallback
		// guarded so ErrNoReplicas isn't masked by stale host_port data.
		var anyReplica bool
		_ = r.DB.QueryRow(ctx, `
			SELECT EXISTS(
			  SELECT 1
			    FROM deployment_replicas r
			    JOIN deployments d ON d.id = r.deployment_id
			   WHERE d.name = $1
			)
		`, name).Scan(&anyReplica)
		if anyReplica {
			return nil, ErrNoReplicas
		}
		fallback, ferr := r.legacyAddress(ctx, name, haEnabled)
		if ferr != nil {
			return nil, ErrNoReplicas
		}
		addrs = append(addrs, fallback)
	}
	for _, rr := range replicas {
		if rr.hostPort == nil && !r.UseNetworkDNS {
			// Replica still booting (no port yet) — skip; picker uses
			// whichever sibling is up.
			continue
		}
		addrs = append(addrs, r.addressFor(name, rr.replicaIndex, haEnabled, rr.hostPort))
	}
	if len(addrs) == 0 {
		return nil, ErrNoReplicas
	}

	ttl := r.CacheTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = make(map[string]cacheEntry)
	}
	r.cache[name] = cacheEntry{
		replicas:  append([]string(nil), addrs...),
		expiresAt: time.Now().Add(ttl),
	}
	r.mu.Unlock()
	return addrs, nil
}

// addressFor builds either the docker-DNS or host-port address for one
// replica. Single-replica (haEnabled=false) keeps the legacy "convex-{name}"
// container name; HA replicas pick up the "-{idx}" suffix.
func (r *Resolver) addressFor(name string, replicaIndex int, haEnabled bool, hostPort *int) string {
	if r.UseNetworkDNS {
		if !haEnabled {
			return "convex-" + name + ":3210"
		}
		return fmt.Sprintf("convex-%s-%d:3210", name, replicaIndex)
	}
	// Host-port mode. hostPort is checked non-nil by the caller (we skip
	// replicas that don't have a port yet).
	return fmt.Sprintf("127.0.0.1:%d", *hostPort)
}

// legacyAddress falls back to deployments.host_port when no
// deployment_replicas rows exist for a deployment. This is unreachable
// in production (the backfill migration creates a replica row for every
// existing deployment) but avoids a hard failure during a half-applied
// rollout.
func (r *Resolver) legacyAddress(ctx context.Context, name string, haEnabled bool) (string, error) {
	var hostPort *int
	var status string
	err := r.DB.QueryRow(ctx,
		`SELECT host_port, status FROM deployments WHERE name = $1`, name,
	).Scan(&hostPort, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("deployment %q not found", name)
	}
	if err != nil {
		return "", err
	}
	if status != "running" {
		return "", fmt.Errorf("deployment %q is %s, not running", name, status)
	}
	if r.UseNetworkDNS {
		return "convex-" + name + ":3210", nil
	}
	if hostPort == nil {
		return "", fmt.Errorf("deployment %q has no host port", name)
	}
	_ = haEnabled
	return fmt.Sprintf("127.0.0.1:%d", *hostPort), nil
}

// Invalidate drops a single name from the cache. Call this after a delete /
// state change so the next request re-reads from the DB.
func (r *Resolver) Invalidate(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cache, name)
}

// Handler returns an http.Handler that proxies to a Convex backend.
// Two routing modes are supported simultaneously:
//
//  1. **Path-based** (always on): URLs of the form
//     `/d/{name}/{rest...}` are forwarded to `http://{address}/{rest...}`.
//     This is the v0.2 contract — every operator with `SYNAPSE_PROXY_ENABLED=true`
//     gets it.
//
//  2. **Host-header-based** (v1.0+, opt-in via baseDomain non-empty):
//     when `r.Host` matches `<name>.<baseDomain>`, route to the named
//     deployment using `r.URL.Path` as the upstream path. Lets operators
//     wire wildcard DNS + on-demand TLS so Convex clients see
//     `https://<name>.<base>` instead of `<base>/d/<name>`.
//
// HA failover: ResolveAll returns the replicas in preference order. The
// handler tries the first; on a connection-level error (Dial / EOF
// before headers arrive) it transparently retries against the next
// replica. HTTP-level errors (4xx/5xx from the backend) flow through
// untouched — they're upstream responses, not unreachable replicas, and
// the caller should see them.
//
// Empty baseDomain disables host-based routing — the path form keeps
// working unchanged, so non-custom-domain installs see no behaviour
// change.
func Handler(resolver *Resolver, logger *slog.Logger, baseDomain string) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var name, rest string

		// Host-based dispatch wins when configured AND the Host
		// matches. We split on the host's leftmost label so a request
		// to "bold-fox-1234.synapse.example.com" routes to
		// "bold-fox-1234". Strip any ":port" suffix Caddy may have
		// passed through. Empty subdomain (just `.<base>`) is a 404
		// — there's no deployment to address.
		if baseDomain != "" {
			host := r.Host
			if i := strings.IndexByte(host, ':'); i >= 0 {
				host = host[:i]
			}
			if sub := matchHostSubdomain(host, baseDomain); sub != "" {
				name, rest = sub, r.URL.Path
				if rest == "" {
					rest = "/"
				}
			}
		}

		// Path-based fallback. Either baseDomain isn't configured or
		// the Host doesn't match — try `/d/{name}/{rest}`.
		if name == "" {
			raw := strings.TrimPrefix(r.URL.Path, "/d/")
			if raw == r.URL.Path {
				http.NotFound(w, r)
				return
			}
			slash := strings.IndexByte(raw, '/')
			if slash < 0 {
				name = raw
				rest = "/"
			} else {
				name = raw[:slash]
				rest = raw[slash:]
			}
		}
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"code":    "bad_request",
				"message": "/d/{deploymentName}/... required",
			})
			return
		}

		addrs, err := resolver.ResolveAll(r.Context(), name)
		if errors.Is(err, ErrNoReplicas) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"code":    "no_replicas",
				"message": "Deployment has no running replicas",
			})
			return
		}
		if errors.Is(err, ErrAsterNotProxied) {
			writeJSON(w, http.StatusNotImplemented, map[string]string{
				"code":    "aster_not_proxied",
				"message": "kind=aster deployments are reachable through the Aster IPC protocol, not HTTP. Use POST /v1/deployments/{name}/aster/invoke for raw-JS smokes; Convex-shaped HTTP access lands with the module-loader frontend.",
				"kind":    "aster",
			})
			return
		}
		if err != nil {
			logger.Info("proxy resolve miss", "name", name, "err", err)
			writeJSON(w, http.StatusNotFound, map[string]string{
				"code":    "deployment_not_found",
				"message": err.Error(),
			})
			return
		}

		// Single-replica deployments take the fast path — no body
		// buffering, no retry. The HA path needs to read the body once
		// so a connection-level failure on replica 0 can retry against
		// replica 1 with the same payload.
		if len(addrs) == 1 {
			proxyOnce(w, r, addrs[0], rest, logger, name)
			return
		}
		proxyWithFailover(w, r, addrs, rest, logger, name)
	})
}

// matchHostSubdomain returns the leftmost label of `host` when it's a
// subdomain of `base` (host == "<sub>.<base>"), or "" otherwise. Case-
// insensitive — DNS isn't case-sensitive, and the operator's `.env` may
// not match the browser's casing.
//
//	matchHostSubdomain("bold-fox.synapse.example.com", "synapse.example.com")
//	  → "bold-fox"
//	matchHostSubdomain("synapse.example.com",          "synapse.example.com") → ""
//	matchHostSubdomain("foo.bar.synapse.example.com",  "synapse.example.com")
//	  → "foo.bar"
func matchHostSubdomain(host, base string) string {
	host = strings.ToLower(host)
	base = strings.ToLower(strings.Trim(base, "."))
	if base == "" || host == "" {
		return ""
	}
	suffix := "." + base
	if !strings.HasSuffix(host, suffix) || host == base {
		return ""
	}
	return host[:len(host)-len(suffix)]
}

// proxyOnce serves the request via a single replica. Equivalent to the
// pre-v0.5 behaviour. Any error gets logged and a 502 sent back.
func proxyOnce(w http.ResponseWriter, r *http.Request, addr, rest string, logger *slog.Logger, name string) {
	target, _ := url.Parse("http://" + addr)
	rp := newReverseProxy(target, logger, name, addr)
	r2 := r.Clone(r.Context())
	rewriteUpstream(r2, target, rest)
	rp.ServeHTTP(w, r2)
}

// proxyWithFailover tries each replica in order. If the first replica's
// upstream call fails with a connection-level error before any bytes
// were written to the client, we transparently retry against the next.
//
// Body buffering: HTTP requests are read-once. To retry we have to
// snapshot the body in memory. Convex requests are small (function
// args, mutation payloads) so the 1MB cap is generous; anything larger
// than that and HA failover degrades gracefully to "single attempt".
func proxyWithFailover(w http.ResponseWriter, r *http.Request, addrs []string, rest string, logger *slog.Logger, name string) {
	const maxBodyForRetry = 1 << 20 // 1MB
	var bodyBytes []byte
	if r.Body != nil && r.ContentLength != 0 {
		var err error
		bodyBytes, err = io.ReadAll(io.LimitReader(r.Body, maxBodyForRetry+1))
		_ = r.Body.Close()
		if err != nil {
			logger.Warn("proxy: read body for retry", "name", name, "err", err)
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}
		if int64(len(bodyBytes)) > maxBodyForRetry {
			// Body too large to safely retry — fall back to single
			// attempt, no failover. Operator gets a generic 502 if the
			// first replica is dead.
			r.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))
			proxyOnce(w, r, addrs[0], rest, logger, name)
			return
		}
	}

	for i, addr := range addrs {
		target, _ := url.Parse("http://" + addr)
		// Capture failures from this attempt without writing anything
		// to the client — that's the contract for retrying.
		captured := &capturingResponseWriter{header: http.Header{}}
		var attemptErr error
		rp := &httputil.ReverseProxy{
			Director: func(_ *http.Request) {},
			ErrorHandler: func(_ http.ResponseWriter, _ *http.Request, err error) {
				attemptErr = err
			},
		}

		r2 := r.Clone(r.Context())
		if bodyBytes != nil {
			r2.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))
			r2.ContentLength = int64(len(bodyBytes))
		}
		rewriteUpstream(r2, target, rest)
		rp.ServeHTTP(captured, r2)

		if attemptErr != nil && isConnError(attemptErr) && i < len(addrs)-1 {
			logger.Info("proxy: replica failover",
				"name", name, "from", addr, "err", attemptErr)
			continue
		}

		// Either success, or the last replica's error — flush captured
		// state to the real ResponseWriter and stop.
		captured.flush(w)
		if attemptErr != nil {
			logger.Warn("proxy: all replicas failed", "name", name, "err", attemptErr)
		}
		return
	}
}

// rewriteUpstream points the request at the upstream replica.
func rewriteUpstream(r *http.Request, target *url.URL, rest string) {
	r.URL.Scheme = target.Scheme
	r.URL.Host = target.Host
	r.URL.Path = rest
	r.Host = target.Host
	// Hide that this came through a proxy — the backend doesn't care, and
	// emitting X-Forwarded-* triggers some Convex-side address-rewriting.
	r.Header.Del("X-Forwarded-For")
	r.Header.Del("X-Forwarded-Host")
	r.Header.Del("X-Forwarded-Proto")
}

// newReverseProxy builds a one-shot ReverseProxy with a JSON 502 error
// handler. Logger annotates with the deployment name + replica address
// so operators can grep through the ones that did fail.
func newReverseProxy(target *url.URL, logger *slog.Logger, deploymentName, addr string) *httputil.ReverseProxy {
	_ = target
	return &httputil.ReverseProxy{
		Director: func(_ *http.Request) {},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Warn("proxy upstream error",
				"path", r.URL.Path, "deployment", deploymentName, "replica", addr, "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"code":    "upstream_error",
				"message": "Deployment is unreachable",
			})
		},
	}
}

// isConnError returns true when err is a connection-level failure that
// makes retrying against another replica safe (no bytes were sent
// upstream / received yet). Stays conservative: an in-flight HTTP
// response that errors mid-stream is NOT retried — the client has
// already seen partial bytes from the upstream by then.
func isConnError(err error) bool {
	if err == nil {
		return false
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	// Wrapped EOF / connection reset / connection refused.
	s := err.Error()
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "no such host") ||
		strings.Contains(s, "EOF")
}

// capturingResponseWriter captures the full response so we can decide
// whether to flush it (success / final-replica failure) or discard it
// (transient failure → retry on the next replica).
type capturingResponseWriter struct {
	header http.Header
	status int
	body   []byte
}

func (c *capturingResponseWriter) Header() http.Header {
	return c.header
}

func (c *capturingResponseWriter) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (c *capturingResponseWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	c.body = append(c.body, p...)
	return len(p), nil
}

// flush copies the captured response onto the real ResponseWriter.
// Called exactly once after the picker has decided not to retry.
func (c *capturingResponseWriter) flush(w http.ResponseWriter) {
	if c.status == 0 {
		// No headers written → ErrorHandler took over and wrote its own
		// JSON. Nothing to flush.
		return
	}
	for k, vs := range c.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(c.status)
	if len(c.body) > 0 {
		_, _ = w.Write(c.body)
	}
}

func writeJSON(w http.ResponseWriter, status int, body map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
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

// healthProbe is a 2-second probe loop that hits /version on each
// configured replica and updates deployment_replicas.last_seen_active_at
// when a 2xx comes back. The picker uses this column to prefer recently-
// alive replicas, so the column is the source of truth for "who's
// healthy right now."
//
// Single goroutine, started once per Synapse process. Multi-node:
// running this on every node is fine — last_seen_active_at converges.
//
// Currently a placeholder (not wired into cmd/server) — chunk 5 ships
// the Resolver changes; the active probe loop arrives in a follow-up
// once we have HA deployments to probe against.
//
// (Left here so reviewers see where the loop will live; spec deliberately
// out-of-scope for this PR per docs/V0_5_PLAN.md.)
type healthProbe struct {
	DB     *pgxpool.Pool
	Period time.Duration
}

var _ = func(_ context.Context, _ *healthProbe) {}
