package runtimeconfig

import (
	"reflect"
	"strings"
	"testing"
)

func TestLoadOAuthTrustedOrigins(t *testing.T) {
	t.Parallel()

	configPath := writeRuntimeConfig(t, `
mcp:
  oauth_trusted_origins:
    - https://yaml-auth.example.invalid
    - http://10.0.0.2:8080/
`)
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
					want: []string{"https://yaml-auth.example.invalid", "http://10.0.0.2:8080"},
				},
				{
					name: "environment overrides yaml",
					args: []string{"--config", configPath},
					env:  map[string]string{"MCP_OAUTH_TRUSTED_ORIGINS": "https://env-auth.example.invalid\nhttp://[::1]:8080/"},
					want: []string{"https://env-auth.example.invalid", "http://[::1]:8080"},
				},
				{
					name: "empty environment clears yaml",
					args: []string{"--config", configPath},
					env:  map[string]string{"MCP_OAUTH_TRUSTED_ORIGINS": ""},
				},
				{
					name: "empty flag clears environment and yaml",
					args: []string{"--config", configPath, "--mcp.oauth-trusted-origin="},
					env:  map[string]string{"MCP_OAUTH_TRUSTED_ORIGINS": "https://env-auth.example.invalid"},
				},
				{
					name: "repeated flags override environment and yaml",
					args: []string{"--config", configPath, "--mcp.oauth-trusted-origin", "https://flag-auth.example.invalid", "--mcp.oauth-trusted-origin", "http://localhost:9000/"},
					env:  map[string]string{"MCP_OAUTH_TRUSTED_ORIGINS": "https://env-auth.example.invalid"},
					want: []string{"https://flag-auth.example.invalid", "http://localhost:9000"},
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					env := map[string]string{
						"CONTROL_PLANE_API_KEY":   testAPIKey,
						"CONTROL_PLANE_TUNNEL_ID": testTunnelID,
						"MCP_SERVER_URL":          "https://mcp.example.invalid/mcp",
					}
					for key, value := range tc.env {
						env[key] = value
					}
					cfg, err := Load(tc.args, flavor, lookupEnvMap(env))
					if err != nil {
						t.Fatalf("Load: %v", err)
					}
					var got []string
					for _, origin := range cfg.MCP.OAuthTrustedOrigins {
						got = append(got, origin.String())
					}
					if !reflect.DeepEqual(got, tc.want) {
						t.Fatalf("OAuthTrustedOrigins = %v, want %v", got, tc.want)
					}
				})
			}
		})
	}
}

func TestLoadRejectsInvalidOAuthTrustedOrigins(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"auth.example.invalid", "//auth.example.invalid", "ftp://auth.example.invalid",
		"https:auth.example.invalid", "https://", "https://:443", "https://user:pass@auth.example.invalid",
		"https://auth.example.invalid/path", "https://auth.example.invalid/%2f", "https://auth.example.invalid?query=1",
		"https://auth.example.invalid?", "https://auth.example.invalid#fragment", "https://auth.example.invalid#",
		"https://auth.example.invalid:", "https://auth.example.invalid:0", "https://auth.example.invalid:65536",
		"https://auth.example.invalid:-1", "https://auth.example.invalid:abc", "https://auth.example.invalid:443:444",
		"https://[invalid]", "https://::1", "https://auth.example.invalid\nhttps://other.example.invalid",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			_, err := Load([]string{"--mcp.oauth-trusted-origin", raw}, FlavorRuntime, lookupEnvMap(map[string]string{
				"CONTROL_PLANE_API_KEY":   testAPIKey,
				"CONTROL_PLANE_TUNNEL_ID": testTunnelID,
				"MCP_SERVER_URL":          "https://mcp.example.invalid/mcp",
			}))
			if err == nil || !strings.Contains(err.Error(), "mcp.oauth-trusted-origin") {
				t.Fatalf("Load(%q) error = %v, want invalid trusted-origin error", raw, err)
			}
		})
	}
}

func TestOAuthTrustedOriginsYAMLRejectsMultipleOriginsInOneElement(t *testing.T) {
	t.Parallel()

	configPath := writeRuntimeConfig(t, `
mcp:
  oauth_trusted_origins:
    - "https://auth.example.invalid\nhttps://other.example.invalid"
`)
	env := map[string]string{
		"CONTROL_PLANE_API_KEY":   testAPIKey,
		"CONTROL_PLANE_TUNNEL_ID": testTunnelID,
		"MCP_SERVER_URL":          "https://mcp.example.invalid/mcp",
	}
	if _, err := Load([]string{"--config", configPath}, FlavorRuntime, lookupEnvMap(env)); err == nil || !strings.Contains(err.Error(), "mcp.oauth_trusted_origins") {
		t.Fatalf("Load error = %v, want invalid YAML trusted-origin error", err)
	}
	env["MCP_OAUTH_TRUSTED_ORIGINS"] = "https://override.example.invalid"
	if _, err := Load([]string{"--config", configPath}, FlavorRuntime, lookupEnvMap(env)); err != nil {
		t.Fatalf("overridden YAML trusted origins should not be validated: %v", err)
	}
}
