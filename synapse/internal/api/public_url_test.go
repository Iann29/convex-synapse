package api

import (
	"testing"

	"github.com/Iann29/synapse/internal/models"
)

// TestPublicDeploymentURL_LegacyShape: with PublicURL empty (the default),
// the handler returns whatever the provisioner stored — host-port URL for
// local dev. Behavior must be unchanged from pre-VPS deployments.
func TestPublicDeploymentURL_LegacyShape(t *testing.T) {
	h := &DeploymentsHandler{} // PublicURL="", ProxyEnabled=false
	d := &models.Deployment{
		Name:          "happy-cat-1234",
		HostPort:      3210,
		DeploymentURL: "http://127.0.0.1:3210",
	}
	if got := h.publicDeploymentURL(d); got != "http://127.0.0.1:3210" {
		t.Errorf("legacy: got %q want http://127.0.0.1:3210", got)
	}
}

// TestPublicDeploymentURL_ProxyMode: when running behind a reverse proxy
// with a known public origin, every request flows through /d/{name}/*.
// The CLI URL the operator pastes into a remote shell becomes the public
// proxy URL — works from anywhere reachable to the Synapse host.
func TestPublicDeploymentURL_ProxyMode(t *testing.T) {
	h := &DeploymentsHandler{
		PublicURL:    "https://synapse.example.com",
		ProxyEnabled: true,
	}
	d := &models.Deployment{
		Name:          "happy-cat-1234",
		HostPort:      3210,
		DeploymentURL: "http://127.0.0.1:3210",
	}
	want := "https://synapse.example.com/d/happy-cat-1234"
	if got := h.publicDeploymentURL(d); got != want {
		t.Errorf("proxy: got %q want %q", got, want)
	}
}

// TestPublicDeploymentURL_HostPortMode: PublicURL set, proxy disabled —
// the operator chose to expose each backend's host port directly. Caller
// gets <PublicURL>:<port>.
func TestPublicDeploymentURL_HostPortMode(t *testing.T) {
	h := &DeploymentsHandler{
		PublicURL:    "https://synapse.example.com",
		ProxyEnabled: false,
	}
	d := &models.Deployment{
		Name:          "happy-cat-1234",
		HostPort:      3210,
		DeploymentURL: "http://127.0.0.1:3210",
	}
	want := "https://synapse.example.com:3210"
	if got := h.publicDeploymentURL(d); got != want {
		t.Errorf("host-port: got %q want %q", got, want)
	}
}

// TestPublicDeploymentURL_AdoptedKeepsOperatorURL: adopted deployments
// already point at an operator-supplied URL (set when they were
// registered). PublicURL must NOT rewrite — Synapse doesn't proxy
// adopted backends and the operator-supplied URL is the right answer
// for both internal and external callers.
func TestPublicDeploymentURL_AdoptedKeepsOperatorURL(t *testing.T) {
	h := &DeploymentsHandler{
		PublicURL:    "https://synapse.example.com",
		ProxyEnabled: true,
	}
	d := &models.Deployment{
		Name:          "imported-app",
		Adopted:       true,
		DeploymentURL: "https://convex.my-other-host.example/api",
	}
	if got := h.publicDeploymentURL(d); got != "https://convex.my-other-host.example/api" {
		t.Errorf("adopted: got %q (rewrite leaked into adopted path)", got)
	}
}

// TestPublicDeploymentURL_HostPortFallback: PublicURL set, proxy off,
// but no host_port (extremely unusual — happens to a row mid-provision).
// Falling back to the legacy DeploymentURL is preferable to emitting
// a malformed "<PublicURL>:0".
func TestPublicDeploymentURL_HostPortFallback(t *testing.T) {
	h := &DeploymentsHandler{
		PublicURL:    "https://synapse.example.com",
		ProxyEnabled: false,
	}
	d := &models.Deployment{
		Name:          "no-port-yet",
		HostPort:      0,
		DeploymentURL: "http://127.0.0.1:3210",
	}
	if got := h.publicDeploymentURL(d); got != "http://127.0.0.1:3210" {
		t.Errorf("no host port: got %q want fallback to DeploymentURL", got)
	}
}

// TestPublicDeploymentURL_TrimsTrailingSlash: a defensive — config.Load()
// already strips trailing slashes from SYNAPSE_PUBLIC_URL, but if a
// future caller wires the handler directly we still want sane output.
// (No code change today; this test pins the contract for the future.)
func TestPublicDeploymentURL_NoDoubleSlash(t *testing.T) {
	h := &DeploymentsHandler{
		PublicURL:    "https://synapse.example.com", // no trailing slash
		ProxyEnabled: true,
	}
	d := &models.Deployment{Name: "happy-cat-1234"}
	want := "https://synapse.example.com/d/happy-cat-1234"
	if got := h.publicDeploymentURL(d); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
