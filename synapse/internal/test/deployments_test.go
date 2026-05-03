package synapsetest

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	dockerprov "github.com/Iann29/synapse/internal/docker"
)

var errBoom = errors.New("simulated docker error")

// waitForStatus polls the deployments table until the named row reaches the
// expected status or the timeout elapses. The async tests use this to wait
// for the provisioning goroutine to settle.
func waitForStatus(t *testing.T, h *Harness, name, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		if err := h.DB.QueryRow(h.rootCtx,
			`SELECT status FROM deployments WHERE name = $1`, name).Scan(&last); err != nil {
			t.Fatalf("read status: %v", err)
		}
		if last == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("status of %q never became %q within %v (last seen: %q)", name, want, timeout, last)
}

// itoa is a tiny stand-in for strconv.Itoa to keep the call sites readable.
func itoa(i int) string { return strconv.Itoa(i) }

type deploymentResp struct {
	ID             string     `json:"id"`
	ProjectID      string     `json:"projectId"`
	Name           string     `json:"name"`
	DeploymentType string     `json:"deploymentType"`
	Kind           string     `json:"kind"`
	Status         string     `json:"status"`
	DeploymentURL  string     `json:"deploymentUrl,omitempty"`
	IsDefault      bool       `json:"isDefault"`
	Reference      string     `json:"reference,omitempty"`
	Creator        string     `json:"creator,omitempty"`
	Adopted        bool       `json:"adopted,omitempty"`
	HAEnabled      bool       `json:"haEnabled,omitempty"`
	ReplicaCount   int        `json:"replicaCount,omitempty"`
	CreateTime     time.Time  `json:"createTime"`
	LastDeployTime *time.Time `json:"lastDeployTime,omitempty"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
}

type deploymentAuthResp struct {
	DeploymentName string `json:"deploymentName"`
	DeploymentURL  string `json:"deploymentUrl"`
	AdminKey       string `json:"adminKey"`
	DeploymentType string `json:"deploymentType"`
}

func TestDeployments_GetReturnsRow(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Dep Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "DepProj")

	id := h.SeedDeployment(proj.ID, "happy-cat-1234", "dev", "running", true, owner.ID, 3211, "")
	_ = id

	var got deploymentResp
	h.DoJSON(http.MethodGet, "/v1/deployments/happy-cat-1234", owner.AccessToken,
		nil, http.StatusOK, &got)
	if got.Name != "happy-cat-1234" {
		t.Errorf("name mismatch: %s", got.Name)
	}
	if got.Status != "running" {
		t.Errorf("status: got %s want running", got.Status)
	}
	if got.DeploymentURL == "" {
		t.Errorf("expected deployment URL")
	}
	// admin_key MUST NOT be in the response.
	// (This is enforced by `json:"-"` in models.Deployment; if a future
	// refactor breaks that, our DisallowUnknownFields decode will catch it.)
}

func TestDeployments_GetUnknown(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	env := h.AssertStatus(http.MethodGet, "/v1/deployments/no-such-name",
		owner.AccessToken, nil, http.StatusNotFound)
	if env.Code != "deployment_not_found" {
		t.Errorf("got code %q want deployment_not_found", env.Code)
	}
}

func TestDeployments_NonMember403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	stranger := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Closed Dep Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Closed")
	h.SeedDeployment(proj.ID, "secret-fox-9999", "prod", "running", false, owner.ID, 3212, "")

	env := h.AssertStatus(http.MethodGet, "/v1/deployments/secret-fox-9999",
		stranger.AccessToken, nil, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("got code %q want forbidden", env.Code)
	}
}

func TestDeployments_AuthReturnsAdminKey(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Auth Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "AuthProj")
	h.SeedDeployment(proj.ID, "auth-bee-5555", "prod", "running", true, owner.ID, 3213, "secret-key-xyz")

	var got deploymentAuthResp
	h.DoJSON(http.MethodGet, "/v1/deployments/auth-bee-5555/auth", owner.AccessToken,
		nil, http.StatusOK, &got)
	if got.AdminKey != "secret-key-xyz" {
		t.Errorf("admin key mismatch: got %q", got.AdminKey)
	}
	if got.DeploymentName != "auth-bee-5555" {
		t.Errorf("name mismatch: %s", got.DeploymentName)
	}
	if got.DeploymentType != "prod" {
		t.Errorf("type mismatch: %s", got.DeploymentType)
	}
	if got.DeploymentURL == "" {
		t.Errorf("expected deployment URL")
	}
}

func TestDeployments_AuthNonMember403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	stranger := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "AuthStranger Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(proj.ID, "stranger-owl-7777", "dev", "running", false, owner.ID, 3214, "")

	h.AssertStatus(http.MethodGet, "/v1/deployments/stranger-owl-7777/auth",
		stranger.AccessToken, nil, http.StatusForbidden)
}

// cliCredentialsResp mirrors the JSON shape returned by
// GET /v1/deployments/{name}/cli_credentials. Decoded with
// DisallowUnknownFields so any drift in the handler payload fails loudly.
type cliCredentialsResp struct {
	DeploymentName string `json:"deploymentName"`
	ConvexURL      string `json:"convexUrl"`
	AdminKey       string `json:"adminKey"`
	ExportSnippet  string `json:"exportSnippet"`
	EnvSnippet     string `json:"envSnippet"`
}

func TestDeployments_CLICredentialsReturnsExportSnippet(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "CLI Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "CLIProj")
	h.SeedDeployment(proj.ID, "cli-rabbit-1234", "prod", "running", true, owner.ID, 3220, "admin-key-abc")

	var got cliCredentialsResp
	h.DoJSON(http.MethodGet, "/v1/deployments/cli-rabbit-1234/cli_credentials",
		owner.AccessToken, nil, http.StatusOK, &got)

	if got.DeploymentName != "cli-rabbit-1234" {
		t.Errorf("deployment name: got %q want cli-rabbit-1234", got.DeploymentName)
	}
	if got.AdminKey != "admin-key-abc" {
		t.Errorf("admin key: got %q want admin-key-abc", got.AdminKey)
	}
	if got.ConvexURL == "" {
		t.Errorf("expected convex URL")
	}

	// Both snippets must contain BOTH env-var names so either copy-paste
	// path sets the CLI up correctly. We don't pin the exact line
	// ordering, but the values must match the structured fields.
	for _, tc := range []struct {
		name    string
		snippet string
		prefix  string // "export " for shell, "" for .env
	}{
		{"export", got.ExportSnippet, "export "},
		{"env", got.EnvSnippet, ""},
	} {
		if !strings.Contains(tc.snippet, tc.prefix+"CONVEX_SELF_HOSTED_URL") {
			t.Errorf("%s snippet missing %sCONVEX_SELF_HOSTED_URL: %q", tc.name, tc.prefix, tc.snippet)
		}
		if !strings.Contains(tc.snippet, tc.prefix+"CONVEX_SELF_HOSTED_ADMIN_KEY") {
			t.Errorf("%s snippet missing %sCONVEX_SELF_HOSTED_ADMIN_KEY: %q", tc.name, tc.prefix, tc.snippet)
		}
		if !strings.Contains(tc.snippet, "admin-key-abc") {
			t.Errorf("%s snippet missing admin key value: %q", tc.name, tc.snippet)
		}
		if !strings.Contains(tc.snippet, got.ConvexURL) {
			t.Errorf("%s snippet missing convex URL %q: %q", tc.name, got.ConvexURL, tc.snippet)
		}
	}
	// Belt-and-suspenders: the .env snippet must NOT carry `export `
	// (otherwise dotenv parsers choke).
	if strings.Contains(got.EnvSnippet, "export ") {
		t.Errorf("env snippet should not contain `export `: %q", got.EnvSnippet)
	}
}

// CLI URL must be a *root* URL the official `npx convex` CLI can hit
// directly. The CLI builds API requests via `new URL("/api/...", baseUrl)`,
// which is host-anchored — a baseUrl like `<host>:8080/d/<name>` would
// resolve to `<host>:8080/api/...` (Synapse 404), not the deployment.
// So the snippet must point at the per-deployment host port instead of
// the path-proxy form, even when ProxyEnabled is on for browsers.
func TestDeployments_CLICredentialsURLBypassesPathProxy(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		PublicURL:    "http://synapse.example.com:8080",
		ProxyEnabled: true,
	})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "CLIProxy Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(proj.ID, "cli-fox-9999", "dev", "running", false, owner.ID, 3290, "key-xyz")

	var got cliCredentialsResp
	h.DoJSON(http.MethodGet, "/v1/deployments/cli-fox-9999/cli_credentials",
		owner.AccessToken, nil, http.StatusOK, &got)

	// CLI-facing URL strips the synapse-api port and appends the
	// deployment's own host_port, giving the CLI a root URL.
	wantURL := "http://synapse.example.com:3290"
	if got.ConvexURL != wantURL {
		t.Errorf("convexUrl: got %q want %q", got.ConvexURL, wantURL)
	}
	// The /d/<name> path-proxy form must NOT leak into the CLI snippet —
	// it'd silently break `npx convex dev`.
	if strings.Contains(got.ConvexURL, "/d/") {
		t.Errorf("CLI URL must not use /d/<name> proxy form: %q", got.ConvexURL)
	}
}

// When BaseDomain is set, every deployment gets its own subdomain — that
// is already a root URL, no port-strip needed. Verifies CLI snippet uses
// the wildcard form instead of falling back.
func TestDeployments_CLICredentialsBaseDomainURL(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		PublicURL:    "http://synapse.example.com:8080",
		ProxyEnabled: true,
		BaseDomain:   "convex.example.com",
	})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "CLIWild Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(proj.ID, "cli-bee-4242", "prod", "running", true, owner.ID, 3300, "key-bd")

	var got cliCredentialsResp
	h.DoJSON(http.MethodGet, "/v1/deployments/cli-bee-4242/cli_credentials",
		owner.AccessToken, nil, http.StatusOK, &got)

	wantURL := "https://cli-bee-4242.convex.example.com"
	if got.ConvexURL != wantURL {
		t.Errorf("convexUrl: got %q want %q", got.ConvexURL, wantURL)
	}
}

func TestDeployments_CLICredentialsAnonymous401(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "CLIAnon Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(proj.ID, "cli-anon-2222", "dev", "running", true, owner.ID, 3221, "")

	// No bearer at all → 401 from the auth middleware.
	env := h.AssertStatus(http.MethodGet, "/v1/deployments/cli-anon-2222/cli_credentials",
		"", nil, http.StatusUnauthorized)
	if env.Code == "" {
		t.Errorf("expected error code on 401, got empty envelope")
	}
}

func TestDeployments_CLICredentialsNonMember403(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	stranger := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "CLIStranger Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(proj.ID, "cli-stranger-3333", "dev", "running", false, owner.ID, 3222, "")

	env := h.AssertStatus(http.MethodGet, "/v1/deployments/cli-stranger-3333/cli_credentials",
		stranger.AccessToken, nil, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("got code %q want forbidden", env.Code)
	}
}

func TestDeployments_CLICredentialsUnknown404(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	env := h.AssertStatus(http.MethodGet, "/v1/deployments/no-such-name/cli_credentials",
		owner.AccessToken, nil, http.StatusNotFound)
	if env.Code != "deployment_not_found" {
		t.Errorf("got code %q want deployment_not_found", env.Code)
	}
}

func TestDeployments_DeleteMarksRowDeletedAndCallsDocker(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Del Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	id := h.SeedDeployment(proj.ID, "del-wolf-3333", "dev", "running", true, owner.ID, 3215, "")

	h.DoJSON(http.MethodPost, "/v1/deployments/del-wolf-3333/delete", owner.AccessToken,
		nil, http.StatusOK, &map[string]string{})

	// Fake docker should have been called with the deployment name.
	if len(h.Docker.Destroyed) != 1 || h.Docker.Destroyed[0] != "del-wolf-3333" {
		t.Errorf("expected fake Destroy([del-wolf-3333]), got %v", h.Docker.Destroyed)
	}

	// Row is now status=deleted with cleared host_port + container_id.
	var status string
	var hostPort *int
	if err := h.DB.QueryRow(h.rootCtx,
		`SELECT status, host_port FROM deployments WHERE id = $1`, id).Scan(&status, &hostPort); err != nil {
		t.Fatalf("read deployment row: %v", err)
	}
	if status != "deleted" {
		t.Errorf("status: got %q want deleted", status)
	}
	if hostPort != nil {
		t.Errorf("expected host_port to be nulled, got %v", *hostPort)
	}
}

func TestDeployments_DeleteAdminOnly(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	member := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Member Del Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(proj.ID, "stay-cat-4444", "dev", "running", true, owner.ID, 3216, "")

	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		team.ID, member.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	env := h.AssertStatus(http.MethodPost, "/v1/deployments/stay-cat-4444/delete",
		member.AccessToken, nil, http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("got code %q want forbidden", env.Code)
	}
	// Fake docker should NOT have been called.
	if len(h.Docker.Destroyed) != 0 {
		t.Errorf("expected no destroys, got %v", h.Docker.Destroyed)
	}
}

func TestDeployments_ListExcludesDeleted(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "List Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")

	// Two live deployments + one deleted.
	h.SeedDeployment(proj.ID, "live-fox-1111", "dev", "running", true, owner.ID, 3217, "")
	h.SeedDeployment(proj.ID, "live-fox-2222", "prod", "running", false, owner.ID, 3218, "")
	h.SeedDeployment(proj.ID, "dead-fox-3333", "dev", "deleted", false, owner.ID, 0, "")

	var fromTeam []deploymentResp
	h.DoJSON(http.MethodGet, "/v1/teams/"+team.Slug+"/list_deployments",
		owner.AccessToken, nil, http.StatusOK, &fromTeam)
	if len(fromTeam) != 2 {
		t.Errorf("team list_deployments: expected 2 live, got %d (%+v)", len(fromTeam), fromTeam)
	}

	var fromProject []deploymentResp
	h.DoJSON(http.MethodGet, "/v1/projects/"+proj.ID+"/list_deployments",
		owner.AccessToken, nil, http.StatusOK, &fromProject)
	if len(fromProject) != 2 {
		t.Errorf("project list_deployments: expected 2 live, got %d", len(fromProject))
	}

	// Deleted one should not be retrievable individually either.
	env := h.AssertStatus(http.MethodGet, "/v1/deployments/dead-fox-3333",
		owner.AccessToken, nil, http.StatusNotFound)
	if env.Code != "deployment_not_found" {
		t.Errorf("got code %q want deployment_not_found", env.Code)
	}
}

// TestDeployments_CreateReturnsImmediatelyAndProvisionsAsync covers the new
// async contract: POST /create_deployment returns 201 the instant the row is
// inserted (status="provisioning"), and FakeDocker.Provision is invoked
// shortly after on a background goroutine.
func TestDeployments_CreateReturnsImmediatelyAndProvisionsAsync(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Async Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "AsyncProj")

	// Make Provision slow-ish so the response definitely beats it. If the
	// handler were still synchronous, our 201 wouldn't arrive until after
	// this sleep — which would fail the elapsed-time assertion below.
	provisionDone := make(chan struct{})
	h.Docker.ProvisionFn = func(_ context.Context, spec dockerprov.DeploymentSpec) (*dockerprov.DeploymentInfo, error) {
		defer close(provisionDone)
		time.Sleep(150 * time.Millisecond)
		return &dockerprov.DeploymentInfo{
			ContainerID:   "fake-" + spec.Name,
			HostPort:      spec.HostPort,
			DeploymentURL: "http://127.0.0.1:" + itoa(spec.HostPort),
		}, nil
	}

	start := time.Now()
	var got deploymentResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/create_deployment",
		owner.AccessToken, map[string]string{"type": "dev"}, http.StatusCreated, &got)
	elapsed := time.Since(start)

	if got.Status != "provisioning" {
		t.Errorf("status: got %q want provisioning", got.Status)
	}
	if got.Name == "" {
		t.Errorf("expected a generated name, got empty")
	}
	// Generous bound — request should return well before Provision finishes.
	if elapsed >= 150*time.Millisecond {
		t.Errorf("expected fast return; elapsed=%v (handler still sync?)", elapsed)
	}

	// Wait for the goroutine to call Provision.
	select {
	case <-provisionDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("Provision was never called by the background goroutine")
	}

	// Now poll until the row flips to "running".
	waitForStatus(t, h, got.Name, "running", 5*time.Second)

	// FakeDocker recorded the call.
	if len(h.Docker.Provisioned) != 1 || h.Docker.Provisioned[0].Name != got.Name {
		t.Errorf("expected Provisioned([%s]), got %+v", got.Name, h.Docker.Provisioned)
	}
}

// TestDeployments_CreateAsyncFailureMarksRowFailed covers the unhappy path:
// FakeDocker returns an error from Provision, so the goroutine should
// transition the row to "failed" (not leave it stuck in "provisioning").
func TestDeployments_CreateAsyncFailureMarksRowFailed(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Fail Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "FailProj")

	h.Docker.ProvisionFn = func(_ context.Context, _ dockerprov.DeploymentSpec) (*dockerprov.DeploymentInfo, error) {
		return nil, errBoom
	}

	var got deploymentResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/create_deployment",
		owner.AccessToken, map[string]string{"type": "dev"}, http.StatusCreated, &got)
	if got.Status != "provisioning" {
		t.Fatalf("status: got %q want provisioning (initial state)", got.Status)
	}

	waitForStatus(t, h, got.Name, "failed", 5*time.Second)

	// last_deploy_at should be populated so the UI knows when we gave up.
	var lastDeploy *time.Time
	if err := h.DB.QueryRow(h.rootCtx,
		`SELECT last_deploy_at FROM deployments WHERE name = $1`, got.Name).
		Scan(&lastDeploy); err != nil {
		t.Fatalf("read last_deploy_at: %v", err)
	}
	if lastDeploy == nil {
		t.Errorf("expected last_deploy_at to be set on failed provision")
	}
}

// TestDeployments_ListIncludesProvisioning ensures the row is visible in
// list_deployments while still mid-provisioning. Without this, the dashboard
// can't show the "provisioning..." badge while the goroutine is in flight.
func TestDeployments_ListIncludesProvisioning(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Vis Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "VisProj")

	// Block Provision so the row stays in "provisioning" for the duration
	// of the assertions below. We unblock at the end so the goroutine can
	// exit cleanly before the test (and its DB) tears down.
	release := make(chan struct{})
	h.Docker.ProvisionFn = func(ctx context.Context, spec dockerprov.DeploymentSpec) (*dockerprov.DeploymentInfo, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &dockerprov.DeploymentInfo{
			ContainerID:   "fake-" + spec.Name,
			HostPort:      spec.HostPort,
			DeploymentURL: "http://127.0.0.1:" + itoa(spec.HostPort),
		}, nil
	}
	defer close(release)

	var created deploymentResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/create_deployment",
		owner.AccessToken, map[string]string{"type": "dev"}, http.StatusCreated, &created)

	var listed []deploymentResp
	h.DoJSON(http.MethodGet, "/v1/projects/"+proj.ID+"/list_deployments",
		owner.AccessToken, nil, http.StatusOK, &listed)
	if len(listed) != 1 {
		t.Fatalf("expected 1 deployment, got %d (%+v)", len(listed), listed)
	}
	if listed[0].Name != created.Name {
		t.Errorf("listed name mismatch: got %q want %q", listed[0].Name, created.Name)
	}
	if listed[0].Status != "provisioning" {
		t.Errorf("status: got %q want provisioning", listed[0].Status)
	}
}

func TestDeployments_DeleteIdempotentOnDockerError(t *testing.T) {
	// If the fake docker returns an error, we should NOT mark the row deleted —
	// the operator can retry. Mirrors the prod contract.
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Err Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	id := h.SeedDeployment(proj.ID, "fail-bee-9999", "dev", "running", true, owner.ID, 3219, "")

	h.Docker.DestroyFn = func(_ context.Context, _ string) error {
		return errBoom
	}

	env := h.AssertStatus(http.MethodPost, "/v1/deployments/fail-bee-9999/delete",
		owner.AccessToken, nil, http.StatusInternalServerError)
	if env.Code != "destroy_failed" {
		t.Errorf("got code %q want destroy_failed", env.Code)
	}

	// Row remains running so the operator can retry after fixing docker.
	var status string
	if err := h.DB.QueryRow(h.rootCtx,
		`SELECT status FROM deployments WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if status != "running" {
		t.Errorf("status: got %q want running (retry-able)", status)
	}
}
