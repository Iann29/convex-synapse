package synapsetest

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// deployKeyResp mirrors the JSON shape returned by
// POST /v1/deployments/{name}/deploy_keys. Decoded with
// DisallowUnknownFields so any drift in the handler payload fails loudly.
type deployKeyResp struct {
	ID            string  `json:"id"`
	DeploymentID  string  `json:"deploymentId"`
	Name          string  `json:"name"`
	AdminKey      string  `json:"adminKey"`
	Prefix        string  `json:"prefix"`
	CreatedBy     *string `json:"createdBy,omitempty"`
	CreatedByName string  `json:"createdByName,omitempty"`
	CreateTime    string  `json:"createTime"`
	EnvSnippet    string  `json:"envSnippet"`
	ExportSnippet string  `json:"exportSnippet"`
}

type listDeployKeysResp struct {
	DeployKeys []struct {
		ID            string  `json:"id"`
		DeploymentID  string  `json:"deploymentId"`
		Name          string  `json:"name"`
		Prefix        string  `json:"prefix"`
		CreatedBy     *string `json:"createdBy,omitempty"`
		CreatedByName string  `json:"createdByName,omitempty"`
		CreateTime    string  `json:"createTime"`
	} `json:"deployKeys"`
}

// uniqueAdminKeyDocker overrides FakeDocker.GenerateAdminKey to return
// distinct values per call (the default implementation is deterministic
// per name, which would let tests accidentally pass when the handler
// doesn't actually mint a fresh key per deploy_key row).
func uniqueAdminKeyDocker(deploymentName string) func(*FakeDocker) {
	var counter int64
	return func(f *FakeDocker) {
		f.GenerateAdminKeyFn = func(_ context.Context, name, _ string) (string, error) {
			n := atomic.AddInt64(&counter, 1)
			// Mirror the real-world shape: "<deployment>|<hex>". The
			// handler uses the bit after `|` for prefix display, so a
			// recognisable suffix makes assertions easier.
			return name + "|" + uniqueHex(n), nil
		}
		_ = deploymentName // kept for callsite readability
	}
}

func uniqueHex(n int64) string {
	const chars = "0123456789abcdef"
	out := []byte("00000000000000000000")
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = chars[n&0xf]
		n >>= 4
	}
	return string(out)
}

func TestDeployKeys_Create_ReturnsAdminKeyAndSnippets(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		PublicURL:    "http://synapse.example.com:8080",
		ProxyEnabled: true,
	})
	uniqueAdminKeyDocker("dk-falcon-1111")(h.Docker)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "DK Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "DKProj")
	h.SeedDeployment(proj.ID, "dk-falcon-1111", "prod", "running", true, owner.ID, 3401, "")

	var got deployKeyResp
	h.DoJSON(http.MethodPost, "/v1/deployments/dk-falcon-1111/deploy_keys",
		owner.AccessToken,
		map[string]any{"name": "vercel"},
		http.StatusCreated, &got)

	if got.ID == "" || got.AdminKey == "" {
		t.Fatalf("expected id + adminKey, got %+v", got)
	}
	if got.Name != "vercel" {
		t.Errorf("name: got %q want vercel", got.Name)
	}
	if got.Prefix == "" || strings.Contains(got.AdminKey, "|") && !strings.Contains(got.AdminKey, got.Prefix) {
		t.Errorf("prefix %q should appear in adminKey %q", got.Prefix, got.AdminKey)
	}
	// Snippets must reference the deployment's host-port (CLI-compatible
	// root URL, not the /d/<name> path-proxy form).
	wantURL := "http://synapse.example.com:3401"
	if !strings.Contains(got.EnvSnippet, wantURL) {
		t.Errorf("env snippet missing %q: %q", wantURL, got.EnvSnippet)
	}
	if !strings.Contains(got.ExportSnippet, wantURL) {
		t.Errorf("export snippet missing %q: %q", wantURL, got.ExportSnippet)
	}
	if !strings.Contains(got.EnvSnippet, got.AdminKey) {
		t.Errorf("env snippet missing adminKey: %q", got.EnvSnippet)
	}
	if strings.Contains(got.EnvSnippet, "export ") {
		t.Errorf(".env snippet must not carry `export `: %q", got.EnvSnippet)
	}
	if !strings.HasPrefix(got.ExportSnippet, "export ") {
		t.Errorf("shell snippet must start with `export `: %q", got.ExportSnippet)
	}
}

func TestDeployKeys_List_OnlyActive(t *testing.T) {
	h := Setup(t)
	uniqueAdminKeyDocker("dk-otter-2222")(h.Docker)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "DK List Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(proj.ID, "dk-otter-2222", "dev", "running", false, owner.ID, 3402, "")

	var k1, k2 deployKeyResp
	h.DoJSON(http.MethodPost, "/v1/deployments/dk-otter-2222/deploy_keys",
		owner.AccessToken, map[string]any{"name": "ci-prod"}, http.StatusCreated, &k1)
	h.DoJSON(http.MethodPost, "/v1/deployments/dk-otter-2222/deploy_keys",
		owner.AccessToken, map[string]any{"name": "ci-staging"}, http.StatusCreated, &k2)

	// Revoke k1 — list should drop it.
	h.AssertStatus(http.MethodPost, "/v1/deployments/dk-otter-2222/deploy_keys/"+k1.ID+"/revoke",
		owner.AccessToken, nil, http.StatusNoContent)

	var listed listDeployKeysResp
	h.DoJSON(http.MethodGet, "/v1/deployments/dk-otter-2222/deploy_keys",
		owner.AccessToken, nil, http.StatusOK, &listed)
	if len(listed.DeployKeys) != 1 {
		t.Fatalf("expected 1 active deploy key after revoke, got %d", len(listed.DeployKeys))
	}
	if listed.DeployKeys[0].ID != k2.ID {
		t.Errorf("listed key id: got %q want %q", listed.DeployKeys[0].ID, k2.ID)
	}
	// List omits the actual admin key — that value is shown ONCE at
	// creation. Drift here would silently leak credentials on every
	// dashboard render.
	if listed.DeployKeys[0].Name == "" || listed.DeployKeys[0].Prefix == "" {
		t.Errorf("expected non-empty name+prefix, got %+v", listed.DeployKeys[0])
	}
}

func TestDeployKeys_Create_DuplicateName_Conflict(t *testing.T) {
	h := Setup(t)
	uniqueAdminKeyDocker("dk-bee-3333")(h.Docker)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "DK Dup Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(proj.ID, "dk-bee-3333", "prod", "running", true, owner.ID, 3403, "")

	h.AssertStatus(http.MethodPost, "/v1/deployments/dk-bee-3333/deploy_keys",
		owner.AccessToken, map[string]any{"name": "vercel"}, http.StatusCreated)
	h.AssertStatus(http.MethodPost, "/v1/deployments/dk-bee-3333/deploy_keys",
		owner.AccessToken, map[string]any{"name": "vercel"}, http.StatusConflict)
}

func TestDeployKeys_Create_NameReusableAfterRevoke(t *testing.T) {
	h := Setup(t)
	uniqueAdminKeyDocker("dk-ant-4444")(h.Docker)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "DK Reuse Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(proj.ID, "dk-ant-4444", "prod", "running", true, owner.ID, 3404, "")

	var first deployKeyResp
	h.DoJSON(http.MethodPost, "/v1/deployments/dk-ant-4444/deploy_keys",
		owner.AccessToken, map[string]any{"name": "vercel"}, http.StatusCreated, &first)
	h.AssertStatus(http.MethodPost, "/v1/deployments/dk-ant-4444/deploy_keys/"+first.ID+"/revoke",
		owner.AccessToken, nil, http.StatusNoContent)

	// Same name is now free again.
	h.AssertStatus(http.MethodPost, "/v1/deployments/dk-ant-4444/deploy_keys",
		owner.AccessToken, map[string]any{"name": "vercel"}, http.StatusCreated)
}

func TestDeployKeys_Create_ValidationErrors(t *testing.T) {
	h := Setup(t)
	uniqueAdminKeyDocker("dk-fox-5555")(h.Docker)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "DK Val Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(proj.ID, "dk-fox-5555", "prod", "running", true, owner.ID, 3405, "")

	// Empty name → 400 missing_name.
	env := h.AssertStatus(http.MethodPost, "/v1/deployments/dk-fox-5555/deploy_keys",
		owner.AccessToken, map[string]any{"name": "  "}, http.StatusBadRequest)
	if env.Code != "missing_name" {
		t.Errorf("empty name: got code %q want missing_name", env.Code)
	}
	// 65-char name → 400 name_too_long.
	long := strings.Repeat("x", 65)
	env = h.AssertStatus(http.MethodPost, "/v1/deployments/dk-fox-5555/deploy_keys",
		owner.AccessToken, map[string]any{"name": long}, http.StatusBadRequest)
	if env.Code != "name_too_long" {
		t.Errorf("long name: got code %q want name_too_long", env.Code)
	}
}

func TestDeployKeys_Revoke_NotFound(t *testing.T) {
	h := Setup(t)
	uniqueAdminKeyDocker("dk-cat-6666")(h.Docker)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "DK 404 Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(proj.ID, "dk-cat-6666", "prod", "running", true, owner.ID, 3406, "")

	bogusID := "00000000-0000-0000-0000-000000000000"
	h.AssertStatus(http.MethodPost, "/v1/deployments/dk-cat-6666/deploy_keys/"+bogusID+"/revoke",
		owner.AccessToken, nil, http.StatusNotFound)
}

func TestDeployKeys_Revoke_AlreadyRevoked_404(t *testing.T) {
	h := Setup(t)
	uniqueAdminKeyDocker("dk-dog-7777")(h.Docker)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "DK Twice Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(proj.ID, "dk-dog-7777", "prod", "running", true, owner.ID, 3407, "")

	var k deployKeyResp
	h.DoJSON(http.MethodPost, "/v1/deployments/dk-dog-7777/deploy_keys",
		owner.AccessToken, map[string]any{"name": "ci"}, http.StatusCreated, &k)
	h.AssertStatus(http.MethodPost, "/v1/deployments/dk-dog-7777/deploy_keys/"+k.ID+"/revoke",
		owner.AccessToken, nil, http.StatusNoContent)
	h.AssertStatus(http.MethodPost, "/v1/deployments/dk-dog-7777/deploy_keys/"+k.ID+"/revoke",
		owner.AccessToken, nil, http.StatusNotFound)
}

func TestDeployKeys_Revoke_CrossDeployment_404(t *testing.T) {
	h := Setup(t)
	uniqueAdminKeyDocker("dk-cross-a")(h.Docker)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "DK Cross Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(proj.ID, "dk-cross-a", "prod", "running", true, owner.ID, 3408, "")
	h.SeedDeployment(proj.ID, "dk-cross-b", "dev", "running", false, owner.ID, 3409, "")

	// Create a deploy key on deployment A, then try to revoke it
	// addressing deployment B's path. The deployment_id guard in the
	// UPDATE should treat it as 404, not silently succeed.
	var k deployKeyResp
	h.DoJSON(http.MethodPost, "/v1/deployments/dk-cross-a/deploy_keys",
		owner.AccessToken, map[string]any{"name": "ci"}, http.StatusCreated, &k)
	h.AssertStatus(http.MethodPost, "/v1/deployments/dk-cross-b/deploy_keys/"+k.ID+"/revoke",
		owner.AccessToken, nil, http.StatusNotFound)
}

func TestDeployKeys_Permissions(t *testing.T) {
	h := Setup(t)
	uniqueAdminKeyDocker("dk-perm-8888")(h.Docker)
	owner := h.RegisterRandomUser()
	stranger := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "DK Perm Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(proj.ID, "dk-perm-8888", "prod", "running", true, owner.ID, 3410, "")

	// Anonymous → 401.
	h.AssertStatus(http.MethodPost, "/v1/deployments/dk-perm-8888/deploy_keys",
		"", map[string]any{"name": "anon"}, http.StatusUnauthorized)

	// Non-member → 403 (loadDeploymentForRequest path).
	h.AssertStatus(http.MethodPost, "/v1/deployments/dk-perm-8888/deploy_keys",
		stranger.AccessToken, map[string]any{"name": "stranger"}, http.StatusForbidden)
}

func TestDeployKeys_AuditLogOnCreateAndRevoke(t *testing.T) {
	h := Setup(t)
	uniqueAdminKeyDocker("dk-audit-9999")(h.Docker)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "DK Audit Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "P")
	h.SeedDeployment(proj.ID, "dk-audit-9999", "prod", "running", true, owner.ID, 3411, "")

	var k deployKeyResp
	h.DoJSON(http.MethodPost, "/v1/deployments/dk-audit-9999/deploy_keys",
		owner.AccessToken, map[string]any{"name": "ci-audit"}, http.StatusCreated, &k)
	h.AssertStatus(http.MethodPost, "/v1/deployments/dk-audit-9999/deploy_keys/"+k.ID+"/revoke",
		owner.AccessToken, nil, http.StatusNoContent)

	// Both events should be persisted, against this team.
	var createCount, revokeCount int
	if err := h.DB.QueryRow(h.rootCtx, `
		SELECT
		  count(*) FILTER (WHERE action = 'createDeployKey'),
		  count(*) FILTER (WHERE action = 'revokeDeployKey')
		FROM audit_events
		WHERE team_id = $1 AND target_type = 'deployKey' AND target_id = $2
	`, team.ID, k.ID).Scan(&createCount, &revokeCount); err != nil {
		t.Fatalf("query audit events: %v", err)
	}
	if createCount != 1 {
		t.Errorf("expected 1 createDeployKey audit row, got %d", createCount)
	}
	if revokeCount != 1 {
		t.Errorf("expected 1 revokeDeployKey audit row, got %d", revokeCount)
	}
}
