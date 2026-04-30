package api

import (
	"context"
	"errors"
	"fmt"
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

	// PublicURL + ProxyEnabled control the URL shape returned by /auth
	// and /cli_credentials. See RouterDeps.PublicURL for the rules.
	// Empty PublicURL keeps the legacy "http://127.0.0.1:<port>" shape.
	PublicURL    string
	ProxyEnabled bool

	// HA carries cluster-wide HA defaults. Empty when HA isn't enabled
	// — the handler refuses requests that ask for ha:true in that case.
	HA HAConfig

	// Crypto encrypts the per-deployment Postgres + S3 secrets stored
	// in deployment_storage. Required when HA is enabled; unused when
	// HA.Enabled is false. Type is interface{ EncryptString(string)
	// ([]byte, error) } so we don't import internal/crypto here —
	// production wiring passes *crypto.SecretBox.
	Crypto SecretEncrypter
}

// publicDeploymentURL returns the URL a *remote* caller (the operator's
// laptop running `npx convex`, the dashboard reaching out from a
// browser tab) should use to reach this deployment. The provisioner
// stores "http://127.0.0.1:<port>" — fine for synapse-side healthchecks
// but useless from outside the host.
//
// Decision matrix:
//   - PublicURL empty                      → return d.DeploymentURL (legacy)
//   - PublicURL set, ProxyEnabled true     → "<PublicURL>/d/<name>"
//   - PublicURL set, ProxyEnabled false    → "<PublicURL>:<host_port>"
//
// Adopted deployments keep d.DeploymentURL — the operator already
// supplied a public URL when they registered it.
func (h *DeploymentsHandler) publicDeploymentURL(d *models.Deployment) string {
	if h.PublicURL == "" || d.Adopted {
		return d.DeploymentURL
	}
	if h.ProxyEnabled {
		return h.PublicURL + "/d/" + d.Name
	}
	if d.HostPort == 0 {
		return d.DeploymentURL
	}
	return fmt.Sprintf("%s:%d", h.PublicURL, d.HostPort)
}

// SecretEncrypter is the *crypto.SecretBox subset the handler needs.
type SecretEncrypter interface {
	EncryptString(s string) ([]byte, error)
}

func (h *DeploymentsHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Route("/{name}", func(r chi.Router) {
		r.Get("/", h.getDeployment)
		r.Post("/delete", h.deleteDeployment)
		r.Get("/auth", h.deploymentAuth)
		r.Get("/cli_credentials", h.deploymentCLICredentials)
		r.Post("/create_deploy_key", h.createDeployKey)
		r.Post("/upgrade_to_ha", h.upgradeToHA)
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
		       d.ha_enabled, d.replica_count,
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
		&d.HAEnabled, &d.ReplicaCount,
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
	ports, err := h.allocatePorts(ctx, 1)
	if err != nil {
		return 0, err
	}
	return ports[0], nil
}

// allocatePorts returns N free host ports from the configured range.
// "Free" considers both the legacy deployments.host_port and the v0.5
// deployment_replicas.host_port columns so single- and HA-mode rows
// don't collide with each other.
//
// Concurrency: the allocator picks candidates in a single SELECT but
// commits them via separate INSERTs in the caller's transaction. Two
// callers can pick overlapping ports; the UNIQUE constraints catch the
// loser, the retry helper picks fresh candidates and tries again.
func (h *DeploymentsHandler) allocatePorts(ctx context.Context, n int) ([]int, error) {
	if n <= 0 {
		return nil, errors.New("allocatePorts: n must be > 0")
	}
	rows, err := h.DB.Query(ctx, `
		WITH used AS (
		  SELECT host_port FROM deployments
		   WHERE host_port IS NOT NULL AND status <> 'deleted'
		  UNION
		  SELECT host_port FROM deployment_replicas
		   WHERE host_port IS NOT NULL AND status <> 'stopped' AND status <> 'failed'
		)
		SELECT p FROM (
		  SELECT generate_series($1::int, $2::int) AS p
		) candidates
		 WHERE p NOT IN (SELECT host_port FROM used)
		 ORDER BY p
		 LIMIT $3
	`, h.PortRangeMin, h.PortRangeMax, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]int, 0, n)
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) < n {
		return nil, fmt.Errorf("allocatePorts: only %d of %d requested ports free in range [%d,%d]",
			len(out), n, h.PortRangeMin, h.PortRangeMax)
	}
	return out, nil
}

// perDeploymentStorage carries the resolved per-deployment Postgres +
// S3 connection material. Every field is plaintext at this point —
// EncryptString runs separately for the credential-bearing fields.
type perDeploymentStorage struct {
	PostgresURL     string
	DBSchema        string
	S3Endpoint      string
	S3Region        string
	S3AccessKey     string
	S3SecretKey     string
	BucketFiles     string
	BucketModules   string
	BucketSearch    string
	BucketExports   string
	BucketSnapshots string
}

// derivePerDeploymentStorage builds the storage spec for a new HA
// deployment by combining cluster-wide defaults with per-deployment
// overrides. The Postgres URL keeps the cluster's host/port/credentials
// but swaps the database name to convex_<deployment>; the S3 buckets
// are <prefix>-<deployment>-{files,modules,search,exports,snapshots}.
func derivePerDeploymentStorage(deploymentName string, cluster HAConfig, overrides *haOverrides) perDeploymentStorage {
	s := perDeploymentStorage{
		PostgresURL: cluster.BackendPostgresURL,
		S3Endpoint:  cluster.BackendS3Endpoint,
		S3Region:    cluster.BackendS3Region,
		S3AccessKey: cluster.BackendS3AccessKey,
		S3SecretKey: cluster.BackendS3SecretKey,
	}
	if overrides != nil {
		if overrides.PostgresURL != "" {
			s.PostgresURL = overrides.PostgresURL
		}
		if overrides.S3Endpoint != "" {
			s.S3Endpoint = overrides.S3Endpoint
		}
		if overrides.S3Region != "" {
			s.S3Region = overrides.S3Region
		}
		if overrides.S3AccessKey != "" {
			s.S3AccessKey = overrides.S3AccessKey
		}
		if overrides.S3SecretKey != "" {
			s.S3SecretKey = overrides.S3SecretKey
		}
	}
	// `convex_<deployment>` becomes the schema/database name. Swap the
	// last path segment of the Postgres URL — the operator ran the
	// cluster default at, say, `postgres://.../convex_admin`, and we
	// route this deployment to `postgres://.../convex_happy_cat_1234`.
	// A dedicated schema/database keeps the upstream backend's tables
	// from colliding across tenants.
	dbName := "convex_" + sqlIdent(deploymentName)
	s.DBSchema = dbName
	s.PostgresURL = swapPostgresDatabase(s.PostgresURL, dbName)

	prefix := cluster.BackendBucketPrefix
	if prefix == "" {
		prefix = "convex"
	}
	bucketBase := prefix + "-" + sqlIdent(deploymentName)
	s.BucketFiles = bucketBase + "-files"
	s.BucketModules = bucketBase + "-modules"
	s.BucketSearch = bucketBase + "-search"
	s.BucketExports = bucketBase + "-exports"
	s.BucketSnapshots = bucketBase + "-snapshots"
	return s
}

// sqlIdent normalises a deployment name for use as a SQL identifier
// fragment / S3 bucket name suffix. We replace dashes with underscores
// for Postgres database names (Postgres allows `-` only in quoted
// identifiers, and we'd rather not quote at backend-config time).
// Buckets keep the dash form via the caller — same string fed into
// "convex-{name}-files" works for S3 since S3 buckets allow dashes.
func sqlIdent(deploymentName string) string {
	out := make([]byte, 0, len(deploymentName))
	for i := 0; i < len(deploymentName); i++ {
		c := deploymentName[i]
		switch {
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+32)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// swapPostgresDatabase replaces the path segment (the database name) of
// a Postgres URL with the supplied dbName, preserving everything else
// (host, port, user, password, query string).
//
// Why not net/url? net/url cleans the path component on serialisation
// and we'd risk losing a non-default port or trailing slash. The
// targeted replacement here is more predictable for the limited input
// shapes we accept (postgres://user:pass@host:port/db?params).
func swapPostgresDatabase(rawURL, dbName string) string {
	// Find the slash after host[:port] — that's where the db name starts.
	// Inputs look like postgres://[user[:pass]@]host[:port]/db[?params].
	scheme := ""
	for _, p := range []string{"postgres://", "postgresql://"} {
		if len(rawURL) >= len(p) && rawURL[:len(p)] == p {
			scheme = p
			break
		}
	}
	if scheme == "" {
		// Unsupported shape — return as-is rather than mangle. The
		// backend container will surface the URL to the operator.
		return rawURL
	}
	rest := rawURL[len(scheme):]
	slash := -1
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			slash = i
			break
		}
	}
	prefix := rawURL
	suffix := ""
	if slash >= 0 {
		prefix = rawURL[:len(scheme)+slash+1]
		afterDB := rest[slash+1:]
		// Cut off existing db name; keep query string intact.
		q := -1
		for i := 0; i < len(afterDB); i++ {
			if afterDB[i] == '?' {
				q = i
				break
			}
		}
		if q >= 0 {
			suffix = afterDB[q:]
		}
	} else {
		// No path at all — append a / before the db name.
		prefix = rawURL + "/"
	}
	return prefix + dbName + suffix
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

	// HA, if true, provisions the deployment with replica_count=2 backed
	// by Postgres + S3 instead of SQLite + local volume. Refused with
	// 400 ha_disabled when SYNAPSE_HA_ENABLED isn't true on this Synapse
	// instance. Default false → existing single-replica behavior.
	HA bool `json:"ha,omitempty"`

	// Per-deployment overrides for the cluster-wide HA defaults. All
	// optional; any field left empty falls back to the value configured
	// at the Synapse-process level (SYNAPSE_BACKEND_POSTGRES_URL etc).
	HAOverrides *haOverrides `json:"haOverrides,omitempty"`
}

type haOverrides struct {
	PostgresURL string `json:"postgresUrl,omitempty"`
	S3Endpoint  string `json:"s3Endpoint,omitempty"`
	S3Region    string `json:"s3Region,omitempty"`
	S3AccessKey string `json:"s3AccessKey,omitempty"`
	S3SecretKey string `json:"s3SecretKey,omitempty"`
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
	if req.HA {
		if !h.HA.Enabled {
			writeError(w, http.StatusBadRequest, "ha_disabled",
				"HA-per-deployment is disabled on this Synapse instance (set SYNAPSE_HA_ENABLED=true)")
			return
		}
		if missing := missingHAClusterFields(h.HA); missing != "" {
			writeError(w, http.StatusBadRequest, "ha_misconfigured",
				"HA is enabled but cluster config is incomplete: "+missing)
			return
		}
		if h.Crypto == nil {
			writeError(w, http.StatusBadRequest, "ha_misconfigured",
				"HA is enabled but SYNAPSE_STORAGE_KEY is not set")
			return
		}
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

	replicaCount := 1
	if req.HA {
		replicaCount = 2
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
	d.HAEnabled = req.HA
	d.ReplicaCount = replicaCount

	var name string
	var ports []int
	var adminKey string

	err = synapsedb.WithRetryOnUniqueViolation(r.Context(), 5, func() error {
		var allocErr error
		name, allocErr = h.allocateDeploymentName(r.Context())
		if allocErr != nil {
			return allocErr
		}
		ports, allocErr = h.allocatePorts(r.Context(), replicaCount)
		if allocErr != nil {
			return allocErr
		}
		adminKey, allocErr = h.Docker.GenerateAdminKey(r.Context(), name, instanceSecret)
		if allocErr != nil {
			return allocErr
		}
		// Insert the deployment row + N replica rows + (optional) storage row +
		// N provisioning jobs in one transaction so we never end up with a
		// half-formed deployment.
		tx, txErr := h.DB.Begin(r.Context())
		if txErr != nil {
			return txErr
		}
		defer tx.Rollback(r.Context())

		// deployments.host_port stays in the row for single-replica
		// back-compat (legacy code paths still read it). For HA, we
		// store the first replica's port so the legacy fallback still
		// resolves to something live during a roll-out.
		primaryPort := ports[0]
		if txErr = tx.QueryRow(r.Context(), `
			INSERT INTO deployments (project_id, name, deployment_type, status, host_port,
			                          admin_key, instance_secret, is_default, reference,
			                          creator_user_id, ha_enabled, replica_count)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''), $10, $11, $12)
			RETURNING id, created_at
		`, projectID, name, req.Type, models.DeploymentStatusProvisioning, primaryPort,
			adminKey, instanceSecret, req.IsDefault, req.Reference, uid,
			req.HA, replicaCount,
		).Scan(&d.ID, &d.CreatedAt); txErr != nil {
			return txErr
		}

		// Per-deployment storage row (HA only). Each deployment gets
		// its own database name + bucket prefix so multiple HA
		// deployments can share a single Postgres + S3 cluster without
		// stepping on each other.
		if req.HA {
			storage := derivePerDeploymentStorage(name, h.HA, req.HAOverrides)
			dbURLEnc, encErr := h.Crypto.EncryptString(storage.PostgresURL)
			if encErr != nil {
				return fmt.Errorf("encrypt db_url: %w", encErr)
			}
			s3KeyEnc, encErr := h.Crypto.EncryptString(storage.S3AccessKey)
			if encErr != nil {
				return fmt.Errorf("encrypt s3_access_key: %w", encErr)
			}
			s3SecretEnc, encErr := h.Crypto.EncryptString(storage.S3SecretKey)
			if encErr != nil {
				return fmt.Errorf("encrypt s3_secret_key: %w", encErr)
			}
			if _, txErr = tx.Exec(r.Context(), `
				INSERT INTO deployment_storage (deployment_id, db_kind, db_url_enc, db_schema,
				                                 s3_endpoint, s3_region,
				                                 s3_access_key_enc, s3_secret_key_enc,
				                                 s3_bucket_files, s3_bucket_modules, s3_bucket_search,
				                                 s3_bucket_exports, s3_bucket_snapshots)
				VALUES ($1, 'postgres', $2, $3,
				        $4, $5, $6, $7,
				        $8, $9, $10, $11, $12)
			`, d.ID, dbURLEnc, storage.DBSchema,
				storage.S3Endpoint, storage.S3Region, s3KeyEnc, s3SecretEnc,
				storage.BucketFiles, storage.BucketModules, storage.BucketSearch,
				storage.BucketExports, storage.BucketSnapshots,
			); txErr != nil {
				return txErr
			}
		}

		// Replica rows + their provisioning jobs.
		for idx, port := range ports {
			var replicaID string
			if txErr = tx.QueryRow(r.Context(), `
				INSERT INTO deployment_replicas (deployment_id, replica_index, host_port, status)
				VALUES ($1, $2, $3, 'provisioning')
				RETURNING id
			`, d.ID, idx, port).Scan(&replicaID); txErr != nil {
				return txErr
			}
			if txErr = provisioner.EnqueueReplica(r.Context(), tx, d.ID, replicaID, h.HealthcheckViaNetwork); txErr != nil {
				return txErr
			}
		}
		return tx.Commit(r.Context())
	})
	if err != nil {
		logErr("insert deployment", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to record deployment")
		return
	}
	d.Name = name
	if len(ports) > 0 {
		d.HostPort = ports[0]
	}
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

// missingHAClusterFields returns "" when every required HA cluster
// default is populated, or a human-readable list of the missing fields
// otherwise. Used to gate HA create_deployment requests behind a
// well-configured Synapse process — partial config (e.g. Postgres URL
// but no S3 endpoint) gets a 400 with a precise hint instead of a
// stack trace from a half-provisioned container.
func missingHAClusterFields(c HAConfig) string {
	missing := make([]string, 0, 5)
	if c.BackendPostgresURL == "" {
		missing = append(missing, "SYNAPSE_BACKEND_POSTGRES_URL")
	}
	if c.BackendS3Endpoint == "" {
		missing = append(missing, "SYNAPSE_BACKEND_S3_ENDPOINT")
	}
	if c.BackendS3AccessKey == "" {
		missing = append(missing, "SYNAPSE_BACKEND_S3_ACCESS_KEY")
	}
	if c.BackendS3SecretKey == "" {
		missing = append(missing, "SYNAPSE_BACKEND_S3_SECRET_KEY")
	}
	if c.BackendBucketPrefix == "" {
		missing = append(missing, "SYNAPSE_BACKEND_S3_BUCKET_PREFIX")
	}
	if len(missing) == 0 {
		return ""
	}
	return strings.Join(missing, ", ")
}

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

	// /api/check_admin_key — Convex's admin-key validator. The backend
	// expects GET with the admin key either as `Authorization: Convex <key>`
	// or as a `?adminKey=` query param. 200 = valid, 401 = invalid. Any
	// other code means the URL responds but isn't a Convex backend.
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/check_admin_key", nil)
	if err != nil {
		return &adoptProbeError{http.StatusBadRequest, "invalid_url", "deploymentUrl is not a valid URL"}
	}
	req.Header.Set("Authorization", "Convex "+adminKey)
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

// ---------- POST /v1/deployments/{name}/upgrade_to_ha ----------

// upgradeToHAReq optionally lets the caller override the cluster-wide
// HA defaults (same shape as createDeployment.HAOverrides). Empty
// payload uses the SYNAPSE_BACKEND_* env defaults.
type upgradeToHAReq struct {
	HAOverrides *haOverrides `json:"haOverrides,omitempty"`
}

// upgradeToHA migrates an existing single-replica deployment to HA.
// The endpoint validates + enqueues the work; the actual mechanics
// (snapshot_export from the existing replica → provision 2 new HA
// replicas → snapshot_import → atomic swap) live on the worker side.
//
// Today the worker rejects upgrade_to_ha jobs with a clear error
// instead of corrupting state mid-migration. That makes the API
// surface stable so operators can wire it up; the heavy lifting is
// scheduled for v0.5.1 (see docs/V0_5_PLAN.md).
//
// Validation refuses early when:
//   - HA isn't enabled on this Synapse instance
//   - cluster config is incomplete
//   - the deployment is already HA (no-op)
//   - the deployment is adopted (Synapse doesn't manage its container,
//     can't migrate it)
//   - the deployment is in a non-running state (provisioning, failed,
//     stopped — operator should resolve before upgrading)
func (h *DeploymentsHandler) upgradeToHA(w http.ResponseWriter, r *http.Request) {
	d, _, t, role, ok := h.loadDeploymentForRequest(w, r)
	if !ok {
		return
	}
	if role != models.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "Only team admins can upgrade deployments")
		return
	}
	if !h.HA.Enabled {
		writeError(w, http.StatusBadRequest, "ha_disabled",
			"HA-per-deployment is disabled on this Synapse instance (set SYNAPSE_HA_ENABLED=true)")
		return
	}
	if missing := missingHAClusterFields(h.HA); missing != "" {
		writeError(w, http.StatusBadRequest, "ha_misconfigured",
			"HA is enabled but cluster config is incomplete: "+missing)
		return
	}
	if h.Crypto == nil {
		writeError(w, http.StatusBadRequest, "ha_misconfigured",
			"HA is enabled but SYNAPSE_STORAGE_KEY is not set")
		return
	}
	if d.HAEnabled {
		writeError(w, http.StatusConflict, "already_ha",
			"Deployment is already running in HA mode")
		return
	}
	if d.Adopted {
		writeError(w, http.StatusBadRequest, "cannot_upgrade_adopted",
			"Adopted deployments are managed externally; convert to HA on the source side and re-adopt")
		return
	}
	if d.Status != models.DeploymentStatusRunning {
		writeError(w, http.StatusConflict, "deployment_not_running",
			"Deployment must be 'running' to upgrade; current status: "+d.Status)
		return
	}

	var req upgradeToHAReq
	// readJSON requires a non-empty body; fall back to default config
	// when the caller posts an empty {}.
	if r.ContentLength > 0 {
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
	}

	// The worker's mechanical work isn't implemented yet — refuse with
	// the same code we use elsewhere for the "API exists, runtime
	// missing" pattern. Once the export/import flow lands, this branch
	// flips to enqueueing an `upgrade_to_ha` job and returning 202.
	writeError(w, http.StatusNotImplemented, "ha_upgrade_not_yet_implemented",
		"upgrade_to_ha is in flight (snapshot_export → re-provision → snapshot_import → swap); "+
			"see docs/V0_5_PLAN.md")

	// Audit the *attempt* even though we refused — operators trying to
	// upgrade need a paper trail of who pinged the endpoint and when.
	uid, _ := auth.UserID(r.Context())
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		TeamID:     t.ID,
		ActorID:    uid,
		Action:     audit.ActionUpgradeToHA,
		TargetType: audit.TargetDeployment,
		TargetID:   d.ID,
		Metadata: map[string]any{
			"name":   d.Name,
			"status": "rejected_not_yet_implemented",
		},
	})
	_ = req
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
		DeploymentURL:  h.publicDeploymentURL(d),
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
	publicURL := h.publicDeploymentURL(d)
	snippet := "export CONVEX_SELF_HOSTED_URL=" + shellQuote(publicURL) + "\n" +
		"export CONVEX_SELF_HOSTED_ADMIN_KEY=" + shellQuote(d.AdminKey)
	writeJSON(w, http.StatusOK, cliCredentialsResp{
		DeploymentName: d.Name,
		ConvexURL:      publicURL,
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
