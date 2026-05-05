package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TLSAskHandler implements the contract Caddy's `tls { on_demand }`
// expects: a 200 means "yes, issue a Let's Encrypt cert for this host";
// a non-200 means "refuse". Without this gate any rando could trigger
// cert issuance for arbitrary subdomains by sending TLS handshakes —
// Caddy would happily ask Let's Encrypt for `evil.<base>` if we let it.
//
// We answer 200 iff one of:
//
//  A. The host is `<sub>.<BaseDomain>` (case-insensitive) AND `<sub>`
//     is the name of a real, non-deleted deployment (wildcard
//     subdomain mode — needs BaseDomain configured).
//  B. The host exactly matches an active row in `deployment_domains`
//     bound to a real, non-deleted deployment (per-deployment custom
//     domain, v1.1+).
//
// Endpoint: GET /v1/internal/tls_ask?domain=<host>
// Public — Caddy hits it from inside the docker network with no JWT.
// We intentionally don't gate on a shared-secret because that would
// require operator setup we don't want to push (and the underlying
// risk is "an attacker on the synapse-network triggers a cert" which
// is bounded by Let's Encrypt rate limits anyway).
type TLSAskHandler struct {
	DB         *pgxpool.Pool
	BaseDomain string
}

func (h *TLSAskHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSpace(r.URL.Query().Get("domain"))
	if host == "" {
		http.Error(w, "domain query param required", http.StatusBadRequest)
		return
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	// (A) Wildcard subdomain path — only when BaseDomain is set.
	// When the host falls under the base, we do the wildcard check
	// AND short-circuit on its result so an attacker can't make us
	// issue `evil.<base>` certs by registering the same string as a
	// custom domain (the unique constraint on deployment_domains
	// would already block it, but defense-in-depth).
	if h.BaseDomain != "" {
		base := strings.ToLower(strings.Trim(h.BaseDomain, ". "))
		suffix := "." + base
		if strings.HasSuffix(host, suffix) && host != base {
			name := host[:len(host)-len(suffix)]
			// Refuse multi-label subdomains — Synapse only addresses a
			// single label per deployment under the wildcard.
			if strings.Contains(name, ".") {
				http.Error(w, "multi-label subdomain", http.StatusForbidden)
				return
			}
			if name == "" {
				http.Error(w, "empty subdomain", http.StatusBadRequest)
				return
			}
			var exists bool
			err := h.DB.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM deployments
					WHERE name = $1 AND status <> 'deleted'
				)`, name).Scan(&exists)
			if err != nil {
				http.Error(w, "db error", http.StatusServiceUnavailable)
				return
			}
			if !exists {
				http.Error(w, "deployment not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		// Falls through to (B) when host isn't under the base.
	}

	// (B) Custom-domain path — accept any host with an active row in
	// deployment_domains pointing at a non-deleted deployment.
	var domainExists bool
	err := h.DB.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM deployment_domains dd
			  JOIN deployments d ON d.id = dd.deployment_id
			 WHERE dd.domain = $1
			   AND dd.status = 'active'
			   AND d.status <> 'deleted'
		)`, host).Scan(&domainExists)
	if err != nil {
		http.Error(w, "db error", http.StatusServiceUnavailable)
		return
	}
	if domainExists {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Neither path matched. 404 covers "host not under base AND not a
	// custom domain"; the wildcard branch above already covered
	// "under base but the deployment doesn't exist".
	http.Error(w, "domain not registered", http.StatusNotFound)
}
