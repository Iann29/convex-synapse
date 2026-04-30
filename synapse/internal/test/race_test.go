package synapsetest

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
)

// These tests stress the retry-on-conflict code path on resource allocators
// (port, deployment name, team slug, project slug). They fire N concurrent
// requests at one Synapse process — equivalent to the multi-node race we
// expect when 3 nodes hit the same allocator at the same instant. Without
// the retry helper, several goroutines would lose to UNIQUE constraints and
// surface 500s; with retry, all of them get a fresh candidate and succeed.

const raceN = 30

// TestRace_ConcurrentTeamCreate_NoSlugCollision dispatches raceN goroutines
// that all create a team with the same display name. Without retry, the
// SELECT-EXISTS pre-check makes them all pick "acme-corp", first INSERT wins
// and the rest 500. With retry, every attempt either commits its slug or
// retries to "acme-corp-1", "acme-corp-2", etc. We assert every goroutine
// got a 201 and every team has a unique slug.
func TestRace_ConcurrentTeamCreate_NoSlugCollision(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()

	var wg sync.WaitGroup
	statuses := make([]int, raceN)
	slugs := make([]string, raceN)
	wg.Add(raceN)

	for i := 0; i < raceN; i++ {
		go func(idx int) {
			defer wg.Done()
			var got teamResp
			resp := h.Do(http.MethodPost, "/v1/teams/create_team", owner.AccessToken,
				map[string]string{"name": "Acme Corp"})
			statuses[idx] = resp.StatusCode
			if resp.StatusCode == http.StatusCreated {
				_ = decodeStrict(resp.Body, &got)
				slugs[idx] = got.Slug
			}
			resp.Body.Close()
		}(i)
	}
	wg.Wait()

	created := 0
	for i, s := range statuses {
		if s == http.StatusCreated {
			created++
		} else {
			t.Errorf("goroutine %d: status %d (expected 201)", i, s)
		}
	}
	if created != raceN {
		t.Fatalf("expected %d teams, got %d", raceN, created)
	}

	// Every slug must be unique. Slug allocator should have walked through
	// "acme-corp", "acme-corp-1", "acme-corp-2", ... — but order doesn't
	// matter; what matters is uniqueness.
	seen := map[string]int{}
	for i, s := range slugs {
		if s == "" {
			t.Errorf("goroutine %d: empty slug", i)
			continue
		}
		seen[s]++
	}
	for s, n := range seen {
		if n > 1 {
			t.Errorf("slug %q assigned to %d teams", s, n)
		}
	}
}

// TestRace_ConcurrentProjectCreate_NoSlugCollision: same pattern within one team.
func TestRace_ConcurrentProjectCreate_NoSlugCollision(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Race Co")

	var wg sync.WaitGroup
	var creates atomic.Int64
	slugs := make([]string, raceN)
	wg.Add(raceN)
	for i := 0; i < raceN; i++ {
		go func(idx int) {
			defer wg.Done()
			var got createProjectResp
			resp := h.Do(http.MethodPost, "/v1/teams/"+team.Slug+"/create_project",
				owner.AccessToken, map[string]string{"projectName": "My App"})
			if resp.StatusCode == http.StatusCreated {
				creates.Add(1)
				_ = decodeStrict(resp.Body, &got)
				slugs[idx] = got.ProjectSlug
			}
			resp.Body.Close()
		}(i)
	}
	wg.Wait()

	if int(creates.Load()) != raceN {
		t.Fatalf("expected %d projects created, got %d", raceN, creates.Load())
	}
	seen := map[string]bool{}
	for _, s := range slugs {
		if s == "" {
			t.Error("empty project slug")
			continue
		}
		if seen[s] {
			t.Errorf("project slug %q assigned twice", s)
		}
		seen[s] = true
	}
}

// TestRace_DuplicateRegistrationReturns409: two concurrent registrations with
// the same email — the loser MUST get a 409 with code "email_taken", not a
// 500. Validates the PgError-based detection in the register handler.
func TestRace_DuplicateRegistrationReturns409(t *testing.T) {
	h := Setup(t)

	const N = 8
	const email = "race-target@example.test"
	body := map[string]string{
		"email":    email,
		"password": "supersecret123",
		"name":     "Race Target",
	}

	var wg sync.WaitGroup
	statuses := make([]int, N)
	codes := make([]string, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			resp := h.Do(http.MethodPost, "/v1/auth/register", "", body)
			statuses[idx] = resp.StatusCode
			if resp.StatusCode != http.StatusCreated {
				var env ErrorEnvelope
				_ = decodeStrict(resp.Body, &env)
				codes[idx] = env.Code
			}
			resp.Body.Close()
		}(i)
	}
	wg.Wait()

	created := 0
	conflicts := 0
	for i, s := range statuses {
		switch s {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
			if codes[i] != "email_taken" {
				t.Errorf("goroutine %d: 409 code %q, want email_taken", i, codes[i])
			}
		default:
			t.Errorf("goroutine %d: unexpected status %d (code %q)", i, s, codes[i])
		}
	}
	if created != 1 {
		t.Errorf("expected exactly 1 successful registration, got %d", created)
	}
	if created+conflicts != N {
		t.Errorf("expected created+conflicts == %d, got created=%d conflicts=%d", N, created, conflicts)
	}
}
