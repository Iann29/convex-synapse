// Package api wires HTTP routes for Synapse.
package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Iann29/synapse/internal/auth"
	"github.com/Iann29/synapse/internal/middleware"
)

type RouterDeps struct {
	Logger *slog.Logger
	DB     *pgxpool.Pool
	JWT    *auth.JWTIssuer
	// Docker is a Provisioner — accepting an interface here lets tests inject
	// a fake without bringing the docker SDK along for the ride. Production
	// wiring passes *dockerprov.Client which already satisfies it.
	Docker                Provisioner
	PortRangeMin          int
	PortRangeMax          int
	HealthcheckViaNetwork bool
	AllowedOrigins        string
	Version               string

	// PublicURL is the externally-reachable origin of the Synapse instance
	// (e.g. "https://synapse.example.com"). When set, /auth and
	// /cli_credentials return URLs the caller's machine can reach instead
	// of the container-internal "http://127.0.0.1:<port>". See
	// config.PublicURL for the full rules.
	PublicURL string
	// ProxyEnabled mirrors config.ProxyEnabled. With PublicURL set, the
	// rewrite becomes "<PublicURL>/d/<name>"; without proxy mode it's
	// "<PublicURL>:<port>" (operator still has to expose the port).
	ProxyEnabled bool

	// BaseDomain (v1.0+) — when set, deployment URLs become
	// "https://<name>.<BaseDomain>". Wins over PublicURL+ProxyEnabled.
	// Empty = custom domains disabled (path-based proxy still works).
	BaseDomain string

	// HA configuration (v0.5+). Zero value = HA disabled, behaves
	// exactly like pre-v0.5. When HA.Enabled is true, create_deployment
	// honours the `ha:true` flag in the request body and provisions
	// replicas backed by the configured Postgres + S3.
	HA HAConfig

	// Crypto encrypts deployment_storage secrets at rest. Required when
	// HA.Enabled is true; nil disables the HA path. The handler refuses
	// ha:true requests with ha_misconfigured when HA is on but Crypto
	// is unset.
	Crypto SecretEncrypter

	// UpdaterSocket is the unix socket path of the synapse-updater
	// systemd daemon. Empty (or unreachable) → /v1/admin/upgrade
	// degrades to "Run setup.sh --upgrade via SSH" via 503. Default
	// in compose: /run/synapse/updater.sock (bind-mounted from host).
	UpdaterSocket string

	// GitHubRepo is "<owner>/<name>" used by /v1/admin/version_check.
	// Default "Iann29/convex-synapse"; overridable so a hard fork can
	// point its dashboard at its own release stream.
	GitHubRepo string

	// GitHubAPIBase is a test seam — defaults to https://api.github.com.
	// Setting it (httptest.Server URL) lets integration tests stub the
	// GitHub fetch without network.
	GitHubAPIBase string

	// PublicIP (v1.1+) is the IPv4 the operator publishes in DNS for
	// per-deployment custom domains. The /domains create + verify
	// handlers gate status='active' on a successful A-record match.
	// Empty disables DNS preflight; rows stay status='pending'.
	PublicIP string

	// DomainCache, when non-nil, is invoked by the domains handler
	// after add / delete / status-flip so the proxy's per-host
	// custom-domain cache drops stale entries instead of waiting for
	// the TTL to elapse. Production wiring passes the *proxy.Resolver
	// (which satisfies the interface). Tests that don't exercise the
	// proxy leave it nil.
	DomainCache DomainCacheInvalidator
}

// DomainCacheInvalidator is the subset of *proxy.Resolver the
// domains handler depends on. Defined as an interface so the api
// package doesn't import internal/proxy (and can stay test-friendly
// with a no-op stub).
type DomainCacheInvalidator interface {
	InvalidateDomain(host string)
}

// HAConfig carries cluster-wide defaults for the per-deployment Postgres
// + S3 backing. Each value can be overridden on a per-deployment basis
// through the create-deployment payload (operator can register a
// different Postgres for a specific tenant).
type HAConfig struct {
	Enabled            bool
	BackendPostgresURL string
	BackendS3Endpoint  string
	BackendS3Region    string
	BackendS3AccessKey string
	BackendS3SecretKey string
	BackendBucketPrefix string
}

// NewRouter builds the top-level chi router. Sub-handlers are mounted by
// resource. Versioned API routes live under /v1; ops endpoints (/health,
// /metrics later) live at the root.
func NewRouter(d RouterDeps) http.Handler {
	r := chi.NewRouter()

	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(middleware.RequestLogger(d.Logger))
	r.Use(middleware.CORS(d.AllowedOrigins))
	r.Use(chimw.Recoverer)
	// Short-circuit OpenAPI paths that Synapse self-hosted intentionally
	// doesn't implement (Convex Cloud's billing, SSO, Discord/Vercel,
	// OAuth apps, cloud backups). Returns 404 not_supported_in_self_hosted
	// before auth so probes reveal the cut without leaking auth state.
	// Catalog: internal/api/not_supported.go.
	r.Use(NotSupportedMiddleware)
	// 30s is plenty now that create_deployment is async — it returns 201 the
	// moment the row is inserted, and the docker pull/start/healthcheck runs
	// in a background goroutine. No request handler should hold a connection
	// longer than this; anything that does is a bug.
	r.Use(chimw.Timeout(30 * time.Second))

	r.Method(http.MethodGet, "/health", &HealthHandler{DB: d.DB, Version: d.Version})

	authH := &AuthHandler{DB: d.DB, JWT: d.JWT}
	meH := &MeHandler{DB: d.DB}
	invitesH := &InvitesHandler{DB: d.DB}
	tokensH := &AccessTokensHandler{DB: d.DB}
	deploymentsH := &DeploymentsHandler{
		DB:                    d.DB,
		Docker:                d.Docker,
		Tokens:                tokensH,
		PortRangeMin:          d.PortRangeMin,
		PortRangeMax:          d.PortRangeMax,
		HealthcheckViaNetwork: d.HealthcheckViaNetwork,
		PublicURL:             d.PublicURL,
		BaseDomain:            d.BaseDomain,
		ProxyEnabled:          d.ProxyEnabled,
		HA:                    d.HA,
		Crypto:                d.Crypto,
	}
	// Per-deployment custom domains (v1.1+). Sub-routes mount under
	// /v1/deployments/{name}/domains; the handler reuses the
	// deployments handler for loadDeploymentForRequest so
	// authorisation logic stays in one place.
	domainsH := &DomainsHandler{
		DB:          d.DB,
		Deployments: deploymentsH,
		PublicIP:    d.PublicIP,
		Cache:       d.DomainCache,
		Logger:      d.Logger,
	}
	deploymentsH.Domains = domainsH
	// teamsH + projectsH carry a *DeploymentsHandler reference so their
	// listDeployments handlers can call publicDeploymentURL — same
	// rewrite as /auth and /cli_credentials so dashboards and CLIs see
	// public URLs instead of the loopback "http://127.0.0.1:<port>".
	// Tokens enables scope-aware access-token CRUD under /access_tokens.
	teamsH := &TeamsHandler{DB: d.DB, Deployments: deploymentsH, Tokens: tokensH}
	projectsH := &ProjectsHandler{DB: d.DB, Deployments: deploymentsH, Tokens: tokensH}

	r.Route("/v1", func(r chi.Router) {
		r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"name": "synapse", "api": "v1"})
		})

		// Public.
		r.Mount("/auth", authH.Routes())
		// install_status is also public — the dashboard hits it pre-auth
		// to decide whether to redirect /login → /setup (first-run wizard).
		r.Method(http.MethodGet, "/install_status", &InstallStatusHandler{DB: d.DB, Version: d.Version})
		// TLS-ask for Caddy on-demand TLS (v1.0+). Public, no auth —
		// Caddy hits it from inside the docker network without a JWT.
		// The handler rejects any host outside `<sub>.<BaseDomain>`,
		// so an unconfigured cluster (BaseDomain empty) always 404s.
		//
		// Wrapped in r.Route("/internal", ...) because chi's r.Method
		// on a multi-segment pattern silently fails to register —
		// real-VPS smoke caught a 404 on every request despite the
		// handler compiling and the path looking right.
		r.Route("/internal", func(r chi.Router) {
			r.Method(http.MethodGet, "/tls_ask", &TLSAskHandler{DB: d.DB, BaseDomain: d.BaseDomain})
			// list_deployments_for_dashboard — cross-origin endpoint
			// the upstream Convex Dashboard hits from inside the
			// /embed/<name> iframe. Public route, but the request
			// must carry a `?token=` query param holding a
			// project-scoped PAT (minted by the Synapse Dashboard
			// before the iframe loads, TTL ~15min). See
			// dashboard_proxy.go for the auth + cors discussion.
			dashProxy := &DashboardProxyHandler{DB: d.DB, Deployments: deploymentsH}
			r.Get("/list_deployments_for_dashboard", dashProxy.listDeploymentsForDashboard)
		})

		// Authenticated.
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticator(d.JWT, d.DB))

			r.Mount("/me", meH.Routes())
			r.Mount("/profile", meH.Routes()) // alias for cloud-dashboard parity
			r.Mount("/teams", teamsH.Routes())
			r.Mount("/projects", projectsH.Routes())
			r.Mount("/deployments", deploymentsH.Routes())
			r.Mount("/team_invites", invitesH.Routes())
			// /v1/admin — instance-level operations (version check + auto-
			// upgrade). The handler's own middleware gates each route to
			// "any team admin"; we mount inside the authenticated group
			// so unauthenticated probes still hit the auth 401 path.
			adminH := &AdminHandler{
				DB:            d.DB,
				Version:       d.Version,
				UpdaterSocket: d.UpdaterSocket,
				GitHubRepo:    d.GitHubRepo,
				GitHubAPIBase: d.GitHubAPIBase,
			}
			r.Mount("/admin", adminH.Routes())
			// Personal access tokens — flat verb-suffixed endpoints under /v1.
			// Registered directly (not via Mount) because chi's Mount("/", ...)
			// collides with the existing GET /v1/ index handler above.
			tokensH.Register(r)
			// Profile-level cloud-spec endpoints — mounted flat the same way
			// as access tokens (Mount("/", ...) collides with the index
			// handler). See MeHandler.RegisterTopLevel for the per-route
			// rationale.
			meH.RegisterTopLevel(r)
		})
	})

	return r
}
