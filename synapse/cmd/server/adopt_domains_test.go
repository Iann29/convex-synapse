package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- parser tests ----------

const sampleCaddyfile = `
# Snippets are reusable directive groups; we should NOT confuse them
# with hostname blocks.
(sec_headers_full) {
    header X-Frame-Options DENY
}

(sec_headers_min) {
    header X-Content-Type-Options nosniff
}

# A port-only listener — should be skipped.
:8080 {
    respond "synapse healthcheck"
}

# A wildcard subdomain — also skipped.
*.staging.example.com {
    reverse_proxy 127.0.0.1:9999
}

# The interesting blocks.
api.fechasul.com.br {
    import sec_headers_full
    @api_v1 path /v1/* /api/* /actions/* /auth/*
    handle @api_v1 {
        reverse_proxy 127.0.0.1:3222
    }
    reverse_proxy 127.0.0.1:3223
}

dashboard.fechasul.com.br {
    import sec_headers_min
    reverse_proxy 127.0.0.1:6797
}

# A block that uses a Docker-network hostname like the operator's
# "convex-foo-backend" → the deployment-name auto-detect should still
# work off the public hostname's second label.
api.othershop.io {
    reverse_proxy convex-othershop-backend:3210
}
`

func TestParseCaddyfile(t *testing.T) {
	blocks, errs := parseCaddyfile([]byte(sampleCaddyfile))
	if len(errs) != 0 {
		t.Fatalf("unexpected parse errors: %+v", errs)
	}

	// Build a map host → block for easy assertions; snippets and
	// non-hostname blocks have empty Hostname so they collapse.
	got := map[string][]caddyUpstream{}
	snippets := 0
	for _, b := range blocks {
		if b.IsSnippet {
			snippets++
			continue
		}
		if b.Hostname == "" {
			continue // :8080, *.staging
		}
		got[b.Hostname] = b.Upstreams
	}

	if snippets != 2 {
		t.Errorf("expected 2 snippet blocks, got %d", snippets)
	}

	want := map[string][]caddyUpstream{
		"api.fechasul.com.br": {
			{Host: "127.0.0.1", Port: 3222}, // inside handle
			{Host: "127.0.0.1", Port: 3223}, // catch-all
		},
		"dashboard.fechasul.com.br": {
			{Host: "127.0.0.1", Port: 6797},
		},
		"api.othershop.io": {
			{Host: "convex-othershop-backend", Port: 3210},
		},
	}
	for host, ups := range want {
		gu, ok := got[host]
		if !ok {
			t.Errorf("missing block for %q", host)
			continue
		}
		if len(gu) != len(ups) {
			t.Errorf("%s: got %d upstreams, want %d (%+v)", host, len(gu), len(ups), gu)
			continue
		}
		for i := range ups {
			if gu[i] != ups[i] {
				t.Errorf("%s upstream[%d]: got %+v want %+v", host, i, gu[i], ups[i])
			}
		}
	}

	// Make sure we didn't pick up "api.staging" or "synapse healthcheck"
	// as hostnames.
	for h := range got {
		if strings.HasPrefix(h, "*") || strings.HasPrefix(h, ":") {
			t.Errorf("parser leaked non-hostname block: %q", h)
		}
	}
}

func TestParseCaddyfile_Malformed(t *testing.T) {
	src := `
api.foo.com {
    reverse_proxy 127.0.0.1:3210
# missing closing brace, EOF below
`
	_, errs := parseCaddyfile([]byte(src))
	if len(errs) == 0 {
		t.Fatal("expected an unclosed-block error, got none")
	}
}

func TestStripComment(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"foo # bar", "foo "},
		{"# all comment", ""},
		{"reverse_proxy 127.0.0.1:3210", "reverse_proxy 127.0.0.1:3210"},
		{"", ""},
	}
	for _, c := range cases {
		if got := stripComment(c.in); got != c.want {
			t.Errorf("stripComment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClassifyAddress(t *testing.T) {
	cases := []struct {
		in           string
		wantHost     string
		wantSnippet  bool
		wantHostNote string
	}{
		{"api.fechasul.com.br", "api.fechasul.com.br", false, "plain"},
		{"DASHBOARD.example.com", "dashboard.example.com", false, "lowercased"},
		{"(sec_headers_full)", "", true, "snippet"},
		{":8080", "", false, "port-only"},
		{"*.example.com", "", false, "wildcard"},
		{"https://api.example.com", "api.example.com", false, "scheme stripped"},
		{"api.example.com:443", "api.example.com", false, "trailing port"},
		{"foo.com, bar.com", "foo.com", false, "first of comma-list"},
		{"localhost", "", false, "single label rejected"},
	}
	for _, c := range cases {
		gotHost, gotSnip := classifyAddress(c.in)
		if gotHost != c.wantHost || gotSnip != c.wantSnippet {
			t.Errorf("%s: classifyAddress(%q) = (%q,%v) want (%q,%v)",
				c.wantHostNote, c.in, gotHost, gotSnip, c.wantHost, c.wantSnippet)
		}
	}
}

func TestParseUpstream(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantPort int
		wantOK   bool
	}{
		{"127.0.0.1:3222", "127.0.0.1", 3222, true},
		{"convex-foo-backend:3210", "convex-foo-backend", 3210, true},
		{"h2c://convex:3210", "convex", 3210, true},
		{"http://api/foo/bar", "api", 0, true},
		{"", "", 0, false},
		{"127.0.0.1:notanumber", "", 0, false},
	}
	for _, c := range cases {
		got, ok := parseUpstream(c.in)
		if ok != c.wantOK {
			t.Errorf("parseUpstream(%q) ok=%v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if !c.wantOK {
			continue
		}
		if got.Host != c.wantHost || got.Port != c.wantPort {
			t.Errorf("parseUpstream(%q) = %+v want host=%s port=%d",
				c.in, got, c.wantHost, c.wantPort)
		}
	}
}

// ---------- inference tests ----------

func TestInferRole(t *testing.T) {
	cases := []struct {
		host    string
		ups     []caddyUpstream
		dflt    string
		want    string
	}{
		// hostname hint wins
		{"dashboard.foo.com", []caddyUpstream{{Port: 3210}}, "api", "dashboard"},
		{"api.foo.com", []caddyUpstream{{Port: 6797}}, "dashboard", "api"},
		// no hostname hint → port range
		{"foo.bar.com", []caddyUpstream{{Port: 3222}}, "dashboard", "api"},
		{"foo.bar.com", []caddyUpstream{{Port: 6797}}, "api", "dashboard"},
		{"foo.bar.com", []caddyUpstream{{Port: 3210}}, "dashboard", "api"},
		// outside both ranges → defaultRole
		{"foo.bar.com", []caddyUpstream{{Port: 9999}}, "api", "api"},
		{"foo.bar.com", []caddyUpstream{{Port: 9999}}, "dashboard", "dashboard"},
		// no upstreams → defaultRole
		{"foo.bar.com", nil, "api", "api"},
		// alternative aliases
		{"dash.foo.com", nil, "api", "dashboard"},
		{"backend.foo.com", nil, "dashboard", "api"},
	}
	for _, c := range cases {
		got := inferRole(c.host, c.ups, c.dflt)
		if got != c.want {
			t.Errorf("inferRole(%s, %+v, %s) = %s want %s",
				c.host, c.ups, c.dflt, got, c.want)
		}
	}
}

func TestInferDeploymentName(t *testing.T) {
	cases := []struct {
		host string
		want string
		ok   bool
	}{
		{"api.fechasul.com.br", "fechasul", true},
		{"dashboard.fechasul.com.br", "fechasul", true},
		{"api.foo.io", "foo", true},
		// fewer than 3 labels — operator must use --map
		{"foo.com", "", false},
		{"localhost", "", false},
		// non-slug second label
		{"api.with_underscore.com", "", false},
	}
	for _, c := range cases {
		got, ok := inferDeploymentName(c.host)
		if ok != c.ok || got != c.want {
			t.Errorf("inferDeploymentName(%q) = (%q,%v) want (%q,%v)",
				c.host, got, ok, c.want, c.ok)
		}
	}
}

// ---------- plan builder ----------

func TestBuildPlan(t *testing.T) {
	blocks, _ := parseCaddyfile([]byte(sampleCaddyfile))

	plan := buildPlan(blocks, "api", nil)
	want := []plannedDomain{
		{Hostname: "api.fechasul.com.br", DeploymentName: "fechasul", Role: "api", Source: "127.0.0.1:3223"},
		{Hostname: "api.othershop.io", DeploymentName: "othershop", Role: "api", Source: "convex-othershop-backend:3210"},
		{Hostname: "dashboard.fechasul.com.br", DeploymentName: "fechasul", Role: "dashboard", Source: "127.0.0.1:6797"},
	}
	if len(plan) != len(want) {
		t.Fatalf("plan size mismatch: got %d, want %d (%+v)", len(plan), len(want), plan)
	}
	for i, w := range want {
		got := plan[i]
		if got.Hostname != w.Hostname || got.DeploymentName != w.DeploymentName ||
			got.Role != w.Role || got.Source != w.Source {
			t.Errorf("plan[%d]: got %+v, want %+v", i, got, w)
		}
		if got.Reason != "" {
			t.Errorf("plan[%d]: unexpected reason %q", i, got.Reason)
		}
	}
}

func TestBuildPlan_Override(t *testing.T) {
	// Hostname with only 2 labels — auto-detect fails, so the
	// operator uses --map to point it at a deployment.
	caddy := `
foo.com {
    reverse_proxy 127.0.0.1:3210
}

api.bar.com {
    reverse_proxy 127.0.0.1:3210
}
`
	blocks, _ := parseCaddyfile([]byte(caddy))

	// No override → first row should carry a Reason.
	plan := buildPlan(blocks, "api", nil)
	var foo *plannedDomain
	for i, p := range plan {
		if p.Hostname == "foo.com" {
			foo = &plan[i]
		}
	}
	if foo == nil {
		t.Fatal("missing foo.com row")
	}
	if foo.Reason == "" {
		t.Error("expected reason for unmappable foo.com, got none")
	}
	if foo.DeploymentName != "" {
		t.Errorf("expected empty deployment for foo.com, got %q", foo.DeploymentName)
	}

	// With override → no Reason, deployment matches.
	plan = buildPlan(blocks, "api", map[string]string{"foo.com": "fooprod"})
	for _, p := range plan {
		if p.Hostname == "foo.com" {
			if p.Reason != "" {
				t.Errorf("expected no reason with override, got %q", p.Reason)
			}
			if p.DeploymentName != "fooprod" {
				t.Errorf("override didn't apply: got %q", p.DeploymentName)
			}
		}
	}
}

func TestBuildPlan_DefaultRoleApplies(t *testing.T) {
	// Hostname that doesn't start with api./dashboard. and an
	// upstream port outside both Convex ranges — defaultRole wins.
	caddy := `
public.example.com {
    reverse_proxy 127.0.0.1:9000
}
`
	blocks, _ := parseCaddyfile([]byte(caddy))
	plan := buildPlan(blocks, "dashboard", nil)
	if len(plan) != 1 {
		t.Fatalf("got %d rows, want 1", len(plan))
	}
	if plan[0].Role != "dashboard" {
		t.Errorf("expected default role to apply, got %q", plan[0].Role)
	}
}

// ---------- live mode ----------

func TestPostPlan_LiveMode(t *testing.T) {
	type recorded struct {
		path  string
		body  map[string]string
		auth  string
	}
	var (
		mu      sync.Mutex
		hits    []recorded
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		hits = append(hits, recorded{
			path: r.URL.Path,
			body: body,
			auth: r.Header.Get("Authorization"),
		})
		// Simulate one of the deployments returning a 409 so we can
		// assert the failure summary.
		if body["domain"] == "dashboard.fechasul.com.br" {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"code":"domain_already_registered"}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	plan := []plannedDomain{
		{Hostname: "api.fechasul.com.br", DeploymentName: "fechasul", Role: "api"},
		{Hostname: "dashboard.fechasul.com.br", DeploymentName: "fechasul", Role: "dashboard"},
		{Hostname: "skipme.com", Reason: "manual"}, // should be skipped
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := postPlan(ctx, srv.Client(), srv.URL, "test-token", plan)
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}

	// One success, one 409, one skip.
	var ok, fail, skip int
	for _, r := range results {
		switch {
		case r.OK:
			ok++
		case strings.HasPrefix(r.Message, "skipped:"):
			skip++
		default:
			fail++
		}
	}
	if ok != 1 || fail != 1 || skip != 1 {
		t.Errorf("counts: ok=%d fail=%d skip=%d (%+v)", ok, fail, skip, results)
	}

	// Two real POSTs hit the server (the third was skipped client-side).
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 2 {
		t.Fatalf("expected 2 server hits, got %d", len(hits))
	}
	for _, h := range hits {
		if h.auth != "Bearer test-token" {
			t.Errorf("auth header missing/wrong: %q", h.auth)
		}
		if !strings.HasPrefix(h.path, "/v1/deployments/fechasul/domains") {
			t.Errorf("unexpected path: %s", h.path)
		}
	}
}

// ---------- printers (smoke) ----------

func TestPrintPlan_Empty(t *testing.T) {
	var buf bytes.Buffer
	printPlan(&buf, nil)
	if !strings.Contains(buf.String(), "no usable hostname blocks") {
		t.Errorf("empty-plan message missing: %q", buf.String())
	}
}

func TestPrintPlan_RendersAllColumns(t *testing.T) {
	var buf bytes.Buffer
	printPlan(&buf, []plannedDomain{
		{Hostname: "api.foo.com", DeploymentName: "foo", Role: "api", Source: "127.0.0.1:3222"},
	})
	out := buf.String()
	for _, want := range []string{"HOSTNAME", "DEPLOYMENT", "ROLE", "SOURCE", "NOTE", "api.foo.com", "foo"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

// ---------- adoptDomainsRun integration (dry-run end-to-end) ----------

func TestAdoptDomainsRun_DryRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Caddyfile")
	if err := os.WriteFile(path, []byte(sampleCaddyfile), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	err := adoptDomainsRun(adoptDomainsFlags{
		Caddyfile:   path,
		DryRun:      true,
		DefaultRole: "api",
	}, &buf)
	if err != nil {
		t.Fatalf("dry-run failed: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"api.fechasul.com.br",
		"dashboard.fechasul.com.br",
		"fechasul",
		"(dry-run)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q\n%s", want, out)
		}
	}
}

func TestAdoptDomainsRun_ValidatesFlags(t *testing.T) {
	// missing caddyfile
	if err := adoptDomainsRun(adoptDomainsFlags{DryRun: true}, io.Discard); err == nil {
		t.Error("expected error when --caddyfile is missing")
	}
	// bad default-role
	dir := t.TempDir()
	path := filepath.Join(dir, "Caddyfile")
	_ = os.WriteFile(path, []byte("foo.com { reverse_proxy 127.0.0.1:3210 }"), 0o644)
	err := adoptDomainsRun(adoptDomainsFlags{
		Caddyfile:   path,
		DryRun:      true,
		DefaultRole: "bogus",
	}, io.Discard)
	if err == nil {
		t.Error("expected error on bogus --default-role")
	}
	// live mode with no token
	err = adoptDomainsRun(adoptDomainsFlags{
		Caddyfile:   path,
		DefaultRole: "api",
	}, io.Discard)
	if err == nil {
		t.Error("expected error when --token is missing in live mode")
	}
}
