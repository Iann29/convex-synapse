package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Iann29/synapse/internal/audit"
	"github.com/Iann29/synapse/internal/auth"
	synapsedb "github.com/Iann29/synapse/internal/db"
	"github.com/Iann29/synapse/internal/models"
)

// TeamsHandler exposes team CRUD and member management.
//
// Deployments is set by router.go after DeploymentsHandler exists,
// solely so listDeployments can call publicDeploymentURL — the same
// rewrite logic /auth and /cli_credentials use to turn the
// container-internal "http://127.0.0.1:<port>" URL into something a
// remote browser/CLI can actually reach. Without this, the dashboard
// renders loopback URLs that nobody outside the VPS can use.
type TeamsHandler struct {
	DB          *pgxpool.Pool
	Deployments *DeploymentsHandler
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
		r.Get("/invites", h.listInvites)
		r.Post("/invites/{inviteID}/cancel", h.cancelInvite)
		r.Get("/audit_log", h.listAuditLog)
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

	// Slug allocation (SELECT-EXISTS pre-check) races with concurrent creates.
	// Wrap the SELECT-then-INSERT pair in the retry helper so two callers
	// landing on the same slug (e.g. "acme-corp") don't surface a 500 — the
	// loser regenerates and retries.
	var t models.Team
	err = synapsedb.WithRetryOnUniqueViolation(r.Context(), 10, func() error {
		slug, allocErr := h.allocateTeamSlug(r.Context(), req.Name)
		if allocErr != nil {
			return allocErr
		}
		tx, txErr := h.DB.Begin(r.Context())
		if txErr != nil {
			return txErr
		}
		defer tx.Rollback(r.Context())

		txErr = tx.QueryRow(r.Context(), `
			INSERT INTO teams (name, slug, creator_user_id, default_region)
			VALUES ($1, $2, $3, $4)
			RETURNING id, name, slug, creator_user_id, default_region, suspended, created_at
		`, req.Name, slug, uid, req.DefaultRegion).Scan(
			&t.ID, &t.Name, &t.Slug, &t.CreatorUserID, &t.DefaultRegion, &t.Suspended, &t.CreatedAt,
		)
		if txErr != nil {
			return txErr
		}
		if _, txErr = tx.Exec(r.Context(), `
			INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'admin')
		`, t.ID, uid); txErr != nil {
			return txErr
		}
		return tx.Commit(r.Context())
	})
	if err != nil {
		logErr("create team", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to create team")
		return
	}
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     t.ID,
		ActorID:    uid,
		Action:     audit.ActionCreateTeam,
		TargetType: audit.TargetTeam,
		TargetID:   t.ID,
		Metadata:   map[string]any{"name": t.Name, "slug": t.Slug},
	})
	writeJSON(w, http.StatusCreated, t)
}

// allocateTeamSlug returns a slug derived from name. The walk is "base",
// "base-1", "base-2", ... up to 8; past that we switch to random suffixes
// ("base-a3f7") to break convoy collisions when many writers race the same
// allocator. The UNIQUE index on `teams.slug` is the source of truth — this
// function only chooses a candidate, the INSERT is the commit point.
func (h *TeamsHandler) allocateTeamSlug(ctx context.Context, name string) (string, error) {
	base := slugify(name)
	for i := 0; i < 50; i++ {
		var candidate string
		switch {
		case i == 0:
			candidate = base
		case i < 8:
			candidate = withSuffix(base, i)
		default:
			candidate = withRandomSuffix(base)
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
	limit, ok := parseListLimit(w, r)
	if !ok {
		return
	}

	// Keyset pagination on (team.created_at ASC, team.id ASC). The membership
	// row's created_at is irrelevant to ordering — what we expose is "teams I
	// belong to in creation order", so the team's own timestamp anchors the
	// page boundary. Cursor is the team id from the previous page.
	cursor := r.URL.Query().Get("cursor")
	var rows pgx.Rows
	if cursor == "" {
		rows, err = h.DB.Query(r.Context(), `
			SELECT t.id, t.name, t.slug, t.creator_user_id, t.default_region, t.suspended, t.created_at
			  FROM teams t
			  JOIN team_members m ON m.team_id = t.id
			 WHERE m.user_id = $1
			 ORDER BY t.created_at ASC, t.id ASC
			 LIMIT $2
		`, uid, limit+1)
	} else {
		// Resolve cursor → (created_at, id) of the team. We require membership
		// in the lookup to avoid leaking timestamps for teams the caller can't
		// see — same defence as the PAT cursor.
		var cursorAt time.Time
		err = h.DB.QueryRow(r.Context(), `
			SELECT t.created_at
			  FROM teams t
			  JOIN team_members m ON m.team_id = t.id
			 WHERE t.id::text = $1 AND m.user_id = $2
		`, cursor, uid).Scan(&cursorAt)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "Cursor does not refer to a team you belong to")
			return
		}
		if err != nil {
			logErr("resolve teams cursor", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to resolve cursor")
			return
		}
		rows, err = h.DB.Query(r.Context(), `
			SELECT t.id, t.name, t.slug, t.creator_user_id, t.default_region, t.suspended, t.created_at
			  FROM teams t
			  JOIN team_members m ON m.team_id = t.id
			 WHERE m.user_id = $1
			   AND (t.created_at, t.id) > ($2, $3)
			 ORDER BY t.created_at ASC, t.id ASC
			 LIMIT $4
		`, uid, cursorAt, cursor, limit+1)
	}
	if err != nil {
		logErr("list teams", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list teams")
		return
	}
	defer rows.Close()

	teams := make([]models.Team, 0, limit)
	for rows.Next() {
		var t models.Team
		if err := rows.Scan(&t.ID, &t.Name, &t.Slug, &t.CreatorUserID, &t.DefaultRegion, &t.Suspended, &t.CreatedAt); err != nil {
			logErr("scan team", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to scan teams")
			return
		}
		teams = append(teams, t)
	}
	if err := rows.Err(); err != nil {
		logErr("iterate teams", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to read teams")
		return
	}
	if len(teams) > limit {
		setNextCursor(w, teams[limit-1].ID)
		teams = teams[:limit]
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
	limit, ok := parseListLimit(w, r)
	if !ok {
		return
	}

	cursor := r.URL.Query().Get("cursor")
	var rows pgx.Rows
	var err error
	if cursor == "" {
		rows, err = h.DB.Query(r.Context(), `
			SELECT id, team_id, name, slug, is_demo, created_at
			  FROM projects
			 WHERE team_id = $1
			 ORDER BY created_at ASC, id ASC
			 LIMIT $2
		`, t.ID, limit+1)
	} else {
		var cursorAt time.Time
		err = h.DB.QueryRow(r.Context(),
			`SELECT created_at FROM projects WHERE id::text = $1 AND team_id = $2`,
			cursor, t.ID).Scan(&cursorAt)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "Cursor does not refer to a project in this team")
			return
		}
		if err != nil {
			logErr("resolve projects cursor", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to resolve cursor")
			return
		}
		rows, err = h.DB.Query(r.Context(), `
			SELECT id, team_id, name, slug, is_demo, created_at
			  FROM projects
			 WHERE team_id = $1
			   AND (created_at, id) > ($2, $3)
			 ORDER BY created_at ASC, id ASC
			 LIMIT $4
		`, t.ID, cursorAt, cursor, limit+1)
	}
	if err != nil {
		logErr("list projects", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list projects")
		return
	}
	defer rows.Close()

	projects := make([]models.Project, 0, limit)
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
	if err := rows.Err(); err != nil {
		logErr("iterate projects", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to read projects")
		return
	}
	if len(projects) > limit {
		setNextCursor(w, projects[limit-1].ID)
		projects = projects[:limit]
	}
	writeJSON(w, http.StatusOK, projects)
}

// ---------- GET /v1/teams/{teamRef}/list_members ----------

func (h *TeamsHandler) listMembers(w http.ResponseWriter, r *http.Request) {
	t, _, ok := h.loadTeamForRequest(w, r)
	if !ok {
		return
	}
	limit, ok := parseListLimit(w, r)
	if !ok {
		return
	}

	// Cursor here is the member's user_id. Membership rows are unique on
	// (team_id, user_id), so user_id alone disambiguates the position when
	// paired with the membership's created_at.
	cursor := r.URL.Query().Get("cursor")
	var rows pgx.Rows
	var err error
	if cursor == "" {
		rows, err = h.DB.Query(r.Context(), `
			SELECT u.id, u.email, u.name, m.role, m.created_at
			  FROM team_members m
			  JOIN users u ON u.id = m.user_id
			 WHERE m.team_id = $1
			 ORDER BY m.created_at ASC, u.id ASC
			 LIMIT $2
		`, t.ID, limit+1)
	} else {
		var cursorAt time.Time
		err = h.DB.QueryRow(r.Context(),
			`SELECT created_at FROM team_members WHERE team_id = $1 AND user_id::text = $2`,
			t.ID, cursor).Scan(&cursorAt)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "Cursor does not refer to a member of this team")
			return
		}
		if err != nil {
			logErr("resolve members cursor", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to resolve cursor")
			return
		}
		rows, err = h.DB.Query(r.Context(), `
			SELECT u.id, u.email, u.name, m.role, m.created_at
			  FROM team_members m
			  JOIN users u ON u.id = m.user_id
			 WHERE m.team_id = $1
			   AND (m.created_at, u.id) > ($2, $3)
			 ORDER BY m.created_at ASC, u.id ASC
			 LIMIT $4
		`, t.ID, cursorAt, cursor, limit+1)
	}
	if err != nil {
		logErr("list members", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list members")
		return
	}
	defer rows.Close()

	members := make([]models.TeamMember, 0, limit)
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
	if err := rows.Err(); err != nil {
		logErr("iterate members", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to read members")
		return
	}
	if len(members) > limit {
		setNextCursor(w, members[limit-1].UserID)
		members = members[:limit]
	}
	writeJSON(w, http.StatusOK, members)
}

// ---------- GET /v1/teams/{teamRef}/list_deployments ----------

func (h *TeamsHandler) listDeployments(w http.ResponseWriter, r *http.Request) {
	t, _, ok := h.loadTeamForRequest(w, r)
	if !ok {
		return
	}
	limit, ok := parseListLimit(w, r)
	if !ok {
		return
	}

	cursor := r.URL.Query().Get("cursor")
	var rows pgx.Rows
	var err error
	if cursor == "" {
		rows, err = h.DB.Query(r.Context(), `
			SELECT d.id, d.project_id, d.name, d.deployment_type, d.status,
			       d.deployment_url, d.is_default, d.reference, d.creator_user_id, d.created_at,
			       d.adopted
			  FROM deployments d
			  JOIN projects p ON p.id = d.project_id
			 WHERE p.team_id = $1
			   AND d.status <> 'deleted'
			 ORDER BY d.created_at ASC, d.id ASC
			 LIMIT $2
		`, t.ID, limit+1)
	} else {
		// Cursor must refer to a non-deleted deployment in this team — keeps
		// callers from probing other teams' deployment timestamps.
		var cursorAt time.Time
		err = h.DB.QueryRow(r.Context(), `
			SELECT d.created_at
			  FROM deployments d
			  JOIN projects p ON p.id = d.project_id
			 WHERE d.id::text = $1 AND p.team_id = $2 AND d.status <> 'deleted'
		`, cursor, t.ID).Scan(&cursorAt)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "Cursor does not refer to a deployment in this team")
			return
		}
		if err != nil {
			logErr("resolve deployments cursor", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to resolve cursor")
			return
		}
		rows, err = h.DB.Query(r.Context(), `
			SELECT d.id, d.project_id, d.name, d.deployment_type, d.status,
			       d.deployment_url, d.is_default, d.reference, d.creator_user_id, d.created_at,
			       d.adopted
			  FROM deployments d
			  JOIN projects p ON p.id = d.project_id
			 WHERE p.team_id = $1
			   AND d.status <> 'deleted'
			   AND (d.created_at, d.id) > ($2, $3)
			 ORDER BY d.created_at ASC, d.id ASC
			 LIMIT $4
		`, t.ID, cursorAt, cursor, limit+1)
	}
	if err != nil {
		logErr("list deployments", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list deployments")
		return
	}
	defer rows.Close()

	deployments := make([]models.Deployment, 0, limit)
	for rows.Next() {
		var d models.Deployment
		var url, ref, creator *string
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.Name, &d.DeploymentType, &d.Status,
			&url, &d.IsDefault, &ref, &creator, &d.CreatedAt, &d.Adopted); err != nil {
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
		// Same rewrite the create/get handlers apply — turn the
		// container-internal "http://127.0.0.1:<port>" into something
		// the dashboard's browser can hit.
		if h.Deployments != nil {
			d.DeploymentURL = h.Deployments.publicDeploymentURL(&d)
		}
		deployments = append(deployments, d)
	}
	if err := rows.Err(); err != nil {
		logErr("iterate deployments", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to read deployments")
		return
	}
	if len(deployments) > limit {
		setNextCursor(w, deployments[limit-1].ID)
		deployments = deployments[:limit]
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

	// Project slug uniqueness is enforced by `UNIQUE (team_id, slug)`. Two
	// concurrent creates of "My App" within the same team race the
	// SELECT-EXISTS pre-check; the loser hits the constraint and retries.
	var p models.Project
	err := synapsedb.WithRetryOnUniqueViolation(r.Context(), 10, func() error {
		slug, allocErr := h.allocateProjectSlug(r.Context(), t.ID, req.ProjectName)
		if allocErr != nil {
			return allocErr
		}
		return h.DB.QueryRow(r.Context(), `
			INSERT INTO projects (team_id, name, slug)
			VALUES ($1, $2, $3)
			RETURNING id, team_id, name, slug, is_demo, created_at
		`, t.ID, req.ProjectName, slug).Scan(&p.ID, &p.TeamID, &p.Name, &p.Slug, &p.IsDemo, &p.CreatedAt)
	})
	if err != nil {
		logErr("create project", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to create project")
		return
	}
	p.TeamSlug = t.Slug

	uid, _ := auth.UserID(r.Context())
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     t.ID,
		ActorID:    uid,
		Action:     audit.ActionCreateProject,
		TargetType: audit.TargetProject,
		TargetID:   p.ID,
		Metadata:   map[string]any{"name": p.Name, "slug": p.Slug},
	})
	writeJSON(w, http.StatusCreated, createProjectResp{
		ProjectID:   p.ID,
		ProjectSlug: p.Slug,
		Project:     p,
	})
}

func (h *TeamsHandler) allocateProjectSlug(ctx context.Context, teamID, name string) (string, error) {
	base := slugify(name)
	for i := 0; i < 50; i++ {
		var candidate string
		switch {
		case i == 0:
			candidate = base
		case i < 8:
			candidate = withSuffix(base, i)
		default:
			candidate = withRandomSuffix(base)
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

	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     t.ID,
		ActorID:    uid,
		Action:     audit.ActionInviteTeamMember,
		TargetType: audit.TargetInvite,
		TargetID:   inviteID,
		Metadata:   map[string]any{"email": req.Email, "role": req.Role},
	})
	writeJSON(w, http.StatusOK, map[string]string{
		"inviteId":    inviteID,
		"email":       req.Email,
		"role":        req.Role,
		"inviteToken": plain,
	})
}

// ---------- GET /v1/teams/{teamRef}/invites ----------
//
// Lists pending invites for the team. Admin-only — invite tokens are
// privileged data: anyone holding a token becomes a member.

type pendingInvite struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Token     string    `json:"token"`
	InvitedBy string    `json:"invitedBy"`
	CreatedAt time.Time `json:"createTime"`
}

func (h *TeamsHandler) listInvites(w http.ResponseWriter, r *http.Request) {
	t, role, ok := h.loadTeamForRequest(w, r)
	if !ok {
		return
	}
	if role != models.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "Only team admins can list invites")
		return
	}
	rows, err := h.DB.Query(r.Context(), `
		SELECT id, email, role, token, invited_by, created_at
		  FROM team_invites
		 WHERE team_id = $1 AND accepted_at IS NULL
		 ORDER BY created_at DESC
	`, t.ID)
	if err != nil {
		logErr("list invites", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list invites")
		return
	}
	defer rows.Close()

	out := make([]pendingInvite, 0)
	for rows.Next() {
		var inv pendingInvite
		if err := rows.Scan(&inv.ID, &inv.Email, &inv.Role, &inv.Token, &inv.InvitedBy, &inv.CreatedAt); err != nil {
			logErr("scan invite", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to scan invites")
			return
		}
		out = append(out, inv)
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------- POST /v1/teams/{teamRef}/invites/{inviteID}/cancel ----------

func (h *TeamsHandler) cancelInvite(w http.ResponseWriter, r *http.Request) {
	t, role, ok := h.loadTeamForRequest(w, r)
	if !ok {
		return
	}
	if role != models.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "Only team admins can cancel invites")
		return
	}
	id := chi.URLParam(r, "inviteID")
	tag, err := h.DB.Exec(r.Context(),
		`DELETE FROM team_invites WHERE id::text = $1 AND team_id = $2 AND accepted_at IS NULL`,
		id, t.ID)
	if err != nil {
		logErr("cancel invite", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to cancel invite")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "invite_not_found", "Invite not found or already accepted")
		return
	}
	uid, _ := auth.UserID(r.Context())
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     t.ID,
		ActorID:    uid,
		Action:     audit.ActionCancelInvite,
		TargetType: audit.TargetInvite,
		TargetID:   id,
	})
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "cancelled"})
}
