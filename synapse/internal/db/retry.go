package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// uniqueViolationCode is Postgres's SQLSTATE for "duplicate key value violates
// unique constraint" — what we see when two writers race a SELECT-then-INSERT
// allocation pattern. Stable across all supported Postgres versions.
const uniqueViolationCode = "23505"

// IsUniqueViolation reports whether err is a Postgres unique-constraint violation.
// Use this in handlers to disambiguate "user-induced 409" from "transient race
// the caller can retry".
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == uniqueViolationCode
}

// IsUniqueViolationOn reports whether err is a unique-constraint violation
// AND the constraint name matches one of the provided names.
//
//	if db.IsUniqueViolationOn(err, "users_email_key") { return 409 }
//
// Use the constraint name (`<table>_<column>_key` for plain UNIQUE columns,
// or whatever you named in the migration). String-matching the error message
// is fragile and breaks across Postgres minor versions; the constraint name
// is part of the protocol.
func IsUniqueViolationOn(err error, constraints ...string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != uniqueViolationCode {
		return false
	}
	for _, c := range constraints {
		if pgErr.ConstraintName == c {
			return true
		}
	}
	return false
}

// WithRetryOnUniqueViolation runs fn up to maxAttempts times, retrying only on
// 23505 errors. The first attempt is not delayed; subsequent attempts wait for
// a small jittered backoff so two racing callers don't lock-step into the
// same conflict twice.
//
// Use this for SELECT-then-INSERT resource allocators (port, name, slug):
// the SELECT picks a candidate, the INSERT racing might lose to UNIQUE, retry
// generates a fresh candidate. Single-node use is free — collisions are rare,
// retry never fires. Multi-node makes collisions visible and recoverable.
//
// Caller is responsible for making fn pure-function-like: re-running it must
// re-pick the candidate, not reuse a value bound on a prior attempt.
func WithRetryOnUniqueViolation(ctx context.Context, maxAttempts int, fn func() error) error {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// Tiny jitter so two callers that started at the same instant
			// don't keep colliding. 5-50ms is plenty for our race window —
			// the pattern is "DB was hit while my candidate was in flight",
			// not "I need to wait for state to settle".
			delay := time.Duration(5+attempt*15) * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		err := fn()
		if err == nil {
			return nil
		}
		if !IsUniqueViolation(err) {
			// Non-unique-violation errors aren't retriable; surface them
			// immediately so callers don't accidentally hide bugs.
			return err
		}
		lastErr = err
	}
	return lastErr
}
