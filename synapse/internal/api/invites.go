package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Iann29/synapse/internal/auth"
)

// InvitesHandler exposes the invite-acceptance endpoint. The list and cancel
// operations live on the TeamsHandler since they are scoped to a team.
type InvitesHandler struct {
	DB *pgxpool.Pool
}

func (h *InvitesHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/accept", h.accept)
	return r
}

// ---------- POST /v1/team_invites/accept ----------
//
// Accepts an open invite token. The caller must already be authenticated
// (i.e. have an account). On success the user is added to the team as the
// role recorded in the invite. The invite token is consumed (accepted_at set).

type acceptInviteReq struct {
	Token string `json:"token"`
}

type acceptInviteResp struct {
	TeamID   string `json:"teamId"`
	TeamSlug string `json:"teamSlug"`
	TeamName string `json:"teamName"`
	Role     string `json:"role"`
}

func (h *InvitesHandler) accept(w http.ResponseWriter, r *http.Request) {
	uid, err := auth.UserID(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Not authenticated")
		return
	}
	var req acceptInviteReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "missing_token", "Invite token is required")
		return
	}

	// Single transaction: look up + consume + add membership atomically so
	// double-accept races collapse to a single row.
	tx, err := h.DB.Begin(r.Context())
	if err != nil {
		logErr("tx begin", err)
		writeError(w, http.StatusInternalServerError, "internal", "Database error")
		return
	}
	defer tx.Rollback(r.Context())

	var inviteID, teamID, role string
	err = tx.QueryRow(r.Context(), `
		SELECT id, team_id, role
		  FROM team_invites
		 WHERE token = $1
		   AND accepted_at IS NULL
		 FOR UPDATE
	`, req.Token).Scan(&inviteID, &teamID, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "invite_not_found", "Invite token is invalid or already used")
		return
	}
	if err != nil {
		logErr("lookup invite", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to load invite")
		return
	}

	// Insert membership; ignore conflict so re-accepting from a second
	// session is a no-op rather than a 500.
	_, err = tx.Exec(r.Context(), `
		INSERT INTO team_members (team_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (team_id, user_id) DO NOTHING
	`, teamID, uid, role)
	if err != nil {
		logErr("add member", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to join team")
		return
	}

	// Mark consumed.
	_, err = tx.Exec(r.Context(),
		`UPDATE team_invites SET accepted_at = now() WHERE id = $1`, inviteID)
	if err != nil {
		logErr("mark accepted", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to consume invite")
		return
	}

	// Read team metadata for the response.
	var teamName, teamSlug string
	if err := tx.QueryRow(r.Context(),
		`SELECT name, slug FROM teams WHERE id = $1`, teamID).Scan(&teamName, &teamSlug); err != nil {
		logErr("read team", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to load team")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		logErr("tx commit", err)
		writeError(w, http.StatusInternalServerError, "internal", "Database error")
		return
	}

	writeJSON(w, http.StatusOK, acceptInviteResp{
		TeamID:   teamID,
		TeamSlug: teamSlug,
		TeamName: teamName,
		Role:     role,
	})
}
