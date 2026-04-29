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
	dockerprov "github.com/Iann29/synapse/internal/docker"
	"github.com/Iann29/synapse/internal/models"
)

// Provisioner is the subset of the docker provisioner that the deployments
// handler depends on. Pulled out behind an interface so tests can swap in a
// fake without spinning up a real Docker daemon. *dockerprov.Client implements
// this (Provision/Destroy/Status), so production wiring is unchanged.
type Provisioner interface {
	Provision(ctx context.Context, spec dockerprov.DeploymentSpec) (*dockerprov.DeploymentInfo, error)
	Destroy(ctx context.Context, deploymentName string) error
	Status(ctx context.Context, deploymentName string) (string, error)
}

// DeploymentsHandler exposes the deployment lifecycle: create (which provisions
// a Docker container), list, get, delete, plus the dashboard-auth endpoint
// that returns the deployment URL + admin key for the calling user.
type DeploymentsHandler struct {
	DB                    *pgxpool.Pool
	Docker                Provisioner
	PortRangeMin          int
	PortRangeMax          int
	HealthcheckViaNetwork bool
}

func (h *DeploymentsHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Route("/{name}", func(r chi.Router) {
		r.Get("/", h.getDeployment)
		r.Post("/delete", h.deleteDeployment)
		r.Get("/auth", h.deploymentAuth)
		r.Post("/create_deploy_key", h.createDeployKey)
	})
	return r
}

// MountProjectScopedRoutes adds POST /v1/projects/{id}/create_deployment to
// the projects sub-router. We do this so the URL hierarchy stays cloud-
// compatible ({project_id}/create_deployment) without leaking the deployments
// handler into projects.go.
func (h *DeploymentsHandler) MountProjectScopedRoutes(r chi.Router) {
	r.Post("/create_deployment", h.createDeployment)
	r.Get("/deployment", h.getProjectDeployment)
}

// ---------- helpers ----------

// loadDeploymentForRequest resolves /v1/deployments/{name} and asserts
// caller membership in the owning team. Like loadProjectForRequest, but
// at the deployment grain.
func (h *DeploymentsHandler) loadDeploymentForRequest(w http.ResponseWriter, r *http.Request) (*models.Deployment, *models.Project, *models.Team, string, bool) {
	uid, err := auth.UserID(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Not authenticated")
		return nil, nil, nil, "", false
	}
	name := chi.URLParam(r, "name")

	d, p, t, err := loadDeployment(r.Context(), h.DB, name)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "deployment_not_found", "Deployment not found")
		return nil, nil, nil, "", false
	}
	if err != nil {
		logErr("load deployment", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to load deployment")
		return nil, nil, nil, "", false
	}

	var role string
	err = h.DB.QueryRow(r.Context(),
		`SELECT role FROM team_members WHERE team_id = $1 AND user_id = $2`,
		t.ID, uid).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusForbidden, "forbidden", "You do not have access to this deployment")
		return nil, nil, nil, "", false
	}
	if err != nil {
		logErr("check membership", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to verify access")
		return nil, nil, nil, "", false
	}
	return d, p, t, role, true
}

func loadDeployment(ctx context.Context, db *pgxpool.Pool, name string) (*models.Deployment, *models.Project, *models.Team, error) {
	var d models.Deployment
	var p models.Project
	var t models.Team
	var url, ref, creator *string
	err := db.QueryRow(ctx, `
		SELECT d.id, d.project_id, d.name, d.deployment_type, d.status,
		       d.deployment_url, d.is_default, d.reference, d.creator_user_id,
		       d.created_at, d.admin_key, d.instance_secret, d.host_port, d.container_id,
		       p.id, p.team_id, p.name, p.slug, p.is_demo, p.created_at,
		       t.id, t.name, t.slug, t.creator_user_id, t.default_region, t.suspended, t.created_at
		  FROM deployments d
		  JOIN projects p ON p.id = d.project_id
		  JOIN teams t ON t.id = p.team_id
		 WHERE d.name = $1
		   AND d.status <> 'deleted'
	`, name).Scan(
		&d.ID, &d.ProjectID, &d.Name, &d.DeploymentType, &d.Status,
		&url, &d.IsDefault, &ref, &creator,
		&d.CreatedAt, &d.AdminKey, &d.InstanceSecret, &d.HostPort, &d.ContainerID,
		&p.ID, &p.TeamID, &p.Name, &p.Slug, &p.IsDemo, &p.CreatedAt,
		&t.ID, &t.Name, &t.Slug, &t.CreatorUserID, &t.DefaultRegion, &t.Suspended, &t.CreatedAt,
	)
	if err != nil {
		return nil, nil, nil, err
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
	p.TeamSlug = t.Slug
	return &d, &p, &t, nil
}

// allocatePort finds the lowest free host port in the configured range.
// Concurrent calls race and lose to the UNIQUE(host_port) constraint —
// the create flow surfaces that as a retryable error.
func (h *DeploymentsHandler) allocatePort(ctx context.Context) (int, error) {
	var port int
	err := h.DB.QueryRow(ctx, `
		WITH used AS (
		  SELECT host_port FROM deployments
		   WHERE host_port IS NOT NULL AND status <> 'deleted'
		)
		SELECT p FROM (
		  SELECT generate_series($1::int, $2::int) AS p
		) candidates
		 WHERE p NOT IN (SELECT host_port FROM used)
		 ORDER BY p
		 LIMIT 1
	`, h.PortRangeMin, h.PortRangeMax).Scan(&port)
	if err != nil {
		return 0, err
	}
	return port, nil
}

// allocateDeploymentName generates a unique friendly name. Race-loses are
// caught by the UNIQUE constraint on deployments.name.
func (h *DeploymentsHandler) allocateDeploymentName(ctx context.Context) (string, error) {
	for i := 0; i < 25; i++ {
		candidate := dockerprov.GenerateDeploymentName()
		var exists bool
		if err := h.DB.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM deployments WHERE name = $1)`,
			candidate).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", errors.New("could not allocate deployment name")
}

// ---------- POST /v1/projects/{id}/create_deployment ----------

type createDeploymentReq struct {
	Type      string `json:"type"`               // dev | prod | preview | custom
	Reference string `json:"reference,omitempty"`
	IsDefault bool   `json:"isDefault,omitempty"`
}

func (h *DeploymentsHandler) createDeployment(w http.ResponseWriter, r *http.Request) {
	uid, err := auth.UserID(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Not authenticated")
		return
	}
	projectID := chi.URLParam(r, "projectID")

	// Authorization: caller must be team admin (provisioning is privileged).
	var teamID string
	err = h.DB.QueryRow(r.Context(), `
		SELECT p.team_id FROM projects p WHERE p.id::text = $1
	`, projectID).Scan(&teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project_not_found", "Project not found")
		return
	}
	if err != nil {
		logErr("lookup project", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to load project")
		return
	}
	var role string
	err = h.DB.QueryRow(r.Context(),
		`SELECT role FROM team_members WHERE team_id = $1 AND user_id = $2`,
		teamID, uid).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) || role != models.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "Only team admins can create deployments")
		return
	}
	if err != nil {
		logErr("check role", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to check role")
		return
	}

	var req createDeploymentReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	switch req.Type {
	case models.DeploymentTypeDev, models.DeploymentTypeProd, models.DeploymentTypePreview, models.DeploymentTypeCustom:
	case "":
		req.Type = models.DeploymentTypeDev
	default:
		writeError(w, http.StatusBadRequest, "invalid_type", "deploymentType must be dev|prod|preview|custom")
		return
	}

	name, err := h.allocateDeploymentName(r.Context())
	if err != nil {
		logErr("alloc name", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to allocate deployment name")
		return
	}
	port, err := h.allocatePort(r.Context())
	if err != nil {
		logErr("alloc port", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to allocate host port")
		return
	}

	instanceSecret, err := dockerprov.RandomHex(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Failed to generate secret")
		return
	}
	adminKey, err := dockerprov.RandomHex(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Failed to generate admin key")
		return
	}

	// Insert as 'provisioning' first so concurrent allocators see this port
	// as taken. We update with container_id + url after Provision returns.
	var d models.Deployment
	d.ProjectID = projectID
	d.Name = name
	d.DeploymentType = req.Type
	d.Status = models.DeploymentStatusProvisioning
	d.HostPort = port
	d.AdminKey = adminKey
	d.InstanceSecret = instanceSecret
	d.IsDefault = req.IsDefault
	d.Reference = req.Reference
	d.CreatorUserID = uid

	err = h.DB.QueryRow(r.Context(), `
		INSERT INTO deployments (project_id, name, deployment_type, status, host_port,
		                          admin_key, instance_secret, is_default, reference,
		                          creator_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''), $10)
		RETURNING id, created_at
	`, projectID, name, req.Type, models.DeploymentStatusProvisioning, port,
		adminKey, instanceSecret, req.IsDefault, req.Reference, uid,
	).Scan(&d.ID, &d.CreatedAt)
	if err != nil {
		logErr("insert deployment", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to record deployment")
		return
	}

	// Provisioning is sync for v0. The handler blocks until the container is
	// running (or healthcheck times out at 60s). Async/queued provisioning
	// is a v0.2 concern — keeps the API simple for now.
	info, provErr := h.Docker.Provision(r.Context(), dockerprov.DeploymentSpec{
		Name:                  name,
		InstanceSecret:        instanceSecret,
		HostPort:              port,
		EnvVars:               map[string]string{},
		HealthcheckViaNetwork: h.HealthcheckViaNetwork,
	})
	if provErr != nil {
		logErr("provision", provErr)
		_, _ = h.DB.Exec(r.Context(),
			`UPDATE deployments SET status = $1 WHERE id = $2`,
			models.DeploymentStatusFailed, d.ID)
		writeError(w, http.StatusInternalServerError, "provision_failed", provErr.Error())
		return
	}

	_, err = h.DB.Exec(r.Context(), `
		UPDATE deployments
		   SET status = $1,
		       container_id = $2,
		       deployment_url = $3
		 WHERE id = $4
	`, models.DeploymentStatusRunning, info.ContainerID, info.DeploymentURL, d.ID)
	if err != nil {
		logErr("update deployment", err)
		writeError(w, http.StatusInternalServerError, "internal", "Provision succeeded but persist failed")
		return
	}

	d.Status = models.DeploymentStatusRunning
	d.ContainerID = info.ContainerID
	d.DeploymentURL = info.DeploymentURL
	writeJSON(w, http.StatusCreated, d)
}

// ---------- GET /v1/projects/{id}/deployment ----------

func (h *DeploymentsHandler) getProjectDeployment(w http.ResponseWriter, r *http.Request) {
	p, _, _, ok := (&ProjectsHandler{DB: h.DB}).loadProjectForRequest(w, r)
	if !ok {
		return
	}

	// Query params per cloud spec.
	q := r.URL.Query()
	ref := q.Get("reference")
	defaultProd := strings.EqualFold(q.Get("defaultProd"), "true")
	defaultDev := strings.EqualFold(q.Get("defaultDev"), "true")

	var args []any
	where := []string{"d.project_id = $1", "d.status <> 'deleted'"}
	args = append(args, p.ID)
	if ref != "" {
		where = append(where, "d.reference = $2")
		args = append(args, ref)
	} else if defaultProd {
		where = append(where, "d.deployment_type = 'prod' AND d.is_default = true")
	} else if defaultDev {
		where = append(where, "d.deployment_type = 'dev' AND d.is_default = true")
	} else {
		// Pick any deployment, preferring default → newest.
		where = append(where, "true")
	}
	query := `
		SELECT d.id, d.project_id, d.name, d.deployment_type, d.status,
		       d.deployment_url, d.is_default, d.reference, d.creator_user_id, d.created_at
		  FROM deployments d
		 WHERE ` + joinAnd(where) + `
		 ORDER BY d.is_default DESC, d.created_at DESC
		 LIMIT 1
	`
	var d models.Deployment
	var url, refDB, creator *string
	err := h.DB.QueryRow(r.Context(), query, args...).Scan(
		&d.ID, &d.ProjectID, &d.Name, &d.DeploymentType, &d.Status,
		&url, &d.IsDefault, &refDB, &creator, &d.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "deployment_not_found", "No matching deployment")
		return
	}
	if err != nil {
		logErr("query deployment", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to load deployment")
		return
	}
	if url != nil {
		d.DeploymentURL = *url
	}
	if refDB != nil {
		d.Reference = *refDB
	}
	if creator != nil {
		d.CreatorUserID = *creator
	}
	writeJSON(w, http.StatusOK, d)
}

func joinAnd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " AND "
		}
		out += p
	}
	return out
}

// ---------- GET /v1/deployments/{name} ----------

func (h *DeploymentsHandler) getDeployment(w http.ResponseWriter, r *http.Request) {
	d, _, _, _, ok := h.loadDeploymentForRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// ---------- POST /v1/deployments/{name}/delete ----------

func (h *DeploymentsHandler) deleteDeployment(w http.ResponseWriter, r *http.Request) {
	d, _, _, role, ok := h.loadDeploymentForRequest(w, r)
	if !ok {
		return
	}
	if role != models.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "Only team admins can delete deployments")
		return
	}

	// Tear down the container/volume first; if that fails, leave the row
	// alone so the operator can retry. A successful Destroy is idempotent.
	if err := h.Docker.Destroy(r.Context(), d.Name); err != nil {
		logErr("docker destroy", err)
		writeError(w, http.StatusInternalServerError, "destroy_failed", err.Error())
		return
	}

	_, err := h.DB.Exec(r.Context(), `
		UPDATE deployments
		   SET status = 'deleted',
		       container_id = NULL,
		       host_port = NULL
		 WHERE id = $1
	`, d.ID)
	if err != nil {
		logErr("mark deleted", err)
		writeError(w, http.StatusInternalServerError, "internal", "Container removed but DB update failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"name": d.Name, "status": "deleted"})
}

// ---------- GET /v1/deployments/{name}/auth ----------
//
// Used by the dashboard. The dashboard never sees the admin key on team listing
// or deployment detail responses; it explicitly asks for it via this endpoint
// when it needs to talk to the deployment directly. Mirrors Convex Cloud's
// /api/dashboard/instances/{deploymentName}/auth.

type deploymentAuthResp struct {
	DeploymentName string `json:"deploymentName"`
	DeploymentURL  string `json:"deploymentUrl"`
	AdminKey       string `json:"adminKey"`
	DeploymentType string `json:"deploymentType"`
}

func (h *DeploymentsHandler) deploymentAuth(w http.ResponseWriter, r *http.Request) {
	d, _, _, _, ok := h.loadDeploymentForRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, deploymentAuthResp{
		DeploymentName: d.Name,
		DeploymentURL:  d.DeploymentURL,
		AdminKey:       d.AdminKey,
		DeploymentType: d.DeploymentType,
	})
}

// ---------- POST /v1/deployments/{name}/create_deploy_key ----------

type createDeployKeyReq struct {
	Name string `json:"name,omitempty"`
}

type createDeployKeyResp struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token"`
}

func (h *DeploymentsHandler) createDeployKey(w http.ResponseWriter, r *http.Request) {
	d, _, _, role, ok := h.loadDeploymentForRequest(w, r)
	if !ok {
		return
	}
	if role != models.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "Only team admins can create deploy keys")
		return
	}
	var req createDeployKeyReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	uid, _ := auth.UserID(r.Context())

	plain, hash, err := auth.GenerateToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Failed to generate token")
		return
	}

	var id string
	err = h.DB.QueryRow(r.Context(), `
		INSERT INTO deploy_keys (deployment_id, name, token_hash, created_by)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, d.ID, req.Name, hash, uid).Scan(&id)
	if err != nil {
		logErr("insert deploy key", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to create deploy key")
		return
	}

	writeJSON(w, http.StatusCreated, createDeployKeyResp{
		ID:    id,
		Name:  req.Name,
		Token: plain,
	})
}
