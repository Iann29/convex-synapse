package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Iann29/synapse/internal/auth"
	"github.com/Iann29/synapse/internal/models"
)

// TeamsHandler exposes team CRUD and member management.
type TeamsHandler struct {
	DB *pgxpool.Pool
}

func (h *TeamsHandler) Routes() chi.Router {
	r := chi.NewRouter()

	// Custom convenience endpoint — list teams the caller belongs to.
	// Mirrors the cloud dashboard's /api/dashboard/teams.
	r.Get("/", h.listMyTeams)

	// Standard v1 endpoints.
	r.Post("/create_team", h.createTeam)

	r.Route("/{teamRef}", func(r chi.Router) {
		r.Get("/", h.getTeam)
		r.Get("/list_projects", h.listProjects)
		r.Get("/list_members", h.listMembers)
		r.Get("/list_deployments", h.listDeployments)
		r.Post("/invite_team_member", h.inviteMember)
		r.Post("/create_project", h.createProject)
	})

	return r
}

// ---------- POST /v1/teams/create_team ----------

type createTeamReq struct {
	Name          string `json:"name"`
	DefaultRegion string `json:"defaultRegion,omitempty"`
}

func (h *TeamsHandler) createTeam(w http.ResponseWriter, r *http.Request) {
	uid, err := auth.UserID(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Not authenticated")
		return
	}
	var req createTeamReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "missing_name", "Team name is required")
		return
	}
	if req.DefaultRegion == "" {
		req.DefaultRegion = "self-hosted"
	}

	slug, err := h.allocateTeamSlug(r.Context(), req.Name)
	if err != nil {
		logErr("alloc team slug", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to allocate team slug")
		return
	}

	tx, err := h.DB.Begin(r.Context())
	if err != nil {
		logErr("tx begin", err)
		writeError(w, http.StatusInternalServerError, "internal", "Database error")
		return
	}
	defer tx.Rollback(r.Context())

	var t models.Team
	err = tx.QueryRow(r.Context(), `
		INSERT INTO teams (name, slug, creator_user_id, default_region)
		VALUES ($1, $2, $3, $4)
		RETURNING id, name, slug, creator_user_id, default_region, suspended, created_at
	`, req.Name, slug, uid, req.DefaultRegion).Scan(
		&t.ID, &t.Name, &t.Slug, &t.CreatorUserID, &t.DefaultRegion, &t.Suspended, &t.CreatedAt,
	)
	if err != nil {
		logErr("insert team", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to create team")
		return
	}

	_, err = tx.Exec(r.Context(), `
		INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'admin')
	`, t.ID, uid)
	if err != nil {
		logErr("insert team member", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to add team member")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		logErr("tx commit", err)
		writeError(w, http.StatusInternalServerError, "internal", "Database error")
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// allocateTeamSlug returns a slug derived from name, appending a numeric
// suffix if needed. Races with concurrent inserts are caught by the unique
// index — caller should treat duplicate-key errors as a retry signal.
func (h *TeamsHandler) allocateTeamSlug(ctx context.Context, name string) (string, error) {
	base := slugify(name)
	for i := 0; i < 50; i++ {
		candidate := base
		if i > 0 {
			candidate = withSuffix(base, i)
		}
		var exists bool
		if err := h.DB.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM teams WHERE slug = $1)`,
			candidate).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", errors.New("could not allocate slug after 50 attempts")
}

// ---------- GET /v1/teams ----------

func (h *TeamsHandler) listMyTeams(w http.ResponseWriter, r *http.Request) {
	uid, err := auth.UserID(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Not authenticated")
		return
	}
	rows, err := h.DB.Query(r.Context(), `
		SELECT t.id, t.name, t.slug, t.creator_user_id, t.default_region, t.suspended, t.created_at
		  FROM teams t
		  JOIN team_members m ON m.team_id = t.id
		 WHERE m.user_id = $1
		 ORDER BY t.created_at ASC
	`, uid)
	if err != nil {
		logErr("list teams", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list teams")
		return
	}
	defer rows.Close()

	teams := make([]models.Team, 0)
	for rows.Next() {
		var t models.Team
		if err := rows.Scan(&t.ID, &t.Name, &t.Slug, &t.CreatorUserID, &t.DefaultRegion, &t.Suspended, &t.CreatedAt); err != nil {
			logErr("scan team", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to scan teams")
			return
		}
		teams = append(teams, t)
	}
	writeJSON(w, http.StatusOK, teams)
}

// ---------- helpers: resolveTeam + assertMember ----------

// resolveTeam looks up a team by id (UUID) or slug.
func (h *TeamsHandler) resolveTeam(ctx context.Context, ref string) (*models.Team, error) {
	var t models.Team
	err := h.DB.QueryRow(ctx, `
		SELECT id, name, slug, creator_user_id, default_region, suspended, created_at
		  FROM teams
		 WHERE id::text = $1 OR slug = $1
	`, ref).Scan(&t.ID, &t.Name, &t.Slug, &t.CreatorUserID, &t.DefaultRegion, &t.Suspended, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// assertMember returns the member's role, or an error if they are not in the team.
func (h *TeamsHandler) assertMember(ctx context.Context, teamID, userID string) (string, error) {
	var role string
	err := h.DB.QueryRow(ctx,
		`SELECT role FROM team_members WHERE team_id = $1 AND user_id = $2`,
		teamID, userID).Scan(&role)
	return role, err
}

// loadTeamForRequest is the common header for endpoints under /v1/teams/{teamRef}/...
// It resolves the team from the URL parameter, asserts the caller is a member,
// and returns the team plus the caller's role. On error it has already written
// the response — the handler should just return.
func (h *TeamsHandler) loadTeamForRequest(w http.ResponseWriter, r *http.Request) (*models.Team, string, bool) {
	uid, err := auth.UserID(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Not authenticated")
		return nil, "", false
	}
	ref := chi.URLParam(r, "teamRef")
	t, err := h.resolveTeam(r.Context(), ref)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "team_not_found", "Team not found")
		return nil, "", false
	}
	if err != nil {
		logErr("resolve team", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to load team")
		return nil, "", false
	}
	role, err := h.assertMember(r.Context(), t.ID, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusForbidden, "forbidden", "You are not a member of this team")
		return nil, "", false
	}
	if err != nil {
		logErr("assert member", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to check membership")
		return nil, "", false
	}
	return t, role, true
}

// ---------- GET /v1/teams/{teamRef} ----------

func (h *TeamsHandler) getTeam(w http.ResponseWriter, r *http.Request) {
	t, _, ok := h.loadTeamForRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// ---------- GET /v1/teams/{teamRef}/list_projects ----------

func (h *TeamsHandler) listProjects(w http.ResponseWriter, r *http.Request) {
	t, _, ok := h.loadTeamForRequest(w, r)
	if !ok {
		return
	}
	rows, err := h.DB.Query(r.Context(), `
		SELECT id, team_id, name, slug, is_demo, created_at
		  FROM projects
		 WHERE team_id = $1
		 ORDER BY created_at ASC
	`, t.ID)
	if err != nil {
		logErr("list projects", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list projects")
		return
	}
	defer rows.Close()

	projects := make([]models.Project, 0)
	for rows.Next() {
		var p models.Project
		if err := rows.Scan(&p.ID, &p.TeamID, &p.Name, &p.Slug, &p.IsDemo, &p.CreatedAt); err != nil {
			logErr("scan project", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to scan projects")
			return
		}
		p.TeamSlug = t.Slug
		projects = append(projects, p)
	}
	writeJSON(w, http.StatusOK, projects)
}

// ---------- GET /v1/teams/{teamRef}/list_members ----------

func (h *TeamsHandler) listMembers(w http.ResponseWriter, r *http.Request) {
	t, _, ok := h.loadTeamForRequest(w, r)
	if !ok {
		return
	}
	rows, err := h.DB.Query(r.Context(), `
		SELECT u.id, u.email, u.name, m.role, m.created_at
		  FROM team_members m
		  JOIN users u ON u.id = m.user_id
		 WHERE m.team_id = $1
		 ORDER BY m.created_at ASC
	`, t.ID)
	if err != nil {
		logErr("list members", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list members")
		return
	}
	defer rows.Close()

	members := make([]models.TeamMember, 0)
	for rows.Next() {
		var m models.TeamMember
		if err := rows.Scan(&m.UserID, &m.Email, &m.Name, &m.Role, &m.CreatedAt); err != nil {
			logErr("scan member", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to scan members")
			return
		}
		m.TeamID = t.ID
		members = append(members, m)
	}
	writeJSON(w, http.StatusOK, members)
}

// ---------- GET /v1/teams/{teamRef}/list_deployments ----------

func (h *TeamsHandler) listDeployments(w http.ResponseWriter, r *http.Request) {
	t, _, ok := h.loadTeamForRequest(w, r)
	if !ok {
		return
	}
	rows, err := h.DB.Query(r.Context(), `
		SELECT d.id, d.project_id, d.name, d.deployment_type, d.status,
		       d.deployment_url, d.is_default, d.reference, d.creator_user_id, d.created_at
		  FROM deployments d
		  JOIN projects p ON p.id = d.project_id
		 WHERE p.team_id = $1
		   AND d.status <> 'deleted'
		 ORDER BY d.created_at ASC
	`, t.ID)
	if err != nil {
		logErr("list deployments", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list deployments")
		return
	}
	defer rows.Close()

	deployments := make([]models.Deployment, 0)
	for rows.Next() {
		var d models.Deployment
		var url, ref, creator *string
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.Name, &d.DeploymentType, &d.Status,
			&url, &d.IsDefault, &ref, &creator, &d.CreatedAt); err != nil {
			logErr("scan deployment", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to scan deployments")
			return
		}
		if url != nil {
			d.DeploymentURL = *url
		}
		if ref != nil {
			d.Reference = *ref
		}
		if creator != nil {
			d.CreatorUserID = *creator
		}
		deployments = append(deployments, d)
	}
	writeJSON(w, http.StatusOK, deployments)
}

// ---------- POST /v1/teams/{teamRef}/create_project ----------

type createProjectReq struct {
	ProjectName      string `json:"projectName"`
	DeploymentType   string `json:"deploymentType,omitempty"`
	DeploymentClass  string `json:"deploymentClass,omitempty"`
	DeploymentRegion string `json:"deploymentRegion,omitempty"`
}

type createProjectResp struct {
	ProjectID   string         `json:"projectId"`
	ProjectSlug string         `json:"projectSlug"`
	Project     models.Project `json:"project"`
}

func (h *TeamsHandler) createProject(w http.ResponseWriter, r *http.Request) {
	t, _, ok := h.loadTeamForRequest(w, r)
	if !ok {
		return
	}
	var req createProjectReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.ProjectName = strings.TrimSpace(req.ProjectName)
	if req.ProjectName == "" {
		writeError(w, http.StatusBadRequest, "missing_name", "Project name is required")
		return
	}

	slug, err := h.allocateProjectSlug(r.Context(), t.ID, req.ProjectName)
	if err != nil {
		logErr("alloc project slug", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to allocate project slug")
		return
	}

	var p models.Project
	err = h.DB.QueryRow(r.Context(), `
		INSERT INTO projects (team_id, name, slug)
		VALUES ($1, $2, $3)
		RETURNING id, team_id, name, slug, is_demo, created_at
	`, t.ID, req.ProjectName, slug).Scan(&p.ID, &p.TeamID, &p.Name, &p.Slug, &p.IsDemo, &p.CreatedAt)
	if err != nil {
		logErr("insert project", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to create project")
		return
	}
	p.TeamSlug = t.Slug

	writeJSON(w, http.StatusCreated, createProjectResp{
		ProjectID:   p.ID,
		ProjectSlug: p.Slug,
		Project:     p,
	})
}

func (h *TeamsHandler) allocateProjectSlug(ctx context.Context, teamID, name string) (string, error) {
	base := slugify(name)
	for i := 0; i < 50; i++ {
		candidate := base
		if i > 0 {
			candidate = withSuffix(base, i)
		}
		var exists bool
		if err := h.DB.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM projects WHERE team_id = $1 AND slug = $2)`,
			teamID, candidate).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", errors.New("could not allocate slug after 50 attempts")
}

// ---------- POST /v1/teams/{teamRef}/invite_team_member ----------

type inviteReq struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (h *TeamsHandler) inviteMember(w http.ResponseWriter, r *http.Request) {
	t, role, ok := h.loadTeamForRequest(w, r)
	if !ok {
		return
	}
	if role != models.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "Only team admins can invite members")
		return
	}
	var req inviteReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		writeError(w, http.StatusBadRequest, "invalid_email", "A valid email is required")
		return
	}
	if req.Role != models.RoleAdmin && req.Role != models.RoleMember {
		req.Role = models.RoleMember
	}

	uid, _ := auth.UserID(r.Context())
	plain, hash, err := auth.GenerateToken()
	if err != nil {
		logErr("gen invite token", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to create invite")
		return
	}
	_ = hash // we store the plain token in v0 (it's already random + scoped).
	// Storing hashes here would block looking up the invite by URL token without
	// extra design work; revisit when invites grow features.

	var inviteID string
	err = h.DB.QueryRow(r.Context(), `
		INSERT INTO team_invites (team_id, email, role, invited_by, token)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (team_id, email) DO UPDATE
		   SET role = EXCLUDED.role,
		       token = EXCLUDED.token,
		       invited_by = EXCLUDED.invited_by,
		       accepted_at = NULL
		RETURNING id
	`, t.ID, req.Email, req.Role, uid, plain).Scan(&inviteID)
	if err != nil {
		logErr("create invite", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to create invite")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"inviteId":    inviteID,
		"email":       req.Email,
		"role":        req.Role,
		"inviteToken": plain,
	})
}
