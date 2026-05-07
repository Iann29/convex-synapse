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
