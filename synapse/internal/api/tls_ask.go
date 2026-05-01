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
// We answer 200 iff:
//   1. BaseDomain is configured (custom-domains mode)
//   2. The asked-about host is `<sub>.<BaseDomain>` (case-insensitive)
//   3. <sub> is the name of a real, non-deleted deployment
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
	// Off when custom domains aren't configured. Returning 404 here
	// (vs 200) means Caddy NEVER issues an on-demand cert without
	// the operator opting in — fail-safe.
	if h.BaseDomain == "" {
		http.Error(w, "custom domains not configured", http.StatusNotFound)
		return
	}
	host := strings.TrimSpace(r.URL.Query().Get("domain"))
	if host == "" {
		http.Error(w, "domain query param required", http.StatusBadRequest)
		return
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	base := strings.ToLower(strings.Trim(h.BaseDomain, ". "))
	suffix := "." + base
	if !strings.HasSuffix(host, suffix) || host == base {
		http.Error(w, "domain not under base", http.StatusForbidden)
		return
	}
	name := host[:len(host)-len(suffix)]
	// Refuse multi-label subdomains — Synapse only addresses a single
	// label per deployment. "foo.bar.<base>" without a dedicated
	// deployment row "foo.bar" is suspicious.
	if strings.Contains(name, ".") {
		http.Error(w, "multi-label subdomain", http.StatusForbidden)
		return
	}
	if name == "" {
		http.Error(w, "empty subdomain", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
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
}
