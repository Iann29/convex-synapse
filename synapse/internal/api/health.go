package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type HealthHandler struct {
	DB      *pgxpool.Pool
	Version string
	// ProxyEnabled mirrors config.Config.ProxyEnabled. When unset (false), the
	// handler falls back to inspecting SYNAPSE_PROXY_ENABLED directly so the
	// /health response remains accurate without threading the flag through
	// router wiring (kept narrow on purpose — see health.go change in the
	// e2e proxy-spec commit). Tests can override by setting it explicitly.
	ProxyEnabled bool
}

type healthResponse struct {
	Status       string `json:"status"`
	Version      string `json:"version"`
	Database     string `json:"database"`
	ProxyEnabled bool   `json:"proxyEnabled"`
}

func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	proxyEnabled := h.ProxyEnabled
	if !proxyEnabled {
		// Fallback: same env var config.Load() reads. Avoids touching
		// router/main wiring just to surface a single boolean.
		proxyEnabled = os.Getenv("SYNAPSE_PROXY_ENABLED") == "true"
	}
	resp := healthResponse{
		Status:       "ok",
		Version:      h.Version,
		Database:     "ok",
		ProxyEnabled: proxyEnabled,
	}

	if h.DB != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := h.DB.Ping(ctx); err != nil {
			resp.Status = "degraded"
			resp.Database = "unreachable"
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	} else {
		resp.Database = "not_configured"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
