package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWTIssuer signs and verifies HS256 JWTs used for dashboard sessions.
// Personal access tokens are a separate concept — they are random bytes
// stored hashed in the DB, not JWTs.
type JWTIssuer struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

func NewJWTIssuer(secret []byte, accessTTL, refreshTTL time.Duration) *JWTIssuer {
	return &JWTIssuer{secret: secret, accessTTL: accessTTL, refreshTTL: refreshTTL}
}

type Claims struct {
	UserID string `json:"sub"`
	Email  string `json:"email,omitempty"`
	// "access" or "refresh".
	Kind string `json:"kind"`
	jwt.RegisteredClaims
}

func (j *JWTIssuer) issue(userID, email, kind string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID,
		Email:  email,
		Kind:   kind,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "synapse",
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString(j.secret)
}

func (j *JWTIssuer) IssueAccess(userID, email string) (string, error) {
	return j.issue(userID, email, "access", j.accessTTL)
}

func (j *JWTIssuer) IssueRefresh(userID, email string) (string, error) {
	return j.issue(userID, email, "refresh", j.refreshTTL)
}

func (j *JWTIssuer) Verify(token string) (*Claims, error) {
	parsed, err := jwt.ParseWithClaims(token, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return j.secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

// AccessTTL exposes the configured access-token lifetime so callers can
// surface it in /v1/auth/login responses.
func (j *JWTIssuer) AccessTTL() time.Duration  { return j.accessTTL }
func (j *JWTIssuer) RefreshTTL() time.Duration { return j.refreshTTL }
