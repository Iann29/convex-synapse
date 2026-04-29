// Package api wires HTTP routes for Synapse.
package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Iann29/synapse/internal/middleware"
)

type RouterDeps struct {
	Logger  *slog.Logger
	DB      *pgxpool.Pool
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
	r.Use(chimw.Timeout(30_000_000_000)) // 30s

	r.Method(http.MethodGet, "/health", &HealthHandler{DB: d.DB, Version: d.Version})

	r.Route("/v1", func(r chi.Router) {
		r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"synapse","api":"v1","status":"placeholder"}`))
		})
	})

	return r
}
