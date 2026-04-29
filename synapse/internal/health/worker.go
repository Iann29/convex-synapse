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
)

// DockerStatusReporter is the subset of the docker provisioner the worker
// needs. The provisioner.Client implements it; tests can pass a fake.
type DockerStatusReporter interface {
	Status(ctx context.Context, deploymentName string) (string, error)
}

// Config controls how often the worker scans and what counts as "stale".
type Config struct {
	// Interval between full sweeps. Sub-second values are clamped to 1s.
	Interval time.Duration
	// Per-status-call timeout. Slow Docker daemons shouldn't stall the loop.
	StatusTimeout time.Duration
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
	DB     *pgxpool.Pool
	Docker DockerStatusReporter
	Config Config
	Logger *slog.Logger
}

// Run blocks until ctx is cancelled. Each tick runs a single sweep and logs
// a summary at INFO level when state changed; a no-op sweep emits a DEBUG
// line so the worker is observable but not noisy.
func (w *Worker) Run(ctx context.Context) {
	cfg := w.Config.sane()
	logger := w.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("health worker starting", "interval", cfg.Interval)

	// Run one sweep immediately so a fresh server doesn't wait `interval`
	// before catching a docker mismatch.
	w.sweep(ctx, logger, cfg)

	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("health worker stopping")
			return
		case <-t.C:
			w.sweep(ctx, logger, cfg)
		}
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
	if tag.RowsAffected() > 0 {
		logger.Info("health: reconciled deployment",
			"deployment", name,
			"docker_status", dockerStatus,
			"new_status", target)
		return true
	}
	return false
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
