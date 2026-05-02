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

	"github.com/Iann29/synapse/internal/audit"
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
	scope := req.Scope
	if scope == "" {
		scope = models.TokenScopeUser
	}
	switch scope {
	case models.TokenScopeUser, models.TokenScopeTeam, models.TokenScopeProject, models.TokenScopeDeployment, models.TokenScopeApp:
		// ok
	default:
		writeError(w, http.StatusBadRequest, "invalid_scope", "Scope must be one of: user, team, project, deployment, app")
		return
	}
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
	view, plain, ok := h.createForOwner(w, r, uid, req.Name, scope, req.ScopeID, req.ExpiresAt)
	if !ok {
		return
	}
	writeJSON(w, http.StatusCreated, createTokenResp{
		Token:       plain,
		AccessToken: view,
	})
}

// createForOwner is the shared insert path for personal AND scoped
// access tokens. Returns the populated view + plaintext token, or ok=false
// after writing the appropriate 4xx/5xx. Reused by the scope-specific
// endpoints under /v1/teams/{ref}/access_tokens etc.
func (h *AccessTokensHandler) createForOwner(
	w http.ResponseWriter, r *http.Request,
	ownerID, name, scope, scopeID string, expiresAt *time.Time,
) (accessTokenView, string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "missing_name", "Token name is required")
		return accessTokenView{}, "", false
	}
	if len(name) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_name", "Token name is too long (max 100 chars)")
		return accessTokenView{}, "", false
	}
	if expiresAt != nil && !expiresAt.After(time.Now()) {
		writeError(w, http.StatusBadRequest, "invalid_expires_at", "expiresAt must be in the future")
		return accessTokenView{}, "", false
	}

	plain, hash, err := auth.GenerateToken()
	if err != nil {
		logErr("generate token", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to generate token")
		return accessTokenView{}, "", false
	}

	var view accessTokenView
	var scopeIDPtr *string
	if scopeID != "" {
		scopeIDPtr = &scopeID
	}
	var dbScopeID *string
	err = h.DB.QueryRow(r.Context(), `
		INSERT INTO access_tokens (user_id, name, token_hash, scope, scope_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, name, scope, scope_id, created_at, expires_at, last_used_at
	`, ownerID, name, hash, scope, scopeIDPtr, expiresAt).Scan(
		&view.ID, &view.Name, &view.Scope, &dbScopeID, &view.CreateTime, &view.ExpiresAt, &view.LastUsedAt,
	)
	if err != nil {
		logErr("insert access token", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to create token")
		return accessTokenView{}, "", false
	}
	if dbScopeID != nil {
		view.ScopeID = *dbScopeID
	}

	// PATs are user-scoped audit-trail-wise — TeamID stays empty so the row
	// shows up in the per-account audit feed, not under any individual team.
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		ActorID:    ownerID,
		Action:     audit.ActionCreatePersonalAccessToken,
		TargetType: audit.TargetAccessToken,
		TargetID:   view.ID,
		Metadata: map[string]any{
			"name":    view.Name,
			"scope":   view.Scope,
			"scopeId": view.ScopeID,
		},
	})
	return view, plain, true
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
	limit, ok := parseTokenListLimit(w, r)
	if !ok {
		return
	}
	resp, ok := h.listForOwner(w, r, uid, "", "", limit, r.URL.Query().Get("cursor"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseTokenListLimit reads the optional `limit` query param. Defaults
// to 50, capped at 200. Validates negative/zero/non-numeric inputs.
func parseTokenListLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	limit := 50
	s := r.URL.Query().Get("limit")
	if s == "" {
		return limit, true
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
		return 0, false
	}
	if n > 200 {
		n = 200
	}
	return n, true
}

// listForOwner is the shared listing path. When scopeFilter is empty
// it returns ALL the user's tokens (the personal-tokens endpoint);
// otherwise it filters to the specific scope+scopeID.
//
// Why filter by user_id even on scope-listings: showing other admins'
// tokens leaks their lifecycle timestamps. Operators who need a
// team-wide audit can hit the audit_log endpoint where token creates/
// deletes already record actor_id + scope.
func (h *AccessTokensHandler) listForOwner(
	w http.ResponseWriter, r *http.Request,
	ownerID, scopeFilter, scopeIDFilter string, limit int, cursor string,
) (listTokensResp, bool) {
	args := []any{ownerID}
	where := "user_id = $1"
	if scopeFilter != "" {
		where += " AND scope = $2 AND scope_id::text = $3"
		args = append(args, scopeFilter, scopeIDFilter)
	}

	var rows pgx.Rows
	var err error
	if cursor == "" {
		rows, err = h.DB.Query(r.Context(),
			`SELECT id, name, scope, scope_id, created_at, expires_at, last_used_at
			   FROM access_tokens
			  WHERE `+where+`
			  ORDER BY created_at DESC, id DESC
			  LIMIT $`+itoa(len(args)+1),
			append(args, limit+1)...,
		)
	} else {
		var cursorAt time.Time
		err = h.DB.QueryRow(r.Context(),
			`SELECT created_at FROM access_tokens WHERE id = $1 AND user_id = $2`,
			cursor, ownerID).Scan(&cursorAt)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "Cursor does not refer to a token you own")
			return listTokensResp{}, false
		}
		if err != nil {
			logErr("resolve cursor", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to resolve cursor")
			return listTokensResp{}, false
		}
		args = append(args, cursorAt, cursor, limit+1)
		rows, err = h.DB.Query(r.Context(),
			`SELECT id, name, scope, scope_id, created_at, expires_at, last_used_at
			   FROM access_tokens
			  WHERE `+where+`
			    AND (created_at, id) < ($`+itoa(len(args)-2)+`, $`+itoa(len(args)-1)+`)
			  ORDER BY created_at DESC, id DESC
			  LIMIT $`+itoa(len(args)),
			args...,
		)
	}
	if err != nil {
		logErr("list access tokens", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list tokens")
		return listTokensResp{}, false
	}
	defer rows.Close()

	items := make([]accessTokenView, 0, limit)
	for rows.Next() {
		var v accessTokenView
		var scopeID *string
		if err := rows.Scan(&v.ID, &v.Name, &v.Scope, &scopeID, &v.CreateTime, &v.ExpiresAt, &v.LastUsedAt); err != nil {
			logErr("scan access token", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to read tokens")
			return listTokensResp{}, false
		}
		if scopeID != nil {
			v.ScopeID = *scopeID
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		logErr("iterate access tokens", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to read tokens")
		return listTokensResp{}, false
	}
	resp := listTokensResp{Items: items}
	if len(items) > limit {
		resp.Items = items[:limit]
		resp.NextCursor = items[limit-1].ID
	}
	return resp, true
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
	_ = audit.Record(r.Context(), h.DB, audit.Options{
		ActorID:    uid,
		Action:     audit.ActionDeletePersonalAccessToken,
		TargetType: audit.TargetAccessToken,
		TargetID:   req.ID,
	})
	writeJSON(w, http.StatusOK, map[string]string{"id": req.ID})
}
