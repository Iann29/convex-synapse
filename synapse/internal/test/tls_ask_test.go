package synapsetest

import (
	"net/http"
	"net/url"
	"testing"
)

// /v1/internal/tls_ask is Caddy's on-demand TLS gate. We answer 200
// only when the asked-about host is a real `<deployment>.<BaseDomain>`
// and the deployment exists. Anything else (no BaseDomain, host
// outside base, multi-label subdomain, deleted deployment) is a
// non-200 so Caddy refuses to provision a Let's Encrypt cert.

func TestTLSAsk_DisabledWhenNoBaseDomain(t *testing.T) {
	h := Setup(t) // BaseDomain=""
	resp := h.Do(http.MethodGet,
		"/v1/internal/tls_ask?domain=foo.synapse.example.com", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("tls_ask without BaseDomain: status=%d want 404", resp.StatusCode)
	}
}

func TestTLSAsk_RejectsMissingDomainParam(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{BaseDomain: "synapse.example.com"})
	resp := h.Do(http.MethodGet, "/v1/internal/tls_ask", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("tls_ask without domain: status=%d want 400", resp.StatusCode)
	}
}

func TestTLSAsk_RejectsHostOutsideBase(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{BaseDomain: "synapse.example.com"})
	q := url.Values{"domain": {"evil.example.com"}}
	resp := h.Do(http.MethodGet, "/v1/internal/tls_ask?"+q.Encode(), "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("tls_ask out-of-base: status=%d want 403", resp.StatusCode)
	}
}

func TestTLSAsk_RejectsBaseItself(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{BaseDomain: "synapse.example.com"})
	q := url.Values{"domain": {"synapse.example.com"}}
	resp := h.Do(http.MethodGet, "/v1/internal/tls_ask?"+q.Encode(), "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("tls_ask base-itself: status=%d want 403", resp.StatusCode)
	}
}

func TestTLSAsk_RejectsMultiLabelSubdomain(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{BaseDomain: "synapse.example.com"})
	q := url.Values{"domain": {"a.b.synapse.example.com"}}
	resp := h.Do(http.MethodGet, "/v1/internal/tls_ask?"+q.Encode(), "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("tls_ask multi-label: status=%d want 403", resp.StatusCode)
	}
}

func TestTLSAsk_404UnknownDeployment(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{BaseDomain: "synapse.example.com"})
	q := url.Values{"domain": {"never-existed.synapse.example.com"}}
	resp := h.Do(http.MethodGet, "/v1/internal/tls_ask?"+q.Encode(), "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("tls_ask unknown deployment: status=%d want 404", resp.StatusCode)
	}
}

func TestTLSAsk_OkForRealDeployment(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{BaseDomain: "synapse.example.com"})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "TLS Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "TLS Proj")
	h.SeedDeployment(proj.ID, "bold-fox-1234", "dev", "running", true, owner.ID, 3242, "")

	q := url.Values{"domain": {"bold-fox-1234.synapse.example.com"}}
	resp := h.Do(http.MethodGet, "/v1/internal/tls_ask?"+q.Encode(), "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("tls_ask real deployment: status=%d want 200", resp.StatusCode)
	}
}

func TestTLSAsk_404ForDeletedDeployment(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{BaseDomain: "synapse.example.com"})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Deleted Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Deleted Proj")
	h.SeedDeployment(proj.ID, "gone-bee-9876", "dev", "deleted", true, owner.ID, 3242, "")

	q := url.Values{"domain": {"gone-bee-9876.synapse.example.com"}}
	resp := h.Do(http.MethodGet, "/v1/internal/tls_ask?"+q.Encode(), "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("tls_ask deleted deployment: status=%d want 404", resp.StatusCode)
	}
}

func TestTLSAsk_CaseInsensitiveHost(t *testing.T) {
	h := SetupWithOpts(t, SetupOpts{BaseDomain: "synapse.example.com"})
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Case Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "Case Proj")
	h.SeedDeployment(proj.ID, "tidy-fox-5555", "dev", "running", true, owner.ID, 3242, "")

	q := url.Values{"domain": {"TIDY-FOX-5555.SYNAPSE.EXAMPLE.COM"}}
	resp := h.Do(http.MethodGet, "/v1/internal/tls_ask?"+q.Encode(), "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("tls_ask uppercase host: status=%d want 200", resp.StatusCode)
	}
}
