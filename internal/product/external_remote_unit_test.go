// SPDX-License-Identifier: MPL-2.0
package product

import (
	"errors"
	"testing"
)

func TestRemoteConfigRequiresDeliberateIsolatedOptIn(t *testing.T) {
	valid := func() Config {
		return Config{
			Control: ControlConfig{Origin: "https://control.example.test", DatabaseEnv: "RIXA_CONTROL"},
			Sites: []SiteConfig{{
				ID: "a", Origin: "https://a.public.example.test",
				ManagementOrigin: "https://a.manage.example.test",
				PreviewOrigin:    "https://a.preview.example.test",
				DatabaseEnv:      "RIXA_A", MediaRoot: "/var/lib/rixa/a",
				Remote: RemoteConfig{Enabled: true, Clients: []RemoteClientConfig{{
					ID: "manual-approved-client", RedirectURIs: []string{"https://client.example/cb"}, Enabled: true,
				}}},
			}},
		}
	}
	if err := valid().validateStructure(); err != nil {
		t.Fatalf("valid staging config: %v", err)
	}
	cases := []struct {
		name   string
		modify func(*Config)
	}{
		{"absent client", func(c *Config) { c.Sites[0].Remote.Clients = nil }},
		{"unisolated management", func(c *Config) { c.Sites[0].ManagementOrigin = ""; c.Sites[0].PreviewOrigin = "" }},
		{"disabled site", func(c *Config) { c.Sites[0].Disabled = true }},
		{"unintended pre-enrollment", func(c *Config) { c.Sites[0].Remote.Enabled = false }},
		{"duplicate clients", func(c *Config) {
			c.Sites[0].Remote.Clients = append(c.Sites[0].Remote.Clients, c.Sites[0].Remote.Clients[0])
		}},
		{"non-HTTPS callback", func(c *Config) { c.Sites[0].Remote.Clients[0].RedirectURIs = []string{"http://client.example/cb"} }},
		{"callback with fragment", func(c *Config) {
			c.Sites[0].Remote.Clients[0].RedirectURIs = []string{"https://client.example/cb#fragment"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.modify(&c)
			if err := c.validateStructure(); !errors.Is(err, ErrConfiguration) {
				t.Fatalf("invalid remote config was accepted: %v", err)
			}
		})
	}
	c := valid()
	c.Sites[0].Remote = RemoteConfig{}
	if err := c.validateStructure(); err != nil {
		t.Fatalf("default disabled must remain compatible: %v", err)
	}
}
