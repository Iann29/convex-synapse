package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type HealthHandler struct {
	DB      *pgxpool.Pool
	Version string
}

type healthResponse struct {
	Status   string `json:"status"`
	Version  string `json:"version"`
	Database string `json:"database"`
}

func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resp := healthResponse{
		Status:   "ok",
		Version:  h.Version,
		Database: "ok",
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
