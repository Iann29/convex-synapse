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
	CreatedAt      time.Time  `json:"createTime"`
	LastDeployAt   *time.Time `json:"lastDeployTime,omitempty"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
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
