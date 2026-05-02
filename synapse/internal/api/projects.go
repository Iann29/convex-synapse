package api

import (
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

	var role string
	err = h.DB.QueryRow(r.Context(),
		`SELECT role FROM team_members WHERE team_id = $1 AND user_id = $2`,
		t.ID, uid).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusForbidden, "forbidden", "You do not have access to this project")
		return nil, nil, "", false
	}
	if err != nil {
		logErr("check membership", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to verify access")
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

type updateProjectReq struct {
	Name *string `json:"name,omitempty"`
}

func (h *ProjectsHandler) updateProject(w http.ResponseWriter, r *http.Request) {
	p, t, role, ok := h.loadProjectForRequest(w, r)
	if !ok {
		return
	}
	if role != models.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "Only team admins can update projects")
		return
	}
	var req updateProjectReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	renamed := false
	oldName := p.Name
	if req.Name != nil {
		newName := strings.TrimSpace(*req.Name)
		if newName == "" {
			writeError(w, http.StatusBadRequest, "missing_name", "Project name is required")
			return
		}
		_, err := h.DB.Exec(r.Context(),
			`UPDATE projects SET name = $1 WHERE id = $2`, newName, p.ID)
		if err != nil {
			logErr("update project", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to update project")
			return
		}
		renamed = newName != oldName
		p.Name = newName
	}
	if renamed {
		uid, _ := auth.UserID(r.Context())
		_ = audit.Record(r.Context(), h.DB, audit.Options{
			TeamID:     t.ID,
			ActorID:    uid,
			Action:     audit.ActionRenameProject,
			TargetType: audit.TargetProject,
			TargetID:   p.ID,
			Metadata:   map[string]any{"oldName": oldName, "newName": p.Name},
		})
	}
	writeJSON(w, http.StatusOK, p)
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
	if role != models.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "Only team admins can transfer projects")
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
	if role != models.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "Only team admins can delete projects")
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
			SELECT id, project_id, name, deployment_type, status,
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
			SELECT id, project_id, name, deployment_type, status,
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
	if role != models.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "Only team admins can edit env vars")
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
