package synapsetest

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Tests for the v1.5.6 auto-config flow on POST /v1/admin/host_domain.
// The unit-level Cloudflare interaction is already exercised by
// dns_credentials_test.go; here we assert the end-to-end glue:
//
//   1. autoConfigureDns=true with a credential covering the zone →
//      Cloudflare POST/PATCH fires + dnsAuto.success in response body.
//   2. autoConfigureDns=true with NO matching credential → dnsAuto with
//      success=false + a humanised reason; reconfigure still proceeds
//      (operator picked "warn-and-proceed" semantics).
//   3. autoConfigureDns=false (default) → no Cloudflare call at all,
//      response has no dnsAuto field.
//
// The fake updater is the same pattern as the existing host-domain
// tests — it returns 200 unless the test wants a different status.

type hostDomainPostRespForTest struct {
	JobID     string                  `json:"jobId"`
	StatusURL string                  `json:"statusUrl"`
	State     string                  `json:"state"`
	DNSAuto   *hostDomainDNSAutoForTest `json:"dnsAuto,omitempty"`
}

type hostDomainDNSAutoForTest struct {
	Attempted     bool   `json:"attempted"`
	Success       bool   `json:"success"`
	Provider      string `json:"provider,omitempty"`
	CredentialID  string `json:"credentialId,omitempty"`
	Zone          string `json:"zone,omitempty"`
	RecordName    string `json:"recordName,omitempty"`
	IP            string `json:"ip,omitempty"`
	IPDetectedVia string `json:"ipDetectedVia,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// makeFakeUpdaterForReconfigure stands up an httptest.Server that
// pretends to be the synapse-updater daemon. Returns 200 to the
// /reconfigure_host_domain POST so the api considers the job dispatched.
func makeFakeUpdaterForReconfigure(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	tok := "fake-bearer-token-for-host-domain-test"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/reconfigure_host_domain" && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"jobId":"updater-fake-job","state":"queued"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv, tok
}

// TestHostDomain_Post_AutoConfigureDNS_Success seeds a Cloudflare
// credential covering the requested zone, then issues POST with
// autoConfigureDns=true. We expect:
//   - 202 Accepted
//   - dnsAuto.attempted=true, success=true, recordName matches request
//   - At least one POST/PATCH against the Cloudflare stub (libdns
//     behaviour: GET first to discover, POST to create when absent)
func TestHostDomain_Post_AutoConfigureDNS_Success(t *testing.T) {
	var upsertHits int64
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones: []stubZone{
			{ID: "zone-1", Name: "example.com"},
		},
		upsertHits: &upsertHits,
	})
	updater, tok := makeFakeUpdaterForReconfigure(t)

	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:        freshCryptoBox(t),
		CloudflareFactory:  cloudflareFactoryForStub(stub),
		UpdaterURL:         updater.URL,
		UpdaterToken:       tok,
		PublicIP:           "203.0.113.10",
		HostDomainResolver: stubResolverFunc(func(host string) ([]string, error) {
			// After the upsert, the stub treats DNS as resolving to the
			// expected IP so the api's preflight passes.
			return []string{"203.0.113.10"}, nil
		}),
	})
	owner := makeAdminUser(t, h)

	// Seed the Cloudflare credential covering example.com.
	var cred dnsCredentialResp
	h.DoJSON(http.MethodPost, "/v1/admin/dns_credentials/cloudflare",
		owner.AccessToken,
		map[string]string{"token": "valid-token", "label": "Personal CF"},
		http.StatusCreated, &cred)

	body := map[string]any{
		"domain":           "synapse.example.com",
		"autoConfigureDns": true,
	}
	var got hostDomainPostRespForTest
	h.DoJSON(http.MethodPost, "/v1/admin/host_domain",
		owner.AccessToken, body, http.StatusAccepted, &got)

	if got.DNSAuto == nil {
		t.Fatalf("expected dnsAuto in response, got nil")
	}
	if !got.DNSAuto.Attempted {
		t.Errorf("dnsAuto.attempted: got false want true")
	}
	if !got.DNSAuto.Success {
		t.Errorf("dnsAuto.success: got false; reason=%q", got.DNSAuto.Reason)
	}
	if got.DNSAuto.Zone != "example.com" {
		t.Errorf("dnsAuto.zone: got %q want example.com", got.DNSAuto.Zone)
	}
	if got.DNSAuto.RecordName != "synapse.example.com" {
		t.Errorf("dnsAuto.recordName: got %q want synapse.example.com",
			got.DNSAuto.RecordName)
	}
	if got.DNSAuto.IP == "" {
		t.Errorf("dnsAuto.ip: empty")
	}
	if got.DNSAuto.CredentialID != cred.ID {
		t.Errorf("dnsAuto.credentialId: got %q want %q",
			got.DNSAuto.CredentialID, cred.ID)
	}
	if atomic.LoadInt64(&upsertHits) == 0 {
		t.Errorf("expected at least one Cloudflare write hit, got 0")
	}
	// last_used_at should now be populated.
	var refreshed listDNSCredentialsResp
	h.DoJSON(http.MethodGet, "/v1/admin/dns_credentials",
		owner.AccessToken, nil, http.StatusOK, &refreshed)
	if len(refreshed.Credentials) != 1 {
		t.Fatalf("expected 1 credential, got %d", len(refreshed.Credentials))
	}
	if refreshed.Credentials[0].LastUsedAt == nil {
		t.Errorf("expected last_used_at to be set after auto-configure")
	}
	// Sanity: last_used_at within the last 60s.
	if refreshed.Credentials[0].LastUsedAt != nil &&
		time.Since(*refreshed.Credentials[0].LastUsedAt) > time.Minute {
		t.Errorf("last_used_at too old: %v", refreshed.Credentials[0].LastUsedAt)
	}
}

// TestHostDomain_Post_AutoConfigureDNS_NoMatchingCredential confirms
// the warn-and-proceed semantic: no credential covers the zone, dnsAuto
// reports success=false with a clear reason, but the reconfigure job is
// still enqueued (202).
func TestHostDomain_Post_AutoConfigureDNS_NoMatchingCredential(t *testing.T) {
	var upsertHits int64
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones: []stubZone{
			{ID: "zone-otherzone", Name: "otherzone.io"},
		},
		upsertHits: &upsertHits,
	})
	updater, tok := makeFakeUpdaterForReconfigure(t)

	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
		UpdaterURL:        updater.URL,
		UpdaterToken:      tok,
		// No PublicIP → DNS preflight skipped → reconfigure proceeds
		// regardless of DNS state. Lets us isolate the auto-config
		// behaviour without coupling to preflight.
	})
	owner := makeAdminUser(t, h)

	// Seed a Cloudflare credential — but its zone DOES NOT cover the
	// requested domain. The auto-config should refuse to act.
	h.DoJSON(http.MethodPost, "/v1/admin/dns_credentials/cloudflare",
		owner.AccessToken,
		map[string]string{"token": "valid-token", "label": "Other CF"},
		http.StatusCreated, &dnsCredentialResp{})

	body := map[string]any{
		"domain":           "synapse.example.com",
		"autoConfigureDns": true,
	}
	var got hostDomainPostRespForTest
	h.DoJSON(http.MethodPost, "/v1/admin/host_domain",
		owner.AccessToken, body, http.StatusAccepted, &got)

	if got.DNSAuto == nil {
		t.Fatalf("expected dnsAuto in response, got nil")
	}
	if !got.DNSAuto.Attempted {
		t.Errorf("dnsAuto.attempted: got false want true")
	}
	if got.DNSAuto.Success {
		t.Errorf("dnsAuto.success: got true; expected false (no matching zone)")
	}
	if got.DNSAuto.Reason == "" {
		t.Errorf("dnsAuto.reason: expected human-readable explanation, got empty")
	}
	// The mismatched-zone path must NOT issue any Cloudflare writes.
	if hits := atomic.LoadInt64(&upsertHits); hits != 0 {
		t.Errorf("expected 0 Cloudflare write hits with mismatched zone, got %d",
			hits)
	}
	// JobID still issued — warn-and-proceed.
	if got.JobID == "" {
		t.Errorf("expected JobID even when auto-config skipped")
	}
}

// TestHostDomain_Post_AutoConfigureDNS_OmittedFlag confirms the default
// path: when the operator doesn't pass autoConfigureDns, the response
// has no dnsAuto field and Cloudflare is never called.
func TestHostDomain_Post_AutoConfigureDNS_OmittedFlag(t *testing.T) {
	var upsertHits int64
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones: []stubZone{
			{ID: "zone-1", Name: "example.com"},
		},
		upsertHits: &upsertHits,
	})
	updater, tok := makeFakeUpdaterForReconfigure(t)

	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
		UpdaterURL:        updater.URL,
		UpdaterToken:      tok,
	})
	owner := makeAdminUser(t, h)

	// Seed credential, but DON'T tick autoConfigureDns. Cloudflare stub
	// must not see any write — the credential's existence alone shouldn't
	// trigger a side effect.
	h.DoJSON(http.MethodPost, "/v1/admin/dns_credentials/cloudflare",
		owner.AccessToken,
		map[string]string{"token": "valid-token", "label": "Personal CF"},
		http.StatusCreated, &dnsCredentialResp{})

	body := map[string]any{
		"domain": "synapse.example.com",
		// autoConfigureDns omitted → defaults to false.
	}
	var got hostDomainPostRespForTest
	h.DoJSON(http.MethodPost, "/v1/admin/host_domain",
		owner.AccessToken, body, http.StatusAccepted, &got)

	if got.DNSAuto != nil {
		t.Errorf("expected no dnsAuto in response when flag omitted, got %+v",
			got.DNSAuto)
	}
	if hits := atomic.LoadInt64(&upsertHits); hits != 0 {
		t.Errorf("expected 0 Cloudflare write hits with autoConfigureDns omitted, got %d",
			hits)
	}
}

// TestHostDomain_DNSAuto_IgnoresProjectScopedCredentials confirms the
// v1.6.5 P1 fix: the host-domain auto-DNS picker (instance scope) MUST
// NOT pull a project-scoped credential, even when the project happens
// to register one whose zones[] covers the operator's host domain.
//
// Without the fix this would silently use the tenant's Cloudflare
// token to mint a host A record — a cross-scope token use. The fix
// scopes the SELECT in findCloudflareCredentialForDomain to
// `project_id IS NULL`. With ONLY a project credential present, the
// auto-config response should report attempted=true, success=false,
// reason="no stored Cloudflare credential covers <host>".
func TestHostDomain_DNSAuto_IgnoresProjectScopedCredentials(t *testing.T) {
	stub := newCloudflareStub(t, &stubConfig{
		verifyResult: true,
		zones:        []stubZone{{ID: "zone-1", Name: "example.com"}},
	})
	updater, tok := makeFakeUpdaterForReconfigure(t)

	h := SetupWithOpts(t, SetupOpts{
		DNSEnvelope:       freshCryptoBox(t),
		CloudflareFactory: cloudflareFactoryForStub(stub),
		UpdaterURL:        updater.URL,
		UpdaterToken:      tok,
		PublicIP:          "203.0.113.10",
		HostDomainResolver: stubResolverFunc(func(host string) ([]string, error) {
			return []string{"203.0.113.10"}, nil
		}),
	})
	owner := makeAdminUser(t, h)

	// Seed a PROJECT credential whose zone covers the host domain.
	// This is the cross-scope case the fix prevents: same operator,
	// same Cloudflare account, but the credential lives under a
	// project (e.g. an agency stored the host's zone in a client
	// project's settings).
	team := createTeam(t, h, owner.AccessToken, "HostScope Co "+randHex(3))
	proj := createProject(t, h, owner.AccessToken, team.Slug, "HostScopeProj")
	var projCred dnsCredentialResp
	h.DoJSON(http.MethodPost, "/v1/projects/"+proj.ID+"/dns_credentials/cloudflare",
		owner.AccessToken,
		map[string]string{"token": "valid", "label": "project-cred"},
		http.StatusCreated, &projCred)
	if projCred.ProjectID == nil {
		t.Fatalf("expected project-scoped credential")
	}

	// Now request host_domain with autoConfigureDns=true. The picker
	// should walk the credentials filtered by project_id IS NULL,
	// find none, and return success=false, reason mentioning "no
	// stored Cloudflare credential".
	body := map[string]any{
		"domain":           "synapse.example.com",
		"autoConfigureDns": true,
	}
	var got hostDomainPostRespForTest
	h.DoJSON(http.MethodPost, "/v1/admin/host_domain",
		owner.AccessToken, body, http.StatusAccepted, &got)

	if got.DNSAuto == nil {
		t.Fatalf("expected dnsAuto in response, got nil")
	}
	if !got.DNSAuto.Attempted {
		t.Errorf("dnsAuto.attempted: got false want true")
	}
	if got.DNSAuto.Success {
		t.Errorf("dnsAuto.success: got true (project credential leaked into host scope!)")
	}
	if got.DNSAuto.CredentialID != "" {
		t.Errorf("dnsAuto.credentialId: got %q want empty (project cred must not be picked)",
			got.DNSAuto.CredentialID)
	}
	// Reason should mention that no covering credential exists. We
	// don't pin the exact string since the message lives in
	// translateCloudflareError-adjacent code, but it should be
	// non-empty and clearly indicate a missing credential.
	if got.DNSAuto.Reason == "" {
		t.Errorf("dnsAuto.reason: empty (operator can't tell why auto-config skipped)")
	}
}
