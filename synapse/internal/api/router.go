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
	// 90s accommodates the slowest endpoint we have today: create_deployment,
	// which blocks on docker pull + container start + healthcheck. When we
	// move to async provisioning (v0.2) this can drop back to 30s.
	r.Use(chimw.Timeout(90 * time.Second))

	r.Method(http.MethodGet, "/health", &HealthHandler{DB: d.DB, Version: d.Version})

	authH := &AuthHandler{DB: d.DB, JWT: d.JWT}
	meH := &MeHandler{DB: d.DB}
	teamsH := &TeamsHandler{DB: d.DB}
	invitesH := &InvitesHandler{DB: d.DB}
	deploymentsH := &DeploymentsHandler{
		DB:                    d.DB,
		Docker:                d.Docker,
		PortRangeMin:          d.PortRangeMin,
		PortRangeMax:          d.PortRangeMax,
		HealthcheckViaNetwork: d.HealthcheckViaNetwork,
	}
	projectsH := &ProjectsHandler{DB: d.DB, Deployments: deploymentsH}

	r.Route("/v1", func(r chi.Router) {
		r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"name": "synapse", "api": "v1"})
		})

		// Public.
		r.Mount("/auth", authH.Routes())

		// Authenticated.
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticator(d.JWT, d.DB))

			r.Mount("/me", meH.Routes())
			r.Mount("/profile", meH.Routes()) // alias for cloud-dashboard parity
			r.Mount("/teams", teamsH.Routes())
			r.Mount("/projects", projectsH.Routes())
			r.Mount("/deployments", deploymentsH.Routes())
			r.Mount("/team_invites", invitesH.Routes())
		})
	})

	return r
}
