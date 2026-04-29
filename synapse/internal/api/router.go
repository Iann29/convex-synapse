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
	Logger  *slog.Logger
	DB      *pgxpool.Pool
	JWT     *auth.JWTIssuer
	Version string
}

// NewRouter builds the top-level chi router. Sub-handlers are mounted by
// resource. Versioned API routes live under /v1; ops endpoints (/health,
// /metrics later) live at the root.
func NewRouter(d RouterDeps) http.Handler {
	r := chi.NewRouter()

	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(middleware.RequestLogger(d.Logger))
	r.Use(chimw.Recoverer)
	r.Use(chimw.Timeout(30 * time.Second))

	r.Method(http.MethodGet, "/health", &HealthHandler{DB: d.DB, Version: d.Version})

	authH := &AuthHandler{DB: d.DB, JWT: d.JWT}
	meH := &MeHandler{DB: d.DB}

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
		})
	})

	return r
}
