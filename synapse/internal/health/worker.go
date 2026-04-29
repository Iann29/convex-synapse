// Package health runs a periodic reconciler that compares the deployments
// recorded in Postgres against what's actually running on Docker. When a
// container has disappeared from under us (operator removed it manually,
// host reboot, etc.) the worker flips the row to "stopped" so the dashboard
// reflects reality and the port can be safely re-allocated.
//
// The worker is intentionally read-mostly: it observes Docker, updates DB.
// It does NOT auto-restart containers — that's a v1.0 feature (see ROADMAP).
package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	synapsedb "github.com/Iann29/synapse/internal/db"
)

// DockerStatusReporter is the subset of the docker provisioner the worker
// needs. The provisioner.Client implements it; tests can pass a fake.
type DockerStatusReporter interface {
	Status(ctx context.Context, deploymentName string) (string, error)
}

// Restarter is the optional auto-recovery hook. When the worker reconciles
// a row to "stopped" and AutoRestart is true, it calls Restart(name). The
// docker.Client's Restart implements this; nil disables auto-recovery.
type Restarter interface {
	Restart(ctx context.Context, deploymentName string) error
}

// Config controls how often the worker scans and what counts as "stale".
type Config struct {
	// Interval between full sweeps. Sub-second values are clamped to 1s.
	Interval time.Duration
	// Per-status-call timeout. Slow Docker daemons shouldn't stall the loop.
	StatusTimeout time.Duration
	// AutoRestart, when true, has the worker attempt to restart a deployment
	// after a sweep flips its status to "stopped". Successful restart flips
	// the row back to "running"; failure leaves it at "failed". Implementer
	// must also set Restarter on the worker.
	AutoRestart bool
}

func (c Config) sane() Config {
	out := c
	if out.Interval < time.Second {
		out.Interval = 30 * time.Second
	}
	if out.StatusTimeout <= 0 {
		out.StatusTimeout = 5 * time.Second
	}
	return out
}

// Worker reconciles deployments.status with Docker reality on a timer.
type Worker struct {
	DB        *pgxpool.Pool
	Docker    DockerStatusReporter
	Restarter Restarter // optional; required when Config.AutoRestart is true
	Config    Config
	Logger    *slog.Logger
}

// Run blocks until ctx is cancelled. Each tick runs a single sweep and logs
// a summary at INFO level when state changed; a no-op sweep emits a DEBUG
// line so the worker is observable but not noisy.
//
// Multi-node coordination: each tick is wrapped in a session-level
// pg_try_advisory_lock(LockHealthWorker). Exactly one node in the fleet
// runs the sweep at any instant; followers observe acquired=false and
// skip silently. Single-node deployments pay one round-trip per tick.
func (w *Worker) Run(ctx context.Context) {
	cfg := w.Config.sane()
	logger := w.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("health worker starting", "interval", cfg.Interval)

	// Run one sweep immediately so a fresh server doesn't wait `interval`
	// before catching a docker mismatch.
	w.tickWithLock(ctx, logger, cfg)

	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("health worker stopping")
			return
		case <-t.C:
			w.tickWithLock(ctx, logger, cfg)
		}
	}
}

// tickWithLock acquires the worker's advisory lock and runs one sweep.
// If the lock is held by another node, the tick is a no-op.
func (w *Worker) tickWithLock(ctx context.Context, logger *slog.Logger, cfg Config) {
	acquired, err := synapsedb.WithTryAdvisoryLock(ctx, w.DB, synapsedb.LockHealthWorker,
		func(ctx context.Context) error {
			w.sweep(ctx, logger, cfg)
			return nil
		})
	if err != nil {
		logger.Warn("health: advisory-lock acquire failed", "err", err)
		return
	}
	if !acquired {
		logger.Debug("health: another node holds the sweep lock; skipping tick")
	}
}

// sweep runs one reconciliation pass. Errors are logged, never returned —
// a transient DB or Docker hiccup must not kill the loop.
func (w *Worker) sweep(ctx context.Context, logger *slog.Logger, cfg Config) {
	rows, err := w.DB.Query(ctx, `
		SELECT id, name FROM deployments
		 WHERE status = 'running'
	`)
	if err != nil {
		logger.Error("health: list running deployments", "err", err)
		return
	}

	type item struct{ id, name string }
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.name); err != nil {
			logger.Error("health: scan row", "err", err)
			rows.Close()
			return
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		logger.Error("health: rows err", "err", err)
		return
	}

	var changed int
	for _, it := range items {
		if w.reconcile(ctx, logger, cfg, it.id, it.name) {
			changed++
		}
	}
	if changed > 0 {
		logger.Info("health sweep", "checked", len(items), "changed", changed)
	} else {
		logger.Debug("health sweep", "checked", len(items))
	}
}

// reconcile returns true if the row's status changed.
func (w *Worker) reconcile(ctx context.Context, logger *slog.Logger, cfg Config, id, name string) bool {
	statusCtx, cancel := context.WithTimeout(ctx, cfg.StatusTimeout)
	defer cancel()
	dockerStatus, err := w.Docker.Status(statusCtx, name)
	if err != nil {
		logger.Warn("health: docker status", "deployment", name, "err", err)
		return false
	}

	// Empty string from Status() means the container is gone.
	// Anything else: docker container.State.Status — running/exited/dead/created/paused/restarting.
	target := classify(dockerStatus)
	if target == "" {
		// Either we got a status we can't interpret or no change is needed.
		return false
	}

	tag, err := w.DB.Exec(ctx, `
		UPDATE deployments
		   SET status = $1
		 WHERE id = $2
		   AND status = 'running'
	`, target, id)
	if err != nil {
		logger.Error("health: update status", "deployment", name, "err", err)
		return false
	}
	if tag.RowsAffected() == 0 {
		return false
	}
	logger.Info("health: reconciled deployment",
		"deployment", name,
		"docker_status", dockerStatus,
		"new_status", target)

	// Auto-recovery: if the row just flipped to "stopped" and the operator
	// opted in to auto-restart, try to bring it back. We don't restart "failed"
	// rows automatically — that state is reserved for situations a human
	// should look at (provisioning crash, image pull error, panic).
	if cfg.AutoRestart && target == "stopped" && w.Restarter != nil {
		w.tryRestart(ctx, logger, cfg, id, name)
	}
	return true
}

// tryRestart attempts a one-shot recovery. On success, flips status back to
// running. On failure (or container missing), leaves the row at stopped/failed
// so a human can decide. Restart loops are NOT in scope — single attempt only.
func (w *Worker) tryRestart(ctx context.Context, logger *slog.Logger, cfg Config, id, name string) {
	startCtx, cancel := context.WithTimeout(ctx, cfg.StatusTimeout)
	defer cancel()

	if err := w.Restarter.Restart(startCtx, name); err != nil {
		logger.Warn("health: auto-restart failed",
			"deployment", name, "err", err)
		// Container gone is unrecoverable from our side — promote to failed.
		if err.Error() == "container not found" {
			_, _ = w.DB.Exec(ctx,
				`UPDATE deployments SET status = 'failed' WHERE id = $1 AND status = 'stopped'`,
				id)
		}
		return
	}

	if _, err := w.DB.Exec(ctx,
		`UPDATE deployments SET status = 'running' WHERE id = $1 AND status = 'stopped'`,
		id); err != nil {
		logger.Error("health: post-restart update",
			"deployment", name, "err", err)
		return
	}
	logger.Info("health: auto-restarted deployment", "deployment", name)
}

// classify maps Docker's container state strings to our deployments.status
// vocabulary. Returns "" when the row should stay unchanged.
//
//	docker          → ours
//	"" (gone)       → "stopped"
//	"exited"        → "stopped"
//	"dead"          → "failed"
//	"running"       → ""  (no change)
//	"created"       → ""  (about to start, give it time)
//	"restarting"    → ""  (transient — the daemon is recovering)
//	"paused"        → "stopped"
//	other           → ""  (be conservative)
func classify(dockerStatus string) string {
	switch dockerStatus {
	case "":
		return "stopped"
	case "exited":
		return "stopped"
	case "dead":
		return "failed"
	case "paused":
		return "stopped"
	}
	return ""
}

// LookupRow is a tiny helper for tests that want to read a deployment's
// current persisted status. Lives here (not in models) to avoid widening
// the public API for a single test affordance.
func LookupRow(ctx context.Context, db *pgxpool.Pool, id string) (string, error) {
	var status string
	err := db.QueryRow(ctx, `SELECT status FROM deployments WHERE id = $1`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("deployment %s not found", id)
	}
	return status, err
}
