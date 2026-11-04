package runtimeconfig

import (
	"bytes"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
)

func TestLoadOAuthForwardOrigins(t *testing.T) {
	t.Parallel()

	profile := []byte(`mcp:
  oauth_trusted_origins: [https://discovery.example.invalid]
harpoon:
  oauth_forward_origins:
    - https://yaml-auth.example.invalid/
    - https://10.0.0.2:8443
`)
	configPath := writeRuntimeConfig(t, string(profile))
	for _, flavor := range []Flavor{FlavorFull, FlavorRuntime, FlavorRuntimeCloudflared} {
		t.Run(string(flavor), func(t *testing.T) {
			t.Parallel()
			for _, tc := range []struct {
				name string
				args []string
				env  map[string]string
				want []string
			}{
				{name: "default"},
				{
					name: "yaml",
					args: []string{"--config", configPath},
					want: []string{"https://yaml-auth.example.invalid", "https://10.0.0.2:8443"},
				},
				{
					name: "environment replaces yaml",
					args: []string{"--config", configPath},
					env:  map[string]string{"HARPOON_OAUTH_FORWARD_ORIGINS": "https://env-auth.example.invalid\nhttps://[::1]:8443/"},
					want: []string{"https://env-auth.example.invalid", "https://[::1]:8443"},
				},
				{
					name: "forwarding does not grant discovery trust",
					env:  map[string]string{"HARPOON_OAUTH_FORWARD_ORIGINS": "https://env-auth.example.invalid"},
					want: []string{"https://env-auth.example.invalid"},
				},
				{
					name: "empty environment clears yaml",
					args: []string{"--config", configPath},
					env:  map[string]string{"HARPOON_OAUTH_FORWARD_ORIGINS": ""},
				},
				{
					name: "empty flag clears environment and yaml",
					args: []string{"--config", configPath, "--harpoon.oauth-forward-origin="},
					env:  map[string]string{"HARPOON_OAUTH_FORWARD_ORIGINS": "https://env-auth.example.invalid"},
				},
				{
					name: "repeated flags replace environment and yaml",
					args: []string{"--config", configPath, "--harpoon.oauth-forward-origin", "https://flag-auth.example.invalid", "--harpoon.oauth-forward-origin", "https://localhost:8443/"},
					env:  map[string]string{"HARPOON_OAUTH_FORWARD_ORIGINS": "https://env-auth.example.invalid"},
					want: []string{"https://flag-auth.example.invalid", "https://localhost:8443"},
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					env := map[string]string{
						"CONTROL_PLANE_API_KEY":   testAPIKey,
						"CONTROL_PLANE_TUNNEL_ID": testTunnelID,
						"MCP_SERVER_URL":          "https://mcp.example.invalid/mcp",
					}
					maps.Copy(env, tc.env)
					cfg, err := Load(tc.args, flavor, lookupEnvMap(env))
					if err != nil {
						t.Fatalf("Load: %v", err)
					}
					var got []string
					for _, origin := range cfg.Harpoon.OAuthForwardOrigins {
						got = append(got, origin.String())
					}
					if !reflect.DeepEqual(got, tc.want) {
						t.Fatalf("OAuthForwardOrigins = %v, want %v", got, tc.want)
					}
					if len(tc.args) > 0 {
						if len(cfg.MCP.OAuthTrustedOrigins) != 1 || cfg.MCP.OAuthTrustedOrigins[0].String() != "https://discovery.example.invalid" {
							t.Fatalf("forwarding configuration changed discovery trust: %v", cfg.MCP.OAuthTrustedOrigins)
						}
						if !bytes.Equal(cfg.Runtime.ConfigFileContents, profile) {
							t.Fatal("loading forwarding policy changed the original profile bytes")
						}
					} else if len(cfg.MCP.OAuthTrustedOrigins) != 0 {
						t.Fatalf("forwarding origins implicitly granted discovery trust: %v", cfg.MCP.OAuthTrustedOrigins)
					}
				})
			}
		})
	}
}

func TestValidateProfileOAuthForwardOrigins(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		origins string
		wantErr bool
	}{
		{name: "omitted"},
		{name: "empty list", origins: "[]"},
		{name: "public and private HTTPS origins", origins: "[https://auth.example.invalid, 'https://[::1]:8443/', https://10.0.0.2]"},
		{name: "plaintext", origins: "[http://auth.example.invalid]", wantErr: true},
		{name: "path", origins: "[https://auth.example.invalid/path]", wantErr: true},
		{name: "credentials", origins: "[https://user:password@auth.example.invalid]", wantErr: true},
		{name: "invalid port", origins: "[https://auth.example.invalid:65536]", wantErr: true},
		{name: "IPv6 zone", origins: "['https://[fe80::1%25eth0]:8443']", wantErr: true},
		{name: "empty origin", origins: "['']", wantErr: true},
		{name: "multiple origins in one element", origins: "[\"https://auth.example.invalid\\nhttps://other.example.invalid\"]", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := "control_plane:\n  api_key: env:UNRESOLVED_API_KEY\nmcp:\n  server_urls:\n    - url: env:UNRESOLVED_MCP_URL\n"
			if tc.origins != "" {
				data += "harpoon:\n  oauth_forward_origins: " + tc.origins + "\n"
			}
			for name, validate := range map[string]func(string, []byte) error{
				"runtime": ValidateProfileBytes,
				"full":    ValidateFullProfileBytes,
			} {
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					err := validate("profile.yaml", []byte(data))
					if tc.wantErr {
						if err == nil || !strings.Contains(err.Error(), "harpoon.oauth_forward_origins") {
							t.Fatalf("profile validation error = %v, want invalid forwarding-origin error", err)
						}
					} else if err != nil {
						t.Fatalf("profile validation: %v", err)
					}
				})
			}
		})
	}
}

func TestOAuthForwardOriginsOverrideInvalidLowerPrecedence(t *testing.T) {
	t.Parallel()

	configPath := writeRuntimeConfig(t, "harpoon:\n  oauth_forward_origins: [\"https://yaml-auth.example.invalid\\nhttps://other.example.invalid\"]\n")
	for _, flavor := range []Flavor{FlavorFull, FlavorRuntime, FlavorRuntimeCloudflared} {
		for _, args := range [][]string{{"--harpoon.oauth-forward-origin="}, {"--harpoon.oauth-forward-origin", ""}} {
			t.Run(fmt.Sprintf("%s/%q", flavor, args), func(t *testing.T) {
				t.Parallel()
				env := map[string]string{
					"CONTROL_PLANE_API_KEY":         testAPIKey,
					"CONTROL_PLANE_TUNNEL_ID":       testTunnelID,
					"MCP_SERVER_URL":                "https://mcp.example.invalid/mcp",
					"HARPOON_OAUTH_FORWARD_ORIGINS": "http://env-auth.example.invalid",
				}
				cfg, err := Load(append([]string{"--config", configPath}, args...), flavor, lookupEnvMap(env))
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				if len(cfg.Harpoon.OAuthForwardOrigins) != 0 {
					t.Fatalf("OAuthForwardOrigins = %v, want empty", cfg.Harpoon.OAuthForwardOrigins)
				}
				env["HARPOON_OAUTH_FORWARD_ORIGINS"] = "https://override.example.invalid"
				if _, err := Load([]string{"--config", configPath}, flavor, lookupEnvMap(env)); err != nil {
					t.Fatalf("environment should override invalid YAML: %v", err)
				}
				delete(env, "HARPOON_OAUTH_FORWARD_ORIGINS")
				if _, err := Load([]string{"--config", configPath}, flavor, lookupEnvMap(env)); err == nil || !strings.Contains(err.Error(), "harpoon.oauth_forward_origins") {
					t.Fatalf("Load error = %v, want invalid YAML forwarding origin", err)
				}
			})
		}
	}
}

func TestLoadRejectsInvalidOAuthForwardOrigins(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"auth.example.invalid", "//auth.example.invalid", "http://auth.example.invalid", "http://127.0.0.1:8443",
		"https:auth.example.invalid", "https://", "https://:443", "https://user:pass@auth.example.invalid",
		"https://auth.example.invalid/path", "https://auth.example.invalid/%2f", "https://auth.example.invalid?query=1",
		"https://auth.example.invalid?", "https://auth.example.invalid#fragment", "https://auth.example.invalid#",
		"https://auth.example.invalid:", "https://auth.example.invalid:0", "https://auth.example.invalid:65536",
		"https://auth.example.invalid:-1", "https://auth.example.invalid:abc", "https://auth.example.invalid:443:444",
		"https://[invalid]", "https://::1", "https://auth.example.invalid\nhttps://other.example.invalid",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			_, err := Load([]string{"--harpoon.oauth-forward-origin", raw, "--harpoon.allow-plaintext-http"}, FlavorRuntime, lookupEnvMap(map[string]string{
				"CONTROL_PLANE_API_KEY":   testAPIKey,
				"CONTROL_PLANE_TUNNEL_ID": testTunnelID,
				"MCP_SERVER_URL":          "https://mcp.example.invalid/mcp",
			}))
			if err == nil || !strings.Contains(err.Error(), "harpoon.oauth-forward-origin") {
				t.Fatalf("Load(%q) error = %v, want invalid forwarding-origin error", raw, err)
			}
		})
	}
}

func TestOAuthForwardOriginsRejectIPv6ZonesWithoutChangingDiscoveryTrust(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"https://[fe80::1%25eth0]:8443", "https://[fe80::1%25Ethernet]:8443"} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			lookup := lookupEnvMap(map[string]string{
				"CONTROL_PLANE_API_KEY":   testAPIKey,
				"CONTROL_PLANE_TUNNEL_ID": testTunnelID,
				"MCP_SERVER_URL":          "https://mcp.example.invalid/mcp",
			})
			cfg, err := Load([]string{"--mcp.oauth-trusted-origin", raw}, FlavorRuntime, lookup)
			if err != nil {
				t.Fatalf("existing discovery-origin syntax must remain supported: %v", err)
			}
			if len(cfg.MCP.OAuthTrustedOrigins) != 1 || cfg.MCP.OAuthTrustedOrigins[0].String() != raw {
				t.Fatalf("discovery origin changed: %v", cfg.MCP.OAuthTrustedOrigins)
			}
			_, err = Load([]string{"--harpoon.oauth-forward-origin", raw}, FlavorRuntime, lookup)
			if err == nil || !strings.Contains(err.Error(), "IPv6 zone identifier") {
				t.Fatalf("forwarding origin validation error = %v, want IPv6 zone rejection", err)
			}
		})
	}
}
