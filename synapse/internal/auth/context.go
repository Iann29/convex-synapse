// Package auth is split across files: password hashing, opaque token
// generation, JWT issuance/verification, and request-context plumbing.
package auth

import (
	"context"
	"errors"
)

type ctxKey int

const (
	ctxKeyUserID ctxKey = iota
	ctxKeyEmail
)

// WithUser attaches authenticated user info to a request context.
func WithUser(ctx context.Context, userID, email string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyUserID, userID)
	ctx = context.WithValue(ctx, ctxKeyEmail, email)
	return ctx
}

func UserID(ctx context.Context) (string, error) {
	v, _ := ctx.Value(ctxKeyUserID).(string)
	if v == "" {
		return "", errors.New("no authenticated user in context")
	}
	return v, nil
}

func Email(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyEmail).(string)
	return v
}
