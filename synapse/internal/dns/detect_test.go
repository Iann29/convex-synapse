package dns

import (
	"context"
	"errors"
	"net"
	"testing"
)

// stubResolver implements the resolver interface used by detect().
type stubResolver struct {
	hosts []string
	err   error
}

func (s stubResolver) LookupNS(_ context.Context, _ string) ([]*net.NS, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([]*net.NS, 0, len(s.hosts))
	for _, h := range s.hosts {
		out = append(out, &net.NS{Host: h})
	}
	return out, nil
}

// perNameResolver answers from a name->hosts table so a single test
// can exercise the parent-climb path (subdomain misses, apex hits).
// Missing entries simulate the real-world empty/NXDOMAIN behaviour
// of net.LookupNS against a non-delegated subdomain.
type perNameResolver struct {
	answers   map[string][]string
	notFound  map[string]bool // names that should return *net.DNSError IsNotFound
	calls     []string
}

func (p *perNameResolver) LookupNS(_ context.Context, name string) ([]*net.NS, error) {
	p.calls = append(p.calls, name)
	if p.notFound[name] {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	hosts, ok := p.answers[name]
	if !ok {
		// Match the shape glibc's resolver returns for a name that
		// has no NS record: empty slice, nil error. The IsNotFound
		// path is covered separately via the notFound map.
		return nil, nil
	}
	out := make([]*net.NS, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, &net.NS{Host: h})
	}
	return out, nil
}

func TestProvider_Cloudflare(t *testing.T) {
	r := stubResolver{hosts: []string{"isla.ns.cloudflare.com.", "tom.ns.cloudflare.com."}}
	got, hosts, err := detect(context.Background(), r, "fechasul.com.br")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "cloudflare" {
		t.Errorf("provider: got %q want cloudflare", got)
	}
	if len(hosts) != 2 {
		t.Errorf("expected 2 ns hosts, got %d", len(hosts))
	}
}

func TestProvider_Route53(t *testing.T) {
	// Real Route53 NS shape: ns-123.awsdns-12.com / .net / .org / .co.uk
	r := stubResolver{hosts: []string{
		"ns-100.awsdns-12.com.",
		"ns-200.awsdns-34.net.",
	}}
	got, _, err := detect(context.Background(), r, "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "route53" {
		t.Errorf("provider: got %q want route53", got)
	}
}

func TestProvider_Google(t *testing.T) {
	r := stubResolver{hosts: []string{"ns-cloud-a1.googledomains.com."}}
	got, _, err := detect(context.Background(), r, "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "google" {
		t.Errorf("provider: got %q want google", got)
	}
}

func TestProvider_Unknown(t *testing.T) {
	r := stubResolver{hosts: []string{"ns1.someregistrar.example.", "ns2.someregistrar.example."}}
	got, hosts, err := detect(context.Background(), r, "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "unknown" {
		t.Errorf("provider: got %q want unknown", got)
	}
	if len(hosts) != 2 {
		t.Errorf("expected 2 ns hosts, got %d", len(hosts))
	}
}

func TestProvider_EmptyNSList(t *testing.T) {
	r := stubResolver{hosts: nil}
	got, hosts, err := detect(context.Background(), r, "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "unknown" {
		t.Errorf("provider: got %q want unknown", got)
	}
	if hosts != nil {
		t.Errorf("expected nil hosts, got %v", hosts)
	}
}

func TestProvider_LookupError(t *testing.T) {
	r := stubResolver{err: errors.New("resolver dead")}
	got, hosts, err := detect(context.Background(), r, "example.com")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if got != "unknown" {
		t.Errorf("provider: got %q want unknown", got)
	}
	if hosts != nil {
		t.Errorf("expected nil hosts on error, got %v", hosts)
	}
}

func TestProvider_EmptyDomain(t *testing.T) {
	r := stubResolver{hosts: []string{"isla.ns.cloudflare.com."}}
	got, _, err := detect(context.Background(), r, "  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// We bail before the lookup on empty input.
	if got != "unknown" {
		t.Errorf("provider: got %q want unknown", got)
	}
}

func TestProvider_TrailingDotNormalisation(t *testing.T) {
	// Hosts without trailing dot still match — we add the dot during
	// normalisation so the suffix table behaves consistently.
	r := stubResolver{hosts: []string{"isla.ns.cloudflare.com"}}
	got, _, err := detect(context.Background(), r, "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "cloudflare" {
		t.Errorf("provider: got %q want cloudflare", got)
	}
}

// Subdomain NS records typically don't exist — only the zone apex
// carries them. Detection must climb from the typed leaf up to the
// nearest ancestor that has NS records and classify those, otherwise
// the dashboard shows "DNS provider not detected" for the common
// "api.example.com" case where the user IS on Cloudflare.
func TestProvider_SubdomainClimbsToApex(t *testing.T) {
	r := &perNameResolver{
		answers: map[string][]string{
			"ingreis.com": {"isla.ns.cloudflare.com.", "tom.ns.cloudflare.com."},
		},
	}
	got, hosts, err := detect(context.Background(), r, "api.ingreis.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "cloudflare" {
		t.Errorf("provider: got %q want cloudflare", got)
	}
	if len(hosts) != 2 {
		t.Errorf("expected 2 ns hosts, got %d", len(hosts))
	}
	// Verify we actually probed the leaf first (and only climbed one
	// level). Catches an accidental "skip-to-parent" regression.
	if len(r.calls) != 2 || r.calls[0] != "api.ingreis.com" || r.calls[1] != "ingreis.com" {
		t.Errorf("unexpected call order: %v", r.calls)
	}
}

// A delegated subzone should win over its parent. If the operator
// types "api.example.com" AND that subdomain has its own NS records
// (e.g. delegated to Route53 while example.com sits on Cloudflare),
// detection must return the leaf's provider, not the apex's.
func TestProvider_DelegatedSubzoneWins(t *testing.T) {
	r := &perNameResolver{
		answers: map[string][]string{
			"api.example.com": {"ns-100.awsdns-12.com."},
			"example.com":     {"isla.ns.cloudflare.com."},
		},
	}
	got, _, err := detect(context.Background(), r, "api.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "route53" {
		t.Errorf("provider: got %q want route53 (delegated subzone)", got)
	}
	if len(r.calls) != 1 {
		t.Errorf("expected to stop after the first hit, got %d calls", len(r.calls))
	}
}

// A deep subdomain ("foo.bar.baz.example.com") must keep climbing
// until it hits the apex. We also want to make sure we never query
// a bare TLD: LookupNS("com") would return Verisign and pollute the
// detection signal.
func TestProvider_DeepSubdomainNeverProbesTLD(t *testing.T) {
	r := &perNameResolver{
		answers: map[string][]string{
			"example.com": {"ns-cloud-a1.googledomains.com."},
		},
	}
	got, _, err := detect(context.Background(), r, "foo.bar.baz.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "google" {
		t.Errorf("provider: got %q want google", got)
	}
	for _, c := range r.calls {
		if c == "com" {
			t.Errorf("must not query the bare TLD: %v", r.calls)
		}
	}
}

// NXDOMAIN on the leaf must not short-circuit the climb. glibc's
// resolver surfaces "no NS record" as IsNotFound on some Linux
// systems; we still need to probe the parent.
func TestProvider_NotFoundOnLeafKeepsClimbing(t *testing.T) {
	r := &perNameResolver{
		answers: map[string][]string{
			"example.com": {"isla.ns.cloudflare.com."},
		},
		notFound: map[string]bool{
			"api.example.com": true,
		},
	}
	got, _, err := detect(context.Background(), r, "api.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "cloudflare" {
		t.Errorf("provider: got %q want cloudflare", got)
	}
}

// Trailing-dot inputs (FQDN form) should be normalised before the
// climb so we don't query "ingreis.com." literally.
func TestProvider_TrailingDotInput(t *testing.T) {
	r := &perNameResolver{
		answers: map[string][]string{
			"ingreis.com": {"isla.ns.cloudflare.com."},
		},
	}
	got, _, err := detect(context.Background(), r, "api.ingreis.com.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "cloudflare" {
		t.Errorf("provider: got %q want cloudflare", got)
	}
}
