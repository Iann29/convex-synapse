package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// TokenPrefix is what every Synapse-issued personal access token starts with
// — analogous to how GitHub tokens start with "ghp_". Helps secret-scanners
// catch leaks and helps users identify what a token is for.
const TokenPrefix = "syn_"

// GenerateToken returns a fresh random token (with prefix) and the hash that
// should be stored in the database. The plain token is returned to the caller
// only once — at issuance.
func GenerateToken() (plain, hash string, err error) {
	buf := make([]byte, 32) // 256 bits of entropy
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	plain = TokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	hash = HashToken(plain)
	return plain, hash, nil
}

// HashToken returns the SHA-256 hex digest of a plain token. We use SHA-256
// (not bcrypt) because tokens are uniformly random with 256 bits of entropy —
// brute-forcing the hash is impossible regardless of the function's speed,
// and SHA-256 lets us look the token up by hash via a unique-index probe.
func HashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}
