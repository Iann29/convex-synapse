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

// ProjectsHandler exposes project CRUD and env-var management.
//
// Routes are split between two mount points to mirror the OpenAPI v1 spec:
//   - team-scoped (/v1/teams/{teamRef}/create_project) lives in TeamsHandler
//     to keep the URL hierarchy intact
//   - project-scoped (/v1/projects/{id}/...) lives here
type ProjectsHandler struct {
	DB          *pgxpool.Pool
	Deployments *DeploymentsHandler
	Tokens      *AccessTokensHandler
}

// canAdminProject is true for full administrators of a project.
// Mutations that destroy or rename the project itself, or that touch
// the membership list, gate on this. Intentionally narrow — a v1.0+
// project member with role="member" can edit env vars and create
// deployments but can't delete the project.
func canAdminProject(role string) bool {
	return role == models.RoleAdmin
}

// canEditProject is the broader gate for non-destructive writes:
// updating env vars, creating deployments, etc. Members + admins
// pass; viewers don't.
func canEditProject(role string) bool {
	return role == models.RoleAdmin || role == models.RoleMember
}

// effectiveProjectRole resolves the role a user has on a specific
// project. project_members rows override team_members; absence falls
// through to team. If neither row exists, returns pgx.ErrNoRows.
//
// Used by loadProjectForRequest + loadDeploymentForRequest to compute
// the role passed to the handler. New writers should NOT touch the
// raw `team_members` SELECT — go through this helper so per-project
// overrides are honoured everywhere.
func effectiveProjectRole(ctx context.Context, db *pgxpool.Pool, projectID, teamID, userID string) (string, error) {
	var role string
	err := db.QueryRow(ctx,
		`SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID).Scan(&role)
	if err == nil {
		return role, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	err = db.QueryRow(ctx,
		`SELECT role FROM team_members WHERE team_id = $1 AND user_id = $2`,
		teamID, userID).Scan(&role)
	return role, err
}

func (h *ProjectsHandler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Route("/{projectID}", func(r chi.Router) {
		r.Get("/", h.getProject)
		r.Put("/", h.updateProject)
		r.Post("/delete", h.deleteProject)
		r.Post("/transfer", h.transferProject)
		r.Get("/list_deployments", h.listDeployments)
		r.Get("/list_default_environment_variables", h.listEnvVars)
		r.Post("/update_default_environment_variables", h.updateEnvVars)
		// Project-level RBAC (v1.0+, migration 000008).
		r.Get("/list_members", h.listProjectMembers)
		r.Post("/add_member", h.addProjectMember)
		r.Post("/update_member_role", h.updateProjectMemberRole)
		r.Post("/remove_member", h.removeProjectMember)
		// Scoped access tokens (v1.0+). Project-scoped tokens carry
		// scope=project; the cloud-spec separates "app" tokens
		// (preview-deploy keys) from regular project tokens — we expose
		// both endpoints, scope-tagged differently so the dashboard can
		// render two lists.
		r.Post("/access_tokens", h.createProjectAccessToken)
		r.Get("/access_tokens", h.listProjectAccessTokens)
		r.Post("/app_access_tokens", h.createAppAccessToken)
		r.Get("/app_access_tokens", h.listAppAccessTokens)
		if h.Deployments != nil {
			h.Deployments.MountProjectScopedRoutes(r)
		}
	})

	return r
}

// loadProjectForRequest resolves the project from the URL parameter, asserts
// the caller is a member of the owning team, and returns the project + team.
func (h *ProjectsHandler) loadProjectForRequest(w http.ResponseWriter, r *http.Request) (*models.Project, *models.Team, string, bool) {
	uid, err := auth.UserID(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Not authenticated")
		return nil, nil, "", false
	}
	id := chi.URLParam(r, "projectID")
	var p models.Project
	var t models.Team
	err = h.DB.QueryRow(r.Context(), `
		SELECT p.id, p.team_id, p.name, p.slug, p.is_demo, p.created_at,
		       t.id, t.name, t.slug, t.creator_user_id, t.default_region, t.suspended, t.created_at
		  FROM projects p
		  JOIN teams t ON t.id = p.team_id
		 WHERE p.id::text = $1
	`, id).Scan(
		&p.ID, &p.TeamID, &p.Name, &p.Slug, &p.IsDemo, &p.CreatedAt,
		&t.ID, &t.Name, &t.Slug, &t.CreatorUserID, &t.DefaultRegion, &t.Suspended, &t.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project_not_found", "Project not found")
		return nil, nil, "", false
	}
	if err != nil {
		logErr("resolve project", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to load project")
		return nil, nil, "", false
	}
	p.TeamSlug = t.Slug

	role, err := effectiveProjectRole(r.Context(), h.DB, p.ID, t.ID, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusForbidden, "forbidden", "You do not have access to this project")
		return nil, nil, "", false
	}
	if err != nil {
		logErr("check membership", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to verify access")
		return nil, nil, "", false
	}
	if !enforceProjectAccess(w, r.Context(), p.ID, t.ID) {
		return nil, nil, "", false
	}
	return &p, &t, role, true
}

// ---------- GET /v1/projects/{id} ----------

func (h *ProjectsHandler) getProject(w http.ResponseWriter, r *http.Request) {
	p, _, _, ok := h.loadProjectForRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// ---------- PUT /v1/projects/{id} ----------
//
// Mirrors Convex Cloud's update_project. Both `name` and `slug` are
// optional (UpdateProjectArgs in the OpenAPI spec). The cloud spec
// returns 204 No Content; we return 200 + the updated project for
// dashboard convenience — a no-op no-args call is therefore a cheap
// "echo back the current state".
//
// Slug uniqueness is per-team (UNIQUE(team_id, slug)). The
// SELECT-then-UPDATE shape races concurrent renames; we lean on the
// constraint and surface its violation as a structured 409 slug_taken.
// We don't auto-pick a free slug here — a user-supplied slug is
// authoritative; mangling it silently ("blog" → "blog-1") would
// surprise the operator.

type updateProjectReq struct {
	Name *string `json:"name,omitempty"`
	Slug *string `json:"slug,omitempty"`
}

func (h *ProjectsHandler) updateProject(w http.ResponseWriter, r *http.Request) {
	p, t, role, ok := h.loadProjectForRequest(w, r)
	if !ok {
		return
	}
	if !canAdminProject(role) {
		writeError(w, http.StatusForbidden, "forbidden", "Only project admins can update the project")
		return
	}
	var req updateProjectReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	oldName := p.Name
	oldSlug := p.Slug
	var newName, newSlug *string

	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			writeError(w, http.StatusBadRequest, "missing_name", "Project name is required")
			return
		}
		newName = &trimmed
	}
	if req.Slug != nil {
		trimmed := strings.TrimSpace(*req.Slug)
		if trimmed == "" {
			writeError(w, http.StatusBadRequest, "missing_slug", "Project slug is required")
			return
		}
		// Cloud's slug shape: lowercase letters, digits, dashes. Be liberal
		// and accept what slugify() would produce so callers can pre-render
		// before sending. Reject anything else loudly.
		if !isValidSlug(trimmed) {
			writeError(w, http.StatusBadRequest, "invalid_slug",
				"slug must contain only lowercase letters, digits, and dashes")
			return
		}
		newSlug = &trimmed
	}

	if newName == nil && newSlug == nil {
		// No-op call (Cloud allows empty body). Return the current state so
		// callers can use this endpoint as a "fetch then echo" probe.
		writeJSON(w, http.StatusOK, p)
		return
	}

	// Build a single UPDATE that touches only the supplied fields. Either
	// COALESCE (with NULL meaning "keep existing") or a small builder works;
	// COALESCE keeps the SQL stable and the parameter shape predictable.
	tag, err := h.DB.Exec(r.Context(), `
		UPDATE projects
		   SET name = COALESCE($1, name),
		       slug = COALESCE($2, slug)
		 WHERE id = $3
	`, sqlNullableString(newName), sqlNullableString(newSlug), p.ID)
	if err != nil {
		if synapsedb.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "slug_taken",
				"A project with this slug already exists in the team")
			return
		}
		logErr("update project", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to update project")
		return
	}
	if tag.RowsAffected() == 0 {
		// Should not happen — loadProjectForRequest already proved the row
		// exists. Defensive: surface as 404 rather than a confusing 200 with
		// stale state.
		writeError(w, http.StatusNotFound, "project_not_found", "Project not found")
		return
	}

	if newName != nil {
		p.Name = *newName
	}
	if newSlug != nil {
		p.Slug = *newSlug
	}

	uid, _ := auth.UserID(r.Context())
	// Keep the legacy renameProject action when the change is name-only —
	// existing audit-log dashboards/queries already filter on that string.
	// Slug-touching updates use the more general updateProject vocabulary.
	action := audit.ActionUpdateProject
	if newSlug == nil && newName != nil {
		action = audit.ActionRenameProject
	}
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     t.ID,
		ActorID:    uid,
		Action:     action,
		TargetType: audit.TargetProject,
		TargetID:   p.ID,
		Metadata: map[string]any{
			"oldName": oldName,
			"newName": p.Name,
			"oldSlug": oldSlug,
			"newSlug": p.Slug,
		},
	})
	writeJSON(w, http.StatusOK, p)
}

// ---------- POST /v1/projects/{id}/access_tokens ----------
//
// Project-scoped tokens (and the "app" variant below) require team-admin
// role to create. The created token's scope_id is the project id; the
// auth middleware's load*ForRequest helpers verify that the bearer can
// actually reach the project at request time.

func (h *ProjectsHandler) createProjectAccessToken(w http.ResponseWriter, r *http.Request) {
	h.createProjectScopedToken(w, r, models.TokenScopeProject)
}

func (h *ProjectsHandler) createAppAccessToken(w http.ResponseWriter, r *http.Request) {
	h.createProjectScopedToken(w, r, models.TokenScopeApp)
}

func (h *ProjectsHandler) createProjectScopedToken(w http.ResponseWriter, r *http.Request, scope string) {
	p, _, role, ok := h.loadProjectForRequest(w, r)
	if !ok {
		return
	}
	if !canAdminProject(role) {
		writeError(w, http.StatusForbidden, "forbidden",
			"Only project admins can create project access tokens")
		return
	}
	if h.Tokens == nil {
		writeError(w, http.StatusInternalServerError, "internal", "Tokens handler not wired")
		return
	}
	uid, _ := auth.UserID(r.Context())
	var req createScopedTokenReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	view, plain, ok := h.Tokens.createForOwner(w, r, uid, req.Name, scope, p.ID, req.ExpiresAt)
	if !ok {
		return
	}
	writeJSON(w, http.StatusCreated, createTokenResp{Token: plain, AccessToken: view})
}

func (h *ProjectsHandler) listProjectAccessTokens(w http.ResponseWriter, r *http.Request) {
	h.listProjectScopedTokens(w, r, models.TokenScopeProject)
}

func (h *ProjectsHandler) listAppAccessTokens(w http.ResponseWriter, r *http.Request) {
	h.listProjectScopedTokens(w, r, models.TokenScopeApp)
}

func (h *ProjectsHandler) listProjectScopedTokens(w http.ResponseWriter, r *http.Request, scope string) {
	p, _, _, ok := h.loadProjectForRequest(w, r)
	if !ok {
		return
	}
	if h.Tokens == nil {
		writeError(w, http.StatusInternalServerError, "internal", "Tokens handler not wired")
		return
	}
	uid, _ := auth.UserID(r.Context())
	limit, ok := parseTokenListLimit(w, r)
	if !ok {
		return
	}
	resp, ok := h.Tokens.listForOwner(w, r, uid, scope, p.ID, limit, r.URL.Query().Get("cursor"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// sqlNullableString turns a *string into the value pgx will treat as NULL
// when nil. Helper kept private — only used by COALESCE-style updates that
// need three-valued logic ("not present in JSON" ≠ "set to ''").
func sqlNullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// isValidSlug enforces the lowercase-letters/digits/dashes shape that
// slugify() produces. Empty input returns false; callers should reject
// missing-slug separately.
func isValidSlug(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-':
		default:
			return false
		}
	}
	return true
}

// ---------- POST /v1/projects/{id}/transfer ----------
//
// Mirrors Convex Cloud's transfer_project (operationId on the OpenAPI spec).
// Caller must be admin of BOTH the source team (the project's current owner)
// AND the destination team — otherwise this would let anyone with admin on
// any team they belong to forklift projects out of teams they have nothing
// to do with.
//
// FK + cascades: projects.team_id is a plain UUID FK to teams. Deployments,
// env vars and audit_events all hang off project_id (CASCADE / SET NULL),
// not team_id, so the row update is the only mutation needed. Existing
// access_tokens scoped to this project keep working — scope is project_id,
// not team_id.

type transferProjectReq struct {
	DestinationTeamID string `json:"destinationTeamId"`
}

func (h *ProjectsHandler) transferProject(w http.ResponseWriter, r *http.Request) {
	p, srcTeam, role, ok := h.loadProjectForRequest(w, r)
	if !ok {
		return
	}
	if !canAdminProject(role) {
		writeError(w, http.StatusForbidden, "forbidden", "Only project admins can transfer the project")
		return
	}
	var req transferProjectReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.DestinationTeamID = strings.TrimSpace(req.DestinationTeamID)
	if req.DestinationTeamID == "" {
		writeError(w, http.StatusBadRequest, "missing_destination", "destinationTeamId is required")
		return
	}
	if req.DestinationTeamID == srcTeam.ID {
		// Already there — no-op so callers that retry don't get a misleading
		// 409. Cloud returns 204 here too.
		writeJSON(w, http.StatusNoContent, nil)
		return
	}

	uid, _ := auth.UserID(r.Context())

	// Destination must exist AND the caller must be admin there. Resolve by
	// id only (the spec uses TeamId, not slug or ref) so we don't accidentally
	// match a team whose slug happens to collide with a UUID-shaped string.
	var destRole string
	err := h.DB.QueryRow(r.Context(), `
		SELECT m.role
		  FROM teams t
		  JOIN team_members m ON m.team_id = t.id
		 WHERE t.id::text = $1 AND m.user_id = $2
	`, req.DestinationTeamID, uid).Scan(&destRole)
	if errors.Is(err, pgx.ErrNoRows) {
		// Distinguish "team doesn't exist" from "team exists but you're not
		// a member" so the operator gets a useful hint. Cheap second probe.
		var exists bool
		_ = h.DB.QueryRow(r.Context(),
			`SELECT EXISTS (SELECT 1 FROM teams WHERE id::text = $1)`,
			req.DestinationTeamID).Scan(&exists)
		if !exists {
			writeError(w, http.StatusNotFound, "team_not_found", "Destination team not found")
			return
		}
		writeError(w, http.StatusForbidden, "forbidden", "You are not a member of the destination team")
		return
	}
	if err != nil {
		logErr("check destination team", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to verify destination team")
		return
	}
	if destRole != models.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "You must be an admin of the destination team")
		return
	}

	// Slug uniqueness is per-team (UNIQUE(team_id, slug)). A project with the
	// same slug as ours could already live in the destination — surface that
	// as a structured 409 instead of a 500 from the constraint.
	if _, err := h.DB.Exec(r.Context(), `
		UPDATE projects SET team_id = $1 WHERE id = $2
	`, req.DestinationTeamID, p.ID); err != nil {
		if synapsedb.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "slug_taken",
				"A project with this slug already exists in the destination team")
			return
		}
		logErr("transfer project", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to transfer project")
		return
	}

	// Audit on BOTH teams — losing one is the kind of thing the operator
	// will want to find via either side's audit log. Best-effort, so a
	// failure on one doesn't block the other.
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     srcTeam.ID,
		ActorID:    uid,
		Action:     audit.ActionTransferProject,
		TargetType: audit.TargetProject,
		TargetID:   p.ID,
		Metadata: map[string]any{
			"name":              p.Name,
			"slug":              p.Slug,
			"sourceTeamId":      srcTeam.ID,
			"destinationTeamId": req.DestinationTeamID,
			"direction":         "out",
		},
	})
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     req.DestinationTeamID,
		ActorID:    uid,
		Action:     audit.ActionTransferProject,
		TargetType: audit.TargetProject,
		TargetID:   p.ID,
		Metadata: map[string]any{
			"name":              p.Name,
			"slug":              p.Slug,
			"sourceTeamId":      srcTeam.ID,
			"destinationTeamId": req.DestinationTeamID,
			"direction":         "in",
		},
	})
	writeJSON(w, http.StatusNoContent, nil)
}

// ---------- POST /v1/projects/{id}/delete ----------

func (h *ProjectsHandler) deleteProject(w http.ResponseWriter, r *http.Request) {
	p, t, role, ok := h.loadProjectForRequest(w, r)
	if !ok {
		return
	}
	if !canAdminProject(role) {
		writeError(w, http.StatusForbidden, "forbidden", "Only project admins can delete the project")
		return
	}

	// CASCADE removes deployments + env vars + deploy_keys. The provisioner
	// is responsible for tearing down running containers (via async janitor
	// or a future explicit hook); for v0 we mark deployments deleted first.
	tx, err := h.DB.Begin(r.Context())
	if err != nil {
		logErr("tx begin", err)
		writeError(w, http.StatusInternalServerError, "internal", "Database error")
		return
	}
	defer tx.Rollback(r.Context())

	_, err = tx.Exec(r.Context(),
		`UPDATE deployments SET status = 'deleted' WHERE project_id = $1`, p.ID)
	if err != nil {
		logErr("mark deployments deleted", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to delete project")
		return
	}

	if _, err := tx.Exec(r.Context(), `DELETE FROM projects WHERE id = $1`, p.ID); err != nil {
		logErr("delete project", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to delete project")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		logErr("tx commit", err)
		writeError(w, http.StatusInternalServerError, "internal", "Database error")
		return
	}

	uid, _ := auth.UserID(r.Context())
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     t.ID,
		ActorID:    uid,
		Action:     audit.ActionDeleteProject,
		TargetType: audit.TargetProject,
		TargetID:   p.ID,
		Metadata:   map[string]any{"name": p.Name, "slug": p.Slug},
	})
	writeJSON(w, http.StatusOK, map[string]string{"id": p.ID, "status": "deleted"})
}

// ---------- GET /v1/projects/{id}/list_deployments ----------

func (h *ProjectsHandler) listDeployments(w http.ResponseWriter, r *http.Request) {
	p, _, _, ok := h.loadProjectForRequest(w, r)
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
			SELECT id, project_id, name, deployment_type, kind, status,
			       deployment_url, is_default, reference, creator_user_id, created_at,
			       adopted
			  FROM deployments
			 WHERE project_id = $1 AND status <> 'deleted'
			 ORDER BY created_at ASC, id ASC
			 LIMIT $2
		`, p.ID, limit+1)
	} else {
		var cursorAt time.Time
		err = h.DB.QueryRow(r.Context(),
			`SELECT created_at FROM deployments WHERE id::text = $1 AND project_id = $2 AND status <> 'deleted'`,
			cursor, p.ID).Scan(&cursorAt)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "Cursor does not refer to a deployment in this project")
			return
		}
		if err != nil {
			logErr("resolve project deployments cursor", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to resolve cursor")
			return
		}
		rows, err = h.DB.Query(r.Context(), `
			SELECT id, project_id, name, deployment_type, kind, status,
			       deployment_url, is_default, reference, creator_user_id, created_at,
			       adopted
			  FROM deployments
			 WHERE project_id = $1
			   AND status <> 'deleted'
			   AND (created_at, id) > ($2, $3)
			 ORDER BY created_at ASC, id ASC
			 LIMIT $4
		`, p.ID, cursorAt, cursor, limit+1)
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
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.Name, &d.DeploymentType, &d.Kind, &d.Status,
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

// ---------- GET /v1/projects/{id}/list_default_environment_variables ----------

type envVarConfig struct {
	Name            string   `json:"name"`
	Value           string   `json:"value"`
	DeploymentTypes []string `json:"deploymentTypes"`
}

type listEnvVarsResp struct {
	Configs []envVarConfig `json:"configs"`
}

func (h *ProjectsHandler) listEnvVars(w http.ResponseWriter, r *http.Request) {
	p, _, _, ok := h.loadProjectForRequest(w, r)
	if !ok {
		return
	}
	rows, err := h.DB.Query(r.Context(), `
		SELECT name, value, deployment_types
		  FROM project_env_vars
		 WHERE project_id = $1
		 ORDER BY name ASC
	`, p.ID)
	if err != nil {
		logErr("list env vars", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list env vars")
		return
	}
	defer rows.Close()

	resp := listEnvVarsResp{Configs: []envVarConfig{}}
	for rows.Next() {
		var c envVarConfig
		if err := rows.Scan(&c.Name, &c.Value, &c.DeploymentTypes); err != nil {
			logErr("scan env var", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to scan env vars")
			return
		}
		resp.Configs = append(resp.Configs, c)
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---------- POST /v1/projects/{id}/update_default_environment_variables ----------
//
// Cloud expects a "batch changes" array: { changes: [{ op: "set"|"delete", name, value?, deploymentTypes? }] }.
// We support that shape so existing tooling works.

type envVarChange struct {
	Op              string   `json:"op"`
	Name            string   `json:"name"`
	Value           string   `json:"value,omitempty"`
	DeploymentTypes []string `json:"deploymentTypes,omitempty"`
}

type updateEnvVarsReq struct {
	Changes []envVarChange `json:"changes"`
}

func (h *ProjectsHandler) updateEnvVars(w http.ResponseWriter, r *http.Request) {
	p, t, role, ok := h.loadProjectForRequest(w, r)
	if !ok {
		return
	}
	if !canEditProject(role) {
		writeError(w, http.StatusForbidden, "forbidden", "Viewers cannot edit env vars; ask a project admin or member")
		return
	}
	var req updateEnvVarsReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	tx, err := h.DB.Begin(r.Context())
	if err != nil {
		logErr("tx begin", err)
		writeError(w, http.StatusInternalServerError, "internal", "Database error")
		return
	}
	defer tx.Rollback(r.Context())

	for _, c := range req.Changes {
		c.Name = strings.TrimSpace(c.Name)
		if c.Name == "" {
			writeError(w, http.StatusBadRequest, "missing_name", "env var name is required")
			return
		}
		switch c.Op {
		case "set":
			types := c.DeploymentTypes
			if len(types) == 0 {
				types = []string{"dev", "prod", "preview"}
			}
			_, err = tx.Exec(r.Context(), `
				INSERT INTO project_env_vars (project_id, name, value, deployment_types, updated_at)
				VALUES ($1, $2, $3, $4, now())
				ON CONFLICT (project_id, name) DO UPDATE
				   SET value = EXCLUDED.value,
				       deployment_types = EXCLUDED.deployment_types,
				       updated_at = now()
			`, p.ID, c.Name, c.Value, types)
		case "delete":
			_, err = tx.Exec(r.Context(),
				`DELETE FROM project_env_vars WHERE project_id = $1 AND name = $2`,
				p.ID, c.Name)
		default:
			writeError(w, http.StatusBadRequest, "bad_op", "op must be 'set' or 'delete'")
			return
		}
		if err != nil {
			logErr("env var change", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to apply env var change")
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		logErr("tx commit", err)
		writeError(w, http.StatusInternalServerError, "internal", "Database error")
		return
	}
	uid, _ := auth.UserID(r.Context())
	// Capture the count + change names so admins can audit which keys
	// were touched without leaking values (which often contain secrets).
	names := make([]string, 0, len(req.Changes))
	for _, c := range req.Changes {
		names = append(names, c.Name)
	}
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     t.ID,
		ActorID:    uid,
		Action:     audit.ActionUpdateEnvVars,
		TargetType: audit.TargetProject,
		TargetID:   p.ID,
		Metadata:   map[string]any{"applied": len(req.Changes), "names": names},
	})
	writeJSON(w, http.StatusOK, map[string]any{"applied": len(req.Changes)})
}

// ---------- GET /v1/projects/{id}/list_members ----------
//
// Returns the merged member list for a project: every team member of
// the owning team, with the role they actually have on this project
// (project_members override when present, team role when not). The
// `source` field flags which way each row came in so the dashboard
// can render "team admin (project viewer)" without recomputing.
//
// Visible to anyone with access to the project — viewers included.
// Listing members is informational; promoting / demoting is gated
// on canAdminProject.

type projectMemberView struct {
	ID         string    `json:"id"`
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	Role       string    `json:"role"`
	Source     string    `json:"source"`
	CreateTime time.Time `json:"createTime"`
}

func (h *ProjectsHandler) listProjectMembers(w http.ResponseWriter, r *http.Request) {
	p, t, _, ok := h.loadProjectForRequest(w, r)
	if !ok {
		return
	}
	// One query, joined: team_members LEFT JOIN project_members on
	// (team.team_id, team.user_id) → (project.project_id, project.user_id).
	// COALESCE picks the override role when project_members has a row.
	rows, err := h.DB.Query(r.Context(), `
		SELECT u.id, u.email, u.name,
		       COALESCE(pm.role, tm.role) AS role,
		       CASE WHEN pm.role IS NOT NULL THEN 'project' ELSE 'team' END AS source,
		       COALESCE(pm.created_at, tm.created_at) AS created_at
		  FROM team_members tm
		  JOIN users u ON u.id = tm.user_id
		  LEFT JOIN project_members pm
		         ON pm.project_id = $1 AND pm.user_id = tm.user_id
		 WHERE tm.team_id = $2
		 ORDER BY tm.created_at ASC, u.id ASC
	`, p.ID, t.ID)
	if err != nil {
		logErr("list project members", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list members")
		return
	}
	defer rows.Close()
	out := make([]projectMemberView, 0)
	for rows.Next() {
		var v projectMemberView
		if err := rows.Scan(&v.ID, &v.Email, &v.Name, &v.Role, &v.Source, &v.CreateTime); err != nil {
			logErr("scan project member", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to scan members")
			return
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------- POST /v1/projects/{id}/add_member ----------
//
// Adds (or upserts) a project_members override for a user that's
// already a team_member. Body: { userId, role: "admin"|"member"|"viewer" }.
// Project admin only. The "must already be team member" guard keeps
// the team as the security boundary — adding random users requires
// the team-invite flow first.

type addProjectMemberReq struct {
	UserID string `json:"userId"`
	Role   string `json:"role"`
}

func (h *ProjectsHandler) addProjectMember(w http.ResponseWriter, r *http.Request) {
	p, t, role, ok := h.loadProjectForRequest(w, r)
	if !ok {
		return
	}
	if !canAdminProject(role) {
		writeError(w, http.StatusForbidden, "forbidden", "Only project admins can manage members")
		return
	}
	var req addProjectMemberReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	if req.UserID == "" {
		writeError(w, http.StatusBadRequest, "missing_user", "userId is required")
		return
	}
	storedRole, err := normaliseProjectRole(req.Role)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_role", err.Error())
		return
	}

	// Target must already be a team member of the project's team. Lets
	// us keep "team" as the trust boundary — projects are partitions of
	// a team, not a parallel access plane.
	var exists bool
	if err := h.DB.QueryRow(r.Context(),
		`SELECT EXISTS (SELECT 1 FROM team_members WHERE team_id = $1 AND user_id = $2)`,
		t.ID, req.UserID).Scan(&exists); err != nil {
		logErr("check team membership", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to verify team membership")
		return
	}
	if !exists {
		writeError(w, http.StatusBadRequest, "not_team_member",
			"User must be a member of the project's team before being added to a project")
		return
	}

	if _, err := h.DB.Exec(r.Context(), `
		INSERT INTO project_members (project_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role
	`, p.ID, req.UserID, storedRole); err != nil {
		logErr("insert project member", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to add member")
		return
	}

	uid, _ := auth.UserID(r.Context())
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     t.ID,
		ActorID:    uid,
		Action:     audit.ActionAddProjectMember,
		TargetType: audit.TargetProject,
		TargetID:   p.ID,
		Metadata: map[string]any{
			"memberId": req.UserID,
			"role":     storedRole,
		},
	})
	writeJSON(w, http.StatusOK, map[string]string{
		"projectId": p.ID,
		"userId":    req.UserID,
		"role":      storedRole,
	})
}

// ---------- POST /v1/projects/{id}/update_member_role ----------
//
// Same shape as team-level update_member_role for parity. Upserts
// project_members.role; if the user has no override row yet, this is
// equivalent to add_member with the same target. Project admin only.

type updateProjectMemberRoleReq struct {
	MemberID string `json:"memberId"`
	Role     string `json:"role"`
}

func (h *ProjectsHandler) updateProjectMemberRole(w http.ResponseWriter, r *http.Request) {
	p, t, role, ok := h.loadProjectForRequest(w, r)
	if !ok {
		return
	}
	if !canAdminProject(role) {
		writeError(w, http.StatusForbidden, "forbidden", "Only project admins can manage members")
		return
	}
	var req updateProjectMemberRoleReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.MemberID = strings.TrimSpace(req.MemberID)
	if req.MemberID == "" {
		writeError(w, http.StatusBadRequest, "missing_member", "memberId is required")
		return
	}
	storedRole, err := normaliseProjectRole(req.Role)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_role", err.Error())
		return
	}

	// Same team-membership guard as add_member — keeps semantics
	// identical regardless of whether the override row exists yet.
	var exists bool
	if err := h.DB.QueryRow(r.Context(),
		`SELECT EXISTS (SELECT 1 FROM team_members WHERE team_id = $1 AND user_id = $2)`,
		t.ID, req.MemberID).Scan(&exists); err != nil {
		logErr("check team membership", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to verify team membership")
		return
	}
	if !exists {
		writeError(w, http.StatusBadRequest, "not_team_member",
			"User must be a member of the project's team")
		return
	}

	if _, err := h.DB.Exec(r.Context(), `
		INSERT INTO project_members (project_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role
	`, p.ID, req.MemberID, storedRole); err != nil {
		logErr("update project member role", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to update role")
		return
	}

	uid, _ := auth.UserID(r.Context())
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     t.ID,
		ActorID:    uid,
		Action:     audit.ActionUpdateProjectMemberRole,
		TargetType: audit.TargetProject,
		TargetID:   p.ID,
		Metadata: map[string]any{
			"memberId": req.MemberID,
			"newRole":  storedRole,
		},
	})
	writeJSON(w, http.StatusOK, map[string]string{
		"memberId": req.MemberID,
		"role":     storedRole,
	})
}

// ---------- POST /v1/projects/{id}/remove_member ----------
//
// Removes the project_members override for a user. After remove, the
// user falls back to whatever role they have at the team level — if
// any. Operators wanting to fully kick someone from a project should
// remove them from the team instead (project_members CASCADE).
//
// Self-removal is allowed (any role can call with their own id); other
// users can be removed only by project admins.

type removeProjectMemberReq struct {
	MemberID string `json:"memberId"`
}

func (h *ProjectsHandler) removeProjectMember(w http.ResponseWriter, r *http.Request) {
	p, t, role, ok := h.loadProjectForRequest(w, r)
	if !ok {
		return
	}
	uid, _ := auth.UserID(r.Context())
	var req removeProjectMemberReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.MemberID = strings.TrimSpace(req.MemberID)
	if req.MemberID == "" {
		writeError(w, http.StatusBadRequest, "missing_member", "memberId is required")
		return
	}
	selfRemoval := req.MemberID == uid
	if !selfRemoval && !canAdminProject(role) {
		writeError(w, http.StatusForbidden, "forbidden",
			"Only project admins can remove other members")
		return
	}

	tag, err := h.DB.Exec(r.Context(),
		`DELETE FROM project_members WHERE project_id = $1 AND user_id::text = $2`,
		p.ID, req.MemberID)
	if err != nil {
		logErr("remove project member", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to remove member")
		return
	}
	if tag.RowsAffected() == 0 {
		// No override row to remove — surface as 404 so callers can
		// distinguish "already removed" from "didn't exist". The
		// member may still exist via team_members fallback; that's
		// fine — they keep their team role.
		writeError(w, http.StatusNotFound, "no_override",
			"User has no project-level override; their team role is in effect")
		return
	}

	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     t.ID,
		ActorID:    uid,
		Action:     audit.ActionRemoveProjectMember,
		TargetType: audit.TargetProject,
		TargetID:   p.ID,
		Metadata: map[string]any{
			"memberId":    req.MemberID,
			"selfRemoval": selfRemoval,
		},
	})
	writeJSON(w, http.StatusOK, map[string]string{
		"memberId": req.MemberID,
		"status":   "override_removed",
	})
}

// normaliseProjectRole accepts admin / member / viewer (Cloud's role
// vocabulary for project-level overrides). Returns the canonical
// stored value or an error explaining the allowed set.
func normaliseProjectRole(role string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "admin":
		return models.RoleAdmin, nil
	case "member":
		return models.RoleMember, nil
	case "viewer":
		return models.RoleViewer, nil
	default:
		return "", errors.New("role must be 'admin', 'member', or 'viewer'")
	}
}
