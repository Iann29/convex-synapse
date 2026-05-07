package dns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/libdns/cloudflare"
	"github.com/libdns/libdns"

	"github.com/Iann29/synapse/internal/models"
)

// CloudflareClient is a thin Synapse-flavoured wrapper over the
// libdns/cloudflare Provider. We expose only the four operations the
// dashboard's auto-configure flow actually uses: VerifyToken (at save
// time), ListZones (also at save time, to populate the cached zones
// jsonb column), UpsertARecord (when an operator clicks "auto-configure"
// on a custom domain) and DeleteARecord (best-effort cleanup when the
// domain row is deleted).
//
// BaseURL is a test seam. Empty = use Cloudflare's real API. When set,
// the client rewrites every request URL's host+scheme so an
// httptest.Server can pretend to be Cloudflare. Production wiring leaves
// this empty.
type CloudflareClient struct {
	Token   string // Bearer token; required.
	BaseURL string // Optional override for tests, e.g. httptestSrv.URL.

	// HTTPClient lets callers inject a transport (timeouts, retries,
	// instrumentation). Nil = use a sensible default (10s timeout).
	HTTPClient *http.Client
}

// ZoneInfo is an alias for models.ZoneInfo — kept here so callers in
// internal/api can write `dns.ZoneInfo` without doubling-up imports,
// while the canonical definition lives next to DNSCredential in
// models so other packages can reference it without depending on dns.
type ZoneInfo = models.ZoneInfo

// ErrUnauthorized is returned by VerifyToken / UpsertARecord /
// DeleteARecord when Cloudflare answers HTTP 401 (or the
// authenticated-equivalent status path on /user/tokens/verify, "token
// inactive"). The api package surfaces this as 400 token_invalid_or_revoked
// and writes a last_error onto the credential row.
var ErrUnauthorized = errors.New("cloudflare: token unauthorized or revoked")

// cfBaseURL mirrors libdns/cloudflare's hard-coded baseURL so we can
// rewrite request URLs without forking the library.
const cfBaseURL = "https://api.cloudflare.com/client/v4"

// defaultHTTPTimeout caps each individual Cloudflare API call. 10s is
// generous; libdns lookups for /zones may paginate but each page is
// well under a second on a healthy network.
const defaultHTTPTimeout = 10 * time.Second

// httpClient returns the configured client or a fresh default with
// the package timeout applied.
func (c *CloudflareClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: defaultHTTPTimeout}
}

// rewritingTransport wraps an underlying RoundTripper so every
// libdns/cloudflare request gets its scheme+host rewritten from the
// hard-coded api.cloudflare.com to whatever BaseURL points at. Used
// purely as a test seam; production omits BaseURL and goes direct.
type rewritingTransport struct {
	target *url.URL
	inner  http.RoundTripper
}

func (rt *rewritingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasPrefix(req.URL.String(), cfBaseURL) {
		// Re-parse so we don't mutate the caller's URL value (libdns
		// reuses URLs across paginated calls).
		newURL := *req.URL
		newURL.Scheme = rt.target.Scheme
		newURL.Host = rt.target.Host
		// Strip the /client/v4 prefix — the test server mounts at /.
		newURL.Path = strings.TrimPrefix(newURL.Path, "/client/v4")
		clone := req.Clone(req.Context())
		clone.URL = &newURL
		clone.Host = rt.target.Host
		req = clone
	}
	if rt.inner != nil {
		return rt.inner.RoundTrip(req)
	}
	return http.DefaultTransport.RoundTrip(req)
}

// provider builds a libdns/cloudflare Provider configured to use our
// (possibly URL-rewriting) HTTP client. Returns the provider and a
// "client" satisfying libdns/cloudflare.HTTPClient that the verify-
// token path uses for direct calls (libdns has no VerifyToken method).
func (c *CloudflareClient) provider() (*cloudflare.Provider, *http.Client) {
	hc := c.httpClient()
	if c.BaseURL != "" {
		target, err := url.Parse(c.BaseURL)
		if err == nil {
			// Wrap the existing transport so retries / timeouts the
			// caller configured stay applied.
			inner := hc.Transport
			if inner == nil {
				inner = http.DefaultTransport
			}
			hc = &http.Client{
				Transport: &rewritingTransport{target: target, inner: inner},
				Timeout:   hc.Timeout,
			}
		}
	}
	return &cloudflare.Provider{
		APIToken:   c.Token,
		HTTPClient: hc,
	}, hc
}

// apiURL builds a Cloudflare API URL using BaseURL when set, falling
// back to the real api.cloudflare.com. Used only by VerifyToken which
// hits an endpoint libdns doesn't expose.
func (c *CloudflareClient) apiURL(path string) string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/") + path
	}
	return cfBaseURL + path
}

// tokenVerifyResp matches the shape of GET /user/tokens/verify. We
// only inspect `success` and the first error code so the rest of the
// document can drift without breaking us.
type tokenVerifyResp struct {
	Success bool `json:"success"`
	Result  struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"result"`
	Errors []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// VerifyToken calls Cloudflare's GET /user/tokens/verify. Returns nil
// if the token is valid + active. Used at credential-save time to
// reject a typoed/expired token before we encrypt and persist it.
func (c *CloudflareClient) VerifyToken(ctx context.Context) error {
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("cloudflare: empty token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.apiURL("/user/tokens/verify"), nil)
	if err != nil {
		return fmt.Errorf("cloudflare: build verify request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")

	hc := c.httpClient()
	if c.BaseURL != "" {
		// Direct hit — we don't need the libdns rewrite path here,
		// but we do need to use the BaseURL.
		hc = c.httpClient()
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare: verify token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrUnauthorized
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var parsed tokenVerifyResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("cloudflare: decode verify response: %w (body=%s)", err, truncate(string(body), 200))
	}
	if !parsed.Success {
		// Surface the first error message so the dashboard can show
		// "code 1000: Invalid API Token" instead of a bare bool.
		if len(parsed.Errors) > 0 {
			if parsed.Errors[0].Code == 1000 || parsed.Errors[0].Code == 1001 {
				return ErrUnauthorized
			}
			return fmt.Errorf("cloudflare: verify failed: %d %s",
				parsed.Errors[0].Code, parsed.Errors[0].Message)
		}
		return errors.New("cloudflare: verify failed")
	}
	if parsed.Result.Status != "" && parsed.Result.Status != "active" {
		return fmt.Errorf("cloudflare: token status %q (expected 'active')", parsed.Result.Status)
	}
	return nil
}

// ListZones returns the zones this token has access to. Cached in the
// dns_credentials.zones jsonb column at save time so the dashboard
// doesn't re-call Cloudflare on every page render.
func (c *CloudflareClient) ListZones(ctx context.Context) ([]ZoneInfo, error) {
	prov, _ := c.provider()
	zones, err := prov.ListZones(ctx)
	if err != nil {
		return nil, mapAuthError(err)
	}
	// libdns's Zone has only Name (FQDN with trailing dot). We need
	// the Cloudflare-side ID too so the dashboard can deep-link, so
	// we hit /zones?name=<bare> for each entry. In practice tokens
	// scope to ~1 zone; the loop is fine.
	out := make([]ZoneInfo, 0, len(zones))
	for _, z := range zones {
		bare := strings.TrimSuffix(z.Name, ".")
		if bare == "" {
			continue
		}
		id, err := c.lookupZoneID(ctx, bare)
		if err != nil {
			return nil, err
		}
		out = append(out, ZoneInfo{ID: id, Name: bare})
	}
	return out, nil
}

// zonesQueryResp matches the shape of GET /zones?name=<name>. Only
// the result array's first entry's id is consumed.
type zonesQueryResp struct {
	Success bool `json:"success"`
	Result  []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"result"`
	Errors []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

func (c *CloudflareClient) lookupZoneID(ctx context.Context, name string) (string, error) {
	q := url.Values{}
	q.Set("name", name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.apiURL("/zones?"+q.Encode()), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("cloudflare: lookup zone %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", ErrUnauthorized
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var parsed zonesQueryResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("cloudflare: decode zones response: %w", err)
	}
	if !parsed.Success || len(parsed.Result) == 0 {
		if len(parsed.Errors) > 0 {
			return "", fmt.Errorf("cloudflare: zone lookup failed: %d %s",
				parsed.Errors[0].Code, parsed.Errors[0].Message)
		}
		return "", fmt.Errorf("cloudflare: zone %q not found", name)
	}
	return parsed.Result[0].ID, nil
}

// UpsertARecord creates or updates an A record in `zoneName`. proxied
// is intentionally always false: orange-cloud breaks Caddy on-demand
// TLS because Cloudflare terminates TLS first and Caddy never sees
// the ACME handshake. ttl=1 means "auto" in Cloudflare-land (the API
// rejects ttl<60 for non-auto values).
func (c *CloudflareClient) UpsertARecord(ctx context.Context, zoneName, recordName, ipv4 string) error {
	addr, err := netip.ParseAddr(ipv4)
	if err != nil {
		return fmt.Errorf("cloudflare: parse IPv4 %q: %w", ipv4, err)
	}
	if !addr.Is4() {
		return fmt.Errorf("cloudflare: %q is not an IPv4 address", ipv4)
	}
	prov, _ := c.provider()
	rec := libdns.Address{
		Name: recordName,
		TTL:  1 * time.Second, // libdns rounds to seconds; Cloudflare maps any value <60 here to "auto"
		IP:   addr,
	}
	// SetRecords does upsert: creates if missing, patches the existing
	// record otherwise. Exactly the semantics we want.
	_, err = prov.SetRecords(ctx, zoneName, []libdns.Record{rec})
	if err != nil {
		return mapAuthError(fmt.Errorf("cloudflare: upsert A record %s.%s: %w",
			recordName, zoneName, err))
	}
	return nil
}

// DeleteARecord removes the A record at `recordName` in `zoneName`.
// Best-effort: returns nil if the record is already gone (libdns
// reports "no records to delete" via empty result, not an error, but
// we treat any "not found" pattern as success-ish so callers can fan
// this in to a defer).
func (c *CloudflareClient) DeleteARecord(ctx context.Context, zoneName, recordName string) error {
	prov, _ := c.provider()
	rec := libdns.Address{
		Name: recordName,
		// IP zero value tells libdns "match the record by name+type"
		// — see libdns.Address.RR which converts the zero IP to an
		// empty Data field.
	}
	deleted, err := prov.DeleteRecords(ctx, zoneName, []libdns.Record{rec})
	if err != nil {
		// Don't swallow auth errors — those mean the credential is
		// dead and the caller should know about it.
		mapped := mapAuthError(err)
		if errors.Is(mapped, ErrUnauthorized) {
			return mapped
		}
		// Anything else is best-effort: the record might already be
		// gone, or Cloudflare may have raced with another deletion.
		// Return the wrapped error so the caller can log it but not
		// block the deployment_domain delete.
		return fmt.Errorf("cloudflare: delete A record %s.%s: %w",
			recordName, zoneName, err)
	}
	_ = deleted
	return nil
}

// mapAuthError translates a libdns error string into ErrUnauthorized
// when Cloudflare reported HTTP 401. libdns wraps the error as
// "got error status: HTTP 401: ..." (see client.go in libdns/cloudflare).
// We pattern-match on that prefix; brittle, but we ship one libdns
// version and pin it.
func mapAuthError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "HTTP 401") || strings.Contains(msg, "HTTP 403") {
		return fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
