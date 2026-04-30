package crypto

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// freshKey generates a 32-byte key for tests. We never want to ship a
// hard-coded key in test data — even in a test, the smell of "constant
// secret" is bad enough to trip review eyes.
func freshKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return k
}

func TestSecretBox_RoundTrip(t *testing.T) {
	box, err := New(freshKey(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	plaintexts := []string{
		"",
		"postgres://convex:hunter2@db.example:5432/convex_happy_cat?sslmode=require",
		"AKIAIOSFODNN7EXAMPLE",
		strings.Repeat("longish-secret-payload-", 200),
	}
	for _, pt := range plaintexts {
		ct, err := box.EncryptString(pt)
		if err != nil {
			t.Fatalf("EncryptString: %v", err)
		}
		got, err := box.DecryptString(ct)
		if err != nil {
			t.Fatalf("DecryptString: %v", err)
		}
		if got != pt {
			t.Errorf("round-trip mismatch: got %q want %q", got, pt)
		}
	}
}

func TestSecretBox_NonDeterministic(t *testing.T) {
	// Two encrypts of the same plaintext must produce different
	// ciphertexts — this is what hides "deployments sharing a Postgres
	// URL" from a database snapshot. AES-GCM with random nonce gives us
	// this for free; the test exists to catch any future regression
	// where someone "fixes" the nonce to avoid the rand.Reader read.
	box, err := New(freshKey(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a, _ := box.EncryptString("same-plaintext")
	b, _ := box.EncryptString("same-plaintext")
	if bytes.Equal(a, b) {
		t.Fatalf("two encrypts produced identical ciphertext — nonce is not random")
	}
}

func TestSecretBox_TamperingDetected(t *testing.T) {
	box, err := New(freshKey(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct, err := box.EncryptString("hello world")
	if err != nil {
		t.Fatalf("EncryptString: %v", err)
	}
	// Flip one bit in the middle of the ciphertext.
	ct[len(ct)/2] ^= 0x01
	if _, err := box.Decrypt(ct); err == nil {
		t.Fatalf("expected GCM to detect tampered ciphertext, got nil error")
	}
}

func TestSecretBox_WrongKey(t *testing.T) {
	a, err := New(freshKey(t))
	if err != nil {
		t.Fatalf("New a: %v", err)
	}
	b, err := New(freshKey(t))
	if err != nil {
		t.Fatalf("New b: %v", err)
	}
	ct, err := a.EncryptString("hello world")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := b.Decrypt(ct); err == nil {
		t.Fatalf("decrypt with wrong key returned nil; expected auth failure")
	}
}

func TestSecretBox_RejectsBadKeyLength(t *testing.T) {
	for _, n := range []int{0, 1, 16, 24, 31, 33, 64} {
		key := make([]byte, n)
		if _, err := New(key); err == nil {
			t.Errorf("New(%d-byte key) returned nil error; want a length error", n)
		}
	}
}

func TestSecretBox_RejectsTooShortCiphertext(t *testing.T) {
	box, err := New(freshKey(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := box.Decrypt([]byte{1, 2, 3}); !errors.Is(err, ErrCiphertextTooShort) {
		t.Fatalf("Decrypt(short): err=%v, want ErrCiphertextTooShort", err)
	}
}

func TestNewFromEnv(t *testing.T) {
	t.Setenv(EnvKey, "")
	if _, err := NewFromEnv(); !errors.Is(err, ErrKeyMissing) {
		t.Errorf("unset env: got %v, want ErrKeyMissing", err)
	}

	t.Setenv(EnvKey, "not-hex!!")
	if _, err := NewFromEnv(); !errors.Is(err, ErrKeyMalformed) {
		t.Errorf("non-hex env: got %v, want ErrKeyMalformed", err)
	}

	// Hex of the wrong length decodes fine but rejects in New() — surface
	// that as the same malformed error so callers don't have to know
	// AES-256 specifically.
	t.Setenv(EnvKey, hex.EncodeToString(make([]byte, 16)))
	if _, err := NewFromEnv(); err == nil {
		t.Errorf("16-byte hex env: got nil err, want length error")
	}

	// Happy path.
	keyHex := hex.EncodeToString(freshKey(t))
	t.Setenv(EnvKey, keyHex)
	box, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv (valid): %v", err)
	}
	ct, err := box.EncryptString("ok")
	if err != nil {
		t.Fatalf("Encrypt after NewFromEnv: %v", err)
	}
	pt, err := box.DecryptString(ct)
	if err != nil {
		t.Fatalf("Decrypt after NewFromEnv: %v", err)
	}
	if pt != "ok" {
		t.Errorf("round trip via env: got %q want %q", pt, "ok")
	}
}
