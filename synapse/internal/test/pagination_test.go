package synapsetest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestPagination_TeamsListWalk creates 5 teams, paginates with limit=2, and
// checks that the cursor walk yields all 5 distinct ids in order.
func TestPagination_TeamsListWalk(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()

	const total = 5
	want := make([]string, 0, total)
	for i := 0; i < total; i++ {
		// Sleep 1ms between creates so created_at strictly increases — otherwise
		// the keyset tiebreak (id) takes over and the test order matches insertion
		// order only by chance.
		time.Sleep(1 * time.Millisecond)
		team := createTeam(t, h, u.AccessToken, fmt.Sprintf("Team %d", i))
		want = append(want, team.ID)
	}

	got := walkTeams(t, h, u.AccessToken, "/v1/teams/?limit=2")
	if len(got) != total {
		t.Fatalf("walked %d teams, want %d (got=%v)", len(got), total, got)
	}
	for i, id := range want {
		if got[i] != id {
			t.Errorf("page walk order: pos=%d got=%s want=%s", i, got[i], id)
		}
	}
}

// TestPagination_TeamsListSinglePageNoHeader confirms that a small result set
// returns no X-Next-Cursor (so clients can stop paging).
func TestPagination_TeamsListSinglePageNoHeader(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	createTeam(t, h, u.AccessToken, "Only Team")

	resp := h.Do(http.MethodGet, "/v1/teams/", u.AccessToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if c := resp.Header.Get("X-Next-Cursor"); c != "" {
		t.Errorf("expected no X-Next-Cursor for fully-fitted page, got %q", c)
	}
}

// TestPagination_InvalidLimit rejects limit=0, negative, or non-numeric.
func TestPagination_InvalidLimit(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()

	for _, bad := range []string{"0", "-1", "abc"} {
		env := h.AssertStatus(http.MethodGet, "/v1/teams/?limit="+bad, u.AccessToken,
			nil, http.StatusBadRequest)
		if env.Code != "invalid_limit" {
			t.Errorf("limit=%q: expected code invalid_limit, got %q", bad, env.Code)
		}
	}
}

// TestPagination_InvalidCursor rejects a cursor that doesn't refer to a
// resource the caller can see.
func TestPagination_InvalidCursor(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	createTeam(t, h, u.AccessToken, "Team A")

	// Random UUID-shaped cursor — well-formed but doesn't match any team.
	bogus := "11111111-2222-3333-4444-555555555555"
	env := h.AssertStatus(http.MethodGet, "/v1/teams/?cursor="+bogus, u.AccessToken,
		nil, http.StatusBadRequest)
	if env.Code != "invalid_cursor" {
		t.Errorf("expected code invalid_cursor, got %q", env.Code)
	}
}

// TestPagination_LimitClamp asks for an unreasonable limit and confirms the
// server caps at maxListLimit (500). Easy to verify: insert 4 teams, request
// limit=10000 — caller still gets 4 back, no header.
func TestPagination_LimitClamp(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	for i := 0; i < 4; i++ {
		time.Sleep(1 * time.Millisecond)
		createTeam(t, h, u.AccessToken, fmt.Sprintf("Team %d", i))
	}

	resp := h.Do(http.MethodGet, "/v1/teams/?limit=10000", u.AccessToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if c := resp.Header.Get("X-Next-Cursor"); c != "" {
		t.Errorf("expected no cursor when all rows fit, got %q", c)
	}
	var teams []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&teams); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(teams) != 4 {
		t.Errorf("expected 4 teams, got %d", len(teams))
	}
}

// TestPagination_ProjectsList exercises the project listing on the same
// pattern. Different table, different cursor target — confirms the helper
// works generically and not just for teams.
func TestPagination_ProjectsList(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	team := createTeam(t, h, u.AccessToken, "PaginatedProjects")

	const total = 6
	want := make([]string, 0, total)
	for i := 0; i < total; i++ {
		time.Sleep(1 * time.Millisecond)
		var resp struct {
			ProjectID   string         `json:"projectId"`
			ProjectSlug string         `json:"projectSlug"`
			Project     map[string]any `json:"project"`
		}
		h.DoJSON(http.MethodPost, "/v1/teams/"+team.Slug+"/create_project", u.AccessToken,
			map[string]string{"projectName": fmt.Sprintf("Proj %d", i)},
			http.StatusCreated, &resp)
		want = append(want, resp.ProjectID)
	}

	got := walkProjects(t, h, u.AccessToken,
		"/v1/teams/"+team.Slug+"/list_projects?limit=2")
	if len(got) != total {
		t.Fatalf("walked %d projects, want %d", len(got), total)
	}
	for i, id := range want {
		if got[i] != id {
			t.Errorf("page walk order: pos=%d got=%s want=%s", i, got[i], id)
		}
	}
}

// ---------- helpers ----------

// walkTeams paginates through the teams listing, returning the ids in the
// order they were visited. Paging stops when X-Next-Cursor is empty.
func walkTeams(t *testing.T, h *Harness, bearer, firstPath string) []string {
	t.Helper()
	out := make([]string, 0)
	path := firstPath
	for {
		resp := h.Do(http.MethodGet, path, bearer, nil)
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("walk teams %s: status=%d body=%s", path, resp.StatusCode, body)
		}
		var page []struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			resp.Body.Close()
			t.Fatalf("decode: %v", err)
		}
		next := resp.Header.Get("X-Next-Cursor")
		resp.Body.Close()
		for _, p := range page {
			out = append(out, p.ID)
		}
		if next == "" {
			return out
		}
		path = appendCursor(firstPath, next)
	}
}

func walkProjects(t *testing.T, h *Harness, bearer, firstPath string) []string {
	t.Helper()
	out := make([]string, 0)
	path := firstPath
	for {
		resp := h.Do(http.MethodGet, path, bearer, nil)
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("walk projects %s: status=%d body=%s", path, resp.StatusCode, body)
		}
		var page []struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			resp.Body.Close()
			t.Fatalf("decode: %v", err)
		}
		next := resp.Header.Get("X-Next-Cursor")
		resp.Body.Close()
		for _, p := range page {
			out = append(out, p.ID)
		}
		if next == "" {
			return out
		}
		path = appendCursor(firstPath, next)
	}
}

// appendCursor stitches a cursor onto a path that already may have a query
// string. It replaces any existing cursor= value so we don't accumulate stale
// values across page hops.
func appendCursor(path, cursor string) string {
	base, query, hasQuery := strings.Cut(path, "?")
	q := url.Values{}
	if hasQuery {
		q, _ = url.ParseQuery(query)
	}
	q.Set("cursor", cursor)
	return base + "?" + q.Encode()
}
