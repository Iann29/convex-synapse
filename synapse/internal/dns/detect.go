// Package dns provides DNS-provider detection (so the dashboard can
// show the right "auto-configure" UI) and a Cloudflare API wrapper
// (so Synapse can actually create the A record on the operator's
// behalf when they paste in a Cloudflare API token).
//
// The package is intentionally tiny: detect.go does an NS lookup and
// matches against a hard-coded suffix table; cloudflare.go wraps the
// libdns/cloudflare provider. We don't try to abstract over multiple
// providers behind a generic interface yet — Cloudflare is the only
// one we implement here, and Route53/Google/etc. would each have
// enough provider-specific quirks (auth shape, zone discovery) that
// premature abstraction would just be moved cost.
package dns

import (
	"context"
	"net"
	"strings"
	"time"
)

// providerSuffixes maps a hostname suffix found in a domain's NS
// records to a provider identifier. Trailing dots are deliberate:
// net.LookupNS returns FQDNs ending in "." (e.g. "ns1.cloudflare.com.")
// so we match with the dot to avoid a false positive on
// "ns1.cloudflare.community.".
//
// Order matters here only for deterministic output; we walk the
// list in lookup order and pick the first match.
var providerSuffixes = map[string]string{
	".ns.cloudflare.com.":     "cloudflare",
	".awsdns-":                "route53", // matches awsdns-XX.com/.net/.org/.co.uk
	".googledomains.com.":     "google",
	".registrar-servers.com.": "namecheap",
	".domaincontrol.com.":     "godaddy",
}

// lookupTimeout caps the synchronous NS lookup. 5s matches the
// timeout the domains handler uses for A-record verification, so an
// unreachable resolver doesn't keep an admin POST open past the
// router timeout.
const lookupTimeout = 5 * time.Second

// resolver is overridable in tests so the detection path doesn't
// reach out to the real internet from the integration suite.
type resolver interface {
	LookupNS(ctx context.Context, name string) ([]*net.NS, error)
}

// defaultResolver delegates to net.DefaultResolver. Production
// callers leave Provider's resolver hook nil; tests inject a stub.
var defaultResolver resolver = net.DefaultResolver

// Provider returns the DNS-provider identifier (e.g. "cloudflare",
// "route53", "unknown") for the given domain, plus the raw NS hosts
// that informed the decision. The dashboard uses the provider
// identifier to decide which auto-configure UI to render; the NS
// hosts are surfaced so the operator can sanity-check the answer.
//
// Returns ("unknown", nameservers, nil) when the lookup succeeds but
// no suffix matches. Returns ("unknown", nil, err) on lookup error.
// Empty NS list (theoretically impossible — every domain has an NS
// record — but possible against a misconfigured resolver) is treated
// as "unknown" with nil error to keep the caller's branch table
// small.
func Provider(ctx context.Context, domain string) (string, []string, error) {
	return detect(ctx, defaultResolver, domain)
}

// detect is the test seam. Production callers go through Provider.
func detect(ctx context.Context, r resolver, domain string) (string, []string, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return "unknown", nil, nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	nss, err := r.LookupNS(lookupCtx, domain)
	if err != nil {
		return "unknown", nil, err
	}
	if len(nss) == 0 {
		return "unknown", nil, nil
	}

	// Build the lowercase, trailing-dot-normalised host list once so
	// both the suffix match AND the returned slice see the same view.
	hosts := make([]string, 0, len(nss))
	for _, ns := range nss {
		h := strings.ToLower(strings.TrimSpace(ns.Host))
		if h == "" {
			continue
		}
		if !strings.HasSuffix(h, ".") {
			h += "."
		}
		hosts = append(hosts, h)
	}

	for _, h := range hosts {
		for suffix, name := range providerSuffixes {
			if strings.Contains(h, suffix) {
				return name, hosts, nil
			}
		}
	}
	return "unknown", hosts, nil
}
