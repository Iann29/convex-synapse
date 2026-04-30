package api

import (
	"net/http"
	"strconv"
)

// Pagination defaults for the bounded listing endpoints. The cap exists to
// keep a single GET from materialising an unbounded result set into memory —
// the dashboard never asks for more than a screenful, and `npx convex` doesn't
// list teams/projects via this surface.
const (
	defaultListLimit = 100
	maxListLimit     = 500
)

// parseListLimit reads ?limit from the query string and clamps it to
// [1, maxListLimit]. On invalid input it writes a 400 and returns ok=false.
//
// The contract is shared with parseListCursor below: both must succeed before
// the handler can issue its query. Why not a single parser? The cursor lookup
// is per-endpoint (different table for each list), so we keep cursor parsing
// inline at the call site.
func parseListLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	s := r.URL.Query().Get("limit")
	if s == "" {
		return defaultListLimit, true
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
		return 0, false
	}
	if n > maxListLimit {
		n = maxListLimit
	}
	return n, true
}

// setNextCursor announces the cursor for the next page. Must be called before
// writeJSON — once the body starts streaming, headers can no longer be set.
func setNextCursor(w http.ResponseWriter, cursor string) {
	if cursor == "" {
		return
	}
	w.Header().Set("X-Next-Cursor", cursor)
}
