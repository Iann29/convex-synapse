package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// dispatchTestCase is a single path → expected upstream/redirect
// assertion. Helps keep the table-driven test focused on the dispatch
// logic, not the plumbing.
type dispatchTestCase struct {
	name           string
	path           string
	wantStatus     int
	wantUpstream   string // "api" / "convex" / "shell" / ""
	wantBodyMarker string // substring expected in upstream response body
	wantLocation   string // for redirects only
}

// TestDashboardHostHandler_PathDispatch covers the v1.6.11 contract:
// when a request lands on a role='dashboard' custom domain, the path
// decides where it goes. Three upstream stubs (api, convex, shell)
// echo a marker in the response body so we can assert which one
// actually served the request — not just "did SOME upstream
// respond".
func TestDashboardHostHandler_PathDispatch(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("api:" + r.URL.Path))
	}))
	defer api.Close()

	convex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("convex:" + r.URL.Path))
	}))
	defer convex.Close()

	shell := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("shell:" + r.URL.Path))
	}))
	defer shell.Close()

	build := func() http.Handler {
		return &DashboardHostHandler{
			APIHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				resp, err := http.Get(api.URL + r.URL.Path)
				if err != nil {
					t.Fatalf("api proxy: %v", err)
				}
				defer resp.Body.Close()
				b, _ := io.ReadAll(resp.Body)
				_, _ = w.Write(b)
			}),
			ConvexAddr:     strings.TrimPrefix(convex.URL, "http://"),
			ShellAddr:      strings.TrimPrefix(shell.URL, "http://"),
			DeploymentName: "fast-kestrel-2142",
		}
	}

	cases := []dispatchTestCase{
		{
			name:         "root redirects to embed page for bound deployment",
			path:         "/",
			wantStatus:   http.StatusFound,
			wantLocation: "/embed/fast-kestrel-2142",
		},
		{
			name:         "empty path also redirects (defensive — http normally fills /)",
			path:         "",
			wantStatus:   http.StatusFound,
			wantLocation: "/embed/fast-kestrel-2142",
		},
		{
			name:           "v1 API call passes to APIHandler",
			path:           "/v1/me",
			wantStatus:     http.StatusOK,
			wantUpstream:   "api",
			wantBodyMarker: "api:/v1/me",
		},
		{
			name:           "v1 auth login passes to APIHandler",
			path:           "/v1/auth/login",
			wantStatus:     http.StatusOK,
			wantUpstream:   "api",
			wantBodyMarker: "api:/v1/auth/login",
		},
		{
			name:           "/d/ deployment proxy passes to APIHandler",
			path:           "/d/some-name/api/query",
			wantStatus:     http.StatusOK,
			wantUpstream:   "api",
			wantBodyMarker: "api:/d/some-name/api/query",
		},
		{
			name:           "/health passes to APIHandler",
			path:           "/health",
			wantStatus:     http.StatusOK,
			wantUpstream:   "api",
			wantBodyMarker: "api:/health",
		},
		{
			name:           "/__convex/ root strips prefix to /",
			path:           "/__convex/",
			wantStatus:     http.StatusOK,
			wantUpstream:   "api",
			wantBodyMarker: "api:/__convex/",
		},
		{
			name:           "/__convex/asset strips prefix on the upstream",
			path:           "/__convex/_next/static/foo.js",
			wantStatus:     http.StatusOK,
			wantUpstream:   "api",
			wantBodyMarker: "api:/__convex/_next/static/foo.js",
		},
		{
			name:           "/login goes to Synapse Next.js shell",
			path:           "/login",
			wantStatus:     http.StatusOK,
			wantUpstream:   "shell",
			wantBodyMarker: "shell:/login",
		},
		{
			name:           "/embed/<other> deep link goes to shell (lets operator hand-edit URL)",
			path:           "/embed/another-deployment",
			wantStatus:     http.StatusOK,
			wantUpstream:   "shell",
			wantBodyMarker: "shell:/embed/another-deployment",
		},
		{
			name:           "/teams/foo goes to shell",
			path:           "/teams/foo",
			wantStatus:     http.StatusOK,
			wantUpstream:   "shell",
			wantBodyMarker: "shell:/teams/foo",
		},
		{
			name:           "Next.js static assets go to shell",
			path:           "/_next/static/chunks/main.js",
			wantStatus:     http.StatusOK,
			wantUpstream:   "shell",
			wantBodyMarker: "shell:/_next/static/chunks/main.js",
		},
		{
			name:           "favicon goes to shell",
			path:           "/favicon.ico",
			wantStatus:     http.StatusOK,
			wantUpstream:   "shell",
			wantBodyMarker: "shell:/favicon.ico",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := build()
			req := httptest.NewRequest(http.MethodGet, "http://dashboard.example.com"+tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d want %d (body=%q)",
					rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantLocation != "" {
				got := rec.Header().Get("Location")
				if got != tc.wantLocation {
					t.Errorf("Location: got %q want %q", got, tc.wantLocation)
				}
			}
			if tc.wantBodyMarker != "" {
				if !strings.Contains(rec.Body.String(), tc.wantBodyMarker) {
					t.Errorf("body: got %q, expected to contain %q",
						rec.Body.String(), tc.wantBodyMarker)
				}
			}
		})
	}
}

// TestDashboardHostHandler_ShellAddrMissing covers the misconfig
// path: operator hasn't wired a Synapse-dashboard upstream, but they
// did register a role='dashboard' custom domain. We don't want
// operator-facing URLs to 500 or worse render the bare Convex login
// form; surface 503 dashboard_shell_not_configured instead so the
// operator notices in their dashboard logs.
func TestDashboardHostHandler_ShellAddrMissing(t *testing.T) {
	h := &DashboardHostHandler{
		APIHandler:     http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}),
		ConvexAddr:     "ignored",
		ShellAddr:      "",
		DeploymentName: "fast-kestrel-2142",
	}
	req := httptest.NewRequest(http.MethodGet, "http://dashboard.example.com/login", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "dashboard_shell_not_configured") {
		t.Errorf("body should mention the misconfig code, got %q", rec.Body.String())
	}
}

// TestDashboardHostHandler_PreservesMethod covers a subtle bug class:
// the path-dispatch switch must NOT bind on r.Method. /v1/auth/login
// is POST, /login is GET, /__convex/api/whatever can be anything —
// each must route correctly.
func TestDashboardHostHandler_PreservesMethod(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Method + ":api"))
	}))
	defer api.Close()
	shell := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Method + ":shell"))
	}))
	defer shell.Close()

	h := &DashboardHostHandler{
		APIHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Forward the request preserving method + body so the
			// upstream stub can echo `r.Method` accurately. Using
			// http.Get hides this entirely; use a real round-trip.
			fwd, err := http.NewRequestWithContext(r.Context(), r.Method, api.URL+r.URL.Path, r.Body)
			if err != nil {
				t.Fatalf("api fwd build: %v", err)
			}
			resp, err := http.DefaultClient.Do(fwd)
			if err != nil {
				t.Fatalf("api fwd do: %v", err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			_, _ = w.Write(b)
		}),
		ConvexAddr:     "ignored",
		ShellAddr:      strings.TrimPrefix(shell.URL, "http://"),
		DeploymentName: "fast-kestrel-2142",
	}

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method+"_v1_auth_login", func(t *testing.T) {
			req := httptest.NewRequest(method, "http://dashboard.example.com/v1/auth/login", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if !strings.Contains(rec.Body.String(), method+":api") {
				t.Errorf("expected %s:api in body, got %q", method, rec.Body.String())
			}
		})
		t.Run(method+"_login_shell", func(t *testing.T) {
			req := httptest.NewRequest(method, "http://dashboard.example.com/login", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if !strings.Contains(rec.Body.String(), method+":shell") {
				t.Errorf("expected %s:shell in body, got %q", method, rec.Body.String())
			}
		})
	}
}
