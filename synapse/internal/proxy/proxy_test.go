package proxy

import (
	"strings"
	"testing"
)

func TestEscapeJSON(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello", "hello"},
		{`he"llo`, `he\"llo`},
		{"line\nbreak", `line\nbreak`},
		{`back\slash`, `back\\slash`},
		{"tab\there", `tab\there`},
	}
	for _, tc := range cases {
		got := escapeJSON(tc.in)
		if got != tc.want {
			t.Errorf("escapeJSON(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// Path parsing inside Handler: stripping "/d/" then splitting at the first
// '/'. Hard to unit-test the full handler without a fake DB; the integration
// test in internal/test/proxy_test.go covers the live path.

func TestPathSplit(t *testing.T) {
	cases := []struct {
		path       string
		wantName   string
		wantRest   string
		wantPrefix bool
	}{
		{"/d/foo", "foo", "/", true},
		{"/d/foo/", "foo", "/", true},
		{"/d/foo/api/check_admin_key", "foo", "/api/check_admin_key", true},
		{"/d/foo/a/b?c=d", "foo", "/a/b?c=d", true},
		{"/d/", "", "/", true},
		{"/v1/teams", "", "", false},
	}
	for _, tc := range cases {
		raw, hasPrefix := stripDPrefix(tc.path)
		if hasPrefix != tc.wantPrefix {
			t.Errorf("path %q hasPrefix=%v want %v", tc.path, hasPrefix, tc.wantPrefix)
			continue
		}
		if !hasPrefix {
			continue
		}
		name, rest := splitFirst(raw)
		if name != tc.wantName || rest != tc.wantRest {
			t.Errorf("path %q → name=%q rest=%q; want name=%q rest=%q",
				tc.path, name, rest, tc.wantName, tc.wantRest)
		}
	}
}

// helpers exposed only to tests — same logic as Handler above, kept inline
// in the production handler for clarity.
func stripDPrefix(path string) (string, bool) {
	const prefix = "/d/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	return path[len(prefix):], true
}

func splitFirst(raw string) (name, rest string) {
	slash := strings.IndexByte(raw, '/')
	if slash < 0 {
		return raw, "/"
	}
	return raw[:slash], raw[slash:]
}
