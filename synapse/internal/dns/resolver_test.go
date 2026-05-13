package dns

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// TestExternalResolver_DialsConfiguredServer proves the Dial closure
// actually reaches the addresses in externalResolverServers. Without
// this guard a refactor that swaps in net.DefaultResolver's dial
// path would silently regress the whole point of this helper (which
// is bypassing the OS resolver). We point the package var at a
// localhost listener and confirm a TCP accept fires.
func TestExternalResolver_DialsConfiguredServer(t *testing.T) {
	orig := externalResolverServers
	t.Cleanup(func() { externalResolverServers = orig })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	accepted := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- c.RemoteAddr().String()
		c.Close()
	}()

	externalResolverServers = []string{ln.Addr().String()}

	conn, err := ExternalResolver().Dial(context.Background(), "tcp", "ignored")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	conn.Close()

	select {
	case <-accepted:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("listener never accepted; ExternalResolver dialled somewhere else")
	}
}

// TestExternalResolver_FallsBackOnDialError covers the multi-upstream
// failover: if the primary (1.1.1.1 in prod) is unreachable, we want
// to keep trying the rest of the list rather than surfacing the
// error from the first attempt. Operator egress firewalls that allow
// 8.8.8.8 but not 1.1.1.1 are surprisingly common in enterprise
// VPCs.
func TestExternalResolver_FallsBackOnDialError(t *testing.T) {
	orig := externalResolverServers
	t.Cleanup(func() { externalResolverServers = orig })

	// "Closed" address: bind, capture the port, immediately release
	// the listener so dials get connection-refused. Reusing the
	// freshly-released port is statistically unlikely in the test
	// window; if it happens the test would falsely pass (dial would
	// connect to whatever new listener stole the port). Acceptable
	// flake budget for a CI run.
	closedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen closed: %v", err)
	}
	closedAddr := closedLn.Addr().String()
	closedLn.Close()

	openLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen open: %v", err)
	}
	t.Cleanup(func() { openLn.Close() })

	accepted := make(chan struct{}, 1)
	go func() {
		c, err := openLn.Accept()
		if err != nil {
			return
		}
		accepted <- struct{}{}
		c.Close()
	}()

	externalResolverServers = []string{closedAddr, openLn.Addr().String()}

	conn, err := ExternalResolver().Dial(context.Background(), "tcp", "ignored")
	if err != nil {
		t.Fatalf("Dial: %v (expected fallback success)", err)
	}
	conn.Close()

	select {
	case <-accepted:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("fallback upstream never received a dial")
	}
}

// TestExternalResolver_AllServersDownReturnsFirstError covers the
// error-shape contract: when every upstream fails, we want the
// caller to see the first attempt's error (which usually points at
// the primary they expected to work). Surfacing the second error
// would mislead operators who haven't tweaked their firewall.
func TestExternalResolver_AllServersDownReturnsFirstError(t *testing.T) {
	orig := externalResolverServers
	t.Cleanup(func() { externalResolverServers = orig })

	closedA, _ := net.Listen("tcp", "127.0.0.1:0")
	closedAAddr := closedA.Addr().String()
	closedA.Close()

	closedB, _ := net.Listen("tcp", "127.0.0.1:0")
	closedBAddr := closedB.Addr().String()
	closedB.Close()

	externalResolverServers = []string{closedAAddr, closedBAddr}

	_, err := ExternalResolver().Dial(context.Background(), "tcp", "ignored")
	if err == nil {
		t.Fatal("expected error when every upstream is closed")
	}
	// Sanity: the surfaced error should mention the first server, not
	// the second. We can't assert the exact string (OS-dependent) but
	// we can assert it references the closed-A port.
	if firstAddr := closedAAddr; !containsHostPort(err.Error(), firstAddr) {
		t.Errorf("expected error to reference first server %q, got %q",
			firstAddr, err.Error())
	}
}

// TestExternalResolver_RespectsContextCancellation prevents the
// upstream loop from outliving a cancelled caller. A long Timeout on
// each dial would otherwise multiply across upstreams and blow past
// any per-request deadline the caller imposed.
func TestExternalResolver_RespectsContextCancellation(t *testing.T) {
	orig := externalResolverServers
	t.Cleanup(func() { externalResolverServers = orig })

	// Unrouteable address — TEST-NET-1 per RFC 5737, guaranteed to
	// time out (no host) so we can prove the cancel triggers before
	// the timeout naturally fires.
	externalResolverServers = []string{"192.0.2.1:53"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	start := time.Now()
	_, err := ExternalResolver().Dial(ctx, "tcp", "ignored")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if elapsed > externalDialTimeout/2 {
		t.Errorf("dial took %v, expected near-immediate cancel return", elapsed)
	}
}

// containsHostPort is a small helper that avoids pulling strings
// just for one Contains check. We compare exact ":port" suffixes
// because the host part formats differently across OSes.
func containsHostPort(haystack, addr string) bool {
	return len(haystack) >= len(addr) && stringIndex(haystack, addr) >= 0
}

func stringIndex(s, substr string) int {
	if substr == "" {
		return 0
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// Compile-time guard: ExternalResolver must return something that
// satisfies the resolver interface detect.go consumes. If a refactor
// drops LookupNS we want a build failure here, not a runtime
// surprise.
var _ interface {
	LookupNS(ctx context.Context, name string) ([]*net.NS, error)
} = ExternalResolver()

// Compile-time guard: ExternalResolver must satisfy the
// internal/api LookupIPResolver contract too. Cross-package check
// would need an import cycle, so we mirror the signature here.
var _ interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
} = ExternalResolver()

// suppress unused-import warning if the test file is the only place
// pulling sync — keeps go vet quiet when the cleanup helpers above
// are the only consumers.
var _ = sync.Mutex{}
