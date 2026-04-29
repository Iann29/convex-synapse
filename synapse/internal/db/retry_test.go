package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func uniqueErr(constraint string) error {
	return &pgconn.PgError{Code: uniqueViolationCode, ConstraintName: constraint}
}

func TestIsUniqueViolation(t *testing.T) {
	if !IsUniqueViolation(uniqueErr("anything")) {
		t.Errorf("23505 PgError should be unique violation")
	}
	if IsUniqueViolation(errors.New("plain error")) {
		t.Errorf("plain error is not unique violation")
	}
	if IsUniqueViolation(&pgconn.PgError{Code: "42P01"}) {
		t.Errorf("non-23505 PgError is not unique violation")
	}
}

func TestIsUniqueViolationOn(t *testing.T) {
	err := uniqueErr("users_email_key")
	if !IsUniqueViolationOn(err, "users_email_key") {
		t.Errorf("should match by constraint name")
	}
	if !IsUniqueViolationOn(err, "other_key", "users_email_key") {
		t.Errorf("should match any of the listed constraints")
	}
	if IsUniqueViolationOn(err, "different_key") {
		t.Errorf("should not match an unrelated constraint")
	}
	if IsUniqueViolationOn(errors.New("plain"), "users_email_key") {
		t.Errorf("plain error should not match")
	}
}

func TestWithRetryOnUniqueViolation_FirstAttemptSucceeds(t *testing.T) {
	calls := 0
	err := WithRetryOnUniqueViolation(context.Background(), 5, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestWithRetryOnUniqueViolation_RetriesOnUniqueThenSucceeds(t *testing.T) {
	calls := 0
	err := WithRetryOnUniqueViolation(context.Background(), 5, func() error {
		calls++
		if calls < 3 {
			return uniqueErr("foo")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil after retry, got %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestWithRetryOnUniqueViolation_ExhaustsAndReturnsLast(t *testing.T) {
	calls := 0
	err := WithRetryOnUniqueViolation(context.Background(), 3, func() error {
		calls++
		return uniqueErr("foo")
	})
	if !IsUniqueViolation(err) {
		t.Errorf("expected unique violation as final error, got %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestWithRetryOnUniqueViolation_NonRetriableErrorReturnsImmediately(t *testing.T) {
	calls := 0
	plain := errors.New("DB down")
	err := WithRetryOnUniqueViolation(context.Background(), 5, func() error {
		calls++
		return plain
	})
	if !errors.Is(err, plain) {
		t.Errorf("expected plain error to bubble up, got %v", err)
	}
	if calls != 1 {
		t.Errorf("non-retriable should run exactly once, got %d", calls)
	}
}

func TestWithRetryOnUniqueViolation_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	calls := 0
	err := WithRetryOnUniqueViolation(ctx, 100, func() error {
		calls++
		return uniqueErr("foo")
	})
	if err == nil {
		t.Errorf("expected ctx error or unique violation; got nil")
	}
	if calls > 10 {
		t.Errorf("expected ctx to cut us short well under 100; got %d calls", calls)
	}
}
