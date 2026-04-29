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
func Enqueue(ctx context.Context, db Execer, deploymentID string, healthcheckViaNetwork bool) error {
	_, err := db.Exec(ctx, `
		INSERT INTO provisioning_jobs (deployment_id, kind, status, healthcheck_via_network)
		VALUES ($1, 'provision', 'pending', $2)
	`, deploymentID, healthcheckViaNetwork)
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
}

// claimNext pulls the oldest pending job, atomically marks it 'claimed',
// and joins the deployments row for the data the worker needs (name,
// host_port, instance_secret).
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
	err = tx.QueryRow(ctx, `
		SELECT j.id, j.deployment_id, d.name, d.host_port, d.instance_secret, j.healthcheck_via_network
		  FROM provisioning_jobs j
		  JOIN deployments d ON d.id = j.deployment_id
		 WHERE j.status = 'pending'
		 ORDER BY j.created_at ASC
		 FOR UPDATE OF j SKIP LOCKED
		 LIMIT 1
	`).Scan(&j.JobID, &j.DeploymentID, &j.Name, &j.HostPort, &j.InstanceSecret, &j.HealthcheckViaNetwork)
	if errors.Is(err, pgx.ErrNoRows) {
		return claimedJob{}, false
	}
	if err != nil {
		logger.Error("provisioner: claim query", "err", err)
		return claimedJob{}, false
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

	// Pre-check: if the deployment row was already deleted (test truncate
	// between claim and run, or a real /delete that arrived after we
	// claimed but before we started Docker.Provision), skip the costly
	// Provision call entirely. The same check exists post-Provision as a
	// safety net, but the pre-check turns "create container then destroy
	// it" into a no-op SQL probe, which matters under heavy queue churn.
	var current string
	if err := w.DB.QueryRow(ctx,
		`SELECT status FROM deployments WHERE id = $1`, j.DeploymentID,
	).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logger.Info("provisioner: deployment row gone before provision; skipping",
				"deployment_id", j.DeploymentID, "name", j.Name)
			w.markDone(j.JobID)
			return
		}
		// Transient DB error — fall through to Provision and let the
		// post-Provision UPDATE catch it.
		logger.Warn("provisioner: pre-check status query failed",
			"deployment_id", j.DeploymentID, "err", err)
	} else if current != models.DeploymentStatusProvisioning {
		logger.Info("provisioner: deployment no longer provisioning; skipping",
			"deployment_id", j.DeploymentID, "name", j.Name, "status", current)
		w.markDone(j.JobID)
		return
	}

	info, err := w.Docker.Provision(ctx, dockerprov.DeploymentSpec{
		Name:                  j.Name,
		InstanceSecret:        j.InstanceSecret,
		HostPort:              j.HostPort,
		EnvVars:               map[string]string{},
		HealthcheckViaNetwork: j.HealthcheckViaNetwork,
	})
	if err != nil {
		logger.Error("provisioner: provision failed",
			"job_id", j.JobID, "deployment", j.Name, "err", err)
		w.markFailed(j.JobID, j.DeploymentID, err.Error())
		return
	}

	// Atomic UPDATE WHERE status='provisioning' — if /delete flipped the
	// row to 'deleted' while we were creating, this matches 0 rows and
	// we know to tear down.
	tag, err := w.DB.Exec(ctx, `
		UPDATE deployments
		   SET status         = $1,
		       container_id   = $2,
		       deployment_url = $3,
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
		logger.Warn("provisioner: deployment no longer provisioning; cleaning up",
			"deployment_id", j.DeploymentID, "name", j.Name)
		if destroyErr := w.Docker.Destroy(ctx, j.Name); destroyErr != nil {
			logger.Warn("provisioner: cleanup destroy failed",
				"deployment_id", j.DeploymentID, "err", destroyErr)
		}
		// Mark the job done either way — it executed successfully, the
		// row was just deleted out from under us.
		w.markDone(j.JobID)
		return
	}

	w.markDone(j.JobID)
	logger.Info("provisioner: deployment ready",
		"deployment_id", j.DeploymentID, "name", j.Name, "job_id", j.JobID)
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
