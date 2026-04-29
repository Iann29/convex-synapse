package docker

import (
	"crypto/rand"
	"encoding/hex"
)

// RandomHex returns n bytes of crypto-random data hex-encoded (so the result
// is 2n characters long).
func RandomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
