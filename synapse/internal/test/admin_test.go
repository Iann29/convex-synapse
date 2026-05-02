package synapsetest

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// versionCheckResp mirrors the JSON shape the handler emits.
type versionCheckResp struct {
	Current         string `json:"current"`
	Latest          string `json:"latest,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable"`
	ReleaseURL      string `json:"releaseUrl,omitempty"`
	ReleaseNotes    string `json:"releaseNotes,omitempty"`
	PublishedAt     string `json:"publishedAt,omitempty"`
	FetchedAt       string `json:"fetchedAt,omitempty"`
	Error           string `json:"error,omitempty"`
}

// stubGitHub returns an httptest.Server pretending to be the GitHub API.
// `tag` is the tag_name to ship in /releases/latest. If `fail` is true,
// every response is a 503 — useful for the offline-fallback test. The
// returned counter is bumped on every /releases/latest hit so callers
// can assert on cache behaviour.
func stubGitHub(t *testing.T, tag string, fail bool) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		if fail {
			http.Error(w, "stub down", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.github+json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":     tag,
			"name":         "Synapse " + tag,
			"html_url":     "https://example.com/releases/" + tag,
			"published_at": "2026-05-02T12:00:00Z",
			"body":         "release notes for " + tag,
			"prerelease":   false,
			"draft":        false,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

// stubUpdater spins up a unix-socket HTTP server pretending to be the
// synapse-updater daemon. fn lets each test inject the response shape
// it cares about. Returns the socket path.
func stubUpdater(t *testing.T, fn http.HandlerFunc) string {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "updater.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := &http.Server{Handler: fn}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = os.Remove(sock)
	})
	return sock
}

// makeAdminUser returns a user that belongs to a team as admin (the
// default role for a team creator) — that's enough to satisfy
// /v1/admin/* gating.
func makeAdminUser(t *testing.T, h *Harness) *User {
	t.Helper()
	u := h.RegisterRandomUser()
	createTeam(t, h, u.AccessToken, "Admin Co")
	return u
}

// makeNonAdminUser registers a user with no team memberships at all.
// For tests that need a "stranger" with valid auth but no admin reach.
func makeNonAdminUser(t *testing.T, h *Harness) *User {
	t.Helper()
	return h.RegisterRandomUser()
}

func TestAdmin_VersionCheck_UpdateAvailable(t *testing.T) {
	gh, hits := stubGitHub(t, "v1.2.0", false)
	h := SetupWithOpts(t, SetupOpts{
		GitHubRepo:    "Iann29/convex-synapse",
		GitHubAPIBase: gh.URL,
	})
	owner := makeAdminUser(t, h)

	var got versionCheckResp
	h.DoJSON(http.MethodGet, "/v1/admin/version_check",
		owner.AccessToken, nil, http.StatusOK, &got)

	if got.Current != "test" {
		t.Errorf("current: got %q want test", got.Current)
	}
	if got.Latest != "1.2.0" {
		t.Errorf("latest: got %q want 1.2.0", got.Latest)
	}
	// Version "test" isn't valid semver, so the comparator returns false
	// (we'd rather miss showing a banner than wrongly tell the operator
	// they're stale). Verify the conservative behaviour explicitly so
	// future changes to the comparator don't regress silently.
	if got.UpdateAvailable {
		t.Errorf("non-semver current should compare as 'not stale'")
	}
	if got.ReleaseNotes == "" || got.ReleaseURL == "" {
		t.Errorf("expected release metadata: %+v", got)
	}
	if atomic.LoadInt64(hits) != 1 {
		t.Errorf("expected exactly 1 GitHub fetch, got %d", *hits)
	}
}

func TestAdmin_VersionCheck_CacheHit(t *testing.T) {
	gh, hits := stubGitHub(t, "v1.5.0", false)
	h := SetupWithOpts(t, SetupOpts{
		GitHubRepo:    "Iann29/convex-synapse",
		GitHubAPIBase: gh.URL,
	})
	owner := makeAdminUser(t, h)

	// Two back-to-back calls should yield exactly one upstream fetch
	// thanks to the 15-min cache. This is the load-shedding guarantee
	// for high-traffic dashboards.
	for i := 0; i < 5; i++ {
		var got versionCheckResp
		h.DoJSON(http.MethodGet, "/v1/admin/version_check",
			owner.AccessToken, nil, http.StatusOK, &got)
	}
	if got := atomic.LoadInt64(hits); got != 1 {
		t.Errorf("expected exactly 1 GitHub fetch across 5 requests, got %d", got)
	}
}

func TestAdmin_VersionCheck_GitHubDown_GracefulFallback(t *testing.T) {
	gh, _ := stubGitHub(t, "", true) // every response is 503
	h := SetupWithOpts(t, SetupOpts{
		GitHubRepo:    "Iann29/convex-synapse",
		GitHubAPIBase: gh.URL,
	})
	owner := makeAdminUser(t, h)

	var got versionCheckResp
	h.DoJSON(http.MethodGet, "/v1/admin/version_check",
		owner.AccessToken, nil, http.StatusOK, &got)
	if got.Current == "" {
		t.Errorf("current must always be populated, even when GitHub is down: %+v", got)
	}
	if got.Error == "" {
		t.Errorf("expected error message in response when GitHub is down: %+v", got)
	}
	if got.UpdateAvailable {
		t.Errorf("can't claim updateAvailable when GitHub is unreachable")
	}
}

func TestAdmin_VersionCheck_NotAdmin_403(t *testing.T) {
	gh, _ := stubGitHub(t, "v1.0.0", false)
	h := SetupWithOpts(t, SetupOpts{
		GitHubRepo:    "Iann29/convex-synapse",
		GitHubAPIBase: gh.URL,
	})
	stranger := makeNonAdminUser(t, h)

	h.AssertStatus(http.MethodGet, "/v1/admin/version_check",
		stranger.AccessToken, nil, http.StatusForbidden)
}

func TestAdmin_VersionCheck_Anonymous_401(t *testing.T) {
	gh, _ := stubGitHub(t, "v1.0.0", false)
	h := SetupWithOpts(t, SetupOpts{
		GitHubRepo:    "Iann29/convex-synapse",
		GitHubAPIBase: gh.URL,
	})
	h.AssertStatus(http.MethodGet, "/v1/admin/version_check",
		"", nil, http.StatusUnauthorized)
}

func TestAdmin_Upgrade_NoUpdater_503(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		// UpdaterSocket left empty — the "this host has no daemon" path.
	})
	owner := makeAdminUser(t, h)
	env := h.AssertStatus(http.MethodPost, "/v1/admin/upgrade",
		owner.AccessToken, map[string]any{}, http.StatusServiceUnavailable)
	if env.Code != "updater_unavailable" && env.Code != "updater_unreachable" {
		t.Errorf("expected updater_unavailable or updater_unreachable, got %q", env.Code)
	}
}

func TestAdmin_Upgrade_ForwardsToUpdater(t *testing.T) {
	var seenBody []byte
	sock := stubUpdater(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/upgrade" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		body := make([]byte, 1024)
		n, _ := r.Body.Read(body)
		seenBody = body[:n]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"started":true,"ref":"v1.2.0"}`))
	})
	h := SetupWithOpts(t, SetupOpts{UpdaterSocket: sock})
	owner := makeAdminUser(t, h)

	var got struct {
		Started bool   `json:"started"`
		Ref     string `json:"ref"`
	}
	h.DoJSON(http.MethodPost, "/v1/admin/upgrade",
		owner.AccessToken, map[string]any{"ref": "v1.2.0"},
		http.StatusAccepted, &got)
	if !got.Started || got.Ref != "v1.2.0" {
		t.Errorf("response not forwarded: %+v", got)
	}
	if seenBody == nil || string(seenBody) == "" {
		t.Errorf("updater never received the body")
	}
}

func TestAdmin_Upgrade_AuditLogEntry(t *testing.T) {
	sock := stubUpdater(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"started":true,"ref":"latest"}`))
	})
	h := SetupWithOpts(t, SetupOpts{UpdaterSocket: sock})
	owner := makeAdminUser(t, h)

	h.AssertStatus(http.MethodPost, "/v1/admin/upgrade",
		owner.AccessToken, map[string]any{}, http.StatusAccepted)

	var count int
	if err := h.DB.QueryRow(h.rootCtx, `
		SELECT count(*) FROM audit_events
		WHERE actor_id = $1 AND action = 'upgradeStarted'
		AND target_type = 'synapse'
	`, owner.ID).Scan(&count); err != nil {
		t.Fatalf("query audit events: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 upgradeStarted audit row, got %d", count)
	}
}

func TestAdmin_Upgrade_PassesThroughUpdater409(t *testing.T) {
	sock := stubUpdater(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"upgrade_in_progress"}`))
	})
	h := SetupWithOpts(t, SetupOpts{UpdaterSocket: sock})
	owner := makeAdminUser(t, h)

	env := h.AssertStatus(http.MethodPost, "/v1/admin/upgrade",
		owner.AccessToken, map[string]any{}, http.StatusConflict)
	if env.Code != "upgrade_in_progress" {
		t.Errorf("expected upgrade_in_progress code, got %q", env.Code)
	}
}

func TestAdmin_Upgrade_NotAdmin_403(t *testing.T) {
	sock := stubUpdater(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("updater should NEVER be hit when caller is not an admin")
		w.WriteHeader(http.StatusInternalServerError)
	})
	h := SetupWithOpts(t, SetupOpts{UpdaterSocket: sock})
	stranger := makeNonAdminUser(t, h)
	h.AssertStatus(http.MethodPost, "/v1/admin/upgrade",
		stranger.AccessToken, map[string]any{}, http.StatusForbidden)
}

func TestAdmin_UpgradeStatus_PassesThrough(t *testing.T) {
	sock := stubUpdater(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"state":"running","ref":"v1.2.0","logTail":["installing"]}`))
			return
		}
		http.NotFound(w, r)
	})
	h := SetupWithOpts(t, SetupOpts{UpdaterSocket: sock})
	owner := makeAdminUser(t, h)

	var got struct {
		State   string   `json:"state"`
		Ref     string   `json:"ref"`
		LogTail []string `json:"logTail"`
	}
	h.DoJSON(http.MethodGet, "/v1/admin/upgrade/status",
		owner.AccessToken, nil, http.StatusOK, &got)
	if got.State != "running" || got.Ref != "v1.2.0" {
		t.Errorf("status not forwarded: %+v", got)
	}
	if len(got.LogTail) == 0 {
		t.Errorf("log tail dropped: %+v", got)
	}
}

func TestAdmin_UpgradeStatus_NoUpdater_Unavailable(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{})
	owner := makeAdminUser(t, h)

	var got struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	h.DoJSON(http.MethodGet, "/v1/admin/upgrade/status",
		owner.AccessToken, nil, http.StatusOK, &got)
	if got.State != "unavailable" {
		t.Errorf("expected state=unavailable, got %q (full: %+v)", got.State, got)
	}
}

// (Compilation only: ensures fmt is used somewhere if I refactor — skips
// "imported and not used" if every test happens to drop it.)
var _ = fmt.Sprintf
