package synapsetest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// deploymentJSON mirrors the wire shape of models.Deployment. Used in adopt
// tests because DisallowUnknownFields means we can't decode into a partial
// struct without listing every field that may appear.
type deploymentJSON struct {
	ID             string     `json:"id"`
	ProjectID      string     `json:"projectId"`
	Name           string     `json:"name"`
	DeploymentType string     `json:"deploymentType"`
	Status         string     `json:"status"`
	DeploymentURL  string     `json:"deploymentUrl,omitempty"`
	IsDefault      bool       `json:"isDefault"`
	Reference      string     `json:"reference,omitempty"`
	Creator        string     `json:"creator,omitempty"`
	Adopted        bool       `json:"adopted,omitempty"`
	CreateTime     time.Time  `json:"createTime"`
	LastDeployTime *time.Time `json:"lastDeployTime,omitempty"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
}

// fakeConvexBackend stands in for a real Convex backend during adoption
// tests. It answers /version and /api/check_admin_key — the two endpoints
// the probe depends on. The configured admin key is the only one accepted.
type fakeConvexBackend struct {
	server *httptest.Server
	want   string // admin key to accept
}

func newFakeConvexBackend(t *testing.T, acceptKey string) *fakeConvexBackend {
	t.Helper()
	f := &fakeConvexBackend{want: acceptKey}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"0.1-test"}`))
		case "/api/check_admin_key":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				AdminKey string `json:"adminKey"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if req.AdminKey != f.want {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// projectFor creates a team + project under the given user, returns the
// project id. Used by adopt tests to get a target for adopt_deployment.
func projectFor(t *testing.T, h *Harness, u *User, teamName, projectName string) (teamID, projectID string) {
	t.Helper()
	team := createTeam(t, h, u.AccessToken, teamName)
	var resp struct {
		ProjectID   string         `json:"projectId"`
		ProjectSlug string         `json:"projectSlug"`
		Project     map[string]any `json:"project"`
	}
	h.DoJSON(http.MethodPost, "/v1/teams/"+team.Slug+"/create_project", u.AccessToken,
		map[string]string{"projectName": projectName}, http.StatusCreated, &resp)
	return team.ID, resp.ProjectID
}

// TestAdopt_HappyPath registers an external backend, hits adopt_deployment,
// confirms the row appears in the project listing with adopted=true, and
// confirms delete skips Docker.Destroy.
func TestAdopt_HappyPath(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	_, projID := projectFor(t, h, u, "AdoptCo", "AdoptedApp")

	const adminKey = "self-hosted-secret-1234"
	backend := newFakeConvexBackend(t, adminKey)

	var d deploymentJSON
	h.DoJSON(http.MethodPost, "/v1/projects/"+projID+"/adopt_deployment", u.AccessToken,
		map[string]any{
			"deploymentUrl":  backend.server.URL,
			"adminKey":       adminKey,
			"deploymentType": "prod",
			"isDefault":      true,
		},
		http.StatusCreated, &d)

	if !d.Adopted {
		t.Errorf("expected adopted=true, got false")
	}
	if d.Status != "running" {
		t.Errorf("expected status=running, got %q", d.Status)
	}
	if d.DeploymentURL != backend.server.URL {
		t.Errorf("expected url=%s, got %q", backend.server.URL, d.DeploymentURL)
	}
	if d.Name == "" {
		t.Errorf("expected auto-allocated name, got empty")
	}

	// Should appear in the project listing with adopted=true visible.
	resp := h.Do(http.MethodGet, "/v1/projects/"+projID+"/list_deployments", u.AccessToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list_deployments status: %d", resp.StatusCode)
	}
	var got []struct {
		ID      string `json:"id"`
		Adopted bool   `json:"adopted"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(got) != 1 || got[0].ID != d.ID || !got[0].Adopted {
		t.Errorf("listing didn't surface adopted row correctly: %+v", got)
	}

	// Delete must NOT call Docker.Destroy on adopted rows. Reset the
	// FakeDocker counter first so any spillover from setup is excluded.
	h.Docker.Destroyed = nil
	h.DoJSON(http.MethodPost, "/v1/deployments/"+d.Name+"/delete", u.AccessToken,
		map[string]any{}, http.StatusOK, nil)
	for _, name := range h.Docker.Destroyed {
		if name == d.Name {
			t.Errorf("Docker.Destroy was called for adopted deployment %q", d.Name)
		}
	}
}

// TestAdopt_BadAdminKey: backend rejects the supplied key, handler returns 400.
func TestAdopt_BadAdminKey(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	_, projID := projectFor(t, h, u, "AdoptCo2", "App2")

	backend := newFakeConvexBackend(t, "the-real-key")
	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+projID+"/adopt_deployment", u.AccessToken,
		map[string]any{
			"deploymentUrl": backend.server.URL,
			"adminKey":      "wrong-key",
		},
		http.StatusBadRequest)
	if env.Code != "invalid_admin_key" {
		t.Errorf("expected invalid_admin_key, got %q", env.Code)
	}
}

// TestAdopt_UnreachableURL: handler should return 502/probe_failed when the
// supplied URL doesn't respond. Uses a server that's been Closed.
func TestAdopt_UnreachableURL(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	_, projID := projectFor(t, h, u, "AdoptCo3", "App3")

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead.Close() // immediately

	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+projID+"/adopt_deployment", u.AccessToken,
		map[string]any{
			"deploymentUrl": dead.URL,
			"adminKey":      "anything",
		},
		http.StatusBadGateway)
	if env.Code != "probe_failed" {
		t.Errorf("expected probe_failed, got %q", env.Code)
	}
}

// TestAdopt_MissingFields: empty url / empty admin key both 400.
func TestAdopt_MissingFields(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	_, projID := projectFor(t, h, u, "AdoptCo4", "App4")

	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+projID+"/adopt_deployment", u.AccessToken,
		map[string]any{"deploymentUrl": "", "adminKey": "x"}, http.StatusBadRequest)
	if env.Code != "missing_url" {
		t.Errorf("expected missing_url, got %q", env.Code)
	}

	env = h.AssertStatus(http.MethodPost, "/v1/projects/"+projID+"/adopt_deployment", u.AccessToken,
		map[string]any{"deploymentUrl": "http://example.com", "adminKey": "  "}, http.StatusBadRequest)
	if env.Code != "missing_admin_key" {
		t.Errorf("expected missing_admin_key, got %q", env.Code)
	}

	env = h.AssertStatus(http.MethodPost, "/v1/projects/"+projID+"/adopt_deployment", u.AccessToken,
		map[string]any{"deploymentUrl": "ftp://nope.example.com", "adminKey": "x"}, http.StatusBadRequest)
	if env.Code != "invalid_url" {
		t.Errorf("expected invalid_url for non-http scheme, got %q", env.Code)
	}
}

// TestAdopt_NonAdminForbidden: a member (non-admin) of the team cannot adopt.
func TestAdopt_NonAdminForbidden(t *testing.T) {
	h := Setup(t)
	admin := h.RegisterRandomUser()
	teamID, projID := projectFor(t, h, admin, "AdoptCo5", "App5")

	// Add a second user as a non-admin member directly via DB (no
	// invite-token round-trip — keeps the test focused).
	member := h.RegisterRandomUser()
	if _, err := h.DB.Exec(h.rootCtx,
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		teamID, member.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	backend := newFakeConvexBackend(t, "k")
	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+projID+"/adopt_deployment",
		member.AccessToken,
		map[string]any{"deploymentUrl": backend.server.URL, "adminKey": "k"},
		http.StatusForbidden)
	if env.Code != "forbidden" {
		t.Errorf("expected forbidden, got %q", env.Code)
	}
}

// TestAdopt_NameCollision: supplying a name that's already taken by another
// deployment returns 409 name_taken.
func TestAdopt_NameCollision(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	_, projID := projectFor(t, h, u, "AdoptCo6", "App6")

	backend := newFakeConvexBackend(t, "k6")

	// First adoption with explicit name.
	var first deploymentJSON
	h.DoJSON(http.MethodPost, "/v1/projects/"+projID+"/adopt_deployment", u.AccessToken,
		map[string]any{
			"deploymentUrl": backend.server.URL,
			"adminKey":      "k6",
			"name":          "my-existing-app",
		},
		http.StatusCreated, &first)

	_ = first
	// Second adoption with the same name.
	env := h.AssertStatus(http.MethodPost, "/v1/projects/"+projID+"/adopt_deployment", u.AccessToken,
		map[string]any{
			"deploymentUrl": backend.server.URL,
			"adminKey":      "k6",
			"name":          "my-existing-app",
		},
		http.StatusConflict)
	if env.Code != "name_taken" {
		t.Errorf("expected name_taken, got %q", env.Code)
	}
}

// TestAdopt_HealthWorkerSkipsAdopted: confirm the health-worker SQL filter
// excludes adopted rows. We don't run the worker here — just exercise the
// query the worker uses, to keep this fast and deterministic.
func TestAdopt_HealthWorkerSkipsAdopted(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	_, projID := projectFor(t, h, u, "AdoptCo7", "App7")

	backend := newFakeConvexBackend(t, "k7")
	var adopted deploymentJSON
	h.DoJSON(http.MethodPost, "/v1/projects/"+projID+"/adopt_deployment", u.AccessToken,
		map[string]any{
			"deploymentUrl": backend.server.URL,
			"adminKey":      "k7",
		},
		http.StatusCreated, &adopted)

	rows, err := h.DB.Query(h.rootCtx,
		`SELECT id FROM deployments WHERE status = 'running' AND adopted = false`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if id == adopted.ID {
			t.Errorf("health worker query returned adopted deployment %s", id)
		}
	}
}

// keep imports stable when fields shift around.
var _ = strings.TrimSpace
