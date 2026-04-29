package synapsetest

import (
	"net/http"
	"testing"
	"time"
)

// userResp mirrors models.User's JSON shape for strict decoding in tests.
// Defined here (not exported from the harness) so each test file can extend
// it locally if its endpoint adds fields.
type userResp struct {
	ID         string    `json:"id"`
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	CreateTime time.Time `json:"createTime"`
	UpdateTime time.Time `json:"updateTime"`
}

func TestRegister_HappyPath(t *testing.T) {
	h := Setup(t)
	body := map[string]string{
		"email":    "alice@example.test",
		"password": "supersecret123",
		"name":     "Alice",
	}
	var got registerResp
	h.DoJSON(http.MethodPost, "/v1/auth/register", "", body, http.StatusCreated, &got)

	if got.User.ID == "" {
		t.Errorf("expected user id to be set")
	}
	if got.User.Email != "alice@example.test" {
		t.Errorf("email mismatch: got %q", got.User.Email)
	}
	if got.AccessToken == "" || got.RefreshToken == "" {
		t.Errorf("expected token pair, got %+v", got)
	}
	if got.TokenType != "Bearer" {
		t.Errorf("expected token type Bearer, got %q", got.TokenType)
	}
	if got.ExpiresIn <= 0 {
		t.Errorf("expected positive expiresIn, got %d", got.ExpiresIn)
	}
}

func TestRegister_DuplicateEmail(t *testing.T) {
	h := Setup(t)
	body := map[string]string{
		"email":    "dup@example.test",
		"password": "supersecret123",
		"name":     "Dup",
	}
	h.DoJSON(http.MethodPost, "/v1/auth/register", "", body, http.StatusCreated, &registerResp{})
	env := h.AssertStatus(http.MethodPost, "/v1/auth/register", "", body, http.StatusConflict)
	if env.Code != "email_taken" {
		t.Errorf("expected code email_taken, got %q", env.Code)
	}
}

func TestRegister_Validation(t *testing.T) {
	h := Setup(t)
	cases := []struct {
		name     string
		body     map[string]string
		status   int
		wantCode string
	}{
		{
			name:     "weak password",
			body:     map[string]string{"email": "x@example.test", "password": "short", "name": "X"},
			status:   http.StatusBadRequest,
			wantCode: "weak_password",
		},
		{
			name:     "missing email",
			body:     map[string]string{"email": "", "password": "supersecret123", "name": "X"},
			status:   http.StatusBadRequest,
			wantCode: "invalid_email",
		},
		{
			name:     "malformed email",
			body:     map[string]string{"email": "not-an-email", "password": "supersecret123", "name": "X"},
			status:   http.StatusBadRequest,
			wantCode: "invalid_email",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := h.AssertStatus(http.MethodPost, "/v1/auth/register", "", tc.body, tc.status)
			if env.Code != tc.wantCode {
				t.Errorf("expected code %q, got %q", tc.wantCode, env.Code)
			}
		})
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	h := Setup(t)
	u := h.RegisterUser("login-bad@example.test", "supersecret123", "Login Bad")
	_ = u
	env := h.AssertStatus(http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email":    "login-bad@example.test",
		"password": "wrong-password-1",
	}, http.StatusUnauthorized)
	if env.Code != "invalid_credentials" {
		t.Errorf("expected code invalid_credentials, got %q", env.Code)
	}
}

func TestLogin_UnknownEmail(t *testing.T) {
	h := Setup(t)
	env := h.AssertStatus(http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email":    "nobody-here@example.test",
		"password": "whatever123",
	}, http.StatusUnauthorized)
	if env.Code != "invalid_credentials" {
		t.Errorf("expected code invalid_credentials, got %q", env.Code)
	}
}

func TestLogin_HappyPath(t *testing.T) {
	h := Setup(t)
	h.RegisterUser("login-ok@example.test", "supersecret123", "Login OK")
	var got registerResp
	h.DoJSON(http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email":    "login-ok@example.test",
		"password": "supersecret123",
	}, http.StatusOK, &got)
	if got.AccessToken == "" {
		t.Errorf("expected access token after login")
	}
	if got.User.Email != "login-ok@example.test" {
		t.Errorf("login returned wrong user: %+v", got.User)
	}
}

func TestMe_RequiresBearer(t *testing.T) {
	h := Setup(t)
	env := h.AssertStatus(http.MethodGet, "/v1/me/", "", nil, http.StatusUnauthorized)
	if env.Code != "missing_authorization" {
		t.Errorf("expected code missing_authorization, got %q", env.Code)
	}
}

func TestMe_InvalidBearer(t *testing.T) {
	h := Setup(t)
	env := h.AssertStatus(http.MethodGet, "/v1/me/", "this-is-not-a-jwt", nil, http.StatusUnauthorized)
	if env.Code != "invalid_token" {
		t.Errorf("expected code invalid_token, got %q", env.Code)
	}
}

func TestMe_HappyPath(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	var me userResp
	h.DoJSON(http.MethodGet, "/v1/me/", u.AccessToken, nil, http.StatusOK, &me)
	if me.ID != u.ID || me.Email != u.Email {
		t.Errorf("me mismatch: got %+v want id=%s email=%s", me, u.ID, u.Email)
	}
}

func TestMe_RefreshTokenRejected(t *testing.T) {
	// /v1/me only accepts access tokens; refresh tokens should bounce.
	h := Setup(t)
	u := h.RegisterRandomUser()
	env := h.AssertStatus(http.MethodGet, "/v1/me/", u.RefreshToken, nil, http.StatusUnauthorized)
	if env.Code != "invalid_token" {
		t.Errorf("expected code invalid_token, got %q", env.Code)
	}
}

func TestRefresh_RoundTrip(t *testing.T) {
	h := Setup(t)
	u := h.RegisterRandomUser()
	var got registerResp
	h.DoJSON(http.MethodPost, "/v1/auth/refresh", "", map[string]string{
		"refreshToken": u.RefreshToken,
	}, http.StatusOK, &got)
	if got.AccessToken == "" || got.RefreshToken == "" {
		t.Errorf("expected fresh token pair, got %+v", got)
	}
	// New access token should work against /v1/me.
	var me userResp
	h.DoJSON(http.MethodGet, "/v1/me/", got.AccessToken, nil, http.StatusOK, &me)
	if me.ID != u.ID {
		t.Errorf("refreshed token returns wrong user: %s vs %s", me.ID, u.ID)
	}
}

func TestRefresh_Invalid(t *testing.T) {
	h := Setup(t)
	env := h.AssertStatus(http.MethodPost, "/v1/auth/refresh", "", map[string]string{
		"refreshToken": "garbage",
	}, http.StatusUnauthorized)
	if env.Code != "invalid_refresh" {
		t.Errorf("expected code invalid_refresh, got %q", env.Code)
	}
}

func TestRefresh_AccessTokenRejected(t *testing.T) {
	// Access tokens have kind="access" — handing one to refresh must fail.
	h := Setup(t)
	u := h.RegisterRandomUser()
	env := h.AssertStatus(http.MethodPost, "/v1/auth/refresh", "", map[string]string{
		"refreshToken": u.AccessToken,
	}, http.StatusUnauthorized)
	if env.Code != "invalid_refresh" {
		t.Errorf("expected code invalid_refresh, got %q", env.Code)
	}
}
