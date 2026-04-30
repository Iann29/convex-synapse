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
	Status         string     `json:"status"`
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
)

const (
	TokenScopeUser       = "user"
	TokenScopeTeam       = "team"
	TokenScopeProject    = "project"
	TokenScopeDeployment = "deployment"
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
