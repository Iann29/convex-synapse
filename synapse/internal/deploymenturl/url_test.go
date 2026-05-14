package deploymenturl

import (
	"testing"

	"github.com/Iann29/synapse/internal/models"
)

func TestComputer_Public(t *testing.T) {
	d := &models.Deployment{Name: "happy-cat-1234", HostPort: 3211, DeploymentURL: "http://127.0.0.1:3211"}
	cases := []struct {
		name   string
		comp   Computer
		dep    *models.Deployment
		domain string
		want   string
	}{
		{
			name: "adopted wins over everything",
			comp: Computer{PublicURL: "https://x.test", ProxyEnabled: true, BaseDomain: "syn.test"},
			dep: &models.Deployment{
				Name: "ext", Adopted: true, DeploymentURL: "https://operator.example.com",
				HostPort: 9999,
			},
			domain: "api.client.com",
			want:   "https://operator.example.com",
		},
		{
			name:   "active api domain wins over BaseDomain",
			comp:   Computer{PublicURL: "https://x.test", BaseDomain: "syn.test"},
			dep:    d,
			domain: "api.client.com",
			want:   "https://api.client.com",
		},
		{
			name:   "BaseDomain wins over PublicURL",
			comp:   Computer{PublicURL: "https://x.test", ProxyEnabled: true, BaseDomain: "syn.test"},
			dep:    d,
			domain: "",
			want:   "https://happy-cat-1234.syn.test",
		},
		{
			name:   "empty PublicURL falls back to row",
			comp:   Computer{},
			dep:    d,
			domain: "",
			want:   "http://127.0.0.1:3211",
		},
		{
			name:   "proxy mode produces path URL",
			comp:   Computer{PublicURL: "https://x.test", ProxyEnabled: true},
			dep:    d,
			domain: "",
			want:   "https://x.test/d/happy-cat-1234",
		},
		{
			name:   "no-proxy mode produces host:port",
			comp:   Computer{PublicURL: "https://x.test", ProxyEnabled: false},
			dep:    d,
			domain: "",
			want:   "https://x.test:3211",
		},
		{
			name:   "no-proxy with HostPort=0 falls back",
			comp:   Computer{PublicURL: "https://x.test", ProxyEnabled: false},
			dep:    &models.Deployment{Name: "x", HostPort: 0, DeploymentURL: "http://127.0.0.1:0"},
			domain: "",
			want:   "http://127.0.0.1:0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.comp.Public(tc.dep, tc.domain)
			if got != tc.want {
				t.Errorf("Public: got %q want %q", got, tc.want)
			}
		})
	}
}

func TestComputer_CLI(t *testing.T) {
	d := &models.Deployment{Name: "happy-cat-1234", HostPort: 3211, DeploymentURL: "http://127.0.0.1:3211"}
	cases := []struct {
		name   string
		comp   Computer
		dep    *models.Deployment
		domain string
		want   string
	}{
		{
			name:   "active api domain wins (no port, ready for CLI)",
			comp:   Computer{PublicURL: "https://x.test:8080", BaseDomain: "syn.test"},
			dep:    d,
			domain: "api.client.com",
			want:   "https://api.client.com",
		},
		{
			name:   "BaseDomain wins over PublicURL",
			comp:   Computer{PublicURL: "https://x.test:8080", BaseDomain: "syn.test"},
			dep:    d,
			domain: "",
			want:   "https://happy-cat-1234.syn.test",
		},
		{
			name:   "PublicURL with HostPort -> scheme://host:HostPort (drops PublicURL port)",
			comp:   Computer{PublicURL: "https://x.test:8080", ProxyEnabled: true},
			dep:    d,
			domain: "",
			want:   "https://x.test:3211",
		},
		{
			name:   "ProxyEnabled does NOT produce a path URL (CLI needs host-anchored)",
			comp:   Computer{PublicURL: "https://x.test", ProxyEnabled: true},
			dep:    d,
			domain: "",
			want:   "https://x.test:3211",
		},
		{
			name:   "no config falls back to row URL",
			comp:   Computer{},
			dep:    d,
			domain: "",
			want:   "http://127.0.0.1:3211",
		},
		{
			name:   "HostPort=0 falls back to row URL",
			comp:   Computer{PublicURL: "https://x.test"},
			dep:    &models.Deployment{Name: "x", HostPort: 0, DeploymentURL: "http://127.0.0.1:0"},
			domain: "",
			want:   "http://127.0.0.1:0",
		},
		{
			name:   "malformed PublicURL falls back to row URL",
			comp:   Computer{PublicURL: "://oops"},
			dep:    d,
			domain: "",
			want:   "http://127.0.0.1:3211",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.comp.CLI(tc.dep, tc.domain)
			if got != tc.want {
				t.Errorf("CLI: got %q want %q", got, tc.want)
			}
		})
	}
}

// TestNilDeployment: every helper must tolerate a nil receiver argument
// instead of panicking — callers under transient db blips can construct
// a partial models.Deployment, and an empty pointer is safer to fail
// gracefully on than a crash.
func TestNilDeployment(t *testing.T) {
	c := Computer{PublicURL: "https://x.test"}
	if got := c.Public(nil, ""); got != "" {
		t.Errorf("Public(nil) = %q, want empty", got)
	}
	if got := c.CLI(nil, ""); got != "" {
		t.Errorf("CLI(nil) = %q, want empty", got)
	}
}
