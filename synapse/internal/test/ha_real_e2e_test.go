package synapsetest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Iann29/synapse/internal/api"
	dockerprov "github.com/Iann29/synapse/internal/docker"
	"github.com/Iann29/synapse/internal/proxy"
	"github.com/jackc/pgx/v5"
)

// TestHA_RealBackend_Failover exercises HA-per-deployment against a live
// Postgres + S3 + Convex backend cluster. Skipped by default: spinning
// up MinIO + a backing Postgres + 2 Convex backend containers takes
// ~3 minutes and isn't appropriate for the per-PR CI loop.
//
// To run:
//
//	docker compose --profile ha up -d   # starts minio + backend-postgres
//	SYNAPSE_HA_E2E=1 \
//	  SYNAPSE_HA_BACKEND_POSTGRES_URL='postgres://convex:convex@backend-postgres:5432/postgres?sslmode=disable' \
//	  SYNAPSE_HA_BACKEND_POSTGRES_PROBE_URL='postgres://convex:convex@localhost:5433/postgres?sslmode=disable' \
//	  SYNAPSE_HA_BACKEND_S3_ENDPOINT='http://minio:9000' \
//	  SYNAPSE_HA_BACKEND_S3_ACCESS_KEY=minioadmin \
//	  SYNAPSE_HA_BACKEND_S3_SECRET_KEY=minioadmin \
//	  go test ./synapse/internal/test/ -run TestHA_RealBackend_Failover -count=1 -v
//
// The test injects a real *dockerprov.Client into the harness, provisions
// real Convex backend containers, kills replica 0, and asserts the proxy
// still reaches replica 1.
func TestHA_RealBackend_Failover(t *testing.T) {
	if os.Getenv("SYNAPSE_HA_E2E") != "1" {
		t.Skip("HA real-backend e2e skipped (set SYNAPSE_HA_E2E=1 to run; see docs/HA_TESTING.md)")
	}
	requireEnv := func(name string) string {
		v := os.Getenv(name)
		if v == "" {
			t.Skipf("HA real-backend e2e skipped: missing env %s", name)
		}
		return v
	}
	pgURL := requireEnv("SYNAPSE_HA_BACKEND_POSTGRES_URL")
	s3End := requireEnv("SYNAPSE_HA_BACKEND_S3_ENDPOINT")
	s3Key := requireEnv("SYNAPSE_HA_BACKEND_S3_ACCESS_KEY")
	s3Sec := requireEnv("SYNAPSE_HA_BACKEND_S3_SECRET_KEY")
	pgProbeURL := os.Getenv("SYNAPSE_HA_BACKEND_POSTGRES_PROBE_URL")
	if pgProbeURL == "" {
		pgProbeURL = pgURL
	}

	// Verify the real backing Postgres is actually reachable before
	// burning test time provisioning containers — easier to debug a
	// flake here than later in the worker.
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer probeCancel()
	probeConn, err := pgx.Connect(probeCtx, pgProbeURL)
	if err != nil {
		t.Skipf("HA backend Postgres unreachable at %s: %v", pgProbeURL, err)
	}
	_ = probeConn.Close(probeCtx)

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	dockerHost := os.Getenv("SYNAPSE_DOCKER_HOST")
	if dockerHost == "" {
		dockerHost = "unix:///var/run/docker.sock"
	}
	backendImage := os.Getenv("SYNAPSE_BACKEND_IMAGE")
	if backendImage == "" {
		backendImage = "ghcr.io/get-convex/convex-backend:latest"
	}
	network := os.Getenv("SYNAPSE_DOCKER_NETWORK")
	if network == "" {
		network = "synapse-network"
	}
	dockerClient, err := dockerprov.NewClient(dockerHost, backendImage, network, logger)
	if err != nil {
		t.Skipf("Docker unavailable for HA real e2e: %v", err)
	}
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer pingCancel()
	if err := dockerClient.Ping(pingCtx); err != nil {
		t.Skipf("Docker ping failed for HA real e2e: %v", err)
	}

	h := SetupHAWithOpts(t, SetupOpts{
		Docker: dockerClient,
		HA: api.HAConfig{
			Enabled:             true,
			BackendPostgresURL:  pgURL,
			BackendS3Endpoint:   s3End,
			BackendS3Region:     "us-east-1",
			BackendS3AccessKey:  s3Key,
			BackendS3SecretKey:  s3Sec,
			BackendBucketPrefix: "convex",
		},
	})

	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "HA Real Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")

	// Surface the env values so a future test that wires the live
	// docker client can read them without re-reading os.Getenv.
	t.Logf("HA backend: pg=%s s3=%s key=%s secret=%s",
		pgURL, s3End, s3Key, "<redacted "+fmt.Sprint(len(s3Sec))+" chars>")

	// Create an HA deployment and confirm the control plane path works
	// end-to-end against the real Postgres-backed storage row.
	var got deploymentResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/create_deployment",
		owner.AccessToken,
		map[string]any{"type": "prod", "ha": true},
		http.StatusCreated, &got)

	if !got.HAEnabled || got.ReplicaCount != 2 {
		t.Fatalf("HA create: got haEnabled=%v replicaCount=%d, want true/2",
			got.HAEnabled, got.ReplicaCount)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = dockerClient.DestroyReplica(cleanupCtx, got.Name, 0, true)
		_ = dockerClient.DestroyReplica(cleanupCtx, got.Name, 1, true)
	})

	waitForStatus(t, h, got.Name, "running", 5*time.Minute)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var running int
		_ = h.DB.QueryRow(h.rootCtx, `
			SELECT count(*) FROM deployment_replicas
			 WHERE deployment_id = $1 AND status = 'running'
		`, got.ID).Scan(&running)
		if running == 2 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	var running int
	if err := h.DB.QueryRow(h.rootCtx, `
		SELECT count(*) FROM deployment_replicas
		 WHERE deployment_id = $1 AND status = 'running'
	`, got.ID).Scan(&running); err != nil {
		t.Fatalf("count running replicas: %v", err)
	}
	if running != 2 {
		t.Fatalf("running replicas=%d want 2", running)
	}

	resolver := &proxy.Resolver{DB: h.DB, UseNetworkDNS: false, CacheTTL: 10 * time.Millisecond}
	proxySrv := httptest.NewServer(proxy.Handler(resolver, logger, ""))
	t.Cleanup(proxySrv.Close)

	assertVersion := func(label string) {
		reqCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, proxySrv.URL+"/d/"+got.Name+"/version", nil)
		if err != nil {
			t.Fatalf("%s request: %v", label, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s GET /version: %v", label, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			t.Fatalf("%s /version status=%d want 2xx", label, resp.StatusCode)
		}
	}

	assertVersion("before failover")
	killCtx, killCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer killCancel()
	if err := dockerClient.DestroyReplica(killCtx, got.Name, 0, true); err != nil {
		t.Fatalf("destroy replica 0: %v", err)
	}
	resolver.Invalidate(got.Name)
	assertVersion("after replica 0 destroy")
}
