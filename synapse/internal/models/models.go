// Package models holds the domain types persisted by Synapse.
//
// Field names match the OpenAPI v1 platform schema where applicable so that
// JSON marshalling produces wire-compatible responses; Go-side fields use
// idiomatic naming and conversions happen in the api/ layer when needed.
package models

import "time"

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"createTime"`
	UpdatedAt    time.Time `json:"updateTime"`
}

type Team struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Slug          string    `json:"slug"`
	CreatorUserID string    `json:"creator"`
	DefaultRegion string    `json:"defaultRegion"`
	Suspended     bool      `json:"suspended"`
	CreatedAt     time.Time `json:"createTime"`
}

type TeamMember struct {
	TeamID    string    `json:"teamId"`
	UserID    string    `json:"id"`
	Role      string    `json:"role"`
	Email     string    `json:"email,omitempty"`
	Name      string    `json:"name,omitempty"`
	CreatedAt time.Time `json:"createTime"`
}

type Project struct {
	ID         string    `json:"id"`
	TeamID     string    `json:"teamId"`
	TeamSlug   string    `json:"teamSlug,omitempty"`
	Name       string    `json:"name"`
	Slug       string    `json:"slug"`
	IsDemo     bool      `json:"isDemo"`
	CreatedAt  time.Time `json:"createTime"`
}

type ProjectEnvVar struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"projectId"`
	Name            string    `json:"name"`
	Value           string    `json:"value"`
	DeploymentTypes []string  `json:"deploymentTypes"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

const (
	DeploymentTypeDev     = "dev"
	DeploymentTypeProd    = "prod"
	DeploymentTypePreview = "preview"
	DeploymentTypeCustom  = "custom"

	DeploymentStatusProvisioning = "provisioning"
	DeploymentStatusRunning      = "running"
	DeploymentStatusStopped      = "stopped"
	DeploymentStatusFailed       = "failed"
	DeploymentStatusDeleted      = "deleted"

	// Kind selects the runtime backing the deployment. "convex" provisions
	// the upstream Convex backend container (every deployment before v1.1).
	// "aster" registers a placeholder for an Aster runner cell — Synapse
	// owns the metadata + RBAC, but no container is provisioned and no
	// proxy is wired yet (the Aster image is not released).
	DeploymentKindConvex = "convex"
	DeploymentKindAster  = "aster"
)

// Deployment is the metadata Synapse persists for a provisioned Convex backend.
// Fields like AdminKey/InstanceSecret never leave the server unless explicitly
// requested via a privileged endpoint (e.g. dashboard auth).
//
// Optional timestamps use *time.Time so JSON marshalling emits null instead
// of the Go zero value ("0001-01-01T00:00:00Z").
type Deployment struct {
	ID             string     `json:"id"`
	ProjectID      string     `json:"projectId"`
	Name           string     `json:"name"`
	DeploymentType string     `json:"deploymentType"`
	// Kind is "convex" (default) or "aster". See the DeploymentKind*
	// constants. Always emitted so dashboards/CLIs can branch UI without
	// a second round-trip.
	Kind   string `json:"kind"`
	Status string `json:"status"`
	ContainerID    string     `json:"-"`
	HostPort       int        `json:"-"`
	DeploymentURL  string     `json:"deploymentUrl,omitempty"`
	AdminKey       string     `json:"-"`
	InstanceSecret string     `json:"-"`
	IsDefault      bool       `json:"isDefault"`
	Reference      string     `json:"reference,omitempty"`
	CreatorUserID  string     `json:"creator,omitempty"`
	// Adopted deployments are external backends registered into Synapse
	// (rather than provisioned by it). Lifecycle hooks like delete skip
	// Docker calls for these rows; the operator manages the container.
	Adopted bool `json:"adopted,omitempty"`
	// HA flags (v0.5+). HAEnabled=false + ReplicaCount=1 is the default
	// and matches every deployment that existed before v0.5. The fields
	// are exposed in API responses so dashboard / CLI tooling can render
	// "ha (2 replicas)" badges without a second round-trip.
	HAEnabled    bool       `json:"haEnabled,omitempty"`
	ReplicaCount int        `json:"replicaCount,omitempty"`
	CreatedAt    time.Time  `json:"createTime"`
	LastDeployAt *time.Time `json:"lastDeployTime,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
}

// DeploymentReplicaStatus enumerates the per-replica lifecycle states.
// Distinct from the deployment-level status: a deployment is
// `status=running` as long as at least one replica is `running`; only
// promotes to `failed` when all replicas are stopped/failed.
const (
	ReplicaStatusProvisioning = "provisioning"
	ReplicaStatusRunning      = "running"
	ReplicaStatusStopped      = "stopped"
	ReplicaStatusFailed       = "failed"
)

// DeploymentReplica is one running container backing a deployment. A
// single-replica deployment has exactly one of these (replica_index=0)
// mirroring its host_port + container_id; HA-enabled deployments have N.
type DeploymentReplica struct {
	ID               string     `json:"id"`
	DeploymentID     string     `json:"deploymentId"`
	ReplicaIndex     int        `json:"replicaIndex"`
	ContainerID      string     `json:"-"`
	HostPort         int        `json:"-"`
	Status           string     `json:"status"`
	LastSeenActiveAt *time.Time `json:"lastSeenActiveAt,omitempty"`
	CreatedAt        time.Time  `json:"createTime"`
}

// DeploymentStorage describes the (optional) Postgres + S3 backing for a
// deployment. SQLite + local-volume deployments have no row in
// `deployment_storage` and read no fields here.
//
// Encrypted columns (DBURL, S3AccessKey, S3SecretKey) are AES-GCM
// ciphertexts produced by internal/crypto/secrets.go. The plaintext is
// only handed to a freshly-spawned container as an env var; never
// returned over the API, never logged.
type DeploymentStorage struct {
	DeploymentID      string
	DBKind            string // "postgres" today; "mysql"/"cockroach" later
	DBURLEnc          []byte
	DBSchema          string
	S3Endpoint        string
	S3Region          string
	S3AccessKeyEnc    []byte
	S3SecretKeyEnc    []byte
	S3BucketFiles     string
	S3BucketModules   string
	S3BucketSearch    string
	S3BucketExports   string
	S3BucketSnapshots string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

const (
	RoleAdmin  = "admin"
	RoleMember = "member"
	// RoleViewer is project-scoped only — there is no team-level
	// viewer. A `project_members` row with role="viewer" downgrades a
	// team admin/member to read-only access on that one project.
	// v1.0+ via migration 000008.
	RoleViewer = "viewer"
)

// ProjectMember is a (project_id, user_id, role) override on top of
// team_members. When a row exists for (project, user), its role wins
// over the user's team-level role for that project. See migration
// 000008 for the rationale + resolution rules.
type ProjectMember struct {
	ProjectID string    `json:"projectId"`
	UserID    string    `json:"id"`
	Role      string    `json:"role"`
	Email     string    `json:"email,omitempty"`
	Name      string    `json:"name,omitempty"`
	CreatedAt time.Time `json:"createTime"`
	// Source records where the effective role came from. "project" =
	// row in project_members; "team" = fell through to team_members.
	// Useful for the dashboard members panel: "team admin (project
	// viewer)" lets operators reason about overrides at a glance.
	Source string `json:"source,omitempty"`
}

// DeployKey is a named alias for an admin key on a single deployment,
// used by CI integrations (Vercel, GitHub Actions, etc) so the operator
// gets a clean audit trail per credential. Mirrors Convex Cloud's
// "Personal Deployment Settings → Deploy Keys" UX.
//
// IMPORTANT: revoke is best-effort — the Convex backend authenticates
// admin keys by signature against INSTANCE_SECRET (stateless), so we
// cannot per-key revoke without rotating the deployment's instance
// secret. revoked_at hides the row from the dashboard list; real
// invalidation requires a deployment-wide rotation. The dashboard
// surfaces that gotcha. See migration 000009 for the full design note.
//
// AdminKey is non-empty *only* on the create-response struct (the operator
// gets the value back exactly once, GitHub-PAT-style); subsequent reads
// see only Prefix.
type DeployKey struct {
	ID            string     `json:"id"`
	DeploymentID  string     `json:"deploymentId"`
	Name          string     `json:"name"`
	AdminKey      string     `json:"adminKey,omitempty"`
	Prefix        string     `json:"prefix"`
	CreatedBy     *string    `json:"createdBy,omitempty"`
	CreatedByName string     `json:"createdByName,omitempty"`
	CreatedAt     time.Time  `json:"createTime"`
	LastUsedAt    *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt     *time.Time `json:"revokedAt,omitempty"`
}

const (
	TokenScopeUser       = "user"
	TokenScopeTeam       = "team"
	TokenScopeProject    = "project"
	TokenScopeDeployment = "deployment"
	// TokenScopeApp mirrors Convex Cloud's app_access_tokens family — a
	// short-lived per-project key targeted at CI/CD preview deploys. From
	// Synapse's authorization standpoint it behaves like a project-scoped
	// token; the distinct label exists so the dashboard can categorise it
	// separately ("app tokens" vs "project tokens"). v1.0+ migration 000007.
	TokenScopeApp = "app"
)

type AccessToken struct {
	ID         string     `json:"id"`
	UserID     string     `json:"userId"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope"`
	ScopeID    string     `json:"scopeId,omitempty"`
	CreatedAt  time.Time  `json:"createTime"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}
