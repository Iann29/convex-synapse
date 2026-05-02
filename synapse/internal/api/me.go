package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Iann29/synapse/internal/audit"
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
	// Cloud's spec puts these at the top level (/update_profile_name and
	// /delete_account); router.go also registers them there. We mirror them
	// inside /me as a convenience so dashboards built around the /me bag of
	// account endpoints find them in the same prefix.
	r.Put("/update_profile_name", h.updateProfileName)
	r.Post("/delete_account", h.deleteAccount)
	// member_data is read-only and roughly an alias for /me with extra
	// teams/projects/deployments fields. We return /me-shape today; the
	// dashboard treats the extra fields as optional.
	r.Get("/member_data", h.memberData)
	return r
}

// RegisterTopLevel installs the cloud-spec flat endpoints onto the supplied
// authenticated router. Mounted from router.go alongside access_tokens.Register.
//
// PUT /v1/update_profile_name        — update the caller's display name
// POST /v1/delete_account            — delete the caller's account
// GET /v1/member_data                — alias for /v1/me with extra fields
// GET /v1/optins                     — TOS / marketing opt-ins (always [])
//
// Why not use Routes() for these? chi's Mount("/", ...) collides with the
// existing GET /v1/ index handler, so we register the endpoints flat the
// same way AccessTokensHandler does.
func (h *MeHandler) RegisterTopLevel(r chi.Router) {
	r.Put("/update_profile_name", h.updateProfileName)
	r.Post("/delete_account", h.deleteAccount)
	r.Get("/member_data", h.memberData)
	r.Get("/optins", h.optins)
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

// ---------- PUT /v1/update_profile_name ----------
//
// Mirrors update_profile_name. Cloud spec returns 204; we return 200 + the
// updated user shape so the dashboard avoids a follow-up GET. Empty name is
// rejected (the column is NOT NULL DEFAULT ''; allowing "" would let the
// dashboard render a blank greeting which is worse than a clear 400).

type updateProfileNameReq struct {
	Name string `json:"name"`
}

func (h *MeHandler) updateProfileName(w http.ResponseWriter, r *http.Request) {
	uid, err := auth.UserID(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Not authenticated")
		return
	}
	var req updateProfileNameReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "missing_name", "Name is required")
		return
	}

	var u models.User
	err = h.DB.QueryRow(r.Context(), `
		UPDATE users SET name = $1, updated_at = now() WHERE id = $2
		RETURNING id, email, name, created_at, updated_at
	`, req.Name, uid).Scan(&u.ID, &u.Email, &u.Name, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "user_not_found", "User no longer exists")
		return
	}
	if err != nil {
		logErr("update profile name", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to update name")
		return
	}

	// No team_id: this is an account-level event, not team-scoped.
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		ActorID:    uid,
		Action:     audit.ActionUpdateProfileName,
		TargetType: audit.TargetUser,
		TargetID:   uid,
		Metadata:   map[string]any{"name": u.Name},
	})
	writeJSON(w, http.StatusOK, u)
}

// ---------- POST /v1/delete_account ----------
//
// Mirrors delete_account. Refuses if the caller is the last admin of any
// team they belong to OR the creator of any existing team — `teams.creator_user_id`
// is ON DELETE RESTRICT, so the underlying DELETE would fail anyway. We
// surface that as a precise 409 instead of a 500.
//
// Caveat: since teams.creator_user_id RESTRICTs, the operator's only path
// to deleting their account is to first delete (or transfer creation of)
// every team they bootstrapped. Cloud has the same constraint. For
// self-hosted, the simplest workaround is to delete the team(s) first via
// /v1/teams/{ref}/delete (which we now expose).

func (h *MeHandler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	uid, err := auth.UserID(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Not authenticated")
		return
	}

	// Pull the email so we can record it in the audit log before the row
	// vanishes — once the user is gone, audit_events.actor_id SET NULLs and
	// the operator loses the "who?" answer.
	var email string
	err = h.DB.QueryRow(r.Context(),
		`SELECT email FROM users WHERE id = $1`, uid).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "user_not_found", "User no longer exists")
		return
	}
	if err != nil {
		logErr("load user", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to load user")
		return
	}

	// Refuse if any team this user belongs to as admin would be left with
	// zero admins after the cascade. team_members CASCADE on user delete,
	// so we have to count BEFORE issuing DELETE.
	var orphanedTeams int
	if err := h.DB.QueryRow(r.Context(), `
		SELECT COUNT(*)
		  FROM team_members m
		 WHERE m.user_id = $1
		   AND m.role = 'admin'
		   AND (
		     SELECT COUNT(*) FROM team_members m2
		      WHERE m2.team_id = m.team_id AND m2.role = 'admin'
		   ) = 1
	`, uid).Scan(&orphanedTeams); err != nil {
		logErr("count orphaned teams", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to check team membership")
		return
	}
	if orphanedTeams > 0 {
		writeError(w, http.StatusConflict, "last_admin",
			"You are the last admin of one or more teams; promote another admin or delete the team first")
		return
	}

	// Refuse if user is `creator_user_id` of any team — RESTRICT FK would
	// surface as a generic 500 otherwise. Same workaround as above:
	// delete the team(s) first.
	var createdTeams int
	if err := h.DB.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM teams WHERE creator_user_id = $1`, uid).
		Scan(&createdTeams); err != nil {
		logErr("count created teams", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to check team ownership")
		return
	}
	if createdTeams > 0 {
		writeError(w, http.StatusConflict, "team_creator",
			"You created one or more teams; delete those teams first")
		return
	}

	// Audit BEFORE the delete so actor_id is still readable. Once the user
	// row goes away, audit_events.actor_id SET-NULLs.
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		ActorID:    uid,
		Action:     audit.ActionDeleteAccount,
		TargetType: audit.TargetUser,
		TargetID:   uid,
		Metadata:   map[string]any{"email": email},
	})

	if _, err := h.DB.Exec(r.Context(), `DELETE FROM users WHERE id = $1`, uid); err != nil {
		logErr("delete user", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to delete account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": uid, "status": "deleted"})
}

// ---------- GET /v1/member_data ----------
//
// Mirrors get_member_data. The cloud response is { teams, projects,
// deployments, optInsToAccept }. Self-hosted has no opt-ins, so optInsToAccept
// is always []. Returns the same data the dashboard would otherwise stitch
// together via three round-trips (/me + /list_teams + per-team /list_projects)
// — bookmarked endpoint for the cloud dashboard's existing client code.

type memberDataResp struct {
	Teams           []models.Team       `json:"teams"`
	Projects        []models.Project    `json:"projects"`
	Deployments     []models.Deployment `json:"deployments"`
	OptInsToAccept  []any               `json:"optInsToAccept"`
}

func (h *MeHandler) memberData(w http.ResponseWriter, r *http.Request) {
	uid, err := auth.UserID(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Not authenticated")
		return
	}

	resp := memberDataResp{
		Teams:          []models.Team{},
		Projects:       []models.Project{},
		Deployments:    []models.Deployment{},
		OptInsToAccept: []any{},
	}

	// Teams the caller belongs to.
	teamRows, err := h.DB.Query(r.Context(), `
		SELECT t.id, t.name, t.slug, t.creator_user_id, t.default_region, t.suspended, t.created_at
		  FROM teams t
		  JOIN team_members m ON m.team_id = t.id
		 WHERE m.user_id = $1
		 ORDER BY t.created_at ASC, t.id ASC
	`, uid)
	if err != nil {
		logErr("list teams for member_data", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to load teams")
		return
	}
	teamIDs := make([]string, 0)
	for teamRows.Next() {
		var t models.Team
		if err := teamRows.Scan(&t.ID, &t.Name, &t.Slug, &t.CreatorUserID,
			&t.DefaultRegion, &t.Suspended, &t.CreatedAt); err != nil {
			teamRows.Close()
			logErr("scan team", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to scan teams")
			return
		}
		resp.Teams = append(resp.Teams, t)
		teamIDs = append(teamIDs, t.ID)
	}
	teamRows.Close()

	if len(teamIDs) > 0 {
		projectRows, err := h.DB.Query(r.Context(), `
			SELECT id, team_id, name, slug, is_demo, created_at
			  FROM projects
			 WHERE team_id = ANY($1::uuid[])
			 ORDER BY created_at ASC, id ASC
		`, teamIDs)
		if err != nil {
			logErr("list projects for member_data", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to load projects")
			return
		}
		for projectRows.Next() {
			var p models.Project
			if err := projectRows.Scan(&p.ID, &p.TeamID, &p.Name, &p.Slug, &p.IsDemo, &p.CreatedAt); err != nil {
				projectRows.Close()
				logErr("scan project", err)
				writeError(w, http.StatusInternalServerError, "internal", "Failed to scan projects")
				return
			}
			resp.Projects = append(resp.Projects, p)
		}
		projectRows.Close()
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---------- GET /v1/optins ----------
//
// Mirrors dashboard_get_opt_ins. Self-hosted operators don't agree to
// Convex Cloud's TOS or marketing opt-ins — the operator owns the box.
// Always returns the empty list. The PUT counterpart (accept_opt_ins) is
// not exposed; clients receive 404 from chi's default handler when they
// try to write to a path that isn't registered, which is the right
// behaviour ("nothing to accept").
func (h *MeHandler) optins(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"optInsToAccept": []any{},
	})
}
