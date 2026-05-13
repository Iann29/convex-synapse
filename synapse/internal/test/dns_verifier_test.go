package synapsetest

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	synapsedns "github.com/Iann29/synapse/internal/dns"
)

// stubLookupResolver lets each test return canned IPs (or an error)
// without reaching real DNS. Mirrors the surface of *net.Resolver
// the verifier consumes.
type stubLookupResolver struct {
	mu    sync.Mutex
	calls int
	fn    func(host string) ([]net.IP, error)
}

func (s *stubLookupResolver) LookupIP(_ context.Context, _ string, host string) ([]net.IP, error) {
	s.mu.Lock()
	s.calls++
	fn := s.fn
	s.mu.Unlock()
	if fn == nil {
		return nil, errors.New("stub: no fn configured")
	}
	return fn(host)
}

func (s *stubLookupResolver) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// fixedClock is a Clock implementation that returns whatever Now()
// the test stamped — we override it explicitly for the aged-out
// scenario so MaxAge fires without a real time.Sleep.
type fixedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (f *fixedClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixedClock) Set(t time.Time) {
	f.mu.Lock()
	f.now = t
	f.mu.Unlock()
}

// seedAutoConfiguredDomain inserts a deployment_domains row with
// auto_configured=true + status='pending', mimicking the state the
// auto_configure handler leaves the row in. Returns the row id.
func seedAutoConfiguredDomain(t *testing.T, h *Harness, deploymentID, domain string) string {
	t.Helper()
	var id string
	if err := h.DB.QueryRow(h.rootCtx, `
		INSERT INTO deployment_domains
		    (deployment_id, domain, role, status, auto_configured)
		VALUES ($1, $2, 'api', 'pending', true)
		RETURNING id
	`, deploymentID, domain).Scan(&id); err != nil {
		t.Fatalf("seed auto-configured domain: %v", err)
	}
	return id
}

// readDomainStatus pulls (status, dnsVerifiedAt, lastDnsError) for a
// row so tests can assert on the verifier's effects.
func readDomainStatus(t *testing.T, h *Harness, id string) (string, *time.Time, string) {
	t.Helper()
	var status, lastErr string
	var verifiedAt *time.Time
	if err := h.DB.QueryRow(h.rootCtx, `
		SELECT status, dns_verified_at, COALESCE(last_dns_error, '')
		  FROM deployment_domains
		 WHERE id = $1
	`, id).Scan(&status, &verifiedAt, &lastErr); err != nil {
		t.Fatalf("read domain status: %v", err)
	}
	return status, verifiedAt, lastErr
}

// countDomainAuditEvents returns how many rows in audit_events match
// (action, target_type, target_id) — used to assert "exactly one
// domain.verified for this row" without false positives from the
// /verify endpoint that other tests in the suite might run.
func countDomainAuditEvents(t *testing.T, h *Harness, action, targetID string) int {
	t.Helper()
	var n int
	if err := h.DB.QueryRow(h.rootCtx, `
		SELECT count(*) FROM audit_events
		 WHERE action = $1 AND target_type = 'domain' AND target_id = $2
	`, action, targetID).Scan(&n); err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	return n
}

// 1) Happy path: row pending+auto_configured, resolver returns the
// expected IP, one tick flips it to active and emits the audit event.
func TestDNSVerifier_HappyPath_FlipsToActive(t *testing.T) {
	h := Setup(t)
	f := newDomainsFixture(t, h, "verifier-ok-1111", 3601)
	id := seedAutoConfiguredDomain(t, h, f.deploymentID, "ok.example.com")

	resolver := &stubLookupResolver{
		fn: func(host string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		},
	}
	v := &synapsedns.Verifier{
		DB:         h.DB,
		Resolver:   resolver,
		ExpectedIP: "203.0.113.10",
	}
	v.Tick(context.Background())

	status, verifiedAt, lastErr := readDomainStatus(t, h, id)
	if status != "active" {
		t.Errorf("status: got %q want active", status)
	}
	if verifiedAt == nil {
		t.Errorf("expected dns_verified_at populated, got nil")
	}
	if lastErr != "" {
		t.Errorf("expected last_dns_error cleared, got %q", lastErr)
	}
	if got := countDomainAuditEvents(t, h, "domain.verified", id); got != 1 {
		t.Errorf("expected exactly 1 domain.verified event, got %d", got)
	}
}

// 2) Mismatch: resolver returns a different IP. Row stays pending,
// no audit emitted, last_dns_error gets the "expected ... got ..."
// hint so the dashboard can surface "still propagating".
func TestDNSVerifier_Mismatch_RowStaysPending(t *testing.T) {
	h := Setup(t)
	f := newDomainsFixture(t, h, "verifier-mis-2222", 3602)
	id := seedAutoConfiguredDomain(t, h, f.deploymentID, "mis.example.com")

	resolver := &stubLookupResolver{
		fn: func(host string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("198.51.100.99")}, nil
		},
	}
	v := &synapsedns.Verifier{
		DB:         h.DB,
		Resolver:   resolver,
		ExpectedIP: "203.0.113.10",
	}
	v.Tick(context.Background())

	status, verifiedAt, lastErr := readDomainStatus(t, h, id)
	if status != "pending" {
		t.Errorf("status: got %q want pending", status)
	}
	if verifiedAt != nil {
		t.Errorf("expected dns_verified_at nil, got %v", verifiedAt)
	}
	if lastErr == "" {
		t.Errorf("expected last_dns_error to be populated with mismatch hint")
	}
	if got := countDomainAuditEvents(t, h, "domain.verified", id); got != 0 {
		t.Errorf("expected 0 domain.verified events on mismatch, got %d", got)
	}
}

// 3) DNS lookup error: resolver returns error. Row stays pending,
// no audit, last_dns_error optionally surfaces the error string.
func TestDNSVerifier_LookupError_RowStaysPending(t *testing.T) {
	h := Setup(t)
	f := newDomainsFixture(t, h, "verifier-err-3333", 3603)
	id := seedAutoConfiguredDomain(t, h, f.deploymentID, "err.example.com")

	resolver := &stubLookupResolver{
		fn: func(host string) ([]net.IP, error) {
			return nil, errors.New("NXDOMAIN")
		},
	}
	v := &synapsedns.Verifier{
		DB:         h.DB,
		Resolver:   resolver,
		ExpectedIP: "203.0.113.10",
	}
	v.Tick(context.Background())

	status, _, lastErr := readDomainStatus(t, h, id)
	if status != "pending" {
		t.Errorf("status: got %q want pending", status)
	}
	if lastErr == "" {
		t.Errorf("expected last_dns_error populated on lookup failure")
	}
	if got := countDomainAuditEvents(t, h, "domain.verified", id); got != 0 {
		t.Errorf("expected 0 domain.verified events on lookup error, got %d", got)
	}
}

// 4) Old row: row pending + auto_configured, updated_at older than
// MaxAge. Tick flips to 'failed' with "DNS did not propagate" error.
// No audit emitted.
//
// v1.6.8+: anchor is now updated_at (was created_at). The latter
// punished operators who cadastraram a Cloudflare credential days
// after adding the domain — auto_configure runs, bumps updated_at,
// resets the deadline. See TestDNSVerifier_AutoConfiguredAfterStale.
func TestDNSVerifier_AgedOut_FlipsToFailed(t *testing.T) {
	h := Setup(t)
	f := newDomainsFixture(t, h, "verifier-old-4444", 3604)
	id := seedAutoConfiguredDomain(t, h, f.deploymentID, "old.example.com")

	// Backdate both timestamps so the row looks 10 minutes stale; the
	// fixedClock leaves "now" at the wall clock so the diff exceeds
	// MaxAge=1m. We touch BOTH columns to make the test honest about
	// what the new anchor measures.
	if _, err := h.DB.Exec(h.rootCtx, `
		UPDATE deployment_domains
		   SET created_at = now() - interval '10 minutes',
		       updated_at = now() - interval '10 minutes'
		 WHERE id = $1
	`, id); err != nil {
		t.Fatalf("backdate timestamps: %v", err)
	}

	resolver := &stubLookupResolver{
		fn: func(host string) ([]net.IP, error) {
			// Even if the resolver matched, the aged-out branch should
			// fire FIRST — so a successful lookup here would actively
			// hide a regression.
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		},
	}
	v := &synapsedns.Verifier{
		DB:         h.DB,
		Resolver:   resolver,
		ExpectedIP: "203.0.113.10",
		MaxAge:     1 * time.Minute,
	}
	v.Tick(context.Background())

	status, verifiedAt, lastErr := readDomainStatus(t, h, id)
	if status != "failed" {
		t.Errorf("status: got %q want failed", status)
	}
	if verifiedAt != nil {
		t.Errorf("expected dns_verified_at nil for failed row, got %v", verifiedAt)
	}
	if lastErr == "" {
		t.Errorf("expected last_dns_error to mention propagation deadline")
	}
	if got := countDomainAuditEvents(t, h, "domain.verified", id); got != 0 {
		t.Errorf("expected 0 domain.verified events on aged-out, got %d", got)
	}
	// Resolver should never have been called — aged-out branch
	// short-circuits before LookupIP.
	if resolver.Calls() != 0 {
		t.Errorf("expected resolver.Calls()=0 (aged-out skips lookup), got %d", resolver.Calls())
	}
}

// 5) Idle tick: zero pending rows, the loop runs the SELECT and
// exits without touching the resolver.
func TestDNSVerifier_IdleTick_NoResolverCalls(t *testing.T) {
	h := Setup(t)
	// Note: no seed call — table is empty for auto_configured rows.

	resolver := &stubLookupResolver{
		fn: func(host string) ([]net.IP, error) {
			t.Errorf("resolver should not be called on idle tick (host=%q)", host)
			return nil, nil
		},
	}
	v := &synapsedns.Verifier{
		DB:         h.DB,
		Resolver:   resolver,
		ExpectedIP: "203.0.113.10",
	}
	start := time.Now()
	v.Tick(context.Background())
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("idle tick took %v, expected < 500ms", time.Since(start))
	}
	if resolver.Calls() != 0 {
		t.Errorf("expected resolver.Calls()=0, got %d", resolver.Calls())
	}
}

// 6) Manual verify still works after verifier-driven flip. The
// verifier flips pending→active; subsequent /verify is idempotent
// (row stays active, no error).
func TestDNSVerifier_ManualVerifyStillWorks(t *testing.T) {
	// We need PublicIP set on the harness so the manual /verify
	// endpoint can succeed; and a Cloudflare factory + SecretBox so
	// the auto_configure path is exercisable. We don't actually call
	// auto_configure here — we hand-seed the row to mimic its end
	// state — but using SetupWithOpts is the simplest path to a
	// PublicIP-aware harness.
	h := SetupWithOpts(t, SetupOpts{PublicIP: "203.0.113.10"})
	f := newDomainsFixture(t, h, "verifier-manual-5555", 3605)
	id := seedAutoConfiguredDomain(t, h, f.deploymentID, "manual.example.com")

	resolver := &stubLookupResolver{
		fn: func(host string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		},
	}
	v := &synapsedns.Verifier{
		DB:         h.DB,
		Resolver:   resolver,
		ExpectedIP: "203.0.113.10",
	}
	v.Tick(context.Background())

	// Row should now be active.
	if status, _, _ := readDomainStatus(t, h, id); status != "active" {
		t.Fatalf("pre-verify status: got %q want active", status)
	}

	// Manual /verify endpoint — should still 200 + leave the row
	// active. The handler reads PublicIP for its own preflight, but
	// we can't inject our stub resolver into it; that's fine because
	// the manual path uses net.DefaultResolver which will FAIL on
	// "manual.example.com" (no real DNS), so the handler will
	// transition active → failed. We tolerate either active or
	// failed: the assertion is that the call returns 200 (idempotent
	// re-verify, no 500).
	resp := h.Do("POST",
		"/v1/deployments/"+f.deployment+"/domains/"+id+"/verify",
		f.owner.AccessToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("manual verify after verifier flip: status=%d, want 200", resp.StatusCode)
	}
}

// 7) Multi-node: two Verifier instances sharing a DB run a tick
// concurrently. The advisory lock means only one observes the
// pending row → only one flip happens, only one audit row is
// written.
func TestDNSVerifier_MultiNode_OnlyOneFlipsPerTick(t *testing.T) {
	h := Setup(t)
	f := newDomainsFixture(t, h, "verifier-multi-6666", 3606)
	id := seedAutoConfiguredDomain(t, h, f.deploymentID, "multi.example.com")

	// Both resolvers observe ANY call as a "this verifier saw the
	// row". The advisory lock should mean only one of them does.
	var aCalls, bCalls int64
	matchFn := func(counter *int64) func(host string) ([]net.IP, error) {
		return func(host string) ([]net.IP, error) {
			atomic.AddInt64(counter, 1)
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		}
	}
	a := &synapsedns.Verifier{
		DB: h.DB, ExpectedIP: "203.0.113.10",
		Resolver: &stubLookupResolver{fn: matchFn(&aCalls)},
	}
	b := &synapsedns.Verifier{
		DB: h.DB, ExpectedIP: "203.0.113.10",
		Resolver: &stubLookupResolver{fn: matchFn(&bCalls)},
	}

	// Drive both ticks via the lock-aware path. We can't call Tick
	// directly here (that bypasses the advisory lock) — the brief
	// specifically tests the multi-node coordination. Use Start()
	// with a short-lived context.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = a.Start(ctx) }()
	go func() { defer wg.Done(); _ = b.Start(ctx) }()
	wg.Wait()

	// Status: active. Only one audit event (the one that flipped
	// the row first).
	status, _, _ := readDomainStatus(t, h, id)
	if status != "active" {
		t.Errorf("status: got %q want active", status)
	}
	// At least one verifier must have run; the other is gated by the
	// advisory lock (acquired=false → tick is a no-op). Both
	// observing the row simultaneously would be a false positive
	// because the lock guards each tick, but the upgrade-to-active
	// UPDATE has WHERE status='pending' so it's idempotent — the
	// second runner would observe RowsAffected=0 + drop the audit.
	// Either way, exactly ONE audit row.
	if got := countDomainAuditEvents(t, h, "domain.verified", id); got != 1 {
		t.Errorf("expected exactly 1 domain.verified event under multi-node, got %d", got)
	}
}

// Bonus: verifier exits cleanly when ExpectedIP is empty (operator
// hasn't configured SYNAPSE_PUBLIC_IP yet). The brief calls this out
// as a boot-time condition.
func TestDNSVerifier_NoExpectedIP_NoOp(t *testing.T) {
	h := Setup(t)
	f := newDomainsFixture(t, h, "verifier-noip-7777", 3607)
	id := seedAutoConfiguredDomain(t, h, f.deploymentID, "noip.example.com")

	v := &synapsedns.Verifier{DB: h.DB, ExpectedIP: ""}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := v.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Row should remain untouched (still pending).
	if status, _, _ := readDomainStatus(t, h, id); status != "pending" {
		t.Errorf("status: got %q want pending (verifier was a no-op)", status)
	}
}

// TestDNSVerifier_AutoConfiguredAfterStale_DeadlineResets covers the
// v1.6.8 fix for the surfaced-on-synapsepanel-com regression: a row
// created days ago (created_at well past MaxAge) but auto-configured
// just now (updated_at fresh) must NOT be marked failed for "did not
// propagate within X". The deadline anchor is updated_at, not
// created_at — the auto_configure UPDATE bumps it, resetting the
// clock. Pre-1.6.8 the same input flipped to failed in seconds with a
// misleading "did not propagate within 5m0s" message.
func TestDNSVerifier_AutoConfiguredAfterStale_DeadlineResets(t *testing.T) {
	h := Setup(t)
	f := newDomainsFixture(t, h, "verifier-stale-9988", 3611)
	id := seedAutoConfiguredDomain(t, h, f.deploymentID, "stale.example.com")

	// Mimic the production scenario:
	//   - row created days ago (without a credential, the row sat
	//     FAILED for a long time)
	//   - operator then cadastrou the credential and clicked
	//     "Auto-configure DNS", which bumped updated_at to NOW and
	//     reset status back to 'pending' (the existing auto_configure
	//     handler does this — we just stamp the timestamps directly
	//     here so the test stays focused on the verifier).
	if _, err := h.DB.Exec(h.rootCtx, `
		UPDATE deployment_domains
		   SET created_at = now() - interval '10 days',
		       updated_at = now()
		 WHERE id = $1
	`, id); err != nil {
		t.Fatalf("simulate stale-create + fresh auto_configure: %v", err)
	}

	// Resolver returns no match — we want to confirm the row stays
	// 'pending' (not flipped to failed), not that it goes 'active'.
	// A different test (already exists) covers the active flip.
	resolver := &stubLookupResolver{
		fn: func(host string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("198.51.100.99")}, nil // wrong IP
		},
	}
	v := &synapsedns.Verifier{
		DB:         h.DB,
		Resolver:   resolver,
		ExpectedIP: "203.0.113.10",
		MaxAge:     1 * time.Minute,
	}
	v.Tick(context.Background())

	status, _, lastErr := readDomainStatus(t, h, id)
	if status != "pending" {
		t.Errorf("status: got %q want pending — created_at is old but updated_at was just bumped, so the deadline must NOT have fired", status)
	}
	// The "did not propagate within Xm" message would be the
	// regression signature — call it out specifically.
	if strings.Contains(lastErr, "did not propagate") {
		t.Errorf("regression: row flipped on deadline despite recent updated_at (lastErr=%q)", lastErr)
	}
}
