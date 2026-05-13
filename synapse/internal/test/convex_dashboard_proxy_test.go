package synapsetest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestConvexDashboardProxy_SameOriginPath validates the v1.6.11
// `/__convex/*` chi mount. Operator's browser hits
// https://<their-install>/__convex/<asset>, chi strips the
// /__convex prefix, and synapse-api reverse-proxies the rest to
// the convex-dashboard-proxy upstream. Same-origin means no Mixed
// Content blocks and no cross-origin cookie shenanigans for the
// /embed/<name> iframe.
func TestConvexDashboardProxy_SameOriginPath(t *testing.T) {
	// Stub upstream — answers with a marker that echoes the path it
	// received, so the test can assert chi stripped /__convex from
	// the inbound URL.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream:" + r.URL.Path))
	}))
	defer upstream.Close()

	h := SetupWithOpts(t, SetupOpts{
		ConvexDashboardUpstream: strings.TrimPrefix(upstream.URL, "http://"),
	})

	cases := []struct {
		name           string
		requestPath    string
		wantUpstream   string
		wantStatusCode int
	}{
		{
			name:           "root /__convex strips to /",
			requestPath:    "/__convex/",
			wantUpstream:   "upstream:/",
			wantStatusCode: http.StatusOK,
		},
		{
			name:           "leaf path passes through stripped",
			requestPath:    "/__convex/data",
			wantUpstream:   "upstream:/data",
			wantStatusCode: http.StatusOK,
		},
		{
			name:           "nested asset path strips correctly",
			requestPath:    "/__convex/_next/static/chunks/main.js",
			wantUpstream:   "upstream:/_next/static/chunks/main.js",
			wantStatusCode: http.StatusOK,
		},
		{
			name:           "deep team / deployment slug strips correctly",
			requestPath:    "/__convex/team/acme/proj/x/data",
			wantUpstream:   "upstream:/team/acme/proj/x/data",
			wantStatusCode: http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.Do(http.MethodGet, tc.requestPath, "", nil)
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatusCode {
				t.Fatalf("status: got %d want %d", resp.StatusCode, tc.wantStatusCode)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), tc.wantUpstream) {
				t.Errorf("body: got %q, expected to contain %q", body, tc.wantUpstream)
			}
		})
	}
}

// TestConvexDashboardProxy_UpstreamUnset covers the operator-misconfig
// path: the chi mount is wired but the upstream env var is empty.
// We want a clear 503 with a specific code so the operator can
// match against logs / dashboards, not a generic 502 that looks
// like a transient blip.
func TestConvexDashboardProxy_UpstreamUnset(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		ConvexDashboardUpstream: "",
	})

	resp := h.Do(http.MethodGet, "/__convex/", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "dashboard_upstream_unset") {
		t.Errorf("body should mention the misconfig code, got %q", body)
	}
}

// TestConvexDashboardProxy_UpstreamUnreachable covers the runtime-
// failure path: the env is set, but the host is down (typo, container
// crash, etc.). The handler should surface 502 upstream_error so the
// dashboard UI can render "Convex dashboard is unreachable" instead
// of a black iframe.
func TestConvexDashboardProxy_UpstreamUnreachable(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		// Reserved-for-documentation address from RFC 5737. Guaranteed
		// to never be a real listener, so the dial fails fast.
		ConvexDashboardUpstream: "192.0.2.1:80",
	})

	resp := h.Do(http.MethodGet, "/__convex/", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status: got %d want 502", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "upstream_error") {
		t.Errorf("body should mention upstream_error, got %q", body)
	}
}
