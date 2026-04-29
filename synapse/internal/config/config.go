// Package config loads runtime configuration from environment variables.
//
// All env vars are prefixed SYNAPSE_*. A .env file in the repo root is loaded
// during local development; in production, env should be injected by the
// orchestrator (docker-compose, k8s, systemd, etc.).
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	HTTPAddr        string
	LogLevel        slog.Level
	DBURL           string
	JWTSecret       []byte
	JWTAccessTTL    time.Duration
	JWTRefreshTTL   time.Duration
	DockerHost      string
	BackendImage    string
	DockerNetwork   string
	PortRangeMin    int
	PortRangeMax    int
}

// Load reads environment variables and returns a populated Config.
// It tries to load a .env file from common locations (cwd, parent, repo root)
// for local development; in production env should be injected directly.
func Load() (*Config, error) {
	for _, p := range []string{".env", "../.env", "../../.env"} {
		if err := godotenv.Load(p); err == nil {
			break
		}
	}

	jwtSecret := os.Getenv("SYNAPSE_JWT_SECRET")
	if len(jwtSecret) < 32 {
		return nil, errors.New("SYNAPSE_JWT_SECRET must be set and at least 32 chars")
	}

	dbURL := os.Getenv("SYNAPSE_DB_URL")
	if dbURL == "" {
		return nil, errors.New("SYNAPSE_DB_URL must be set")
	}

	accessTTL, err := time.ParseDuration(getEnvDefault("SYNAPSE_JWT_ACCESS_TTL", "15m"))
	if err != nil {
		return nil, fmt.Errorf("SYNAPSE_JWT_ACCESS_TTL: %w", err)
	}
	refreshTTL, err := time.ParseDuration(getEnvDefault("SYNAPSE_JWT_REFRESH_TTL", "720h"))
	if err != nil {
		return nil, fmt.Errorf("SYNAPSE_JWT_REFRESH_TTL: %w", err)
	}

	portMin, err := strconv.Atoi(getEnvDefault("SYNAPSE_PORT_RANGE_MIN", "3210"))
	if err != nil {
		return nil, fmt.Errorf("SYNAPSE_PORT_RANGE_MIN: %w", err)
	}
	portMax, err := strconv.Atoi(getEnvDefault("SYNAPSE_PORT_RANGE_MAX", "3500"))
	if err != nil {
		return nil, fmt.Errorf("SYNAPSE_PORT_RANGE_MAX: %w", err)
	}
	if portMax <= portMin {
		return nil, errors.New("SYNAPSE_PORT_RANGE_MAX must be greater than SYNAPSE_PORT_RANGE_MIN")
	}

	return &Config{
		HTTPAddr:      getEnvDefault("SYNAPSE_HTTP_ADDR", "0.0.0.0:8080"),
		LogLevel:      parseLogLevel(getEnvDefault("SYNAPSE_LOG_LEVEL", "info")),
		DBURL:         dbURL,
		JWTSecret:     []byte(jwtSecret),
		JWTAccessTTL:  accessTTL,
		JWTRefreshTTL: refreshTTL,
		DockerHost:    getEnvDefault("SYNAPSE_DOCKER_HOST", "unix:///var/run/docker.sock"),
		BackendImage:  getEnvDefault("SYNAPSE_BACKEND_IMAGE", "ghcr.io/get-convex/convex-backend:latest"),
		DockerNetwork: getEnvDefault("SYNAPSE_DOCKER_NETWORK", "synapse-network"),
		PortRangeMin:  portMin,
		PortRangeMax:  portMax,
	}, nil
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseLogLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
