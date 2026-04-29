package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Iann29/synapse/internal/auth"
	"github.com/Iann29/synapse/internal/models"
)

// AuthHandler exposes registration, login, and session refresh.
type AuthHandler struct {
	DB  *pgxpool.Pool
	JWT *auth.JWTIssuer
}

func (h *AuthHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/register", h.register)
	r.Post("/login", h.login)
	r.Post("/refresh", h.refresh)
	return r
}

type registerReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

type tokenPair struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	TokenType    string `json:"tokenType"`
	ExpiresIn    int    `json:"expiresIn"` // seconds
	User         models.User `json:"user"`
}

func (h *AuthHandler) register(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		writeError(w, http.StatusBadRequest, "invalid_email", "A valid email is required")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "weak_password", "Password must be at least 8 characters")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		logErr("hash password", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to hash password")
		return
	}

	var u models.User
	err = h.DB.QueryRow(r.Context(), `
		INSERT INTO users (email, password_hash, name)
		VALUES ($1, $2, $3)
		RETURNING id, email, name, created_at, updated_at
	`, req.Email, hash, req.Name).Scan(&u.ID, &u.Email, &u.Name, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		// Unique violation on the email index.
		if strings.Contains(err.Error(), "users_email_key") {
			writeError(w, http.StatusConflict, "email_taken", "An account with that email already exists")
			return
		}
		logErr("create user", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to create user")
		return
	}

	h.respondTokenPair(w, http.StatusCreated, u)
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *AuthHandler) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	var u models.User
	err := h.DB.QueryRow(r.Context(), `
		SELECT id, email, name, password_hash, created_at, updated_at
		  FROM users WHERE email = $1
	`, req.Email).Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Email or password is incorrect")
		return
	}
	if err != nil {
		logErr("lookup user", err)
		writeError(w, http.StatusInternalServerError, "internal", "Login failed")
		return
	}

	if !auth.VerifyPassword(u.PasswordHash, req.Password) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Email or password is incorrect")
		return
	}

	h.respondTokenPair(w, http.StatusOK, u)
}

type refreshReq struct {
	RefreshToken string `json:"refreshToken"`
}

func (h *AuthHandler) refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	claims, err := h.JWT.Verify(req.RefreshToken)
	if err != nil || claims.Kind != "refresh" {
		writeError(w, http.StatusUnauthorized, "invalid_refresh", "Refresh token is invalid or expired")
		return
	}

	var u models.User
	err = h.DB.QueryRow(r.Context(), `
		SELECT id, email, name, created_at, updated_at FROM users WHERE id = $1
	`, claims.UserID).Scan(&u.ID, &u.Email, &u.Name, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "user_not_found", "User no longer exists")
		return
	}

	h.respondTokenPair(w, http.StatusOK, u)
}

func (h *AuthHandler) respondTokenPair(w http.ResponseWriter, status int, u models.User) {
	access, err := h.JWT.IssueAccess(u.ID, u.Email)
	if err != nil {
		logErr("issue access", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to issue token")
		return
	}
	refresh, err := h.JWT.IssueRefresh(u.ID, u.Email)
	if err != nil {
		logErr("issue refresh", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to issue token")
		return
	}

	writeJSON(w, status, tokenPair{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		ExpiresIn:    int(h.JWT.AccessTTL() / time.Second),
		User:         u,
	})
}
