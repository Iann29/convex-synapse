package config

import "testing"

func TestLoadReadsAsterRuntimeConfig(t *testing.T) {
	t.Setenv("SYNAPSE_JWT_SECRET", "test-secret-test-secret-test-secret-test-secret")
	t.Setenv("SYNAPSE_DB_URL", "postgres://synapse:synapse@localhost:5432/synapse?sslmode=disable")
	t.Setenv("SYNAPSE_ASTER_POSTGRES_URL", "postgres://convex:convex@pg:5432/convex_dep?sslmode=disable")
	t.Setenv("SYNAPSE_ASTER_DB_SCHEMA", "convex_dev")
	t.Setenv("SYNAPSE_ASTER_MODULES_DIR", " /srv/convex/data/modules ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AsterPostgresURL != "postgres://convex:convex@pg:5432/convex_dep?sslmode=disable" {
		t.Errorf("AsterPostgresURL: got %q", cfg.AsterPostgresURL)
	}
	if cfg.AsterDBSchema != "convex_dev" {
		t.Errorf("AsterDBSchema: got %q want convex_dev", cfg.AsterDBSchema)
	}
	if cfg.AsterModulesDir != "/srv/convex/data/modules" {
		t.Errorf("AsterModulesDir: got %q", cfg.AsterModulesDir)
	}
}
