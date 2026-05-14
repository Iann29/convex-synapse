// Package deploymenturl computes the externally-reachable URL of a Convex
// deployment under different Synapse routing modes (path proxy, base
// domain wildcard, per-deployment custom domain).
//
// Two helpers, two consumers:
//
//   - Public(d, activeAPIDomain) — what the dashboard renders and the
//     operator copies. May be a path-prefixed URL ("<host>/d/<name>").
//   - CLI(d, activeAPIDomain)    — what `npx convex` and the backend's
//     CONVEX_CLOUD_ORIGIN need. Always host-anchored (no path prefix)
//     because the official CLI builds requests via
//     `new URL("/api/...", baseUrl)`, which drops any path.
//
// Both helpers are infallible — they return d.DeploymentURL as a last
// resort rather than 500 the caller. The api handlers used to duplicate
// this logic; lifting it into a small package lets the provisioner
// worker bake the right CONVEX_CLOUD_ORIGIN into the container at
// create-time (v1.6.15) instead of the legacy `http://127.0.0.1:<port>`
// which leaked into function-spec.url and CONVEX_SITE_URL from the CLI.
package deploymenturl

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Iann29/synapse/internal/models"
)

// Computer carries the cluster-wide config (Caddy mode, base domain) so
// callers can compute URLs without re-reading config on every call.
type Computer struct {
	// PublicURL is the URL the Synapse API server is reachable at
	// (e.g. "https://synapsepanel.com"). Empty disables both proxy and
	// host:port URL shapes; callers fall back to d.DeploymentURL.
	PublicURL string

	// ProxyEnabled selects the path-based proxy ("<PublicURL>/d/<name>")
	// over the legacy host:port form. Custom domains and BaseDomain
	// both bypass this flag.
	ProxyEnabled bool

	// BaseDomain (v1.0+) is the wildcard subdomain root for per-
	// deployment URLs ("<name>.<BaseDomain>"). When set, BaseDomain
	// wins over PublicURL+ProxyEnabled for both Public and CLI helpers.
	BaseDomain string
}

// Public returns the URL a remote browser (dashboard, operator's laptop)
// should hit. May include a path prefix ("/d/<name>") under the path-
// proxy mode; the CLI form (see CLI) never does.
//
// Decision matrix (first match wins):
//
//	d.Adopted                              → d.DeploymentURL
//	activeAPIDomain != ""                  → "https://<activeAPIDomain>"
//	BaseDomain != ""                       → "https://<name>.<BaseDomain>"
//	PublicURL == ""                        → d.DeploymentURL
//	PublicURL set, ProxyEnabled            → "<PublicURL>/d/<name>"
//	PublicURL set, !ProxyEnabled, HostPort → "<PublicURL>:<HostPort>"
//	everything else                        → d.DeploymentURL
func (c Computer) Public(d *models.Deployment, activeAPIDomain string) string {
	if d == nil {
		return ""
	}
	if d.Adopted {
		return d.DeploymentURL
	}
	if activeAPIDomain != "" {
		return "https://" + activeAPIDomain
	}
	if c.BaseDomain != "" {
		return "https://" + d.Name + "." + c.BaseDomain
	}
	if c.PublicURL == "" {
		return d.DeploymentURL
	}
	if c.ProxyEnabled {
		return c.PublicURL + "/d/" + d.Name
	}
	if d.HostPort == 0 {
		return d.DeploymentURL
	}
	return fmt.Sprintf("%s:%d", c.PublicURL, d.HostPort)
}

// CLI returns a host-anchored URL the `npx convex` CLI can hit
// directly. Always falls back to host:port when no custom domain and no
// BaseDomain are configured — never returns a path-prefixed URL because
// the CLI's URL builder drops the path component (it does
// `new URL("/api/...", baseUrl)`).
//
// Decision matrix (first match wins):
//
//	d.Adopted                                → d.DeploymentURL
//	activeAPIDomain != ""                    → "https://<activeAPIDomain>"
//	BaseDomain != ""                         → "https://<name>.<BaseDomain>"
//	PublicURL == "" || HostPort == 0         → d.DeploymentURL
//	otherwise                                → "<PublicURL_scheme>://<PublicURL_host>:<HostPort>"
func (c Computer) CLI(d *models.Deployment, activeAPIDomain string) string {
	if d == nil {
		return ""
	}
	if d.Adopted {
		return d.DeploymentURL
	}
	if activeAPIDomain != "" {
		return "https://" + activeAPIDomain
	}
	if c.BaseDomain != "" {
		return "https://" + d.Name + "." + c.BaseDomain
	}
	if c.PublicURL == "" || d.HostPort == 0 {
		return d.DeploymentURL
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Hostname() == "" {
		return d.DeploymentURL
	}
	return fmt.Sprintf("%s://%s:%d", u.Scheme, u.Hostname(), d.HostPort)
}

// LookupActiveAPIDomain returns the first active deployment_domains row
// for this deployment with role='api', or "" when none is configured or
// the lookup hits a transient blip. Returning "" on error is deliberate:
// callers feed the value into URL helpers that must stay infallible.
//
// The UNIQUE(domain) constraint means at most one row matches, but the
// ORDER BY keeps the choice deterministic if a future schema change
// ever drops uniqueness.
func LookupActiveAPIDomain(ctx context.Context, db *pgxpool.Pool, deploymentID string) string {
	if db == nil || deploymentID == "" {
		return ""
	}
	var domain string
	err := db.QueryRow(ctx, `
		SELECT domain
		  FROM deployment_domains
		 WHERE deployment_id = $1
		   AND role = $2
		   AND status = $3
		 ORDER BY created_at ASC
		 LIMIT 1
	`, deploymentID, models.DomainRoleAPI, models.DomainStatusActive).Scan(&domain)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		// Swallow — callers can't act on the error and must fall
		// back to the legacy decision tree.
		return ""
	}
	return domain
}
