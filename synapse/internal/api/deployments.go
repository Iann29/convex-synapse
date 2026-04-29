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
	dockerprov "github.com/Iann29/synapse/internal/docker"
	"github.com/Iann29/synapse/internal/models"
	"github.com/Iann29/synapse/internal/provisioner"
)

// provisionTimeout caps how long the background goroutine waits for Docker.
// Must be generous enough for cold image pulls on slow networks, but short
// enough that a stuck pull eventually surfaces as a "failed" row instead of
// a goroutine that lives forever.
const provisionTimeout = 5 * time.Minute

// Provisioner is the subset of the docker provisioner that the deployments
// handler depends on. Pulled out behind an interface so tests can swap in a
// fake without spinning up a real Docker daemon. *dockerprov.Client
// implements all four methods, so production wiring is unchanged.
type Provisioner interface {
	Provision(ctx context.Context, spec dockerprov.DeploymentSpec) (*dockerprov.DeploymentInfo, error)
	Destroy(ctx context.Context, deploymentName string) error
	Status(ctx context.Context, deploymentName string) (string, error)
	// GenerateAdminKey runs the convex-backend's `generate_key` binary in a
	// throwaway container so the resulting key passes the running container's
	// `/api/check_admin_key` validation. Random hex strings are rejected by
	// the keybroker which signs admin keys with INSTANCE_SECRET.
	GenerateAdminKey(ctx context.Context, instanceName, instanceSecret string) (string, error)
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
		r.Get("/cli_credentials", h.deploymentCLICredentials)
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
	r.Post("/adopt_deployment", h.adoptDeployment)
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
	var url, ref, creator, containerID *string
	var hostPort *int
	// container_id and host_port are NULL while a deployment is still
	// 'provisioning' (the goroutine fills them in once Provision succeeds);
	// scanning straight into the non-pointer fields blows up on NULL, so
	// we go through pointers and dereference defensively below.
	err := db.QueryRow(ctx, `
		SELECT d.id, d.project_id, d.name, d.deployment_type, d.status,
		       d.deployment_url, d.is_default, d.reference, d.creator_user_id,
		       d.created_at, d.admin_key, d.instance_secret, d.host_port, d.container_id, d.adopted,
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
		&d.CreatedAt, &d.AdminKey, &d.InstanceSecret, &hostPort, &containerID, &d.Adopted,
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
	if containerID != nil {
		d.ContainerID = *containerID
	}
	if hostPort != nil {
		d.HostPort = *hostPort
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

	// INSTANCE_SECRET is independent of name/port so we generate it once and
	// keep it across retries. The admin key, by contrast, is derived from
	// (name, secret) via Convex's `generate_key` — if we regenerate the name
	// we have to regenerate the admin key too, so it lives inside the loop.
	instanceSecret, err := dockerprov.RandomHex(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Failed to generate secret")
		return
	}

	// Allocate (name, port, adminKey) and INSERT atomically. Two synapse
	// nodes (or two concurrent goroutines on one node) can pick the same
	// port or name from the SELECT-EXISTS pre-check; the UNIQUE constraints
	// on `name` and `host_port` reject the loser, the retry helper picks
	// fresh candidates and tries again.
	var d models.Deployment
	d.ProjectID = projectID
	d.DeploymentType = req.Type
	d.Status = models.DeploymentStatusProvisioning
	d.InstanceSecret = instanceSecret
	d.IsDefault = req.IsDefault
	d.Reference = req.Reference
	d.CreatorUserID = uid

	var name string
	var port int
	var adminKey string

	err = synapsedb.WithRetryOnUniqueViolation(r.Context(), 5, func() error {
		var allocErr error
		name, allocErr = h.allocateDeploymentName(r.Context())
		if allocErr != nil {
			return allocErr
		}
		port, allocErr = h.allocatePort(r.Context())
		if allocErr != nil {
			return allocErr
		}
		adminKey, allocErr = h.Docker.GenerateAdminKey(r.Context(), name, instanceSecret)
		if allocErr != nil {
			return allocErr
		}
		// Insert the deployment row + enqueue the provisioning job in a
		// single transaction so we never end up with a row that no worker
		// will pick up (or a queue entry that points at nothing).
		tx, txErr := h.DB.Begin(r.Context())
		if txErr != nil {
			return txErr
		}
		defer tx.Rollback(r.Context())

		if txErr = tx.QueryRow(r.Context(), `
			INSERT INTO deployments (project_id, name, deployment_type, status, host_port,
			                          admin_key, instance_secret, is_default, reference,
			                          creator_user_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''), $10)
			RETURNING id, created_at
		`, projectID, name, req.Type, models.DeploymentStatusProvisioning, port,
			adminKey, instanceSecret, req.IsDefault, req.Reference, uid,
		).Scan(&d.ID, &d.CreatedAt); txErr != nil {
			return txErr
		}
		if txErr = provisioner.Enqueue(r.Context(), tx, d.ID, h.HealthcheckViaNetwork); txErr != nil {
			return txErr
		}
		return tx.Commit(r.Context())
	})
	if err != nil {
		logErr("insert deployment", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to record deployment")
		return
	}
	d.Name = name
	d.HostPort = port
	d.AdminKey = adminKey

	// The provisioner.Worker on this (or any) Synapse process will dequeue
	// the job and drive Docker.Provision. The dashboard's existing SWR
	// polling on /list_deployments picks up the status flip from
	// 'provisioning' to 'running' or 'failed' without any handler-side
	// coordination — same UX as before, just resilient to crashes.

	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     teamID,
		ActorID:    uid,
		Action:     audit.ActionCreateDeployment,
		TargetType: audit.TargetDeployment,
		TargetID:   d.ID,
		Metadata: map[string]any{
			"name":           name,
			"deploymentType": req.Type,
			"projectId":      projectID,
		},
	})
	// Return the row in 'provisioning' state. The dashboard polls and will
	// flip to 'running' (or 'failed') when the goroutine updates the row.
	writeJSON(w, http.StatusCreated, d)
}

// (provisionInBackground was removed in favour of internal/provisioner —
// the same logic now lives in provisioner.Worker, dequeued from the
// `provisioning_jobs` table instead of being spawned as a per-handler
// goroutine. Survival across process restarts; multi-node sharding via
// SELECT FOR UPDATE SKIP LOCKED.)

// ---------- POST /v1/projects/{id}/adopt_deployment ----------

// adoptDeploymentReq registers an existing Convex backend (running outside
// Synapse) into Synapse's catalog. Synapse stores the URL + admin key, never
// touches the container, and skips Docker calls in delete / health flows.
//
// Use case: an operator was running self-hosted Convex by hand, then installs
// Synapse and wants to manage existing deployments through the dashboard.
type adoptDeploymentReq struct {
	DeploymentURL  string `json:"deploymentUrl"`
	AdminKey       string `json:"adminKey"`
	DeploymentType string `json:"deploymentType,omitempty"` // dev|prod|preview|custom (default dev)
	// Name is the externally-facing identifier; if omitted Synapse allocates
	// one. When supplied it must be unique across all deployments — Synapse
	// uses the name for routing (`/d/{name}/*` proxy mode) and as the value
	// of CONVEX_DEPLOYMENT in CLI snippets, so collisions break tools.
	Name      string `json:"name,omitempty"`
	IsDefault bool   `json:"isDefault,omitempty"`
	Reference string `json:"reference,omitempty"`
}

func (h *DeploymentsHandler) adoptDeployment(w http.ResponseWriter, r *http.Request) {
	uid, err := auth.UserID(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Not authenticated")
		return
	}
	projectID := chi.URLParam(r, "projectID")

	var teamID string
	err = h.DB.QueryRow(r.Context(),
		`SELECT p.team_id FROM projects p WHERE p.id::text = $1`, projectID,
	).Scan(&teamID)
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
		writeError(w, http.StatusForbidden, "forbidden", "Only team admins can adopt deployments")
		return
	}
	if err != nil {
		logErr("check role", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to check role")
		return
	}

	var req adoptDeploymentReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.DeploymentURL = strings.TrimRight(strings.TrimSpace(req.DeploymentURL), "/")
	req.AdminKey = strings.TrimSpace(req.AdminKey)
	req.Name = strings.TrimSpace(req.Name)

	if req.DeploymentURL == "" {
		writeError(w, http.StatusBadRequest, "missing_url", "deploymentUrl is required")
		return
	}
	if !strings.HasPrefix(req.DeploymentURL, "http://") && !strings.HasPrefix(req.DeploymentURL, "https://") {
		writeError(w, http.StatusBadRequest, "invalid_url", "deploymentUrl must be http:// or https://")
		return
	}
	if req.AdminKey == "" {
		writeError(w, http.StatusBadRequest, "missing_admin_key", "adminKey is required")
		return
	}
	switch req.DeploymentType {
	case "":
		req.DeploymentType = models.DeploymentTypeDev
	case models.DeploymentTypeDev, models.DeploymentTypeProd, models.DeploymentTypePreview, models.DeploymentTypeCustom:
	default:
		writeError(w, http.StatusBadRequest, "invalid_type", "deploymentType must be dev|prod|preview|custom")
		return
	}

	// Smoke-test the URL + admin key BEFORE creating the row. Keeps a typo'd
	// URL from creating an unusable deployment that would just sit in the
	// dashboard returning errors. Both probes share a single 5-second
	// budget — we don't need to be patient with an "is this URL alive?" call.
	probeCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := probeAdoptedBackend(probeCtx, req.DeploymentURL, req.AdminKey); err != nil {
		var perr *adoptProbeError
		if errors.As(err, &perr) {
			writeError(w, perr.status, perr.code, perr.message)
			return
		}
		logErr("probe adopted backend", err)
		writeError(w, http.StatusBadGateway, "probe_failed", "Failed to reach the deployment URL")
		return
	}

	// Allocate (or validate) the name. UNIQUE on deployments.name catches
	// races against concurrent provisions / adoptions; the retry helper
	// regenerates only when the name was auto-allocated.
	var d models.Deployment
	d.ProjectID = projectID
	d.DeploymentType = req.DeploymentType
	d.Status = models.DeploymentStatusRunning
	d.IsDefault = req.IsDefault
	d.Reference = req.Reference
	d.CreatorUserID = uid
	d.DeploymentURL = req.DeploymentURL
	d.AdminKey = req.AdminKey
	d.Adopted = true

	finalName := req.Name
	err = synapsedb.WithRetryOnUniqueViolation(r.Context(), 5, func() error {
		var insertName string
		if req.Name != "" {
			insertName = req.Name
		} else {
			alloc, allocErr := h.allocateDeploymentName(r.Context())
			if allocErr != nil {
				return allocErr
			}
			insertName = alloc
		}
		// instance_secret is NOT NULL in the schema; adopted rows don't have
		// one (Synapse never generated it), so we store an empty string.
		// Nothing in the codebase uses instance_secret on adopted=true rows.
		err := h.DB.QueryRow(r.Context(), `
			INSERT INTO deployments (project_id, name, deployment_type, status,
			                          admin_key, instance_secret, deployment_url,
			                          is_default, reference, creator_user_id,
			                          adopted)
			VALUES ($1, $2, $3, $4, $5, '', $6, $7, NULLIF($8, ''), $9, true)
			RETURNING id, created_at
		`, projectID, insertName, req.DeploymentType, models.DeploymentStatusRunning,
			req.AdminKey, req.DeploymentURL, req.IsDefault, req.Reference, uid,
		).Scan(&d.ID, &d.CreatedAt)
		if err != nil {
			return err
		}
		finalName = insertName
		return nil
	})
	if err != nil {
		// A user-supplied name that collides surfaces here as a unique
		// violation that the retry helper couldn't paper over (since we
		// don't regenerate user-chosen names). Map to a friendly 409.
		if synapsedb.IsUniqueViolation(err) && req.Name != "" {
			writeError(w, http.StatusConflict, "name_taken", "A deployment with that name already exists")
			return
		}
		logErr("insert adopted deployment", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to record deployment")
		return
	}
	d.Name = finalName

	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     teamID,
		ActorID:    uid,
		Action:     audit.ActionAdoptDeployment,
		TargetType: audit.TargetDeployment,
		TargetID:   d.ID,
		Metadata: map[string]any{
			"name":           d.Name,
			"deploymentType": d.DeploymentType,
			"projectId":      projectID,
			"deploymentUrl":  d.DeploymentURL,
		},
	})
	writeJSON(w, http.StatusCreated, d)
}

// adoptProbeError carries a writeError-shaped triple out of the probe so the
// handler can emit the precise client error (bad URL vs bad admin key vs
// unreachable) without duplicating the if-chain at every call site.
type adoptProbeError struct {
	status  int
	code    string
	message string
}

func (e *adoptProbeError) Error() string { return e.code + ": " + e.message }

// probeAdoptedBackend hits two endpoints on the supplied URL: GET /version (is
// this a live Convex backend?) and POST /api/check_admin_key (does the supplied
// key work?). Either failure is mapped to a 4xx for the caller — we never want
// to record an adopted row that points at a bad URL or a wrong key.
func probeAdoptedBackend(ctx context.Context, baseURL, adminKey string) error {
	client := &http.Client{Timeout: 4 * time.Second}

	// /version — quick reachability check. Convex backends respond with
	// {"version": "0.x.y"}; we don't parse, just want a 2xx so we know the
	// URL is real and the cert (if https) validates.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/version", nil)
	if err != nil {
		return &adoptProbeError{http.StatusBadRequest, "invalid_url", "deploymentUrl is not a valid URL"}
	}
	resp, err := client.Do(req)
	if err != nil {
		return &adoptProbeError{http.StatusBadGateway, "probe_failed", "Could not reach deploymentUrl"}
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return &adoptProbeError{http.StatusBadGateway, "probe_failed",
			"deploymentUrl returned HTTP " + http.StatusText(resp.StatusCode) + " for /version"}
	}

	// /api/check_admin_key — Convex's admin-key validator. Body is
	// {"adminKey": "<key>"}; 200 = valid, 401 = invalid. Any other code is
	// "the URL responds but isn't a Convex backend" → bad URL.
	body := strings.NewReader(`{"adminKey":` + jsonString(adminKey) + `}`)
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/check_admin_key", body)
	if err != nil {
		return &adoptProbeError{http.StatusBadRequest, "invalid_url", "deploymentUrl is not a valid URL"}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		return &adoptProbeError{http.StatusBadGateway, "probe_failed", "Could not reach deploymentUrl"}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &adoptProbeError{http.StatusBadRequest, "invalid_admin_key", "adminKey was rejected by the deployment"}
	default:
		return &adoptProbeError{http.StatusBadGateway, "probe_failed",
			"deploymentUrl /api/check_admin_key returned HTTP " + http.StatusText(resp.StatusCode)}
	}
}

// jsonString emits a JSON-quoted version of s. We avoid encoding/json's
// Marshal-allocates-a-buffer overhead by handling only the characters that
// appear in admin keys (printable ASCII plus quote/backslash); anything else
// would suggest an unsupported key format.
func jsonString(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"', '\\':
			out = append(out, '\\', c)
		default:
			out = append(out, c)
		}
	}
	out = append(out, '"')
	return string(out)
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
		       d.deployment_url, d.is_default, d.reference, d.creator_user_id, d.created_at,
		       d.adopted
		  FROM deployments d
		 WHERE ` + joinAnd(where) + `
		 ORDER BY d.is_default DESC, d.created_at DESC
		 LIMIT 1
	`
	var d models.Deployment
	var url, refDB, creator *string
	err := h.DB.QueryRow(r.Context(), query, args...).Scan(
		&d.ID, &d.ProjectID, &d.Name, &d.DeploymentType, &d.Status,
		&url, &d.IsDefault, &refDB, &creator, &d.CreatedAt, &d.Adopted,
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
	d, _, t, role, ok := h.loadDeploymentForRequest(w, r)
	if !ok {
		return
	}
	if role != models.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "Only team admins can delete deployments")
		return
	}

	// Race with the async provisioner: if the row is still 'provisioning',
	// the goroutine in provisionInBackground may be mid-Provision (creating
	// container + volume right now). Calling Destroy here would race the
	// volume mount and emit "volume in use" errors. Instead, just flip the
	// row to 'deleted' — the provisioning goroutine re-reads status after
	// Provision and tears down whatever it built when it sees the change.
	if d.Status == models.DeploymentStatusProvisioning {
		if _, err := h.DB.Exec(r.Context(), `
			UPDATE deployments
			   SET status = 'deleted'
			 WHERE id = $1
		`, d.ID); err != nil {
			logErr("mark provisioning row deleted", err)
			writeError(w, http.StatusInternalServerError, "internal", "Database error")
			return
		}
		uid, _ := auth.UserID(r.Context())
		_ = audit.Record(r.Context(), h.DB, audit.Options{
			TeamID:     t.ID,
			ActorID:    uid,
			Action:     audit.ActionDeleteDeployment,
			TargetType: audit.TargetDeployment,
			TargetID:   d.ID,
			Metadata:   map[string]any{"name": d.Name, "wasProvisioning": true},
		})
		writeJSON(w, http.StatusOK, map[string]string{"name": d.Name, "status": "deleted"})
		return
	}

	// Adopted deployments are external — Synapse never created the
	// container or volume, so there's nothing to tear down. Just unregister
	// the row. The actual backend keeps running until the operator who
	// owns it stops it.
	if !d.Adopted {
		// Tear down the container/volume first; if that fails, leave the row
		// alone so the operator can retry. A successful Destroy is idempotent.
		if err := h.Docker.Destroy(r.Context(), d.Name); err != nil {
			logErr("docker destroy", err)
			writeError(w, http.StatusInternalServerError, "destroy_failed", err.Error())
			return
		}
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

	uid, _ := auth.UserID(r.Context())
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     t.ID,
		ActorID:    uid,
		Action:     audit.ActionDeleteDeployment,
		TargetType: audit.TargetDeployment,
		TargetID:   d.ID,
		Metadata:   map[string]any{"name": d.Name},
	})
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

// ---------- GET /v1/deployments/{name}/cli_credentials ----------
//
// Returns the env-var pair that `npx convex` looks for when running against a
// self-hosted backend (see CONVEX_SELF_HOSTED_URL_VAR_NAME and
// CONVEX_SELF_HOSTED_ADMIN_KEY_VAR_NAME in the Convex CLI's
// `lib/utils/utils.ts`). The CLI's deployment-selection code (in
// `lib/deploymentSelection.ts`) treats the presence of *both* vars as the
// "selfHosted" path: it skips Big Brain and talks straight to deploymentUrl.
//
// We also return a copy-paste shell snippet so the dashboard can show one
// code block instead of forcing the user to assemble the export lines from
// two fields.
//
// Intentionally a member-level endpoint (same gate as /auth) — anyone who
// can launch the standalone Convex dashboard against a deployment can also
// use the CLI against it.

type cliCredentialsResp struct {
	DeploymentName string `json:"deploymentName"`
	ConvexURL      string `json:"convexUrl"`
	AdminKey       string `json:"adminKey"`
	// ExportSnippet is a shell-pasteable string that sets both env vars at
	// once. Built server-side so the dashboard doesn't have to hand-roll the
	// formatting (and so any future change to the env-var names is owned by
	// one file).
	ExportSnippet string `json:"exportSnippet"`
}

func (h *DeploymentsHandler) deploymentCLICredentials(w http.ResponseWriter, r *http.Request) {
	d, _, _, _, ok := h.loadDeploymentForRequest(w, r)
	if !ok {
		return
	}
	snippet := "export CONVEX_SELF_HOSTED_URL=" + shellQuote(d.DeploymentURL) + "\n" +
		"export CONVEX_SELF_HOSTED_ADMIN_KEY=" + shellQuote(d.AdminKey)
	writeJSON(w, http.StatusOK, cliCredentialsResp{
		DeploymentName: d.Name,
		ConvexURL:      d.DeploymentURL,
		AdminKey:       d.AdminKey,
		ExportSnippet:  snippet,
	})
}

// shellQuote produces a single-quoted POSIX shell literal that survives
// values containing spaces, '$', or other metacharacters. A naked admin key
// is hex-only today, but quoting future-proofs against that ever changing
// (e.g. once Synapse derives a real backend admin key like
// "prod:happy-cat-1234|abc:def…").
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	// Replace each ' with '\'' (close, escaped quote, reopen).
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
