package api

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Hello World", "hello-world"},
		{"Multi   Spaces", "multi-spaces"},
		{"Already-A-Slug", "already-a-slug"},
		{"Trim --- Dashes ---", "trim-dashes"},
		{"  leading-trailing  ", "leading-trailing"},
		{"with_underscores", "with-underscores"},
		{"Acentuação ñ café", "acentuação-ñ-café"},
		{"emoji 🎉 here", "emoji-here"},
		{"", ""}, // empty handled separately below
	}
	for _, tc := range cases {
		got := slugify(tc.in)
		if tc.want == "" {
			// Empty input gets a random fallback; check format only.
			if !strings.HasPrefix(got, "x-") || len(got) < 5 {
				t.Errorf("slugify(%q) = %q; expected x-<hex> fallback", tc.in, got)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("slugify(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestSlugifyMaxLength(t *testing.T) {
	in := strings.Repeat("a", 100)
	got := slugify(in)
	if len(got) > 60 {
		t.Errorf("slug should be <= 60 chars, got %d", len(got))
	}
}

func TestWithSuffix(t *testing.T) {
	cases := []struct {
		base string
		n    int
		want string
	}{
		{"foo", 1, "foo-1"},
		{"foo", 12, "foo-12"},
		{"a-b-c", 0, "a-b-c-0"},
	}
	for _, tc := range cases {
		got := withSuffix(tc.base, tc.n)
		if got != tc.want {
			t.Errorf("withSuffix(%q, %d) = %q; want %q", tc.base, tc.n, got, tc.want)
		}
	}
}

func TestWithSuffixTrimsLong(t *testing.T) {
	long := strings.Repeat("a", 70)
	got := withSuffix(long, 1)
	if len(got) > 60 {
		t.Errorf("withSuffix should keep result <= 60 chars, got %d", len(got))
	}
}
