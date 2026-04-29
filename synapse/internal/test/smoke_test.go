package synapsetest

import (
	"net/http"
	"testing"
)

// TestHarness_Smoke is a 1-second sanity check that Setup returns a working
// server with a fresh database. Any harness regression should fail here first.
func TestHarness_Smoke(t *testing.T) {
	h := Setup(t)
	resp := h.Do(http.MethodGet, "/health", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health: status=%d want 200", resp.StatusCode)
	}
}
