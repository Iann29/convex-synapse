package api

import (
	"context"
	"net/http"
	"strings"

	synapsedns "github.com/Iann29/synapse/internal/dns"
)

// DNSProviderHandler answers GET /v1/internal/dns_provider?domain=<host>.
// The dashboard hits it BEFORE showing the auto-configure CTA so it
// can render the right UI (e.g. "we can do this for you, paste a
// Cloudflare API token" vs "you'll need to add an A record manually
// in Route53"). The endpoint is public + unauthenticated because (a)
// the result is purely informational (no PII, no infrastructure
// detail), and (b) the dashboard probes it from the new-domain form
// which loads before the user has typed anything sensitive.
//
// This is intentionally NOT mounted at /v1/admin/dns_provider — the
// new-domain form is reachable by any project member, not just
// instance admins. /internal/ is the home for "shared lookup tables"
// (cf. /internal/tls_ask, /internal/list_deployments_for_dashboard).
type DNSProviderHandler struct {
	// Lookup is the test seam — production wiring leaves it nil and
	// we delegate to synapsedns.Provider. Tests inject a closure that
	// returns canned NS records so the suite doesn't depend on
	// real-internet resolution.
	Lookup func(ctx context.Context, domain string) (provider string, nameservers []string, err error)
}

type dnsProviderResp struct {
	Provider    string   `json:"provider"`
	Nameservers []string `json:"nameservers"`
}

func (h *DNSProviderHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	domain := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain")))
	if domain == "" {
		writeError(w, http.StatusBadRequest, "missing_domain",
			"domain query param is required")
		return
	}

	lookup := h.Lookup
	if lookup == nil {
		lookup = synapsedns.Provider
	}
	provider, nameservers, err := lookup(r.Context(), domain)
	if err != nil {
		// Surface the lookup failure as 200 with provider="unknown"
		// + an error field, NOT a 5xx — the dashboard renders a
		// "we couldn't tell, fall back to manual" path on this
		// shape, and a non-200 would force it into a generic error
		// banner.
		writeJSON(w, http.StatusOK, map[string]any{
			"provider":    "unknown",
			"nameservers": []string{},
			"error":       err.Error(),
		})
		return
	}
	if nameservers == nil {
		nameservers = []string{}
	}
	writeJSON(w, http.StatusOK, dnsProviderResp{
		Provider:    provider,
		Nameservers: nameservers,
	})
}
