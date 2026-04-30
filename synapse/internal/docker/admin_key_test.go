package docker

import "testing"

func TestExtractAdminKey(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Admin key:\nfoo|abcd1234\n", "foo|abcd1234"},
		{"Admin key:\n  foo|abcd1234  \n", "foo|abcd1234"},
		{"foo|abcd1234", "foo|abcd1234"},
		{"prod:foo-bar-1234|016abc...", "prod:foo-bar-1234|016abc..."},
		{"", ""},
		{"Admin key:\n\n", ""},
		{"unrelated\nlines\n", ""},
	}
	for _, tc := range cases {
		got := extractAdminKey(tc.in)
		if got != tc.want {
			t.Errorf("extractAdminKey(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
