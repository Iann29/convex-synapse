package synapsetest

import (
	"encoding/json"
	"net/http"
	"testing"
)

type installStatusResp struct {
	FirstRun bool   `json:"firstRun"`
	Version  string `json:"version"`
}

// /install_status is unauthenticated (the dashboard hits it pre-auth)
// and reports firstRun=true iff the users table is empty. The first
// register flips it to false. The endpoint underpins the v0.6.3
// first-run wizard: dashboard /login redirects to /setup when
// firstRun=true.

func TestInstallStatus_FreshInstall(t *testing.T) {
	h := Setup(t)
	resp := h.Do(http.MethodGet, "/v1/install_status", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/install_status: status=%d want 200", resp.StatusCode)
	}
	var body installStatusResp
	dec := json.NewDecoder(resp.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		t.Fatalf("/install_status: decode: %v", err)
	}
	if !body.FirstRun {
		t.Errorf("/install_status: firstRun=false on fresh DB; want true")
	}
	if body.Version == "" {
		t.Errorf("/install_status: version empty; want non-empty")
	}
}

func TestInstallStatus_AfterFirstRegister(t *testing.T) {
	h := Setup(t)
	_ = h.RegisterRandomUser()
	resp := h.Do(http.MethodGet, "/v1/install_status", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/install_status: status=%d want 200", resp.StatusCode)
	}
	var body installStatusResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("/install_status: decode: %v", err)
	}
	if body.FirstRun {
		t.Errorf("/install_status: firstRun=true after register; want false")
	}
}

func TestInstallStatus_NoAuthRequired(t *testing.T) {
	h := Setup(t)
	// Hit with bogus token — should still 200 (handler is mounted outside auth group).
	resp := h.Do(http.MethodGet, "/v1/install_status", "Bearer not-a-real-token", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/install_status: status=%d with bogus token; want 200 (unauthenticated)", resp.StatusCode)
	}
}
