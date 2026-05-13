package dns

import (
	"context"
	"net"
	"time"
)

// externalResolverServers is the fixed list of public recursive
// resolvers we dial directly. Cloudflare (1.1.1.1) is primary —
// anycasted globally, no logging policy, consistently low-latency
// across regions. Google (8.8.8.8) is fallback to survive a 1.1.1.1
// outage or operator egress firewall that allows one but not the
// other. Both UDP/TCP 53; net.Resolver tries each entry in order.
var externalResolverServers = []string{"1.1.1.1:53", "8.8.8.8:53"}

// externalDialTimeout caps a single dial attempt against one of the
// upstream resolvers. 5s matches lookupTimeout in detect.go so a
// flaky upstream can't keep the whole NS-detection chain hanging
// past the router's deadline.
const externalDialTimeout = 5 * time.Second

// ExternalResolver returns a *net.Resolver that dials a public
// recursive resolver directly instead of going through the OS
// resolver. Use this for every DNS lookup whose purpose is "is the
// record visible from the public internet" — provider detection, A-
// record verification, async propagation polling.
//
// Why we don't just use net.DefaultResolver:
//
//   - Synapse ships in a distroless container with CGO_ENABLED=0, so
//     Go uses its pure-Go resolver. That resolver reads
//     /etc/resolv.conf at startup and falls back to "127.0.0.1:53" if
//     the file is missing, empty, or unparseable.
//
//   - On a host running systemd-resolved (Ubuntu 24.04 default),
//     /etc/resolv.conf is a stub pointing at 127.0.0.53. Docker
//     detects loopback nameservers and synthesises a container
//     resolv.conf, but the exact output is daemon-version-dependent
//     and the embedded-DNS forwarder at 127.0.0.11 sometimes ends up
//     with no usable upstreams (the host's loopback resolvers can't
//     forward outside the container's netns). Result: Go's resolver
//     falls back to 127.0.0.1:53 — nothing inside our distroless
//     image listens there — and every external lookup returns
//     "no such host". Field-discovered on the synapsepanel.com VPS
//     while debugging custom-domain verification: the A record was
//     globally propagated and `dig` on the host returned the right
//     IP, but the synapse-api container's verifyDomainDNS kept
//     stamping rows FAILED with "lookup ... on 127.0.0.1:53: no
//     such host".
//
//   - Beyond that specific bug, using a fixed public resolver is the
//     semantically correct choice for "did the operator publish this
//     record where the rest of the internet can see it?" — we don't
//     want to consult /etc/hosts overrides, a corporate split-horizon
//     resolver, or whatever cached negative response a local stub
//     might be holding. Going straight to 1.1.1.1 gives the same
//     answer a real visitor's resolver would (give or take
//     propagation lag).
//
// PreferGo is implied by the custom Dial: with Dial set, the
// resolver always uses the Go implementation regardless of the
// PreferGo field. We keep PreferGo=true anyway as belt-and-
// suspenders in case future Go versions tighten that rule.
func ExternalResolver() *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			// network is "udp" / "tcp" depending on what the
			// resolver decided to use for this query (UDP first,
			// TCP for retries / large responses). The caller's
			// `address` is ignored — we always target our fixed
			// upstream list.
			d := net.Dialer{Timeout: externalDialTimeout}
			var firstErr error
			for _, server := range externalResolverServers {
				conn, err := d.DialContext(ctx, network, server)
				if err == nil {
					return conn, nil
				}
				if firstErr == nil {
					firstErr = err
				}
			}
			return nil, firstErr
		},
	}
}
