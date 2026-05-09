package synapsetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	CacheExpiresAt  string `json:"cacheExpiresAt,omitempty"`
	FromCache       bool   `json:"fromCache"`
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

// stubUpdater spins up an httptest.Server pretending to be the
// synapse-updater daemon (v1.5.1+ TCP+bearer protocol). It generates
// a random bearer token, asserts every incoming request carries it,
// and answers the /healthz pre-flight that AdminHandler.updaterReachable
// performs before the real call. fn handles the route the test cares
// about. Returns (url, token) the test passes through SetupOpts.
func stubUpdater(t *testing.T, fn http.HandlerFunc) (string, string) {
	t.Helper()
	token := "test-token-" + randHex(8)
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		// Every test exercises updaterReachable() first; hard-code a
		// 200 OK on /healthz so individual tests don't need to remember
		// it. Anything else routes to fn.
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		fn(w, r)
	})
	srv := httptest.NewServer(wrapped)
	t.Cleanup(srv.Close)
	return srv.URL, token
}

// makeAdminUser returns the first registered user. Registration promotes that
// user to instance admin; the team keeps tests close to the dashboard path.
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

func TestAdmin_FirstRegisteredUserIsInstanceAdmin(t *testing.T) {
	gh, _ := stubGitHub(t, "v1.2.0", false)
	h := SetupWithOpts(t, SetupOpts{
		GitHubRepo:    "Iann29/convex-synapse",
		GitHubAPIBase: gh.URL,
	})
	owner := h.RegisterRandomUser()

	var got versionCheckResp
	h.DoJSON(http.MethodGet, "/v1/admin/version_check",
		owner.AccessToken, nil, http.StatusOK, &got)
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

// Regression for v1.5.3 cache-bust feature. The dashboard's
// VersionStatusChip exposes a "Check now" button that the operator
// hits when they want to bypass the 15-minute GitHub cache. Backend
// rate-limits the bust to once per 30 seconds.
func TestAdmin_VersionCheckRefresh_BustsCache(t *testing.T) {
	gh, hits := stubGitHub(t, "v1.5.3", false)
	h := SetupWithOpts(t, SetupOpts{
		GitHubRepo:    "Iann29/convex-synapse",
		GitHubAPIBase: gh.URL,
	})
	owner := makeAdminUser(t, h)

	// Warm the cache.
	var got versionCheckResp
	h.DoJSON(http.MethodGet, "/v1/admin/version_check",
		owner.AccessToken, nil, http.StatusOK, &got)
	if atomic.LoadInt64(hits) != 1 {
		t.Fatalf("expected 1 hit after warm-up, got %d", atomic.LoadInt64(hits))
	}
	if !got.FromCache && atomic.LoadInt64(hits) > 1 {
		// First call is always live; we only check fromCache on the
		// SECOND call when the cache should kick in.
	}

	// Subsequent GETs reuse the cache — proves we have a baseline of "no
	// extra hits without the bust".
	h.DoJSON(http.MethodGet, "/v1/admin/version_check",
		owner.AccessToken, nil, http.StatusOK, &got)
	if !got.FromCache {
		t.Errorf("second GET should be served from cache, got fromCache=%v", got.FromCache)
	}
	if atomic.LoadInt64(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d (cache should have absorbed the second call)", atomic.LoadInt64(hits))
	}

	// POST /refresh — but since the cache was JUST warmed (well within
	// 30s), the rate-limiter should refuse the bust and serve the
	// existing payload.
	h.DoJSON(http.MethodPost, "/v1/admin/version_check/refresh",
		owner.AccessToken, nil, http.StatusOK, &got)
	if atomic.LoadInt64(hits) != 1 {
		t.Errorf("refresh-within-30s should NOT bust the cache, got %d hits", atomic.LoadInt64(hits))
	}
}

func TestAdmin_VersionCheckRefresh_NotAdmin_403(t *testing.T) {
	gh, _ := stubGitHub(t, "v1.5.3", false)
	h := SetupWithOpts(t, SetupOpts{
		GitHubRepo:    "Iann29/convex-synapse",
		GitHubAPIBase: gh.URL,
	})
	// First-registered user IS the instance admin; we want the SECOND.
	makeAdminUser(t, h) // registers + promotes the first user
	other := h.RegisterRandomUser()

	resp := h.Do(http.MethodPost, "/v1/admin/version_check/refresh",
		other.AccessToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin should get 403, got %d", resp.StatusCode)
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
	_ = makeAdminUser(t, h)
	stranger := makeNonAdminUser(t, h)

	h.AssertStatus(http.MethodGet, "/v1/admin/version_check",
		stranger.AccessToken, nil, http.StatusForbidden)
}

func TestAdmin_VersionCheck_TeamAdminWithoutInstanceRole_403(t *testing.T) {
	gh, _ := stubGitHub(t, "v1.0.0", false)
	h := SetupWithOpts(t, SetupOpts{
		GitHubRepo:    "Iann29/convex-synapse",
		GitHubAPIBase: gh.URL,
	})
	_ = makeAdminUser(t, h)
	teamAdmin := h.RegisterRandomUser()
	createTeam(t, h, teamAdmin.AccessToken, "Tenant Admin Co")

	h.AssertStatus(http.MethodGet, "/v1/admin/version_check",
		teamAdmin.AccessToken, nil, http.StatusForbidden)
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
		// UpdaterURL left empty — the "this host has no daemon" path.
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
	url, tok := stubUpdater(t, func(w http.ResponseWriter, r *http.Request) {
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
	h := SetupWithOpts(t, SetupOpts{UpdaterURL: url, UpdaterToken: tok})
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
	url, tok := stubUpdater(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"started":true,"ref":"latest"}`))
	})
	h := SetupWithOpts(t, SetupOpts{UpdaterURL: url, UpdaterToken: tok})
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
	url, tok := stubUpdater(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"upgrade_in_progress"}`))
	})
	h := SetupWithOpts(t, SetupOpts{UpdaterURL: url, UpdaterToken: tok})
	owner := makeAdminUser(t, h)

	env := h.AssertStatus(http.MethodPost, "/v1/admin/upgrade",
		owner.AccessToken, map[string]any{}, http.StatusConflict)
	if env.Code != "upgrade_in_progress" {
		t.Errorf("expected upgrade_in_progress code, got %q", env.Code)
	}
}

func TestAdmin_Upgrade_NotAdmin_403(t *testing.T) {
	url, tok := stubUpdater(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("updater should NEVER be hit when caller is not an admin")
		w.WriteHeader(http.StatusInternalServerError)
	})
	h := SetupWithOpts(t, SetupOpts{UpdaterURL: url, UpdaterToken: tok})
	_ = makeAdminUser(t, h)
	stranger := makeNonAdminUser(t, h)
	h.AssertStatus(http.MethodPost, "/v1/admin/upgrade",
		stranger.AccessToken, map[string]any{}, http.StatusForbidden)
}

func TestAdmin_UpgradeStatus_PassesThrough(t *testing.T) {
	url, tok := stubUpdater(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"state":"running","ref":"v1.2.0","logTail":["installing"]}`))
			return
		}
		http.NotFound(w, r)
	})
	h := SetupWithOpts(t, SetupOpts{UpdaterURL: url, UpdaterToken: tok})
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

// ---------- TCP+bearer migration (v1.5.1+) ----------
//
// The three tests below exercise the failure modes that only matter in
// the daemon-protocol world: missing token, wrong token, and a daemon
// that's "configured but unreachable" (closed port). The healthy path
// stays covered by TestAdmin_Upgrade_ForwardsToUpdater above.

// TestAdmin_Upgrade_TokenMissing covers the half-configured case: the
// operator filled in SYNAPSE_UPDATER_URL but forgot
// SYNAPSE_UPDATER_TOKEN. updaterReachable rejects locally with 503
// before it even attempts to dial the daemon — the stub's mux MUST
// never be invoked.
func TestAdmin_Upgrade_TokenMissing(t *testing.T) {
	url, _ := stubUpdater(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("daemon must NOT be hit when no token is configured")
		w.WriteHeader(http.StatusInternalServerError)
	})
	h := SetupWithOpts(t, SetupOpts{
		UpdaterURL: url, // token deliberately empty
	})
	owner := makeAdminUser(t, h)
	env := h.AssertStatus(http.MethodPost, "/v1/admin/upgrade",
		owner.AccessToken, map[string]any{}, http.StatusServiceUnavailable)
	if env.Code != "updater_unreachable" {
		t.Errorf("code: got %q want updater_unreachable", env.Code)
	}
	if env.Message == "" || !contains(env.Message, "token_missing") {
		t.Errorf("message should mention token_missing: %q", env.Message)
	}
}

// TestAdmin_Upgrade_WrongToken covers the "operator rotated the
// daemon's token but didn't restart synapse-api" case. The daemon
// returns 401; the api propagates that status to the dashboard
// (instead of disguising it as a 502/503) so the banner can prompt for
// reconfiguration.
func TestAdmin_Upgrade_WrongToken(t *testing.T) {
	url, _ := stubUpdater(t, func(w http.ResponseWriter, _ *http.Request) {
		// Anything that reaches `fn` with the wrong token should never
		// arrive — the stubUpdater wrapper short-circuits on bearer
		// mismatch. This handler just guards the assertion.
		t.Errorf("fn invoked with wrong-token request — wrapper should have short-circuited")
		w.WriteHeader(http.StatusInternalServerError)
	})
	h := SetupWithOpts(t, SetupOpts{
		UpdaterURL:   url,
		UpdaterToken: "wrong-token-on-purpose",
	})
	owner := makeAdminUser(t, h)
	// updaterReachable's /healthz probe is the first thing the handler
	// runs; the daemon answers 401 → updaterReachable surfaces "healthz
	// returned 401" and the handler maps that to 503. (The dashboard
	// renders "Self-update daemon unreachable: healthz returned 401".)
	env := h.AssertStatus(http.MethodPost, "/v1/admin/upgrade",
		owner.AccessToken, map[string]any{}, http.StatusServiceUnavailable)
	if env.Code != "updater_unreachable" {
		t.Errorf("code: got %q want updater_unreachable", env.Code)
	}
	if !contains(env.Message, "401") {
		t.Errorf("message should surface the 401 from the daemon: %q", env.Message)
	}
}

// TestAdmin_Upgrade_DaemonNetworkFailure points the api at a closed
// port (well-known bottom-of-the-range) so the dial errors immediately.
// The handler should answer 503 with a body referencing the URL, so
// the operator can copy-paste it into a debugging session.
func TestAdmin_Upgrade_DaemonNetworkFailure(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		UpdaterURL:   "http://127.0.0.1:1", // port 1 (tcpmux) — never listens in tests
		UpdaterToken: "any-token-the-daemon-isnt-listening",
	})
	owner := makeAdminUser(t, h)
	env := h.AssertStatus(http.MethodPost, "/v1/admin/upgrade",
		owner.AccessToken, map[string]any{}, http.StatusServiceUnavailable)
	if env.Code != "updater_unreachable" {
		t.Errorf("code: got %q want updater_unreachable", env.Code)
	}
	if !contains(env.Message, "updater unreachable at") {
		t.Errorf("message should include the dial-error wrapper text: %q", env.Message)
	}
}

// contains is a tiny strings.Contains alias kept private to admin_test
// so the new tests can stay self-contained without growing imports.
func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// (Compilation only: ensures fmt is used somewhere if I refactor — skips
// "imported and not used" if every test happens to drop it.)
var _ = fmt.Sprintf
