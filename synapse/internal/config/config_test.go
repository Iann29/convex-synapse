package config

import "testing"

func TestLoadReadsBaseConfig(t *testing.T) {
	t.Setenv("SYNAPSE_JWT_SECRET", "test-secret-test-secret-test-secret-test-secret")
	t.Setenv("SYNAPSE_DB_URL", "postgres://synapse:synapse@localhost:5432/synapse?sslmode=disable")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.JWTSecret) == 0 {
		t.Errorf("JWTSecret: empty")
	}
	if cfg.DBURL == "" {
		t.Errorf("DBURL: empty")
	}
}
