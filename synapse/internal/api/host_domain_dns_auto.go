package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	synapsedns "github.com/Iann29/synapse/internal/dns"
)

// dnsAutoResult is the per-attempt outcome of "use a stored Cloudflare
// credential to upsert an A record before the daemon reconfigure
// runs". Surfaced on the POST /v1/admin/host_domain response so the
// dashboard can render either a green "✓ created A record" line or an
// amber warning explaining why DNS still needs manual attention.
//
// Attempted == false means the operator did not check the box (nothing
// to render). Attempted == true with Success == true means the
// Cloudflare API accepted the upsert; with Success == false, Reason
// carries the human-readable cause.
type dnsAutoResult struct {
	Attempted     bool   `json:"attempted"`
	Success       bool   `json:"success"`
	Provider      string `json:"provider,omitempty"`     // "cloudflare"
	CredentialID  string `json:"credentialId,omitempty"` // matched dns_credentials.id
	Zone          string `json:"zone,omitempty"`         // matched zone name
	RecordName    string `json:"recordName,omitempty"`   // the FQDN we upserted
	IP            string `json:"ip,omitempty"`           // the IPv4 we pointed it at
	IPDetectedVia string `json:"ipDetectedVia,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// attemptHostDomainDNSAuto is the "click checkbox → A record exists"
// path. We try it best-effort BEFORE the daemon reconfigure so the
// daemon's preflight DNS check (and Caddy's ACME issuance once it
// starts) actually find the record live in the wild.
//
// Returns a result describing what we did. Never returns an error to
// the caller — the result.Reason carries failure modes. The caller
// (postHostDomain) logs + continues on failure per the operator-chosen
// "warn-and-proceed" semantic; the reconfigure will surface the real
// downstream error if DNS still doesn't resolve in time.
//
// Steps:
//  1. Resolve the IPv4 we should point the record at. Prefer a fresh
//     external probe (operator may have migrated the VPS); fall back
//     to h.PublicIP from .env if the probe fails.
//  2. Find a dns_credential whose zones cover `domain`. We pick the
//     most-specific match (longest zone name).
//  3. Decrypt the stored token via h.DNSCredentials.Crypto.
//  4. Build a Cloudflare client (uses the test-seam factory if set).
//  5. UpsertARecord — atomic create-or-update against the zone's API.
//  6. Best-effort touch dns_credentials.last_used_at / last_error so
//     the credentials panel reflects health.
func (h *AdminHandler) attemptHostDomainDNSAuto(ctx context.Context, domain string) *dnsAutoResult {
	out := &dnsAutoResult{Attempted: true}

	if h.DNSCredentials == nil || h.DNSCredentials.Crypto == nil {
		out.Reason = "DNS credentials are not configured on this Synapse instance (SYNAPSE_STORAGE_KEY not set)"
		return out
	}
	if domain == "" {
		out.Reason = "domain is empty"
		return out
	}

	ip, source := h.detectPublicIPv4(ctx)
	if ip == "" {
		out.Reason = "could not determine this host's public IPv4 (no SYNAPSE_PUBLIC_IP and external probe failed)"
		return out
	}
	out.IP = ip
	out.IPDetectedVia = source

	credID, zoneName, tokenEnc, err := h.findCloudflareCredentialForDomain(ctx, domain)
	if err != nil {
		out.Reason = "no stored Cloudflare credential covers " + domain + " (cadastre uma credencial cuja zona inclua esse domínio)"
		return out
	}
	out.Provider = "cloudflare"
	out.CredentialID = credID
	out.Zone = zoneName

	token, err := h.DNSCredentials.Crypto.DecryptString(tokenEnc)
	if err != nil {
		out.Reason = "could not decrypt the stored Cloudflare token (SYNAPSE_STORAGE_KEY mismatch?)"
		h.touchCredentialError(ctx, credID, "decrypt failed")
		return out
	}

	client := h.DNSCredentials.cloudflareClient(token)
	cfCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	// recordName == zoneName means "@" (root). Cloudflare's API accepts
	// the bare zone name in either form; UpsertARecord handles both.
	recordName := domain
	out.RecordName = recordName

	if err := client.UpsertARecord(cfCtx, zoneName, recordName, ip); err != nil {
		reason := translateCloudflareError(err)
		out.Reason = reason
		h.touchCredentialError(ctx, credID, reason)
		return out
	}

	out.Success = true
	h.touchCredentialUsed(ctx, credID)
	return out
}

// findCloudflareCredentialForDomain returns (id, zoneName, encryptedToken)
// for the INSTANCE-WIDE credential whose stored zones[] best matches
// `domain`. "Best match" = the longest zone name that the domain is
// equal to or a subdomain of. So `synapsepanel.com` matches a
// credential listing `synapsepanel.com`; `app.synapsepanel.com`
// would also match the same credential. If two credentials both
// cover the domain, the more specific zone wins.
//
// Project-scoped credentials (v1.6.4+, project_id IS NOT NULL) are
// deliberately excluded here: this function is the host-domain auto-
// DNS picker (mounted under /v1/admin/host_domain), and host_domain
// is an instance-level concern. If a project happens to register a
// credential whose zone covers the operator's host domain, we must
// NOT use that project's token to mint host DNS — that would be a
// cross-scope token use. The per-project equivalent is
// resolveCredentialForDomain in domains.go, which DOES walk project
// rows for per-deployment custom domains.
func (h *AdminHandler) findCloudflareCredentialForDomain(
	ctx context.Context, domain string,
) (string, string, []byte, error) {
	rows, err := h.DB.Query(ctx, `
		SELECT id, zones, token_encrypted
		  FROM dns_credentials
		 WHERE provider = 'cloudflare'
		   AND project_id IS NULL
	`)
	if err != nil {
		return "", "", nil, err
	}
	defer rows.Close()

	type candidate struct {
		ID       string
		Zone     string
		Token    []byte
		ZoneSpec int
	}
	var best *candidate
	for rows.Next() {
		var (
			id       string
			zonesRaw []byte
			tokenEnc []byte
		)
		if err := rows.Scan(&id, &zonesRaw, &tokenEnc); err != nil {
			return "", "", nil, err
		}
		var zones []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if len(zonesRaw) > 0 {
			if err := json.Unmarshal(zonesRaw, &zones); err != nil {
				continue
			}
		}
		for _, z := range zones {
			zn := strings.ToLower(strings.TrimSpace(z.Name))
			if zn == "" {
				continue
			}
			if !domainCoveredByZone(domain, zn) {
				continue
			}
			if best == nil || len(zn) > best.ZoneSpec {
				best = &candidate{ID: id, Zone: zn, Token: tokenEnc, ZoneSpec: len(zn)}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", nil, err
	}
	if best == nil {
		return "", "", nil, errors.New("no credential covers domain")
	}
	return best.ID, best.Zone, best.Token, nil
}

// domainCoveredByZone reports whether `domain` is the zone apex or a
// subdomain of `zone`. Both inputs are lowercased + trailing-dot
// stripped by the caller; we just do exact + suffix match.
func domainCoveredByZone(domain, zone string) bool {
	d := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(domain, ".")))
	z := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(zone, ".")))
	if d == "" || z == "" {
		return false
	}
	if d == z {
		return true
	}
	return strings.HasSuffix(d, "."+z)
}

// detectPublicIPv4 tries to resolve "what IP should I tell Cloudflare to
// point at" in priority order:
//  1. Fresh external probe via api.ipify.org (handles VPS migration —
//     the .env value can lag if the operator moved hosts).
//  2. Fall back to h.PublicIP (populated from SYNAPSE_PUBLIC_IP at
//     boot) when the probe fails.
//
// Returns ("", "") when neither source produces a value. The second
// return value is a label so the response can tell the operator how
// the IP was determined.
func (h *AdminHandler) detectPublicIPv4(ctx context.Context) (string, string) {
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	ip := probePublicIPv4(probeCtx)
	if ip != "" {
		return ip, "live probe (api.ipify.org)"
	}
	if h.PublicIP != "" {
		return strings.TrimSpace(h.PublicIP), "SYNAPSE_PUBLIC_IP from .env"
	}
	return "", ""
}

// probePublicIPv4 hits api.ipify.org and returns the body trimmed.
// Empty string on any error.
func probePublicIPv4(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org", nil)
	if err != nil {
		return ""
	}
	cli := &http.Client{Timeout: 4 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return ""
	}
	ip := strings.TrimSpace(string(body))
	if !looksLikeIP(ip) {
		return ""
	}
	return ip
}

// translateCloudflareError maps the synapsedns sentinel errors into
// operator-friendly strings. Wraps unknown errors verbatim so we don't
// accidentally swallow detail.
func translateCloudflareError(err error) string {
	switch {
	case errors.Is(err, synapsedns.ErrUnauthorized):
		return "Cloudflare rejected the stored token (revoked or scopes changed). Re-add the credential."
	default:
		return "Cloudflare API call failed: " + err.Error()
	}
}

// touchCredentialUsed marks the credential as freshly used. Best-
// effort. Errors logged + swallowed so a busy DB doesn't block the
// happy path.
func (h *AdminHandler) touchCredentialUsed(ctx context.Context, id string) {
	_, err := h.DB.Exec(ctx, `
		UPDATE dns_credentials
		   SET last_used_at = now(), last_error = NULL
		 WHERE id = $1
	`, id)
	if err != nil {
		slog.Default().Warn("touch dns_credential last_used_at",
			"id", id, "err", err.Error())
	}
}

// touchCredentialError records the most recent failure reason for the
// credential so the panel can show "last error: …". Best-effort.
func (h *AdminHandler) touchCredentialError(ctx context.Context, id, reason string) {
	_, err := h.DB.Exec(ctx, `
		UPDATE dns_credentials
		   SET last_error = $2
		 WHERE id = $1
	`, id, reason)
	if err != nil {
		slog.Default().Warn("touch dns_credential last_error",
			"id", id, "err", err.Error())
	}
}

