package db

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Advisory-lock keys. Postgres session-level advisory locks are addressed
// by a single bigint; we keep them in one place to avoid two callers
// accidentally picking the same key for different workers.
//
// The constants are arbitrary 64-bit ints with an "0xC0DE..." prefix so
// they're easy to grep. Don't change a key without coordinated rollout —
// renaming it would let an old node and a new node both think they hold
// the lock.
const (
	// LockHealthWorker guards the periodic health.Worker sweep. Only one
	// node in the fleet should be reconciling deployments.status with
	// Docker reality at any instant; the others skip the tick.
	LockHealthWorker int64 = 0xC0DE0001

	// LockOrphanSweep guards the startup-time `provisioning` row sweep
	// (cmd/server/main.go::sweepOrphanedProvisioning). 3 nodes booting
	// simultaneously would each issue the same UPDATE; the lock ensures
	// only one runs it.
	LockOrphanSweep int64 = 0xC0DE0002
)

// WithTryAdvisoryLock attempts to acquire a session-level Postgres advisory
// lock and runs fn while holding it. The lock is automatically released
// when the connection returns to the pool (deferred unlock + pool.Release).
//
// Returns:
//   - acquired=true and the result of fn() if the lock was taken
//   - acquired=false and a nil error if another caller holds the lock right
//     now (caller should skip the work, not retry)
//   - acquired=false and a non-nil error if the connection or the lock
//     query itself failed
//
// Usage:
//
//	acquired, err := WithTryAdvisoryLock(ctx, pool, LockHealthWorker, func(ctx context.Context) error {
//	    return doWork(ctx)
//	})
//	if !acquired { return /* skip silently */ }
//	if err != nil { /* handle real failure */ }
//
// Single-node behavior: the lock is always free, the closure always runs,
// the pattern is a no-op tax (a single round-trip pg_try_advisory_lock).
// Multi-node behavior: only the lock-holder runs the closure; followers
// observe acquired=false and return — the next tick they may be the
// holder, by chance.
func WithTryAdvisoryLock(
	ctx context.Context,
	pool *pgxpool.Pool,
	key int64,
	fn func(ctx context.Context) error,
) (acquired bool, err error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false, fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Release()

	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&got); err != nil {
		return false, fmt.Errorf("pg_try_advisory_lock: %w", err)
	}
	if !got {
		return false, nil
	}
	defer func() {
		// Best-effort unlock. If this fails (rare — connection died), the
		// lock is still released when the connection closes; pgxpool
		// destroys the underlying conn on Release error. We log so an
		// operator can see a stuck-lock pattern, not return it.
		if _, unlockErr := conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", key); unlockErr != nil {
			slog.Default().Warn("pg_advisory_unlock failed",
				"key", fmt.Sprintf("%#x", key), "err", unlockErr)
		}
	}()

	return true, fn(ctx)
}
