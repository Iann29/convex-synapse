package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/mod/semver"

	"github.com/Iann29/synapse/internal/audit"
	"github.com/Iann29/synapse/internal/auth"
)

// AdminHandler covers instance-level operations: version check + auto-upgrade.
// Endpoints under /v1/admin are gated to "any team admin" (a user who is
// admin in at least one team). Synapse has no global super-admin role; in a
// single-tenant self-hosted box, anyone trusted enough to admin a team is
// trusted enough to upgrade the box.
type AdminHandler struct {
	DB      *pgxpool.Pool
	Version string

	// UpdaterSocket is the unix socket path the synapse-updater systemd
	// daemon listens on. The synapse-api container bind-mounts /run/synapse
	// from the host so this path resolves identically inside and outside.
	// Empty (or unreachable) → upgrade endpoints return 503.
	UpdaterSocket string

	// GitHubRepo is the repo owner/name pair (e.g. "Iann29/convex-synapse")
	// queried by /version_check. Pinned per-build so a forked deployment
	// with its own release stream can override.
	GitHubRepo string

	// GitHubAPIBase overrides https://api.github.com — primarily a test
	// seam (httptest.Server pretends to be the GitHub API). Production
	// keeps the default; empty falls through to the official endpoint.
	GitHubAPIBase string

	// Cache for the latest-release fetch. GitHub's unauthenticated API limit
	// is 60 req/hour; with this 15min cache, a busy dashboard with N admin
	// pollers stays well under that.
	cacheMu       sync.Mutex
	cachedLatest  *latestRelease
	cachedAt      time.Time
	cachedFromAPI bool
}

const (
	// updaterTimeout caps each call to the local socket. The daemon is
	// in-process tiny — anything taking >5s is a hung child or a bug.
	updaterTimeout = 5 * time.Second
	// versionCheckCacheTTL keeps GitHub fetches off the critical path
	// for dashboards that poll every minute.
	versionCheckCacheTTL = 15 * time.Minute
	// githubFetchTimeout: avoid hanging a request behind a slow GitHub.
	githubFetchTimeout = 6 * time.Second
)

func (h *AdminHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(h.requireAnyTeamAdmin)
	r.Get("/version_check", h.versionCheck)
	r.Post("/upgrade", h.upgrade)
	r.Get("/upgrade/status", h.upgradeStatus)
	return r
}

// requireAnyTeamAdmin gates every /v1/admin/* route. A user counts as an
// admin if they have role='admin' in at least one team_members row. We
// don't expose a global "instance admin" because the typical self-hosted
// flow (single team, single operator) doesn't need that distinction —
// anyone with team-admin reach already controls the data behind Synapse.
func (h *AdminHandler) requireAnyTeamAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, err := auth.UserID(r.Context())
		if err != nil || uid == "" {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Authentication required")
			return
		}
		var hasAdmin bool
		if err := h.DB.QueryRow(r.Context(), `
			SELECT EXISTS(SELECT 1 FROM team_members WHERE user_id = $1 AND role = $2)
		`, uid, "admin").Scan(&hasAdmin); err != nil {
			logErr("admin gate query", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to verify admin status")
			return
		}
		if !hasAdmin {
			writeError(w, http.StatusForbidden, "forbidden", "Instance admin endpoints require team-admin role on at least one team")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- /v1/admin/version_check ---------------------------------

type latestRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Body        string `json:"body"`
	Prerelease  bool   `json:"prerelease"`
	Draft       bool   `json:"draft"`
}

type versionCheckResp struct {
	Current         string `json:"current"`
	Latest          string `json:"latest,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable"`
	ReleaseURL      string `json:"releaseUrl,omitempty"`
	ReleaseNotes    string `json:"releaseNotes,omitempty"`
	PublishedAt     string `json:"publishedAt,omitempty"`
	// FetchedAt mirrors Last-Modified semantics so the dashboard can
	// label a banner like "checked 3min ago". Populated even on cache
	// hits; on hard failure (GitHub unreachable + nothing cached), the
	// field is empty and the caller should treat updateAvailable as
	// "unknown".
	FetchedAt string `json:"fetchedAt,omitempty"`
	// Error holds a short reason when GitHub couldn't be reached. The
	// dashboard renders the banner without the green "click to upgrade"
	// affordance when this is set.
	Error string `json:"error,omitempty"`
}

func (h *AdminHandler) versionCheck(w http.ResponseWriter, r *http.Request) {
	resp := versionCheckResp{Current: trimVersion(h.Version)}

	latest, fetchedAt, fromCache, err := h.fetchLatestRelease(r.Context())
	if err != nil && latest == nil {
		// Never fetched and now offline — return current-only with the
		// error so the dashboard can still display "you're on v1.X.Y".
		resp.Error = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	_ = fromCache // future: surface to a header for debugging

	if latest != nil {
		resp.Latest = trimVersion(latest.TagName)
		resp.ReleaseURL = latest.HTMLURL
		resp.ReleaseNotes = latest.Body
		resp.PublishedAt = latest.PublishedAt
		resp.FetchedAt = fetchedAt.UTC().Format(time.RFC3339)
		// semver.Compare needs a leading "v"; trimVersion strips it.
		// Re-add for the comparison.
		resp.UpdateAvailable = semverNewer("v"+resp.Latest, "v"+resp.Current)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *AdminHandler) fetchLatestRelease(ctx context.Context) (*latestRelease, time.Time, bool, error) {
	h.cacheMu.Lock()
	if h.cachedLatest != nil && time.Since(h.cachedAt) < versionCheckCacheTTL {
		latest := h.cachedLatest
		at := h.cachedAt
		h.cacheMu.Unlock()
		return latest, at, true, nil
	}
	h.cacheMu.Unlock()

	repo := h.GitHubRepo
	if repo == "" {
		return nil, time.Time{}, false, errors.New("no GitHub repo configured")
	}
	base := h.GitHubAPIBase
	if base == "" {
		base = "https://api.github.com"
	}
	apiURL := strings.TrimRight(base, "/") + "/repos/" + repo + "/releases/latest"

	cctx, cancel := context.WithTimeout(ctx, githubFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return h.lastKnownLatest(), time.Time{}, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return h.lastKnownLatest(), time.Time{}, false, fmt.Errorf("github fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return h.lastKnownLatest(), time.Time{}, false, fmt.Errorf("github rate-limited (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return h.lastKnownLatest(), time.Time{}, false, fmt.Errorf("github HTTP %d", resp.StatusCode)
	}

	var release latestRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return h.lastKnownLatest(), time.Time{}, false, fmt.Errorf("decode release: %w", err)
	}
	if release.Draft || release.Prerelease {
		// /releases/latest already filters these out, but defense-in-depth:
		// a misclick on Make Latest could still show up here.
		return h.lastKnownLatest(), time.Time{}, false, errors.New("latest is prerelease/draft")
	}

	now := time.Now()
	h.cacheMu.Lock()
	h.cachedLatest = &release
	h.cachedAt = now
	h.cachedFromAPI = true
	h.cacheMu.Unlock()
	return &release, now, false, nil
}

func (h *AdminHandler) lastKnownLatest() *latestRelease {
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	return h.cachedLatest
}

// trimVersion drops a leading "v" so equality + comparison surfaces don't
// trip on "v1.0.3" vs "1.0.3" mismatches. Callers re-add the v for semver
// compare since x/mod/semver requires it.
func trimVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	return v
}

// semverNewer reports whether `latest` strictly exceeds `current`. Bad
// inputs (non-semver, missing v-prefix) compare as "not newer" — we'd
// rather miss showing a banner than incorrectly tell the operator their
// production-good version is stale.
func semverNewer(latest, current string) bool {
	if !semver.IsValid(latest) || !semver.IsValid(current) {
		return false
	}
	return semver.Compare(latest, current) > 0
}

// ---------- /v1/admin/upgrade ---------------------------------------

type upgradeReq struct {
	Ref string `json:"ref,omitempty"`
}

type upgradeResp struct {
	Started bool   `json:"started"`
	Ref     string `json:"ref"`
}

func (h *AdminHandler) upgrade(w http.ResponseWriter, r *http.Request) {
	if h.UpdaterSocket == "" {
		writeError(w, http.StatusServiceUnavailable, "updater_unavailable",
			"Self-update daemon is not configured on this host. Run setup.sh --upgrade via SSH.")
		return
	}
	if _, err := os.Stat(h.UpdaterSocket); err != nil {
		writeError(w, http.StatusServiceUnavailable, "updater_unreachable",
			"Self-update daemon socket missing — daemon installed but not running, or this host doesn't have systemd. Run setup.sh --upgrade via SSH.")
		return
	}

	var req upgradeReq
	if r.ContentLength > 0 {
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
	}

	body, err := json.Marshal(map[string]any{"ref": req.Ref})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Failed to encode upgrade request")
		return
	}

	status, payload, err := h.callUpdater(r.Context(), http.MethodPost, "/upgrade", body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "updater_unreachable",
			"Could not reach the self-update daemon: "+err.Error())
		return
	}
	if status >= 400 {
		// Re-wrap the updater's `{"error": "..."}` into Synapse's standard
		// `{"code", "message"}` envelope so dashboard error parsing
		// stays uniform across endpoints. The bare-error string from
		// the updater becomes the code; we humanise common ones.
		var ue struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payload, &ue)
		code := ue.Error
		if code == "" {
			code = "updater_error"
		}
		msg := updaterErrorMessage(code)
		writeError(w, status, code, msg)
		return
	}

	var parsed upgradeResp
	if err := json.Unmarshal(payload, &parsed); err != nil {
		// Updater returned 2xx but mangled JSON — log + best-effort response.
		logErr("decode updater /upgrade response", err)
	}

	uid, _ := auth.UserID(r.Context())
	// audit_events.target_id is UUID-typed; the upgrade target is the
	// instance itself, which has no UUID. Leave TargetID empty —
	// audit.Record skips the column when blank, the row still records
	// who pressed the button + when via target_type='synapse'.
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		ActorID:    uid,
		Action:     audit.ActionUpgradeStarted,
		TargetType: audit.TargetSynapse,
		Metadata: map[string]any{
			"ref":            req.Ref,
			"currentVersion": trimVersion(h.Version),
		},
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

// updaterErrorMessage humanises the codes the daemon emits for the
// dashboard's error banner.
func updaterErrorMessage(code string) string {
	switch code {
	case "upgrade_in_progress":
		return "Another upgrade is already running"
	case "invalid_ref":
		return "ref contains characters that aren't allowed (use a tag like v1.2.0 or a branch name)"
	case "invalid_json":
		return "Updater rejected the request body"
	default:
		return "Updater error: " + code
	}
}

// upgradeStatus is a read-through to the updater's /status. We don't cache
// — operators expect log-tail freshness when watching an upgrade run.
func (h *AdminHandler) upgradeStatus(w http.ResponseWriter, r *http.Request) {
	if h.UpdaterSocket == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"state": "unavailable",
			"error": "updater_not_configured",
		})
		return
	}
	if _, err := os.Stat(h.UpdaterSocket); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"state": "unavailable",
			"error": "updater_unreachable",
		})
		return
	}
	status, payload, err := h.callUpdater(r.Context(), http.MethodGet, "/status", nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"state": "unavailable",
			"error": err.Error(),
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

// callUpdater talks to the local unix socket. The HTTP client uses a
// custom Transport that always dials the socket regardless of host.
func (h *AdminHandler) callUpdater(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{}
			return d.DialContext(ctx, "unix", h.UpdaterSocket)
		},
	}
	client := &http.Client{Transport: transport, Timeout: updaterTimeout}

	// The host segment is irrelevant — DialContext rewrites every call to
	// the unix socket. We use "synapse-updater" so logs/traces show
	// something legible.
	u := &url.URL{Scheme: "http", Host: "synapse-updater", Path: path}

	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return 0, nil, err
	}
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, payload, nil
}
