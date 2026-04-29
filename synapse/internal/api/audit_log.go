package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Iann29/synapse/internal/models"
)

// listAuditLog backs GET /v1/teams/{teamRef}/audit_log.
//
// Admin-only — audit data exposes who-did-what and is privileged. Members
// don't get partial visibility (we considered "show me my own events" but
// admins are the trust anchor for compliance use-cases; mixing roles invites
// confusion). 403 for both non-members AND non-admin members.
//
// Pagination uses keyset on (created_at DESC, id DESC) to keep page boundaries
// stable even when new events stream in. Default limit 50, max 200, mirroring
// /v1/list_personal_access_tokens.
func (h *TeamsHandler) listAuditLog(w http.ResponseWriter, r *http.Request) {
	t, role, ok := h.loadTeamForRequest(w, r)
	if !ok {
		return
	}
	if role != models.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "Only team admins can read the audit log")
		return
	}

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

	cursor := r.URL.Query().Get("cursor")
	var rows pgx.Rows
	var err error
	if cursor == "" {
		rows, err = h.DB.Query(r.Context(), `
			SELECT e.id, e.created_at, e.action, e.actor_id, u.email,
			       e.target_type, e.target_id, e.metadata
			  FROM audit_events e
			  LEFT JOIN users u ON u.id = e.actor_id
			 WHERE e.team_id = $1
			 ORDER BY e.created_at DESC, e.id DESC
			 LIMIT $2
		`, t.ID, limit+1)
	} else {
		// Resolve cursor → (created_at, id) of that row; reject if it's not
		// a valid event for this team. Pattern mirrors the access-tokens list.
		cursorID, parseErr := strconv.ParseInt(cursor, 10, 64)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "Cursor is not a valid event id")
			return
		}
		var cursorAt time.Time
		err = h.DB.QueryRow(r.Context(),
			`SELECT created_at FROM audit_events WHERE id = $1 AND team_id = $2`,
			cursorID, t.ID).Scan(&cursorAt)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "Cursor does not refer to an event in this team")
			return
		}
		if err != nil {
			logErr("resolve audit cursor", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to resolve cursor")
			return
		}
		rows, err = h.DB.Query(r.Context(), `
			SELECT e.id, e.created_at, e.action, e.actor_id, u.email,
			       e.target_type, e.target_id, e.metadata
			  FROM audit_events e
			  LEFT JOIN users u ON u.id = e.actor_id
			 WHERE e.team_id = $1
			   AND (e.created_at, e.id) < ($2, $3)
			 ORDER BY e.created_at DESC, e.id DESC
			 LIMIT $4
		`, t.ID, cursorAt, cursorID, limit+1)
	}
	if err != nil {
		logErr("list audit events", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to list audit events")
		return
	}
	defer rows.Close()

	type item struct {
		ID         string         `json:"id"`
		CreateTime time.Time      `json:"createTime"`
		Action     string         `json:"action"`
		ActorID    string         `json:"actorId,omitempty"`
		ActorEmail string         `json:"actorEmail,omitempty"`
		TargetType string         `json:"targetType,omitempty"`
		TargetID   string         `json:"targetId,omitempty"`
		Metadata   map[string]any `json:"metadata,omitempty"`
	}

	out := make([]item, 0, limit)
	for rows.Next() {
		var (
			id              int64
			createTime      time.Time
			action          string
			actorID         *string
			actorEmail      *string
			targetType      *string
			targetID        *string
			metadataRaw     []byte
		)
		if err := rows.Scan(&id, &createTime, &action, &actorID, &actorEmail,
			&targetType, &targetID, &metadataRaw); err != nil {
			logErr("scan audit event", err)
			writeError(w, http.StatusInternalServerError, "internal", "Failed to read audit events")
			return
		}
		it := item{
			ID:         strconv.FormatInt(id, 10),
			CreateTime: createTime,
			Action:     action,
		}
		if actorID != nil {
			it.ActorID = *actorID
		}
		if actorEmail != nil {
			it.ActorEmail = *actorEmail
		}
		if targetType != nil {
			it.TargetType = *targetType
		}
		if targetID != nil {
			it.TargetID = *targetID
		}
		if len(metadataRaw) > 0 {
			// Decode lazily — if the JSONB blob is malformed (shouldn't happen,
			// but defensively), drop it rather than 500.
			if err := json.Unmarshal(metadataRaw, &it.Metadata); err != nil {
				logErr("decode audit metadata", err)
				it.Metadata = nil
			}
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		logErr("iterate audit events", err)
		writeError(w, http.StatusInternalServerError, "internal", "Failed to read audit events")
		return
	}

	type resp struct {
		Items      []item `json:"items"`
		NextCursor string `json:"nextCursor,omitempty"`
	}
	r2 := resp{Items: out}
	if len(out) > limit {
		r2.Items = out[:limit]
		r2.NextCursor = out[limit-1].ID
	}
	writeJSON(w, http.StatusOK, r2)
}
