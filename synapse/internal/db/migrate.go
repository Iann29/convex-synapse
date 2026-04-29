package db

import (
	"embed"
	"errors"
	"fmt"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies any pending SQL migrations against the configured database.
// Migrations are embedded into the binary at build time, so the running
// container does not need access to the source repo.
func Migrate(dsn string, logger *slog.Logger) error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("load migrations fs: %w", err)
	}

	// The pgx/v5 migrate driver needs the "pgx5://" scheme.
	pgxDSN := "pgx5://" + trimScheme(dsn)
	m, err := migrate.NewWithSourceInstance("iofs", src, pgxDSN)
	if err != nil {
		return fmt.Errorf("init migrate: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}

	v, dirty, err := m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return fmt.Errorf("read version: %w", err)
	}
	logger.Info("migrations applied", "version", v, "dirty", dirty)
	return nil
}

// trimScheme returns the connection string with the postgres:// or postgresql://
// prefix removed, for compatibility with the pgx5 migrate driver scheme.
func trimScheme(dsn string) string {
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if len(dsn) > len(prefix) && dsn[:len(prefix)] == prefix {
			return dsn[len(prefix):]
		}
	}
	return dsn
}

// Avoid pgx import in this file being unused if drivers register only via init.
var _ = pgx.Postgres{}
