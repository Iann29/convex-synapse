package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Iann29/synapse/internal/auth"
	"github.com/Iann29/synapse/internal/models"
)

// AccessTokensHandler exposes the personal access token (PAT) endpoints —
// create / list / delete — scoped to the authenticated user. PATs unlock
// CLI / CI usage when JWTs (15-min lifetime) are too short-lived.
//
// Endpoints mirror Convex Cloud's stable v1 spec:
//   POST /v1/create_personal_access_token
//   GET  /v1/list_personal_access_tokens
//   POST /v1/delete_personal_access_token
//
// Why mounted at /v1 (top-level) instead of /v1/me/tokens? These endpoints
// are user-scoped (not team/project-scoped) and the OpenAPI spec puts them
// at the root for parity with the cloud dashboard's existing client code.
type AccessTokensHandler struct {
	DB *pgxpool.Pool
}

// Register installs the three flat endpoints onto the given router. Used by
// router.go inside the /v1 authenticated group.
//
// Why not the usual `Routes() chi.Router` + r.Mount("/", ...) pattern?
// chi's Mount at "/" collides with the existing GET /v1/ index handler;
// registering the verb-suffixed paths directly side-steps that.
func (h *AccessTokensHandler) Register(r chi.Router) {
	r.Post("/create_personal_access_token", h.create)
	r.Get("/list_personal_access_tokens", h.list)
	r.Post("/delete_personal_access_token", h.delete)
}

// accessTokenView is the JSON shape we return to clients. It deliberately
// omits the token hash and the plaintext token so neither leaks via list/get.
type accessTokenView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope"`
	ScopeID    string     `json:"scopeId,omitempty"`
	CreateTime time.Time  `json:"createTime"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// ---------- POST /v1/create_personal_access_token ----------

type createTokenReq struct {
	Name      string     `json:"name"`
	Scope     string     `json:"scope,omitempty"`
	ScopeID   string     `json:"scopeId,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

type createTokenResp struct {
	// Token is the plaintext "syn_*" string. Returned ONCE at creation —
	// clients must persist it themselves; we never recompute it from the
	// database.
	Token       string          `json:"token"`
	AccessToken accessTokenView `json:"accessToken"`
}

func (h *AccessTokensHandler) create(w http.ResponseWriter, r *http.Request) {
	uid, err := auth.UserID(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Not authenticated")
		return
	}
	var req createTokenReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "missing_name", "Token name is required")
		return
	}
	if len(req.Name) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_name", "Token name is too long (max 100 chars)")
		return
	}
	scope := req.Scope
	if scope == "" {
		scope = models.TokenScopeUser
	}
	switch scope {
	case models.TokenScopeUser, models.TokenScopeTeam, models.TokenScopeProject, models.TokenScopeDeployment:
		// ok
	default:
		writeError(w, http.StatusBadRequest, "invalid_scope", "Scope must be one of: user, team, project, deployment")
		return
	}
	// Non-user scopes need a target id.
	if scope != models.TokenScopeUser && req.ScopeID == "" {
		writeError(w, http.StatusBadRequest, "missing_scope_id", "scopeId is required when scope is not 'user'")
		return
	}
	if scope == models.TokenScopeUser && req.ScopeID != "" {
		// Defensive: a user-scoped token doesn't carry a target id; clear it
		// so we don't store a stray pointer at a team/project that might be
		// deleted later.
		req.ScopeID = ""
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		writeError(w, http.StatusBadRequest, "invalid_expires_at", "expiresAt must be in the future")
		return
	}

	plain, hash, err := auth.GenerateToken()
	if err != nil {
		logErr("generate token", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to generate token")
		return
	}

	var view accessTokenView
	var scopeID *string
	if req.ScopeID != "" {
		scopeID = &req.ScopeID
	}
	var dbScopeID *string
	err = h.DB.QueryRow(r.Context(), `
		INSERT INTO access_tokens (user_id, name, token_hash, scope, scope_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, name, scope, scope_id, created_at, expires_at, last_used_at
	`, uid, req.Name, hash, scope, scopeID, req.ExpiresAt).Scan(
		&view.ID, &view.Name, &view.Scope, &dbScopeID, &view.CreateTime, &view.ExpiresAt, &view.LastUsedAt,
	)
	if err != nil {
		logErr("insert access token", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to create token")
		return
	}
	if dbScopeID != nil {
		view.ScopeID = *dbScopeID
	}

	writeJSON(w, http.StatusCreated, createTokenResp{
		Token:       plain,
		AccessToken: view,
	})
}

// ---------- GET /v1/list_personal_access_tokens ----------

type listTokensResp struct {
	Items      []accessTokenView `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

func (h *AccessTokensHandler) list(w http.ResponseWriter, r *http.Request) {
	uid, err := auth.UserID(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Not authenticated")
		return
	}

	// Pagination: ordered by (created_at DESC, id DESC) for stable cursoring.
	// The cursor is the last seen id from the previous page; we then ask for
	// rows whose (created_at, id) is strictly less than the cursor row's
	// values. A simpler approach (offset) would drift if rows are inserted
	// during paging, so we use keyset paging instead.
	limit := 50
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
			return
		}
		if n > 200 {
			n = 200
		}
		limit = n
	}

	var rows pgx.Rows
	cursor := r.URL.Query().Get("cursor")
	if cursor == "" {
		rows, err = h.DB.Query(r.Context(), `
			SELECT id, name, scope, scope_id, created_at, expires_at, last_used_at
			  FROM access_tokens
			 WHERE user_id = $1
			 ORDER BY created_at DESC, id DESC
			 LIMIT $2
		`, uid, limit+1)
	} else {
		// Resolve cursor → (created_at, id) of that row; reject if it doesn't
		// belong to the caller (avoids leaking other users' token timestamps
		// via timing differences).
		var cursorAt time.Time
		err = h.DB.QueryRow(r.Context(),
			`SELECT created_at FROM access_tokens WHERE id = $1 AND user_id = $2`,
			cursor, uid).Scan(&cursorAt)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "Cursor does not refer to a token you own")
			return
		}
		if err != nil {
			logErr("resolve cursor", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to resolve cursor")
			return
		}
		rows, err = h.DB.Query(r.Context(), `
			SELECT id, name, scope, scope_id, created_at, expires_at, last_used_at
			  FROM access_tokens
			 WHERE user_id = $1
			   AND (created_at, id) < ($2, $3)
			 ORDER BY created_at DESC, id DESC
			 LIMIT $4
		`, uid, cursorAt, cursor, limit+1)
	}
	if err != nil {
		logErr("list access tokens", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list tokens")
		return
	}
	defer rows.Close()

	items := make([]accessTokenView, 0, limit)
	for rows.Next() {
		var v accessTokenView
		var scopeID *string
		if err := rows.Scan(&v.ID, &v.Name, &v.Scope, &scopeID, &v.CreateTime, &v.ExpiresAt, &v.LastUsedAt); err != nil {
			logErr("scan access token", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to read tokens")
			return
		}
		if scopeID != nil {
			v.ScopeID = *scopeID
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		logErr("iterate access tokens", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to read tokens")
		return
	}

	// Trim the sentinel row used to detect "more pages exist".
	resp := listTokensResp{Items: items}
	if len(items) > limit {
		resp.Items = items[:limit]
		resp.NextCursor = items[limit-1].ID
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---------- POST /v1/delete_personal_access_token ----------

type deleteTokenReq struct {
	ID string `json:"id"`
}

func (h *AccessTokensHandler) delete(w http.ResponseWriter, r *http.Request) {
	uid, err := auth.UserID(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Not authenticated")
		return
	}
	var req deleteTokenReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "Token id is required")
		return
	}

	// Single-statement delete with user_id in the WHERE clause prevents one
	// user from nuking another user's tokens by guessing UUIDs. RowsAffected
	// is the source of truth for "did we delete anything?".
	tag, err := h.DB.Exec(r.Context(),
		`DELETE FROM access_tokens WHERE id = $1 AND user_id = $2`,
		req.ID, uid)
	if err != nil {
		logErr("delete access token", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to delete token")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "token_not_found", "Token not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": req.ID})
}
