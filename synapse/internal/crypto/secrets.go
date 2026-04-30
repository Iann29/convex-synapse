// Package crypto handles encryption-at-rest for deployment storage
// secrets — Postgres connection strings, S3 access/secret keys, and
// anything else we persist that lets a different process talk to a
// deployment's underlying infrastructure.
//
// The threat model is "operator's metadata DB gets exfiltrated." The
// AES-GCM envelope means a stolen `deployment_storage` table without
// `SYNAPSE_STORAGE_KEY` does not yield usable Postgres / S3 credentials.
// Both must leak together for the secrets to come out.
//
// We intentionally do NOT support per-tenant keys, key rotation, or KMS
// integration in v0.5. The single-tenant operator pattern is the target;
// rotation will be a re-encrypt script wrapped around future versions.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// SecretBox encrypts and decrypts opaque byte strings using AES-256-GCM.
// One instance per process, constructed from SYNAPSE_STORAGE_KEY.
//
// Ciphertext layout: nonce (12 bytes) || sealed.
// AES-GCM authenticates everything, so there's no "ciphertext + MAC"
// split — the trailing 16 bytes of `sealed` are the GCM tag.
type SecretBox struct {
	gcm cipher.AEAD
}

// New returns a SecretBox for the given 32-byte key. Use NewFromEnv in
// production wiring; this constructor exists for tests and key-rotation
// helpers.
func New(key []byte) (*SecretBox, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("crypto: key must be 32 bytes (AES-256), got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: aes.NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: cipher.NewGCM: %w", err)
	}
	return &SecretBox{gcm: gcm}, nil
}

// EnvKey is the env var that holds the operator's storage key. The value
// is interpreted as a hex-encoded 32-byte (256-bit) AES key — 64 hex
// characters. We pick hex over base64 to avoid character-encoding
// confusion in shell + .env files (no `+`/`/`/`=` to escape).
const EnvKey = "SYNAPSE_STORAGE_KEY"

// NewFromEnv reads SYNAPSE_STORAGE_KEY (hex-encoded) and returns a
// SecretBox. Returns ErrKeyMissing if the var is unset and ErrKeyMalformed
// if it's the wrong length / not hex.
func NewFromEnv() (*SecretBox, error) {
	v := os.Getenv(EnvKey)
	if v == "" {
		return nil, ErrKeyMissing
	}
	key, err := hex.DecodeString(v)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKeyMalformed, err)
	}
	return New(key)
}

// ErrKeyMissing means SYNAPSE_STORAGE_KEY wasn't set. Callers turn this
// into "HA is disabled in this Synapse process" — the v0.5 feature
// requires a key, the rest of Synapse runs without one.
var ErrKeyMissing = errors.New("crypto: " + EnvKey + " is not set")

// ErrKeyMalformed means the env var was set but didn't decode to 32 bytes.
var ErrKeyMalformed = errors.New("crypto: " + EnvKey + " must be 32 bytes hex-encoded (64 chars)")

// Encrypt seals plaintext with a fresh random nonce and returns
// nonce||ciphertext||tag as a single byte slice. Output is non-
// deterministic on purpose: encrypting the same plaintext twice yields
// different ciphertexts, so an attacker scanning the deployment_storage
// table can't tell which deployments share, e.g., the same Postgres URL.
func (b *SecretBox) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: read nonce: %w", err)
	}
	// gcm.Seal appends the ciphertext to nonce in-place, so the result is
	// nonce || ciphertext || tag with no separator handling required.
	return b.gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// EncryptString is a convenience for the common case of encrypting a
// connection string / API key.
func (b *SecretBox) EncryptString(s string) ([]byte, error) {
	return b.Encrypt([]byte(s))
}

// Decrypt reverses Encrypt. Returns ErrCiphertextTooShort if the input
// is smaller than the nonce, and surfaces gcm's auth-failure error if
// the ciphertext was tampered with or the key is wrong.
func (b *SecretBox) Decrypt(ciphertext []byte) ([]byte, error) {
	ns := b.gcm.NonceSize()
	if len(ciphertext) < ns {
		return nil, ErrCiphertextTooShort
	}
	nonce, sealed := ciphertext[:ns], ciphertext[ns:]
	plaintext, err := b.gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: open: %w", err)
	}
	return plaintext, nil
}

// DecryptString is a convenience for the common case of decrypting a
// string-typed secret.
func (b *SecretBox) DecryptString(ciphertext []byte) (string, error) {
	pt, err := b.Decrypt(ciphertext)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// ErrCiphertextTooShort is returned when Decrypt is given fewer bytes
// than a GCM nonce (12). Indicates corruption or that the row was
// written by something other than this package.
var ErrCiphertextTooShort = errors.New("crypto: ciphertext shorter than nonce")
