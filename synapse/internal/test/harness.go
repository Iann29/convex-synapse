// Package synapsetest provides integration-test scaffolding for the synapse
// HTTP API. Setup spins up a fresh database (per-test isolation), runs the
// embedded migrations, builds the chi router with a stub Docker client, and
// returns helpers for registering users, issuing JWTs, and making HTTP calls
// against an httptest.Server.
//
// Why a dedicated test package (instead of in-package whitebox tests)?
// internal/test must import internal/api to wire the router; if api_test.go
// also lived in internal/api and imported internal/test, we'd have a cycle.
// Integration tests exercise everything via HTTP anyway, so the loss of
// whitebox visibility is mostly cosmetic.
package synapsetest

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for ad-hoc DDL

	"github.com/Iann29/synapse/internal/api"
	"github.com/Iann29/synapse/internal/auth"
	cryptopkg "github.com/Iann29/synapse/internal/crypto"
	"github.com/Iann29/synapse/internal/db"
	dockerprov "github.com/Iann29/synapse/internal/docker"
	"github.com/Iann29/synapse/internal/provisioner"
)

// defaultDSN is the local docker-compose postgres. Override with
// SYNAPSE_TEST_DB_URL to point at a different server.
const defaultDSN = "postgres://synapse:synapse@localhost:5432/synapse?sslmode=disable"

// jwtSecret is a fixed 64-char dev secret. Tests don't need rotation.
const jwtSecret = "test-secret-test-secret-test-secret-test-secret-test-secret-aaa"

// Harness exposes the live test server plus helpers for crafting requests and
// poking at the database directly. Callers MUST call Cleanup (registered via
// t.Cleanup automatically by Setup) — but they can also reach for the raw
// fields when a test needs to seed a row that has no public API path.
type Harness struct {
	T      *testing.T
	Server *httptest.Server
	DB     *pgxpool.Pool
	JWT    *auth.JWTIssuer
	Docker *FakeDocker
	// Crypto is the AES-GCM box that the harness uses for
	// deployment_storage. Non-nil only on HA harnesses (SetupHA);
	// tests can use it to decrypt deployment_storage values when
	// asserting on what the handler persisted.
	Crypto *cryptopkg.SecretBox

	dsn     string // DSN of the per-test database
	dbName  string // name of the per-test database (for DROP)
	rootCtx context.Context
}

// SetupHA is the HA-enabled variant of Setup. The router is wired with
// HA.Enabled=true plus stub Postgres + S3 cluster credentials and a
// freshly-generated AES-GCM SecretBox so create_deployment can persist
// encrypted deployment_storage rows. The provisioner worker shares the
// same SecretBox so it can decrypt the values when claiming HA jobs.
//
// Tests that need HA flow end-to-end use this; tests that only exercise
// single-replica behavior keep using Setup.
func SetupHA(t *testing.T) *Harness {
	t.Helper()
	return setup(t, true, SetupOpts{})
}

// Setup creates a fresh database, applies migrations, and returns a Harness
// wired to a real chi router and httptest.Server. All resources are released
// via t.Cleanup.
//
// Connection target:
//   - SYNAPSE_TEST_DB_URL env var, if set
//   - postgres://synapse:synapse@localhost:5432/synapse otherwise
//
// If the postgres server is unreachable, Setup t.Skip's the test instead of
// failing — keeping `go test ./...` green on dev machines without docker
// compose running.
func Setup(t *testing.T) *Harness {
	t.Helper()
	return setup(t, false, SetupOpts{})
}

// SetupOpts is the optional knob bag for SetupWithOpts. Tests that want
// the default harness keep using Setup / SetupHA.
type SetupOpts struct {
	// PublicURL mirrors api.RouterDeps.PublicURL — when set, GET
	// handlers (and /auth and /cli_credentials) return rewritten URLs
	// instead of the raw container-internal "http://127.0.0.1:<port>".
	PublicURL string
	// ProxyEnabled mirrors api.RouterDeps.ProxyEnabled. With PublicURL
	// set + ProxyEnabled true, the rewrite becomes
	// "<PublicURL>/d/<name>". With ProxyEnabled false, it becomes
	// "<PublicURL>:<host_port>".
	ProxyEnabled bool
	// BaseDomain mirrors api.RouterDeps.BaseDomain (v1.0+). When
	// non-empty, deployment URLs get rewritten to
	// "https://<name>.<BaseDomain>" — wins over PublicURL+ProxyEnabled.
	BaseDomain string
	// UpdaterSocket mirrors api.RouterDeps.UpdaterSocket — tests that
	// exercise /v1/admin/upgrade either point this at a real-on-disk
	// mock socket (see admin_test.go) or leave it empty to drive the
	// "updater not configured" path.
	UpdaterSocket string
	// GitHubRepo + GitHubAPIBase let admin tests redirect the
	// /version_check fetch at an httptest.Server that pretends to be
	// GitHub. Production wiring leaves both empty (defaults apply).
	GitHubRepo    string
	GitHubAPIBase string
}

// SetupWithOpts is Setup + opts, used by tests that need to drive the
// PublicURL rewrite path (otherwise they'd need to instantiate the
// router by hand).
func SetupWithOpts(t *testing.T, opts SetupOpts) *Harness {
	t.Helper()
	return setup(t, false, opts)
}

func setup(t *testing.T, haEnabled bool, opts SetupOpts) *Harness {
	t.Helper()
	// Each Setup call gets its own database, so tests are independent and can
	// run in parallel. The marginal cost (a CREATE DATABASE + migrate) is
	// already paid; opting tests in for parallel scheduling makes the suite
	// finish in ~5s wall instead of ~30s sequential on a 16-core box.
	t.Parallel()

	rootDSN := os.Getenv("SYNAPSE_TEST_DB_URL")
	if rootDSN == "" {
		rootDSN = defaultDSN
	}

	// Verify connectivity early so we fail fast instead of in the migration
	// step. database/sql + pgx/stdlib gives us a connection we can use to
	// CREATE/DROP DATABASE without a pgxpool tying us to a single DB.
	root, err := sql.Open("pgx", rootDSN)
	if err != nil {
		t.Fatalf("open root db: %v", err)
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := root.PingContext(pingCtx); err != nil {
		_ = root.Close()
		t.Skipf("postgres unavailable at %s (set SYNAPSE_TEST_DB_URL to point at a reachable server): %v", redactDSN(rootDSN), err)
	}

	// Each Setup call gets its own database. Running migrations against a
	// brand-new DB is fast (~200ms on warm postgres) and gives us perfect
	// isolation between tests run in parallel.
	dbName := "synapse_test_" + randHex(6)
	if _, err := root.ExecContext(context.Background(), `CREATE DATABASE `+dbName); err != nil {
		_ = root.Close()
		t.Fatalf("create test database: %v", err)
	}
	_ = root.Close()

	testDSN := replaceDBName(rootDSN, dbName)

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := db.Migrate(testDSN, logger); err != nil {
		dropDatabase(rootDSN, dbName)
		t.Fatalf("migrate: %v", err)
	}

	pool, err := db.Connect(context.Background(), testDSN)
	if err != nil {
		dropDatabase(rootDSN, dbName)
		t.Fatalf("connect pool: %v", err)
	}

	jwt := auth.NewJWTIssuer([]byte(jwtSecret), 15*time.Minute, 24*time.Hour)
	fake := NewFakeDocker()

	deps := api.RouterDeps{
		Logger:                logger,
		DB:                    pool,
		JWT:                   jwt,
		Docker:                fake,
		PortRangeMin:          3210,
		PortRangeMax:          3500,
		HealthcheckViaNetwork: false,
		AllowedOrigins:        "*",
		Version:               "test",
		PublicURL:             opts.PublicURL,
		ProxyEnabled:          opts.ProxyEnabled,
		BaseDomain:            opts.BaseDomain,
		UpdaterSocket:         opts.UpdaterSocket,
		GitHubRepo:            opts.GitHubRepo,
		GitHubAPIBase:         opts.GitHubAPIBase,
	}

	// HA wiring (only when SetupHA was called). The crypto box is a
	// fresh per-test 32-byte key — encrypted material in
	// deployment_storage rows is meaningless to other tests, but stays
	// inspectable through the harness's `cryptoBox` field.
	var box *cryptopkg.SecretBox
	if haEnabled {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			t.Fatalf("rand key: %v", err)
		}
		var berr error
		box, berr = cryptopkg.New(key)
		if berr != nil {
			t.Fatalf("crypto.New: %v", berr)
		}
		deps.HA = api.HAConfig{
			Enabled:             true,
			BackendPostgresURL:  "postgres://synapse:synapse@cluster-pg.test:5432/convex_admin?sslmode=disable",
			BackendS3Endpoint:   "http://minio.test:9000",
			BackendS3Region:     "us-east-1",
			BackendS3AccessKey:  "test-access-key",
			BackendS3SecretKey:  "test-secret-key",
			BackendBucketPrefix: "convex",
		}
		deps.Crypto = box
	}

	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)

	// Provisioner worker — drives the persistent job queue. Tests that
	// don't care about provisioning still get one running; it's harmless
	// when there are no jobs. Use 50ms poll so e2e flow completes
	// snappily within test timeouts.
	workerCtx, workerCancel := context.WithCancel(context.Background())
	pworker := &provisioner.Worker{
		DB:     pool,
		Docker: fake,
		Config: provisioner.Config{
			PollInterval: 50 * time.Millisecond,
			JobTimeout:   30 * time.Second,
			NodeID:       "test-" + dbName,
		},
		Logger: logger,
	}
	if box != nil {
		pworker.Crypto = box
	}
	go pworker.Run(workerCtx)

	h := &Harness{
		T:       t,
		Server:  srv,
		DB:      pool,
		JWT:     jwt,
		Docker:  fake,
		Crypto:  box,
		dsn:     testDSN,
		dbName:  dbName,
		rootCtx: context.Background(),
	}
	t.Cleanup(func() {
		workerCancel()
		srv.Close()
		pool.Close()
		dropDatabase(rootDSN, dbName)
	})
	return h
}

// ---------- request helpers ----------

// User is a minimal handle on a registered user — enough to make subsequent
// authenticated requests on its behalf.
type User struct {
	ID           string
	Email        string
	Name         string
	AccessToken  string
	RefreshToken string
}

type registerResp struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	TokenType    string `json:"tokenType"`
	ExpiresIn    int    `json:"expiresIn"`
	User         struct {
		ID         string    `json:"id"`
		Email      string    `json:"email"`
		Name       string    `json:"name"`
		CreateTime time.Time `json:"createTime"`
		UpdateTime time.Time `json:"updateTime"`
	} `json:"user"`
}

// RegisterUser creates a fresh user via /v1/auth/register and returns the
// resulting tokens + id. Fails the test on any non-201.
func (h *Harness) RegisterUser(email, password, name string) *User {
	h.T.Helper()
	body := map[string]string{"email": email, "password": password, "name": name}
	resp := h.do(http.MethodPost, "/v1/auth/register", "", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		h.T.Fatalf("register %q: status=%d body=%s", email, resp.StatusCode, raw)
	}
	var rr registerResp
	if err := decodeStrict(resp.Body, &rr); err != nil {
		h.T.Fatalf("decode register: %v", err)
	}
	return &User{
		ID:           rr.User.ID,
		Email:        rr.User.Email,
		Name:         rr.User.Name,
		AccessToken:  rr.AccessToken,
		RefreshToken: rr.RefreshToken,
	}
}

// RegisterRandomUser is the common case — generate a unique email & password
// so parallel tests don't collide on the email-uniqueness constraint.
func (h *Harness) RegisterRandomUser() *User {
	h.T.Helper()
	suffix := randHex(6)
	return h.RegisterUser("user-"+suffix+"@example.test", "supersecret123", "User "+suffix)
}

// Do sends an HTTP request against the test server. If `bearer` is non-empty
// it is sent as the Authorization header. `body` is JSON-encoded if non-nil.
func (h *Harness) Do(method, path, bearer string, body any) *http.Response {
	h.T.Helper()
	return h.do(method, path, bearer, body)
}

// DoJSON is like Do, but it also decodes the response body into `out` (with
// DisallowUnknownFields) and asserts the response status. Returns the
// decoded body's status code so callers can do extra assertions.
func (h *Harness) DoJSON(method, path, bearer string, body any, wantStatus int, out any) {
	h.T.Helper()
	resp := h.do(method, path, bearer, body)
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		raw, _ := io.ReadAll(resp.Body)
		h.T.Fatalf("%s %s: status=%d want=%d body=%s", method, path, resp.StatusCode, wantStatus, raw)
	}
	if out != nil {
		if err := decodeStrict(resp.Body, out); err != nil {
			h.T.Fatalf("decode %s %s: %v", method, path, err)
		}
	}
}

// AssertStatus runs a request and confirms the response code, returning the
// decoded error envelope (best-effort) for further assertions.
func (h *Harness) AssertStatus(method, path, bearer string, body any, wantStatus int) ErrorEnvelope {
	h.T.Helper()
	resp := h.do(method, path, bearer, body)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		h.T.Fatalf("%s %s: status=%d want=%d body=%s", method, path, resp.StatusCode, wantStatus, raw)
	}
	var env ErrorEnvelope
	_ = json.Unmarshal(raw, &env) // best-effort; success responses won't decode
	return env
}

// ErrorEnvelope is the {code,message} shape that writeError produces.
type ErrorEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (h *Harness) do(method, path, bearer string, body any) *http.Response {
	h.T.Helper()
	var buf io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.T.Fatalf("marshal body: %v", err)
		}
		buf = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, h.Server.URL+path, buf)
	if err != nil {
		h.T.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.T.Fatalf("do request: %v", err)
	}
	return resp
}

// decodeStrict mirrors readJSON's contract — unknown fields fail the test, so
// drift between handler responses and our fixtures is loud instead of silent.
func decodeStrict(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected extra JSON in response")
	}
	return nil
}

// ---------- DSN/db helpers ----------

func replaceDBName(dsn, newName string) string {
	// Split on '?' to preserve query params.
	base, query, hasQuery := strings.Cut(dsn, "?")
	slash := strings.LastIndex(base, "/")
	if slash < 0 {
		// No path component, append one
		base = base + "/" + newName
	} else {
		base = base[:slash+1] + newName
	}
	if hasQuery {
		return base + "?" + query
	}
	return base
}

func redactDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return "<unparseable>"
	}
	if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	return u.String()
}

func dropDatabase(rootDSN, dbName string) {
	root, err := sql.Open("pgx", rootDSN)
	if err != nil {
		return
	}
	defer root.Close()
	// Force-disconnect any leftover sessions so DROP doesn't get stuck.
	_, _ = root.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, dbName)
	_, _ = root.Exec("DROP DATABASE IF EXISTS " + dbName)
}

func randHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// ---------- FakeDocker ----------

// FakeDocker satisfies api.Provisioner without touching a real Docker daemon.
// It records calls so tests can assert on what the handler did, and lets each
// test override behavior (e.g. force Provision to fail).
type FakeDocker struct {
	ProvisionFn        func(ctx context.Context, spec dockerprov.DeploymentSpec) (*dockerprov.DeploymentInfo, error)
	DestroyFn          func(ctx context.Context, name string) error
	StatusFn           func(ctx context.Context, name string) (string, error)
	StatusReplicaFn    func(ctx context.Context, name string, replicaIndex int) (string, error)
	GenerateAdminKeyFn func(ctx context.Context, name, secret string) (string, error)
	DestroyAsterFn     func(ctx context.Context, name string) error
	StatusAsterFn      func(ctx context.Context, name string) (string, error)
	InvokeAsterCellFn  func(ctx context.Context, req dockerprov.InvokeAsterRequest) (*dockerprov.InvokeAsterResult, error)

	Provisioned       []dockerprov.DeploymentSpec
	Destroyed         []string
	DestroyedAster    []string
	InvokedAsterCells []dockerprov.InvokeAsterRequest
}

func NewFakeDocker() *FakeDocker {
	return &FakeDocker{}
}

func (f *FakeDocker) Provision(ctx context.Context, spec dockerprov.DeploymentSpec) (*dockerprov.DeploymentInfo, error) {
	f.Provisioned = append(f.Provisioned, spec)
	if f.ProvisionFn != nil {
		return f.ProvisionFn(ctx, spec)
	}
	return &dockerprov.DeploymentInfo{
		ContainerID:   "fake-container-" + spec.Name,
		HostPort:      spec.HostPort,
		DeploymentURL: fmt.Sprintf("http://127.0.0.1:%d", spec.HostPort),
	}, nil
}

func (f *FakeDocker) Destroy(ctx context.Context, name string) error {
	f.Destroyed = append(f.Destroyed, name)
	if f.DestroyFn != nil {
		return f.DestroyFn(ctx, name)
	}
	return nil
}

func (f *FakeDocker) Status(ctx context.Context, name string) (string, error) {
	if f.StatusFn != nil {
		return f.StatusFn(ctx, name)
	}
	return "running", nil
}

// StatusReplica is the HA-aware variant. Defaults to delegating to
// StatusFn — most tests don't care about the index. Tests that exercise
// the replica-level path can set StatusReplicaFn directly.
func (f *FakeDocker) StatusReplica(ctx context.Context, name string, replicaIndex int) (string, error) {
	if f.StatusReplicaFn != nil {
		return f.StatusReplicaFn(ctx, name, replicaIndex)
	}
	if f.StatusFn != nil {
		return f.StatusFn(ctx, name)
	}
	return "running", nil
}

// GenerateAdminKey: tests can override; default returns a deterministic
// placeholder that's enough for code that just persists & echoes the value.
func (f *FakeDocker) GenerateAdminKey(ctx context.Context, name, secret string) (string, error) {
	if f.GenerateAdminKeyFn != nil {
		return f.GenerateAdminKeyFn(ctx, name, secret)
	}
	return "fake-admin-key-" + name, nil
}

// DestroyAster mirrors Destroy but for kind=aster deployments. Default
// records the name in DestroyedAster and returns nil; tests can override
// to simulate failures.
func (f *FakeDocker) DestroyAster(ctx context.Context, name string) error {
	f.DestroyedAster = append(f.DestroyedAster, name)
	if f.DestroyAsterFn != nil {
		return f.DestroyAsterFn(ctx, name)
	}
	return nil
}

// StatusAster mirrors Status but for the brokerd container.
func (f *FakeDocker) StatusAster(ctx context.Context, name string) (string, error) {
	if f.StatusAsterFn != nil {
		return f.StatusAsterFn(ctx, name)
	}
	return "running", nil
}

// InvokeAsterCell records the request and delegates to InvokeAsterCellFn
// when present. Default returns a deterministic stdout that exercises the
// full handler → docker → response wiring without spinning up real
// containers. Tests that want to assert on env / deployment / JS shape
// inspect the recorded `InvokedAsterCells` slice.
func (f *FakeDocker) InvokeAsterCell(ctx context.Context, req dockerprov.InvokeAsterRequest) (*dockerprov.InvokeAsterResult, error) {
	f.InvokedAsterCells = append(f.InvokedAsterCells, req)
	if f.InvokeAsterCellFn != nil {
		return f.InvokeAsterCellFn(ctx, req)
	}
	return &dockerprov.InvokeAsterResult{
		Stdout:   `{"output":42,"traps":0,"capsule_hash":"fake-hash"}`,
		Stderr:   "",
		ExitCode: 0,
	}, nil
}

// SeedDeployment inserts a deployments row directly. Useful for exercising
// list/get/delete/auth paths without going through the provisioning handler.
// Returns the new deployment id.
func (h *Harness) SeedDeployment(projectID, name, depType, status string, isDefault bool, creatorUserID string, hostPort int, adminKey string) string {
	h.T.Helper()
	if name == "" {
		name = "test-" + randHex(4)
	}
	if depType == "" {
		depType = "dev"
	}
	if status == "" {
		status = "running"
	}
	if adminKey == "" {
		adminKey = "fake-admin-" + randHex(8)
	}
	// loadDeployment scans container_id straight into a string (not *string), so
	// we have to insert a non-NULL value here. Use a placeholder; the
	// FakeDocker doesn't care what's there.
	var id string
	err := h.DB.QueryRow(h.rootCtx, `
		INSERT INTO deployments (project_id, name, deployment_type, status, host_port,
		                          admin_key, instance_secret, is_default, creator_user_id,
		                          deployment_url, container_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id
	`, projectID, name, depType, status, sqlNull(hostPort), adminKey, "fake-secret-"+randHex(8),
		isDefault, sqlNullStr(creatorUserID), fmt.Sprintf("http://127.0.0.1:%d", hostPort),
		"fake-container-"+name).Scan(&id)
	if err != nil {
		h.T.Fatalf("seed deployment: %v", err)
	}
	// Mirror the row into deployment_replicas — every deployment has at
	// least one replica row, by invariant. The migration backfills
	// pre-existing rows; SeedDeployment runs post-migration so we have
	// to do the same insert ourselves to keep tests in sync with the
	// production code path.
	replicaStatus := status
	if status == "deleted" {
		replicaStatus = "stopped"
	}
	if _, err := h.DB.Exec(h.rootCtx, `
		INSERT INTO deployment_replicas (deployment_id, replica_index, container_id, host_port, status)
		VALUES ($1, 0, $2, $3, $4)
	`, id, "fake-container-"+name, sqlNull(hostPort), replicaStatus); err != nil {
		h.T.Fatalf("seed replica: %v", err)
	}
	return id
}

func sqlNull(i int) any {
	if i == 0 {
		return nil
	}
	return i
}

func sqlNullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
