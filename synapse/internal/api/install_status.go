package api

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// InstallStatusHandler exposes a public, unauthenticated probe the
// dashboard hits on first paint to decide whether to redirect /login
// → /setup (the v0.6.3 first-run wizard). Returns:
//
//	{ "firstRun": <bool>, "version": "<INSTALLER_VERSION>" }
//
// `firstRun` is true iff the users table is empty — which means the
// operator has just bootstrapped Synapse and hasn't created an admin
// yet. The dashboard then walks them through admin-create + (optional
// HA toggle) + a demo deployment without ever showing a config file.
//
// Why a dedicated endpoint and not a `users.count > 0` query inline
// in the dashboard: the dashboard page renders pre-auth, before any
// JWT or PAT exists. /v1/me 401s, /v1/teams 401s — there's no other
// public surface that could carry this signal. Keep it narrow: read
// only, no side effects, no auth.
type InstallStatusHandler struct {
	DB      *pgxpool.Pool
	Version string
}

type installStatusResponse struct {
	FirstRun bool   `json:"firstRun"`
	Version  string `json:"version"`
}

func (h *InstallStatusHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resp := installStatusResponse{Version: h.Version}

	if h.DB != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		var anyUser bool
		// EXISTS short-circuits at the first row, so this stays cheap
		// even after the wizard has run and there are millions of users.
		err := h.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&anyUser)
		if err != nil {
			// Fail closed — pretending we're already-installed when the
			// DB is unreachable is safer than directing the operator
			// into the wizard and having it 500 mid-flow.
			writeError(w, http.StatusServiceUnavailable, "db_unavailable",
				"could not query users table")
			return
		}
		resp.FirstRun = !anyUser
	} else {
		// Tests that wire a nil DB get firstRun=true so the wizard
		// can be exercised without a real postgres.
		resp.FirstRun = true
	}

	writeJSON(w, http.StatusOK, resp)
}
