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
		PortRangeMin:          d.PortRangeMin,
		PortRangeMax:          d.PortRangeMax,
		HealthcheckViaNetwork: d.HealthcheckViaNetwork,
		PublicURL:             d.PublicURL,
		BaseDomain:            d.BaseDomain,
		ProxyEnabled:          d.ProxyEnabled,
		HA:                    d.HA,
		Crypto:                d.Crypto,
	}
	// teamsH + projectsH carry a *DeploymentsHandler reference so their
	// listDeployments handlers can call publicDeploymentURL — same
	// rewrite as /auth and /cli_credentials so dashboards and CLIs see
	// public URLs instead of the loopback "http://127.0.0.1:<port>".
	teamsH := &TeamsHandler{DB: d.DB, Deployments: deploymentsH}
	projectsH := &ProjectsHandler{DB: d.DB, Deployments: deploymentsH}

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
		r.Method(http.MethodGet, "/internal/tls_ask", &TLSAskHandler{DB: d.DB, BaseDomain: d.BaseDomain})

		// Authenticated.
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticator(d.JWT, d.DB))

			r.Mount("/me", meH.Routes())
			r.Mount("/profile", meH.Routes()) // alias for cloud-dashboard parity
			r.Mount("/teams", teamsH.Routes())
			r.Mount("/projects", projectsH.Routes())
			r.Mount("/deployments", deploymentsH.Routes())
			r.Mount("/team_invites", invitesH.Routes())
			// Personal access tokens — flat verb-suffixed endpoints under /v1.
			// Registered directly (not via Mount) because chi's Mount("/", ...)
			// collides with the existing GET /v1/ index handler above.
			tokensH.Register(r)
		})
	})

	return r
}
