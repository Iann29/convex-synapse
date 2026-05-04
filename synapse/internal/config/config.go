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
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	HTTPAddr      string
	LogLevel      slog.Level
	DBURL         string
	JWTSecret     []byte
	JWTAccessTTL  time.Duration
	JWTRefreshTTL time.Duration
	DockerHost    string
	BackendImage  string
	DockerNetwork string
	PortRangeMin  int
	PortRangeMax  int

	// HealthcheckViaNetwork chooses how Synapse polls a freshly-provisioned
	// Convex backend.
	//
	// When false (default), the provisioner polls http://127.0.0.1:{hostPort}
	// — correct when Synapse runs on the docker host alongside the
	// daemon and the backend is reachable through host-port mapping.
	//
	// When true, the provisioner polls http://convex-{name}:3210 — required
	// when Synapse itself runs inside a container that shares the
	// synapse-network bridge with provisioned backends; loopback inside the
	// container does not reach sibling containers.
	HealthcheckViaNetwork bool

	// AllowedOrigins is a comma-separated list of origins permitted to make
	// browser-initiated requests, or "*" to allow any. Defaults to "*" since
	// Synapse runs on operator-controlled infra and every endpoint requires
	// a bearer token anyway.
	AllowedOrigins string

	// ProxyEnabled mounts /d/{name}/* on the same listener that serves the
	// API, forwarding to the deployment's internal address. Lets operators
	// expose a single host port instead of one per deployment.
	ProxyEnabled bool

	// PublicURL is the externally-reachable URL of the Synapse instance
	// (e.g. "https://synapse.example.com"). Used by /v1/deployments/...
	// /auth and /cli_credentials to compute the URL the *caller's*
	// machine should use when talking to a deployment — not the
	// container-internal "http://127.0.0.1:<port>" the provisioner
	// stores for its own healthcheck.
	//
	// When ProxyEnabled and PublicURL are both set, returned URLs
	// become "<PublicURL>/d/<name>" so a remote `npx convex` flows
	// through the Synapse proxy and reaches the right replica
	// (including HA failover) without exposing per-deployment ports.
	//
	// When PublicURL is set but ProxyEnabled is false, the host-port
	// suffix is preserved: "<PublicURL>:<port>". Operators using
	// host-port mode still need to expose those ports.
	//
	// Empty (default) → keep the legacy "http://127.0.0.1:<port>"
	// shape, suitable for local dev.
	PublicURL string

	// BaseDomain (v1.0+) is the wildcard subdomain Synapse provisions
	// per-deployment URLs under. When set, every emitted deployment
	// URL becomes "https://<name>.<BaseDomain>" — the operator points
	// "*.<BaseDomain>" DNS at the VPS and Caddy on-demand TLS issues
	// per-host certs. Wins over PublicURL+ProxyEnabled (i.e. operators
	// who configured both get the subdomain shape).
	//
	// Empty (default) → custom domains disabled; the path-based
	// "<PublicURL>/d/<name>" form continues to work.
	BaseDomain string

	// HealthAutoRestart, when true, has the health worker call docker
	// `start` on a deployment whose status just flipped to "stopped". A
	// missing container is promoted to "failed" instead — restart loops
	// are deliberately out of scope.
	HealthAutoRestart bool

	// HA configuration (v0.5+). Off by default — Synapse continues to
	// behave exactly like v0.4 when SYNAPSE_HA_ENABLED is unset. When
	// enabled, create_deployment accepts a `ha:true` flag in the body
	// and provisions N replicas backed by Postgres + S3.
	HAEnabled bool

	// Cluster-wide defaults for the per-deployment Postgres + S3 backing.
	// Each value is overridable on a per-deployment basis through the
	// create-deployment payload (operator can register a different
	// Postgres for a specific tenant). All cluster defaults must be set
	// when HAEnabled is true; the create-deployment handler refuses to
	// proceed with HA otherwise.
	BackendPostgresURL    string
	BackendS3Endpoint     string
	BackendS3Region       string
	BackendS3AccessKey    string
	BackendS3SecretKey    string
	BackendS3BucketPrefix string

	// Aster runtime knobs (v1.1+). Optional: leaving these empty keeps
	// kind=aster brokerds on the memory-store smoke path. Setting
	// AsterPostgresURL switches brokerd to ASTER_STORE=postgres; setting
	// AsterModulesDir bind-mounts the host's Convex modules directory into
	// brokerd so the Aster module loader can fetch bundle bytes over IPC.
	AsterPostgresURL string
	AsterDBSchema    string
	AsterModulesDir  string

	// Self-update daemon (v1.1.0+).
	// UpdaterSocket: unix socket path the synapse-updater systemd
	// daemon listens on. Default mounted at /run/synapse/updater.sock.
	// Empty (or unreachable) → /v1/admin/upgrade returns 503 with a
	// "run setup.sh --upgrade via SSH" hint.
	UpdaterSocket string
	// GitHubRepo points /v1/admin/version_check at the right release
	// stream. Default Iann29/convex-synapse; overridable for forks.
	GitHubRepo string
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
		HTTPAddr:              getEnvDefault("SYNAPSE_HTTP_ADDR", "0.0.0.0:8080"),
		LogLevel:              parseLogLevel(getEnvDefault("SYNAPSE_LOG_LEVEL", "info")),
		DBURL:                 dbURL,
		JWTSecret:             []byte(jwtSecret),
		JWTAccessTTL:          accessTTL,
		JWTRefreshTTL:         refreshTTL,
		DockerHost:            getEnvDefault("SYNAPSE_DOCKER_HOST", "unix:///var/run/docker.sock"),
		BackendImage:          getEnvDefault("SYNAPSE_BACKEND_IMAGE", "ghcr.io/get-convex/convex-backend:latest"),
		DockerNetwork:         getEnvDefault("SYNAPSE_DOCKER_NETWORK", "synapse-network"),
		PortRangeMin:          portMin,
		PortRangeMax:          portMax,
		HealthcheckViaNetwork: getEnvDefault("SYNAPSE_HEALTHCHECK_VIA_NETWORK", "") == "true",
		AllowedOrigins:        getEnvDefault("SYNAPSE_ALLOWED_ORIGINS", "*"),
		ProxyEnabled:          getEnvDefault("SYNAPSE_PROXY_ENABLED", "") == "true",
		PublicURL:             strings.TrimRight(os.Getenv("SYNAPSE_PUBLIC_URL"), "/"),
		BaseDomain:            strings.Trim(os.Getenv("SYNAPSE_BASE_DOMAIN"), ". "),
		HealthAutoRestart:     getEnvDefault("SYNAPSE_HEALTH_AUTO_RESTART", "") == "true",

		HAEnabled:             getEnvDefault("SYNAPSE_HA_ENABLED", "") == "true",
		BackendPostgresURL:    os.Getenv("SYNAPSE_BACKEND_POSTGRES_URL"),
		BackendS3Endpoint:     os.Getenv("SYNAPSE_BACKEND_S3_ENDPOINT"),
		BackendS3Region:       getEnvDefault("SYNAPSE_BACKEND_S3_REGION", "us-east-1"),
		BackendS3AccessKey:    os.Getenv("SYNAPSE_BACKEND_S3_ACCESS_KEY"),
		BackendS3SecretKey:    os.Getenv("SYNAPSE_BACKEND_S3_SECRET_KEY"),
		BackendS3BucketPrefix: getEnvDefault("SYNAPSE_BACKEND_S3_BUCKET_PREFIX", "convex"),

		AsterPostgresURL: os.Getenv("SYNAPSE_ASTER_POSTGRES_URL"),
		AsterDBSchema:    getEnvDefault("SYNAPSE_ASTER_DB_SCHEMA", "public"),
		AsterModulesDir:  strings.TrimSpace(os.Getenv("SYNAPSE_ASTER_MODULES_DIR")),

		UpdaterSocket: getEnvDefault("SYNAPSE_UPDATER_SOCKET", "/run/synapse/updater.sock"),
		GitHubRepo:    getEnvDefault("SYNAPSE_GITHUB_REPO", "Iann29/convex-synapse"),
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
