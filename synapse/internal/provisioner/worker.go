// Package provisioner runs the persistent work queue that backs the async
// `create_deployment` flow. The HTTP handler inserts a row into
// `provisioning_jobs` (in the same transaction as the `deployments` row),
// returns 201 immediately, and a Worker on this node — or any other —
// dequeues the job and drives Docker.Provision to completion.
//
// Why a queue instead of an in-process goroutine? The previous design
// (handler spawns goroutine, goroutine updates the row when done) was
// non-recoverable: if the originating Synapse process died mid-provision,
// the work was lost and the deployment row was stuck in 'provisioning'
// for ten minutes until the orphan-sweep gave up on it. With work
// persisted as rows + SELECT FOR UPDATE SKIP LOCKED, any process
// restart resumes pending jobs and any sibling node (when we go
// multi-node) can claim work the dying node never finished.
package provisioner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	dockerprov "github.com/Iann29/synapse/internal/docker"
	"github.com/Iann29/synapse/internal/models"
)

// Provisioner is the subset of the docker client this package needs. The
// production wiring passes *dockerprov.Client; tests can substitute a fake.
type Provisioner interface {
	Provision(ctx context.Context, spec dockerprov.DeploymentSpec) (*dockerprov.DeploymentInfo, error)
	Destroy(ctx context.Context, deploymentName string) error
}

// Config controls the worker's polling cadence and failure-recovery window.
type Config struct {
	// PollInterval is how often the worker scans for pending jobs after a
	// drain returns empty. Reasonable: 100ms-2s. Too low: pointless DB
	// chatter. Too high: noticeable latency between handler-enqueue and
	// worker-pickup.
	PollInterval time.Duration

	// JobTimeout caps how long a single Provision call may run before the
	// worker considers it stuck and re-pends the row on the next process
	// start. Should comfortably exceed dockerprov.Provision's healthcheck
	// budget (60s) plus the cold-image-pull latency.
	JobTimeout time.Duration

	// Concurrency is the number of parallel goroutines pulling from the
	// queue. Defaults to 4. Each goroutine claims one job at a time via
	// SELECT FOR UPDATE SKIP LOCKED, so they shard naturally — no extra
	// coordination needed. Set to 1 for sequential debugging.
	Concurrency int

	// NodeID is recorded in claimed_by so an operator can grep `docker logs`
	// to find which Synapse instance handled which job. Free-form; we use
	// hostname when the operator hasn't set one explicitly.
	NodeID string

	// HealthcheckViaNetwork mirrors api.RouterDeps.HealthcheckViaNetwork —
	// the worker passes it through to dockerprov.DeploymentSpec.
	HealthcheckViaNetwork bool

	// Optional Aster runtime wiring. Empty keeps kind=aster brokerds on the
	// memory-store smoke path; set by SYNAPSE_ASTER_* process config.
	AsterPostgresURL     string
	AsterDBSchema        string
	AsterModulesHostPath string
}

func (c Config) sane() Config {
	out := c
	if out.PollInterval <= 0 {
		out.PollInterval = time.Second
	}
	if out.JobTimeout <= 0 {
		out.JobTimeout = 5 * time.Minute
	}
	if out.Concurrency <= 0 {
		out.Concurrency = 4
	}
	if out.NodeID == "" {
		out.NodeID = "synapse"
	}
	return out
}

// Worker pulls pending provision jobs from Postgres and runs them through
// the docker provisioner. Construct one per Synapse process; the SELECT
// FOR UPDATE SKIP LOCKED handles cross-process coordination, no advisory
// lock needed.
type Worker struct {
	DB     *pgxpool.Pool
	Docker Provisioner
	Config Config
	Logger *slog.Logger

	// Crypto, when non-nil, decrypts the per-deployment Postgres + S3
	// secrets in deployment_storage. Required for HA deployments;
	// single-replica deployments don't read it. nil disables HA-mode
	// provisioning (jobs with replica_id pointing at HA deployments
	// will fail with a clear error rather than panic).
	Crypto SecretDecrypter
}

// SecretDecrypter is the *crypto.SecretBox subset the worker needs.
// Pulled out behind an interface so the worker package doesn't import
// internal/crypto, keeping the dependency arrows clean.
type SecretDecrypter interface {
	DecryptString(ciphertext []byte) (string, error)
}

// Execer is the Exec subset of pgx — pool, conn, or tx all satisfy it.
// Lets the caller enqueue inside the same transaction as the
// deployments-row insert so the two are committed atomically.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Enqueue inserts a 'provision' job for deploymentID. The caller is expected
// to have just inserted (or about to insert in the same txn) the matching
// deployments row in 'provisioning' state.
//
// Single-replica deployments leave replicaID empty — the worker resolves
// replica_index=0 from the deployment automatically and behaves exactly
// like pre-v0.5. HA deployments enqueue one job per replica with the
// matching deployment_replicas.id.
func Enqueue(ctx context.Context, db Execer, deploymentID string, healthcheckViaNetwork bool) error {
	return EnqueueReplica(ctx, db, deploymentID, "", healthcheckViaNetwork)
}

// EnqueueReplica is the HA-aware variant of Enqueue. Pass the
// deployment_replicas.id so the worker can read per-replica info
// (replica_index, host_port) and set deployment_replicas.status when
// it finishes. An empty replicaID falls back to the legacy
// "no replica row" behaviour for backwards compatibility.
func EnqueueReplica(ctx context.Context, db Execer, deploymentID, replicaID string, healthcheckViaNetwork bool) error {
	if replicaID == "" {
		_, err := db.Exec(ctx, `
			INSERT INTO provisioning_jobs (deployment_id, kind, status, healthcheck_via_network)
			VALUES ($1, 'provision', 'pending', $2)
		`, deploymentID, healthcheckViaNetwork)
		return err
	}
	_, err := db.Exec(ctx, `
		INSERT INTO provisioning_jobs (deployment_id, replica_id, kind, status, healthcheck_via_network)
		VALUES ($1, $2, 'provision', 'pending', $3)
	`, deploymentID, replicaID, healthcheckViaNetwork)
	return err
}

// Run blocks until ctx is cancelled. On entry, performs a one-shot
// recovery sweep that re-pends jobs claimed but not finished within
// JobTimeout. Then spawns Concurrency worker loops, each independently
// dequeuing via SELECT FOR UPDATE SKIP LOCKED. Returns when ctx is done
// and all worker loops have exited.
func (w *Worker) Run(ctx context.Context) {
	cfg := w.Config.sane()
	logger := w.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("provisioner worker starting",
		"node_id", cfg.NodeID,
		"poll_interval", cfg.PollInterval,
		"job_timeout", cfg.JobTimeout,
		"concurrency", cfg.Concurrency)

	// Recovery: a previous synapse process (or this one before a SIGKILL)
	// may have left jobs in 'claimed' without finishing. Bring them back
	// to 'pending' so we can pick them up.
	if n, err := w.requeueStale(ctx, cfg); err != nil {
		logger.Error("provisioner: requeue stale failed", "err", err)
	} else if n > 0 {
		logger.Warn("provisioner: requeued stale jobs", "count", n)
	}

	done := make(chan struct{}, cfg.Concurrency)
	for i := 0; i < cfg.Concurrency; i++ {
		go w.loop(ctx, logger, cfg, done)
	}
	for i := 0; i < cfg.Concurrency; i++ {
		<-done
	}
	logger.Info("provisioner worker stopping")
}

// loop is one parallel consumer. It drains pending jobs, sleeps when the
// queue is empty, and exits cleanly on ctx cancellation.
func (w *Worker) loop(ctx context.Context, logger *slog.Logger, cfg Config, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	t := time.NewTicker(cfg.PollInterval)
	defer t.Stop()
	for {
		// Drain — pull pending rows until none left.
		for w.processOne(ctx, logger, cfg) {
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// requeueStale resets jobs that were claimed but not finished within
// JobTimeout. Runs on every Run() entry. Idempotent.
func (w *Worker) requeueStale(ctx context.Context, cfg Config) (int64, error) {
	// JobTimeout is a Go duration; convert to seconds for the Postgres
	// interval expression.
	tag, err := w.DB.Exec(ctx, `
		UPDATE provisioning_jobs
		   SET status     = 'pending',
		       claimed_by = NULL,
		       claimed_at = NULL
		 WHERE status     = 'claimed'
		   AND claimed_at < now() - ($1::int * interval '1 second')
	`, int(cfg.JobTimeout.Seconds()))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// processOne dequeues + runs a single job. Returns true if a job was
// processed (caller should loop), false if the queue was empty or the
// dequeue failed (caller should sleep and retry).
func (w *Worker) processOne(ctx context.Context, logger *slog.Logger, cfg Config) bool {
	job, ok := w.claimNext(ctx, logger, cfg)
	if !ok {
		return false
	}

	// Hard timeout for this single job. Independent of ctx so a server
	// shutdown still gives in-flight Provision a chance to settle within
	// the JobTimeout budget.
	jobCtx, cancel := context.WithTimeout(context.Background(), cfg.JobTimeout)
	defer cancel()

	w.runJob(jobCtx, logger, job)
	return true
}

type claimedJob struct {
	JobID                 int64
	DeploymentID          string
	Name                  string
	HostPort              int
	InstanceSecret        string
	HealthcheckViaNetwork bool

	// Kind selects which provisioning path the worker drives:
	// "" / "convex" → existing Convex backend container; "aster" →
	// Aster brokerd container (no host port, no SQLite/Postgres env).
	Kind string

	// Replica targeting (v0.5+). When ReplicaID is empty, this is a
	// pre-v0.5 single-replica job; the worker treats it as
	// replica_index=0 with no HA suffix. When set, the worker reads
	// replica_index from deployment_replicas and uses the HA-aware
	// container naming + storage env-vars.
	ReplicaID    string
	ReplicaIndex int
	HAEnabled    bool
	// Decrypted storage env (Postgres URL + S3) when the deployment
	// runs in HA mode. nil for SQLite/legacy.
	Storage *Storage
}

// Storage carries the per-deployment Postgres + S3 connection info that
// gets pushed into the container as env vars. Decrypted from
// deployment_storage by claimNext using the configured SecretBox.
type Storage struct {
	PostgresURL     string
	DoNotRequireSSL bool
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

// claimNext pulls the oldest pending job, atomically marks it 'claimed',
// and joins the deployments + (optional) deployment_replicas /
// deployment_storage rows for the data the worker needs.
//
// Single-replica jobs (replica_id IS NULL) read host_port from the
// deployments row, exactly like the pre-v0.5 worker. HA jobs
// (replica_id IS NOT NULL) read host_port from the replica row and
// load decrypted storage env vars from deployment_storage.
//
// SELECT … FOR UPDATE SKIP LOCKED is what makes this safe across N nodes:
// each worker grabs a different row, no contention, no doubled work.
func (w *Worker) claimNext(ctx context.Context, logger *slog.Logger, cfg Config) (claimedJob, bool) {
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		logger.Error("provisioner: begin tx", "err", err)
		return claimedJob{}, false
	}
	defer tx.Rollback(ctx)

	var j claimedJob
	var replicaID, replicaIndex *int64
	var replicaIDStr *string
	var replicaHostPort *int
	var deploymentHostPort *int
	err = tx.QueryRow(ctx, `
		SELECT j.id, j.deployment_id, j.replica_id::text,
		       d.name, d.host_port, d.instance_secret, d.ha_enabled, d.kind,
		       r.replica_index, r.host_port,
		       j.healthcheck_via_network
		  FROM provisioning_jobs j
		  JOIN deployments d ON d.id = j.deployment_id
		  LEFT JOIN deployment_replicas r ON r.id = j.replica_id
		 WHERE j.status = 'pending'
		 ORDER BY j.created_at ASC
		 FOR UPDATE OF j SKIP LOCKED
		 LIMIT 1
	`).Scan(&j.JobID, &j.DeploymentID, &replicaIDStr,
		&j.Name, &deploymentHostPort, &j.InstanceSecret, &j.HAEnabled, &j.Kind,
		&replicaIndex, &replicaHostPort,
		&j.HealthcheckViaNetwork)
	_ = replicaID
	if errors.Is(err, pgx.ErrNoRows) {
		return claimedJob{}, false
	}
	if err != nil {
		logger.Error("provisioner: claim query", "err", err)
		return claimedJob{}, false
	}

	// Decide which host_port wins. If we joined a replica row, prefer
	// it (the ground truth for HA deployments). Otherwise fall back to
	// the deployments row.
	if replicaHostPort != nil {
		j.HostPort = *replicaHostPort
	} else if deploymentHostPort != nil {
		j.HostPort = *deploymentHostPort
	}
	if replicaIDStr != nil && *replicaIDStr != "" {
		j.ReplicaID = *replicaIDStr
		if replicaIndex != nil {
			j.ReplicaIndex = int(*replicaIndex)
		}
	}

	// Load decrypted storage env vars for HA deployments. Failure is
	// terminal — without storage we can't provision; mark the job
	// failed and let the worker move on.
	if j.HAEnabled {
		if w.Crypto == nil {
			logger.Error("provisioner: HA job seen but worker has no crypto helper",
				"job_id", j.JobID, "deployment_id", j.DeploymentID)
			// Don't claim — leave it pending so a properly-configured
			// worker can pick it up. (Otherwise we'd flap the row to
			// failed and prevent recovery.)
			return claimedJob{}, false
		}
		storage, loadErr := loadStorage(ctx, tx, w.Crypto, j.DeploymentID)
		if loadErr != nil {
			logger.Error("provisioner: load deployment_storage failed",
				"job_id", j.JobID, "deployment_id", j.DeploymentID, "err", loadErr)
			// Same reasoning — leave the job pending; no claim.
			return claimedJob{}, false
		}
		j.Storage = storage
	}

	if _, err := tx.Exec(ctx, `
		UPDATE provisioning_jobs
		   SET status = 'claimed',
		       claimed_by = $1,
		       claimed_at = now(),
		       attempts = attempts + 1
		 WHERE id = $2
	`, cfg.NodeID, j.JobID); err != nil {
		logger.Error("provisioner: mark claimed", "err", err, "job_id", j.JobID)
		return claimedJob{}, false
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("provisioner: claim commit", "err", err, "job_id", j.JobID)
		return claimedJob{}, false
	}
	return j, true
}

// loadStorage reads a deployment_storage row and decrypts the
// connection material so the worker can hand it to Provision as
// plaintext env vars. Errors (row missing, decryption failure) bubble
// up — the caller leaves the job pending so a re-keyed worker can
// retry without the bad row clogging the queue.
func loadStorage(ctx context.Context, tx pgx.Tx, dec SecretDecrypter, deploymentID string) (*Storage, error) {
	var (
		dbURLEnc        []byte
		s3AccessKeyEnc  []byte
		s3SecretKeyEnc  []byte
		s3Endpoint      string
		s3Region        string
		bucketFiles     string
		bucketModules   string
		bucketSearch    string
		bucketExports   string
		bucketSnapshots string
	)
	err := tx.QueryRow(ctx, `
		SELECT db_url_enc, s3_access_key_enc, s3_secret_key_enc,
		       s3_endpoint, s3_region,
		       s3_bucket_files, s3_bucket_modules, s3_bucket_search,
		       s3_bucket_exports, s3_bucket_snapshots
		  FROM deployment_storage
		 WHERE deployment_id = $1
	`, deploymentID).Scan(&dbURLEnc, &s3AccessKeyEnc, &s3SecretKeyEnc,
		&s3Endpoint, &s3Region,
		&bucketFiles, &bucketModules, &bucketSearch,
		&bucketExports, &bucketSnapshots)
	if err != nil {
		return nil, fmt.Errorf("read deployment_storage: %w", err)
	}

	dbURL, err := dec.DecryptString(dbURLEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt db_url: %w", err)
	}
	s3Access, err := dec.DecryptString(s3AccessKeyEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt s3_access_key: %w", err)
	}
	s3Secret, err := dec.DecryptString(s3SecretKeyEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt s3_secret_key: %w", err)
	}

	return &Storage{
		PostgresURL: dbURL,
		// Operators on internal Postgres / MinIO over plain HTTP need
		// the backend's DO_NOT_REQUIRE_SSL=1; we infer it from the URL
		// scheme on the Postgres side, and the S3 endpoint never sees
		// this flag (S3 SDK negotiates separately).
		DoNotRequireSSL: !hasSSLPrefix(dbURL),
		S3Endpoint:      s3Endpoint,
		S3Region:        s3Region,
		S3AccessKey:     s3Access,
		S3SecretKey:     s3Secret,
		BucketFiles:     bucketFiles,
		BucketModules:   bucketModules,
		BucketSearch:    bucketSearch,
		BucketExports:   bucketExports,
		BucketSnapshots: bucketSnapshots,
	}, nil
}

func hasSSLPrefix(url string) bool {
	// Quick heuristic — `?sslmode=require` (or stricter) anywhere in
	// the URL means TLS is enforced; absence means we should pass
	// DO_NOT_REQUIRE_SSL=1 to the backend so it doesn't refuse the
	// connection. Not a parser; a bad URL fails later in the backend
	// regardless.
	return contains(url, "sslmode=require") ||
		contains(url, "sslmode=verify-ca") ||
		contains(url, "sslmode=verify-full")
}

func contains(s, sub string) bool {
	return len(sub) <= len(s) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// runJob executes the docker provisioning for a claimed job. Updates the
// deployment row to 'running' on success or 'failed' on error, and marks
// the job 'done' / 'failed' in lockstep.
//
// Race with delete: the API handler accepts /delete on a 'provisioning'
// row and just flips status to 'deleted' (it can't safely call Destroy
// while we're mid-create). When we're done, we re-read status: if it's
// no longer 'provisioning', we tear down whatever we built.
func (w *Worker) runJob(ctx context.Context, logger *slog.Logger, j claimedJob) {
	// Panic shield so a bad job never kills the worker.
	defer func() {
		if rec := recover(); rec != nil {
			logger.Error("provisioner: job panicked",
				"job_id", j.JobID,
				"deployment", j.Name,
				"panic", rec,
				"stack", string(debug.Stack()))
			w.markFailed(j.JobID, j.DeploymentID, "panic in worker")
		}
	}()

	// Pre-check: skip costly Provision when the work is no longer needed.
	//
	// HA jobs check the *replica* row's status — sibling replicas might
	// have already flipped the deployment row to 'running' but THIS
	// replica still has work to do. Single-replica jobs check the
	// deployment row directly (legacy behaviour: deployment.status is
	// the only signal).
	var current string
	var query string
	var arg string
	if j.ReplicaID != "" {
		query = `SELECT status FROM deployment_replicas WHERE id = $1`
		arg = j.ReplicaID
	} else {
		query = `SELECT status FROM deployments WHERE id = $1`
		arg = j.DeploymentID
	}
	if err := w.DB.QueryRow(ctx, query, arg).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logger.Info("provisioner: row gone before provision; skipping",
				"deployment_id", j.DeploymentID, "name", j.Name, "replica", j.ReplicaIndex)
			w.markDone(j.JobID)
			return
		}
		// Transient DB error — fall through to Provision and let the
		// post-Provision UPDATE catch it.
		logger.Warn("provisioner: pre-check status query failed",
			"deployment_id", j.DeploymentID, "err", err)
	} else if current != models.DeploymentStatusProvisioning {
		logger.Info("provisioner: row no longer provisioning; skipping",
			"deployment_id", j.DeploymentID, "name", j.Name,
			"replica", j.ReplicaIndex, "status", current)
		w.markDone(j.JobID)
		return
	}

	spec := dockerprov.DeploymentSpec{
		Name:                  j.Name,
		InstanceSecret:        j.InstanceSecret,
		HostPort:              j.HostPort,
		EnvVars:               map[string]string{},
		HealthcheckViaNetwork: j.HealthcheckViaNetwork,
		HAReplica:             j.HAEnabled,
		ReplicaIndex:          j.ReplicaIndex,
		Kind:                  j.Kind,
	}
	if j.Kind == models.DeploymentKindAster {
		spec.AsterPostgresURL = w.Config.AsterPostgresURL
		spec.AsterDBSchema = w.Config.AsterDBSchema
		spec.AsterModulesHostPath = w.Config.AsterModulesHostPath
	}
	if j.Storage != nil {
		spec.Storage = &dockerprov.StorageEnv{
			PostgresURL:     j.Storage.PostgresURL,
			DoNotRequireSSL: j.Storage.DoNotRequireSSL,
			S3Endpoint:      j.Storage.S3Endpoint,
			S3Region:        j.Storage.S3Region,
			S3AccessKey:     j.Storage.S3AccessKey,
			S3SecretKey:     j.Storage.S3SecretKey,
			BucketFiles:     j.Storage.BucketFiles,
			BucketModules:   j.Storage.BucketModules,
			BucketSearch:    j.Storage.BucketSearch,
			BucketExports:   j.Storage.BucketExports,
			BucketSnapshots: j.Storage.BucketSnapshots,
		}
	}

	info, err := w.Docker.Provision(ctx, spec)
	if err != nil {
		logger.Error("provisioner: provision failed",
			"job_id", j.JobID, "deployment", j.Name, "replica", j.ReplicaIndex, "err", err)
		w.markFailed(j.JobID, j.DeploymentID, err.Error())
		return
	}

	// Update both the replica (when one is targeted) and the deployment
	// row. Single-replica deployments (legacy + v0.5 single mode) only
	// have replica_index=0; we still write its replica row so the proxy
	// resolver and health worker can read uniformly from
	// deployment_replicas.
	if j.ReplicaID != "" {
		w.markReplicaRunning(ctx, logger, j.ReplicaID, info.ContainerID)
	}

	// Atomic UPDATE WHERE status='provisioning' — if /delete flipped the
	// row to 'deleted' while we were creating, this matches 0 rows and
	// we know to tear down.
	tag, err := w.DB.Exec(ctx, `
		UPDATE deployments
		   SET status         = $1,
		       container_id   = COALESCE(container_id, $2),
		       deployment_url = COALESCE(deployment_url, $3),
		       last_deploy_at = now()
		 WHERE id = $4
		   AND status = 'provisioning'
	`, models.DeploymentStatusRunning, info.ContainerID, info.DeploymentURL, j.DeploymentID)
	if err != nil {
		logger.Error("provisioner: update deployment", "err", err, "job_id", j.JobID)
		w.markFailed(j.JobID, j.DeploymentID, err.Error())
		return
	}

	if tag.RowsAffected() == 0 {
		// HA: status may already be "running" because a sibling replica
		// finished first — that's fine, leave it. Only treat 0 rows as
		// "deployment was deleted" when there's no other running replica.
		if j.HAEnabled {
			var runningReplicas int
			_ = w.DB.QueryRow(ctx, `
				SELECT count(*) FROM deployment_replicas
				 WHERE deployment_id = $1 AND status = 'running'
			`, j.DeploymentID).Scan(&runningReplicas)
			if runningReplicas > 0 {
				w.markDone(j.JobID)
				logger.Info("provisioner: replica ready (sibling already running)",
					"deployment_id", j.DeploymentID, "name", j.Name,
					"replica", j.ReplicaIndex, "job_id", j.JobID)
				return
			}
		}
		logger.Warn("provisioner: deployment no longer provisioning; cleaning up",
			"deployment_id", j.DeploymentID, "name", j.Name)
		if j.HAEnabled {
			if destroyErr := w.Docker.Destroy(ctx, j.Name); destroyErr != nil {
				logger.Warn("provisioner: cleanup destroy failed",
					"deployment_id", j.DeploymentID, "err", destroyErr)
			}
		} else if destroyErr := w.Docker.Destroy(ctx, j.Name); destroyErr != nil {
			logger.Warn("provisioner: cleanup destroy failed",
				"deployment_id", j.DeploymentID, "err", destroyErr)
		}
		w.markDone(j.JobID)
		return
	}

	w.markDone(j.JobID)
	logger.Info("provisioner: deployment ready",
		"deployment_id", j.DeploymentID, "name", j.Name,
		"replica", j.ReplicaIndex, "job_id", j.JobID)
}

// markReplicaRunning flips a deployment_replicas row to 'running' once
// the worker's Provision call returns. Best-effort: a transient DB
// error is logged but doesn't fail the job — the next health-worker
// sweep will reconcile if the row drifts.
func (w *Worker) markReplicaRunning(ctx context.Context, logger *slog.Logger, replicaID, containerID string) {
	if _, err := w.DB.Exec(ctx, `
		UPDATE deployment_replicas
		   SET status = 'running',
		       container_id = $1
		 WHERE id = $2
		   AND status = 'provisioning'
	`, containerID, replicaID); err != nil {
		logger.Warn("provisioner: mark replica running",
			"replica_id", replicaID, "err", err)
	}
}

func (w *Worker) markDone(jobID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := w.DB.Exec(ctx, `
		UPDATE provisioning_jobs
		   SET status = 'done', finished_at = now()
		 WHERE id = $1
	`, jobID); err != nil {
		slog.Default().Error("provisioner: mark done", "job_id", jobID, "err", err)
	}
}

func (w *Worker) markFailed(jobID int64, deploymentID, errStr string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := w.DB.Exec(ctx, `
		UPDATE provisioning_jobs
		   SET status = 'failed', error = $1, finished_at = now()
		 WHERE id = $2
	`, errStr, jobID); err != nil {
		slog.Default().Error("provisioner: mark failed", "job_id", jobID, "err", err)
	}
	// Mirror to the deployment row so the API surfaces the failure.
	if _, err := w.DB.Exec(ctx, `
		UPDATE deployments
		   SET status = 'failed', last_deploy_at = now()
		 WHERE id = $1
		   AND status = 'provisioning'
	`, deploymentID); err != nil {
		slog.Default().Error("provisioner: mark deployment failed", "deployment_id", deploymentID, "err", err)
	}
}
