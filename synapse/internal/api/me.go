package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Iann29/synapse/internal/auth"
	"github.com/Iann29/synapse/internal/models"
)

// MeHandler exposes endpoints scoped to the authenticated principal.
//
// Convex Cloud calls the equivalent endpoint /api/dashboard/profile;
// we mirror that under both /v1/me and /v1/profile for compatibility.
type MeHandler struct {
	DB *pgxpool.Pool
}

func (h *MeHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.me)
	return r
}

func (h *MeHandler) me(w http.ResponseWriter, r *http.Request) {
	uid, err := auth.UserID(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Not authenticated")
		return
	}
	var u models.User
	err = h.DB.QueryRow(r.Context(), `
		SELECT id, email, name, created_at, updated_at FROM users WHERE id = $1
	`, uid).Scan(&u.ID, &u.Email, &u.Name, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "user_not_found", "User no longer exists")
		return
	}
	if err != nil {
		logErr("fetch user", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to fetch user")
		return
	}
	writeJSON(w, http.StatusOK, u)
}
