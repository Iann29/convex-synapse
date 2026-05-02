// Package audit is a small, best-effort writer for the audit_events table.
//
// Design choice: audit logging MUST NOT be a failure mode for the user's
// request. If we can't write the row (transient DB error, network glitch),
// we log via slog and return — callers don't need to handle the error.
// That trade-off is fine because audit events are observability, not a
// transactional guarantee — losing one in 10,000 to a flaky network is
// preferable to surfacing 500s on the user-visible mutation that triggered
// it.
//
// Action names mirror Convex Cloud's vocabulary where it exists (see
// dashboard-management-openapi.json's auditLogActions). For names that have
// no Cloud counterpart we pick a verb that matches the existing pattern
// (camelCase, verb+noun).
package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Action names. Stable strings stored in the DB; do not rename without a
// migration that rewrites historical rows.
const (
	// Auth.
	ActionLogin = "login"

	// Teams.
	ActionCreateTeam       = "createTeam"
	ActionUpdateTeam       = "updateTeam"
	ActionDeleteTeam       = "deleteTeam"
	ActionInviteTeamMember = "inviteTeamMember"
	ActionCancelInvite     = "cancelInvite"
	ActionAcceptInvite     = "acceptInvite"
	ActionUpdateMemberRole = "updateMemberRole"
	ActionRemoveMember     = "removeMember"

	// Project-level RBAC (v1.0+, migration 000008). Project member
	// actions live alongside team-member actions because they share
	// the same audit_events shape; the metadata.scope field
	// distinguishes "team" vs "project".
	ActionAddProjectMember        = "addProjectMember"
	ActionUpdateProjectMemberRole = "updateProjectMemberRole"
	ActionRemoveProjectMember     = "removeProjectMember"

	// Profile (account-scoped, no team).
	ActionUpdateProfileName = "updateProfileName"
	ActionDeleteAccount     = "deleteAccount"

	// Projects.
	ActionCreateProject   = "createProject"
	ActionDeleteProject   = "deleteProject"
	ActionRenameProject   = "renameProject"
	ActionUpdateProject   = "updateProject"
	ActionTransferProject = "transferProject"
	ActionUpdateEnvVars   = "updateProjectEnvVars"

	// Deployments.
	ActionCreateDeployment = "createDeployment"
	ActionDeleteDeployment = "deleteDeployment"
	// Adopted = registered an existing external Convex backend rather than
	// provisioning a new one. Synapse-original; no Cloud equivalent.
	ActionAdoptDeployment = "adoptDeployment"
	// Upgrade = converted an existing single-replica deployment to HA.
	// Synapse-original; emitted at endpoint-enqueue time. The worker
	// emits no separate audit event today — operators trace progress via
	// provisioning_jobs.status.
	ActionUpgradeToHA = "upgradeToHA"

	// Personal access tokens. Cloud has no equivalent (it uses OAuth flows),
	// so these names are Synapse-original; verbs follow the existing
	// "<verb><Noun>" convention.
	ActionCreatePersonalAccessToken = "createPersonalAccessToken"
	ActionDeletePersonalAccessToken = "deletePersonalAccessToken"

	// Deploy keys (per-deployment, named, used by CI). The names mirror
	// Convex Cloud's /api/dashboard/team/<id>/deploy_keys vocabulary.
	ActionCreateDeployKey = "createDeployKey"
	ActionRevokeDeployKey = "revokeDeployKey"
)

// Target type names.
const (
	TargetTeam        = "team"
	TargetProject     = "project"
	TargetDeployment  = "deployment"
	TargetInvite      = "invite"
	TargetAccessToken = "accessToken"
	TargetUser        = "user"
	TargetDeployKey   = "deployKey"
)

// Options collects the optional fields of an audit event. Empty strings are
// treated as "not set" and stored as NULL.
type Options struct {
	TeamID     string         // optional UUID; empty for account-wide events
	ActorID    string         // optional UUID of the user that did the action
	Action     string         // required: e.g. "createProject"
	TargetType string         // optional: "team" | "project" | …
	TargetID   string         // optional UUID
	Metadata   map[string]any // optional, marshalled to JSONB
}

// Record writes a single audit event. Best-effort: any error is logged to
// slog at WARN and swallowed so the caller can ignore the return value.
//
// The signature still returns error for callers who want to assert in tests,
// but production code should treat the return as advisory.
func Record(ctx context.Context, db *pgxpool.Pool, opts Options) error {
	if opts.Action == "" {
		// A row without an action is meaningless — treat as a programmer
		// error but don't crash. Drop on the floor.
		slog.Default().Warn("audit.Record called without Action; dropping",
			"actor_id", opts.ActorID, "team_id", opts.TeamID)
		return nil
	}

	var teamID, actorID, targetType, targetID *string
	if opts.TeamID != "" {
		teamID = &opts.TeamID
	}
	if opts.ActorID != "" {
		actorID = &opts.ActorID
	}
	if opts.TargetType != "" {
		targetType = &opts.TargetType
	}
	if opts.TargetID != "" {
		targetID = &opts.TargetID
	}

	// Marshal metadata to JSON. nil/empty stays NULL so JSONB queries can
	// distinguish "no metadata" from "{}".
	var metadata []byte
	if len(opts.Metadata) > 0 {
		raw, err := json.Marshal(opts.Metadata)
		if err != nil {
			slog.Default().Warn("audit: marshal metadata failed",
				"err", err, "action", opts.Action)
			// Continue without metadata rather than dropping the whole event.
		} else {
			metadata = raw
		}
	}

	_, err := db.Exec(ctx, `
		INSERT INTO audit_events (team_id, actor_id, action, target_type, target_id, metadata)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, teamID, actorID, opts.Action, targetType, targetID, metadata)
	if err != nil {
		slog.Default().Warn("audit: insert failed",
			"err", err,
			"action", opts.Action,
			"team_id", opts.TeamID,
			"actor_id", opts.ActorID)
		return err
	}
	return nil
}
