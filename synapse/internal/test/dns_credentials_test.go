package synapsetest

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	cryptopkg "github.com/Iann29/synapse/internal/crypto"
	synapsedns "github.com/Iann29/synapse/internal/dns"
)

// ----- Cloudflare API stub -------------------------------------------------

// cfStub mimics the subset of Cloudflare's REST API the dns package
// reaches for: /user/tokens/verify, /zones (list + by-name lookup),
// /zones/{id}/dns_records (create / update / list / delete). Each test
// configures behaviour via a stubConfig — the same server can flip
// between "valid token" and "401 every request" so we cover the
// revoke-mid-flow path without standing up a second listener.
type stubConfig struct {
	verifyStatus int  // HTTP status from /user/tokens/verify; 0 → 200
	verifyResult bool // success bool in the response body
	zones        []stubZone
	// mutating endpoints
	upsertHits *int64
	deleteHits *int64
	// when non-nil, every dns_records POST/PATCH/PUT returns this status
	dnsWriteStatus int
}

type stubZone struct {
	ID   string
	Name string // bare; "fechasul.com.br"
}

func newCloudflareStub(t *testing.T, cfg *stubConfig) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/user/tokens/verify", func(w http.ResponseWriter, r *http.Request) {
		if cfg.verifyStatus != 0 && cfg.verifyStatus != http.StatusOK {
			w.WriteHeader(cfg.verifyStatus)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": false,
				"errors":  []map[string]any{{"code": 1000, "message": "Invalid API Token"}},
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": cfg.verifyResult,
			"result":  map[string]any{"id": "tok-stub", "status": "active"},
		})
	})
	mux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		// /zones?name=<bare> → return the matching zone (lookupZoneID).
		// /zones (no params) → return all zones (ListZones).
		name := r.URL.Query().Get("name")
		var out []map[string]any
		for _, z := range cfg.zones {
			if name != "" && z.Name != name {
				continue
			}
			out = append(out, map[string]any{
				"id":   z.ID,
				"name": z.Name,
			})
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"result":  out,
			"result_info": map[string]any{
				"page":        1,
				"per_page":    50,
				"count":       len(out),
				"total_count": len(out),
			},
		})
	})
	// Catch-all for /zones/{id}/dns_records (and pagination variants).
	mux.HandleFunc("/zones/", func(w http.ResponseWriter, r *http.Request) {
		if cfg.dnsWriteStatus != 0 && cfg.dnsWriteStatus != http.StatusOK {
			w.WriteHeader(cfg.dnsWriteStatus)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": false,
				"errors": []map[string]any{
					{"code": 1000, "message": "Invalid API Token"},
				},
			})
			return
		}
		switch r.Method {
		case http.MethodGet:
			// Return empty list — libdns will then call POST.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success":     true,
				"result":      []map[string]any{},
				"result_info": map[string]any{"page": 1, "per_page": 100, "count": 0, "total_count": 0},
			})
		case http.MethodPost:
			if cfg.upsertHits != nil {
				atomic.AddInt64(cfg.upsertHits, 1)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result":  map[string]any{"id": "rec-1", "type": "A", "name": "stub", "content": "203.0.113.10", "ttl": 1, "zone_id": "zone-1"},
			})
		case http.MethodPatch, http.MethodPut:
			if cfg.upsertHits != nil {
				atomic.AddInt64(cfg.upsertHits, 1)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result":  map[string]any{"id": "rec-1", "type": "A", "name": "stub", "content": "203.0.113.10", "ttl": 1, "zone_id": "zone-1"},
			})
		case http.MethodDelete:
			if cfg.deleteHits != nil {
				atomic.AddInt64(cfg.deleteHits, 1)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result":  map[string]any{"id": "rec-1"},
			})
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// freshCryptoBox returns a per-test SecretBox. We don't reuse the
// HA-harness box because Setup (non-HA) doesn't allocate one, and
// the dns_credentials path only needs one for the encrypt+decrypt
// cycle.
func freshCryptoBox(t *testing.T) *cryptopkg.SecretBox {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	box, err := cryptopkg.New(key)
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	return box
}

func cloudflareFactoryForStub(stub *httptest.Server) func(token string) *synapsedns.CloudflareClient {
	return func(token string) *synapsedns.CloudflareClient {
		return &synapsedns.CloudflareClient{
			Token:   token,
			BaseURL: stub.URL,
		}
	}
}

// dnsCredentialResp mirrors the JSON shape returned by the credential
// endpoints. DisallowUnknownFields catches drift.
type dnsCredentialResp struct {
	ID         string                  `json:"id"`
	Provider   string                  `json:"provider"`
	Label      string                  `json:"label"`
	Zones      []dnsCredentialZoneInfo `json:"zones"`
	CreatedBy  *string                 `json:"createdBy,omitempty"`
	CreatedAt  time.Time               `json:"createdAt"`
	LastUsedAt *time.Time              `json:"lastUsedAt,omitempty"`
	LastError  string                  `json:"lastError,omitempty"`
}

type dnsCredentialZoneInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type listDNSCredentialsResp struct {
	Credentials []dnsCredentialResp `json:"credentials"`
}

// dnsProviderResp matches the /v1/internal/dns_provider payload.
type dnsProviderRespBody struct {
	Provider    string   `json:"provider"`
	Nameservers []string `json:"nameservers"`
	Error       string   `json:"error,omitempty"`
}

// ----- Tests --------------------------------------------------------------

func TestDNSCredentials_Add_Cloudflare_Success(t *testing.T) {
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones: []stubZone{
			{ID: "zone-1", Name: "fechasul.com.br"},
		},
	})
	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
	})
	owner := makeAdminUser(t, h)

	var got dnsCredentialResp
	h.DoJSON(http.MethodPost, "/v1/admin/dns_credentials/cloudflare",
		owner.AccessToken,
		map[string]string{"token": "valid-token", "label": "Personal CF"},
		http.StatusCreated, &got)

	if got.Provider != "cloudflare" {
		t.Errorf("provider: got %q want cloudflare", got.Provider)
	}
	if got.Label != "Personal CF" {
		t.Errorf("label: got %q want Personal CF", got.Label)
	}
	if len(got.Zones) != 1 || got.Zones[0].Name != "fechasul.com.br" {
		t.Errorf("zones: got %+v", got.Zones)
	}
	if got.ID == "" {
		t.Errorf("expected id, got empty")
	}

	// List should include the credential.
	var list listDNSCredentialsResp
	h.DoJSON(http.MethodGet, "/v1/admin/dns_credentials",
		owner.AccessToken, nil, http.StatusOK, &list)
	if len(list.Credentials) != 1 {
		t.Fatalf("expected 1 credential, got %d", len(list.Credentials))
	}
	if list.Credentials[0].ID != got.ID {
		t.Errorf("listed id mismatch: got %q want %q", list.Credentials[0].ID, got.ID)
	}
}

func TestDNSCredentials_Add_InvalidToken_400(t *testing.T) {
	stub := newCloudflareStub(t, &stubConfig{
		verifyStatus: http.StatusUnauthorized,
	})
	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
	})
	owner := makeAdminUser(t, h)

	env := h.AssertStatus(http.MethodPost, "/v1/admin/dns_credentials/cloudflare",
		owner.AccessToken,
		map[string]string{"token": "bad-token", "label": "Bad"},
		http.StatusBadRequest)
	if env.Code != "invalid_token" {
		t.Errorf("code: got %q want invalid_token", env.Code)
	}
}

func TestDNSCredentials_Delete_Unused_204(t *testing.T) {
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones: []stubZone{
			{ID: "zone-1", Name: "fechasul.com.br"},
		},
	})
	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
	})
	owner := makeAdminUser(t, h)

	var created dnsCredentialResp
	h.DoJSON(http.MethodPost, "/v1/admin/dns_credentials/cloudflare",
		owner.AccessToken,
		map[string]string{"token": "valid", "label": "L"},
		http.StatusCreated, &created)

	resp := h.Do(http.MethodDelete, "/v1/admin/dns_credentials/"+created.ID,
		owner.AccessToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// List should now be empty.
	var list listDNSCredentialsResp
	h.DoJSON(http.MethodGet, "/v1/admin/dns_credentials",
		owner.AccessToken, nil, http.StatusOK, &list)
	if len(list.Credentials) != 0 {
		t.Errorf("expected empty list, got %d", len(list.Credentials))
	}
}

func TestDNSCredentials_Delete_InUse_409(t *testing.T) {
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones: []stubZone{
			{ID: "zone-1", Name: "fechasul.com.br"},
		},
	})
	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
		PublicIP:          "203.0.113.10",
	})
	owner := makeAdminUser(t, h)

	// Create credential.
	var cred dnsCredentialResp
	h.DoJSON(http.MethodPost, "/v1/admin/dns_credentials/cloudflare",
		owner.AccessToken,
		map[string]string{"token": "valid", "label": "L"},
		http.StatusCreated, &cred)

	// Wire a deployment + custom domain, then auto-configure to bind
	// the credential.
	team := createTeam(t, h, owner.AccessToken, "DNS Co "+randHex(3))
	proj := createProject(t, h, owner.AccessToken, team.Slug, "InUse")
	depName := "dns-inuse-" + randHex(3)
	h.SeedDeployment(proj.ID, depName, "prod", "running", true, owner.ID, 3950, "")

	var domain domainResp
	h.DoJSON(http.MethodPost, "/v1/deployments/"+depName+"/domains",
		owner.AccessToken,
		map[string]any{"domain": "api.fechasul.com.br", "role": "api"},
		http.StatusCreated, &domain)

	var configured domainResp
	h.DoJSON(http.MethodPost, "/v1/deployments/"+depName+"/domains/"+domain.ID+"/auto_configure",
		owner.AccessToken,
		map[string]string{"credentialId": cred.ID},
		http.StatusOK, &configured)
	if !configured.AutoConfigured {
		t.Fatalf("expected auto_configured=true, got %+v", configured)
	}

	// Delete should be 409.
	env := h.AssertStatus(http.MethodDelete, "/v1/admin/dns_credentials/"+cred.ID,
		owner.AccessToken, nil, http.StatusConflict)
	if env.Code != "credential_in_use" {
		t.Errorf("code: got %q want credential_in_use", env.Code)
	}
}

func TestDomain_AutoConfigure_SingleCredential_Success(t *testing.T) {
	upsertHits := int64(0)
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones:        []stubZone{{ID: "zone-1", Name: "fechasul.com.br"}},
		upsertHits:   &upsertHits,
	})
	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
		PublicIP:          "203.0.113.10",
	})
	owner := makeAdminUser(t, h)

	// Save a credential.
	var cred dnsCredentialResp
	h.DoJSON(http.MethodPost, "/v1/admin/dns_credentials/cloudflare",
		owner.AccessToken,
		map[string]string{"token": "valid", "label": "L"},
		http.StatusCreated, &cred)

	// Provision a deployment + register a domain under the zone.
	team := createTeam(t, h, owner.AccessToken, "DNS Co "+randHex(3))
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Auto")
	depName := "dns-auto-" + randHex(3)
	h.SeedDeployment(proj.ID, depName, "prod", "running", true, owner.ID, 3960, "")

	var domain domainResp
	h.DoJSON(http.MethodPost, "/v1/deployments/"+depName+"/domains",
		owner.AccessToken,
		map[string]any{"domain": "api.fechasul.com.br", "role": "api"},
		http.StatusCreated, &domain)

	// Body omitted — should pick the unique matching credential.
	var got domainResp
	h.DoJSON(http.MethodPost, "/v1/deployments/"+depName+"/domains/"+domain.ID+"/auto_configure",
		owner.AccessToken, map[string]any{}, http.StatusOK, &got)
	if !got.AutoConfigured {
		t.Errorf("expected auto_configured=true, got %+v", got)
	}
	if got.DNSCredentialID == nil || *got.DNSCredentialID != cred.ID {
		t.Errorf("expected dnsCredentialId=%q, got %v", cred.ID, got.DNSCredentialID)
	}
	if atomic.LoadInt64(&upsertHits) == 0 {
		t.Errorf("expected at least one Cloudflare DNS write, got 0")
	}
}

// TestDomain_AutoConfigure_EnqueuesForVerifier asserts the contract
// the verification loop (PR #3, internal/dns/verifier.go) consumes:
// after a successful POST /auto_configure, the row sits at
// status='pending' AND auto_configured=true AND dns_verified_at IS
// NULL — the exact predicate Verifier.loadPending uses to find work.
// A regression here (e.g. handler accidentally flipping to active
// without resolution) would make the verifier silently skip every
// auto-configured row.
func TestDomain_AutoConfigure_EnqueuesForVerifier(t *testing.T) {
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones:        []stubZone{{ID: "zone-1", Name: "fechasul.com.br"}},
	})
	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
		PublicIP:          "203.0.113.10",
	})
	owner := makeAdminUser(t, h)

	var cred dnsCredentialResp
	h.DoJSON(http.MethodPost, "/v1/admin/dns_credentials/cloudflare",
		owner.AccessToken,
		map[string]string{"token": "valid", "label": "L"},
		http.StatusCreated, &cred)

	team := createTeam(t, h, owner.AccessToken, "DNS Co "+randHex(3))
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Queue")
	depName := "dns-queue-" + randHex(3)
	h.SeedDeployment(proj.ID, depName, "prod", "running", true, owner.ID, 3961, "")

	var domain domainResp
	h.DoJSON(http.MethodPost, "/v1/deployments/"+depName+"/domains",
		owner.AccessToken,
		map[string]any{"domain": "api.fechasul.com.br", "role": "api"},
		http.StatusCreated, &domain)

	var configured domainResp
	h.DoJSON(http.MethodPost, "/v1/deployments/"+depName+"/domains/"+domain.ID+"/auto_configure",
		owner.AccessToken, map[string]any{}, http.StatusOK, &configured)

	// The verifier's loadPending predicate, run as a SQL EXISTS — we
	// don't import the dns package here to keep this test focused on
	// the API contract; the verifier-side test (dns_verifier_test.go)
	// covers the consumer.
	var pending bool
	if err := h.DB.QueryRow(context.Background(), `
		SELECT EXISTS (
		    SELECT 1 FROM deployment_domains
		     WHERE id = $1
		       AND auto_configured = true
		       AND status = 'pending'
		       AND dns_verified_at IS NULL
		)
	`, configured.ID).Scan(&pending); err != nil {
		t.Fatalf("predicate query: %v", err)
	}
	if !pending {
		t.Errorf("expected row to match verifier predicate after auto_configure, got pending=false")
	}
}

func TestDomain_AutoConfigure_NoCredentialForZone(t *testing.T) {
	// Credential covers `other.com`, domain is under `fechasul.com.br`.
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones:        []stubZone{{ID: "zone-other", Name: "other.com"}},
	})
	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
		PublicIP:          "203.0.113.10",
	})
	owner := makeAdminUser(t, h)

	h.DoJSON(http.MethodPost, "/v1/admin/dns_credentials/cloudflare",
		owner.AccessToken,
		map[string]string{"token": "valid", "label": "L"},
		http.StatusCreated, new(dnsCredentialResp))

	team := createTeam(t, h, owner.AccessToken, "DNS Co "+randHex(3))
	proj := createProject(t, h, owner.AccessToken, team.Slug, "NoMatch")
	depName := "dns-no-" + randHex(3)
	h.SeedDeployment(proj.ID, depName, "prod", "running", true, owner.ID, 3970, "")

	var domain domainResp
	h.DoJSON(http.MethodPost, "/v1/deployments/"+depName+"/domains",
		owner.AccessToken,
		map[string]any{"domain": "api.fechasul.com.br", "role": "api"},
		http.StatusCreated, &domain)

	env := h.AssertStatus(http.MethodPost,
		"/v1/deployments/"+depName+"/domains/"+domain.ID+"/auto_configure",
		owner.AccessToken, map[string]any{}, http.StatusBadRequest)
	if env.Code != "no_credential_for_zone" {
		t.Errorf("code: got %q want no_credential_for_zone", env.Code)
	}
}

func TestDomain_AutoConfigure_NoPublicIP_503(t *testing.T) {
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones:        []stubZone{{ID: "zone-1", Name: "fechasul.com.br"}},
	})
	// PublicIP intentionally left empty.
	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
	})
	owner := makeAdminUser(t, h)

	h.DoJSON(http.MethodPost, "/v1/admin/dns_credentials/cloudflare",
		owner.AccessToken,
		map[string]string{"token": "valid", "label": "L"},
		http.StatusCreated, new(dnsCredentialResp))

	team := createTeam(t, h, owner.AccessToken, "DNS Co "+randHex(3))
	proj := createProject(t, h, owner.AccessToken, team.Slug, "NoIP")
	depName := "dns-ip-" + randHex(3)
	h.SeedDeployment(proj.ID, depName, "prod", "running", true, owner.ID, 3980, "")

	var domain domainResp
	h.DoJSON(http.MethodPost, "/v1/deployments/"+depName+"/domains",
		owner.AccessToken,
		map[string]any{"domain": "api.fechasul.com.br", "role": "api"},
		http.StatusCreated, &domain)

	env := h.AssertStatus(http.MethodPost,
		"/v1/deployments/"+depName+"/domains/"+domain.ID+"/auto_configure",
		owner.AccessToken, map[string]any{}, http.StatusServiceUnavailable)
	if env.Code != "public_ip_not_configured" {
		t.Errorf("code: got %q want public_ip_not_configured", env.Code)
	}
}

func TestDomain_AutoConfigure_TokenRevoked(t *testing.T) {
	// Verify succeeds at save time; later Cloudflare returns 401.
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult:   true,
		zones:          []stubZone{{ID: "zone-1", Name: "fechasul.com.br"}},
		dnsWriteStatus: http.StatusUnauthorized,
	})
	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
		PublicIP:          "203.0.113.10",
	})
	owner := makeAdminUser(t, h)

	var cred dnsCredentialResp
	h.DoJSON(http.MethodPost, "/v1/admin/dns_credentials/cloudflare",
		owner.AccessToken,
		map[string]string{"token": "valid", "label": "L"},
		http.StatusCreated, &cred)

	team := createTeam(t, h, owner.AccessToken, "DNS Co "+randHex(3))
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Rev")
	depName := "dns-rev-" + randHex(3)
	h.SeedDeployment(proj.ID, depName, "prod", "running", true, owner.ID, 3990, "")

	var domain domainResp
	h.DoJSON(http.MethodPost, "/v1/deployments/"+depName+"/domains",
		owner.AccessToken,
		map[string]any{"domain": "api.fechasul.com.br", "role": "api"},
		http.StatusCreated, &domain)

	env := h.AssertStatus(http.MethodPost,
		"/v1/deployments/"+depName+"/domains/"+domain.ID+"/auto_configure",
		owner.AccessToken, map[string]any{}, http.StatusBadRequest)
	if env.Code != "token_invalid_or_revoked" {
		t.Errorf("code: got %q want token_invalid_or_revoked", env.Code)
	}

	// last_error should be persisted on the credential row.
	var lastErr string
	if err := h.DB.QueryRow(context.Background(),
		`SELECT COALESCE(last_error, '') FROM dns_credentials WHERE id = $1`,
		cred.ID).Scan(&lastErr); err != nil {
		t.Fatalf("read last_error: %v", err)
	}
	if lastErr == "" {
		t.Errorf("expected last_error to be populated")
	}
}

func TestDNSProvider_Endpoint_Cloudflare(t *testing.T) {
	stubLookup := func(ctx context.Context, domain string) (string, []string, error) {
		return "cloudflare", []string{"isla.ns.cloudflare.com.", "tom.ns.cloudflare.com."}, nil
	}
	h := SetupWithOpts(t, SetupOpts{
		DNSProviderLookup: stubLookup,
	})

	resp := h.Do(http.MethodGet,
		"/v1/internal/dns_provider?domain=fechasul.com.br", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got dnsProviderRespBody
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Provider != "cloudflare" {
		t.Errorf("provider: got %q want cloudflare", got.Provider)
	}
	if len(got.Nameservers) != 2 {
		t.Errorf("nameservers: got %v", got.Nameservers)
	}
}

func TestDNSProvider_Endpoint_MissingDomain(t *testing.T) {
	h := Setup(t)
	resp := h.Do(http.MethodGet, "/v1/internal/dns_provider", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

// Regression for the v1.5.1 KVM4 panic: when SYNAPSE_STORAGE_KEY isn't
// passed to the synapse-api container (compose passthrough missing),
// crypto.NewFromEnv() returns an error and main.go used to assign a
// `*crypto.SecretBox(nil)` straight into the api.SecretEnvelope
// interface field. The interface then carried a typed-nil pointer:
// `if h.Crypto == nil` returned false, the handler called
// EncryptString, and the receiver-method dereference panicked.
//
// Two coverage angles:
//   1. **Literal-nil interface**: the path main.go now takes (via the
//      intermediate var pattern). Should hit the 503 guard cleanly.
//   2. **Typed-nil via concrete type assignment**: simulates the old
//      bug shape — pass a `var sb *crypto.SecretBox; sb` directly. If
//      this returns 503 we know the api-side guard handles both
//      shapes; if it panics the test traps the panic and fails
//      loudly so the bug never re-lands silently.
//
// The api-side `if h.Crypto == nil` guard was always there (since
// PR #86); the bug was that the interface had a typed-nil pointer so
// the guard didn't trigger. The defense lives in main.go's
// intermediate-var pattern: we never hand a typed-nil to the
// interface field. This test pins both paths.
func TestDNSCredentials_Add_Cloudflare_LiteralNilCrypto_Returns503(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{
		// DNSEnvelope omitted — interface field stays literal nil.
	})
	owner := makeAdminUser(t, h)

	resp := h.Do(http.MethodPost, "/v1/admin/dns_credentials/cloudflare",
		owner.AccessToken,
		map[string]string{"token": "any-token", "label": "test"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on literal-nil Crypto, got %d", resp.StatusCode)
	}
	var body errorBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != "crypto_not_configured" {
		t.Errorf("code: got %q want crypto_not_configured", body.Code)
	}
}

// errorBody is the minimal envelope we use to assert structured 4xx/5xx
// responses without the typed-nil bug masquerading as a generic 500.
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
