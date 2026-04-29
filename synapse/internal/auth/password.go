package auth

import "golang.org/x/crypto/bcrypt"

// bcryptCost is intentionally on the higher end. Authentication is rare and
// off the hot path, and a slower hash is the whole point.
const bcryptCost = 12

func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
