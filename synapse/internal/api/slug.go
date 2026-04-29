package api

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"unicode"
)

// slugify converts a free-form name into a URL-safe slug.
// Empty input yields a random slug — keeps the DB happy when the user
// passes a name with only punctuation.
func slugify(name string) string {
	var b strings.Builder
	b.Grow(len(name))

	prevDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == ' ':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}

	out := strings.Trim(b.String(), "-")
	if out == "" {
		buf := make([]byte, 4)
		_, _ = rand.Read(buf)
		out = "x-" + hex.EncodeToString(buf)
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

// withSuffix returns slug-N for use when slug already exists.
func withSuffix(slug string, n int) string {
	if len(slug) > 56 {
		slug = slug[:56]
	}
	return slug + "-" + itoa(n)
}

// withRandomSuffix returns slug-XXXX where XXXX is 2 bytes of hex-encoded
// randomness. Used as a fallback when the deterministic withSuffix walk
// keeps colliding under heavy concurrency — N writers trying to allocate
// "name", "name-1", "name-2" lock-step into the same next slot, so jittering
// breaks the convoy.
func withRandomSuffix(slug string) string {
	if len(slug) > 55 {
		slug = slug[:55]
	}
	var buf [2]byte
	_, _ = rand.Read(buf[:])
	return slug + "-" + hex.EncodeToString(buf[:])
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
