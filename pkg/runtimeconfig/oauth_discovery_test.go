package runtimeconfig

import (
	"strings"
	"testing"
)

func TestOAuthDiscoveryConfiguration(t *testing.T) {
	t.Parallel()

	profile := writeRuntimeConfig(t, "mcp:\n  oauth_discovery: disabled\n")
	for _, flavor := range []Flavor{FlavorFull, FlavorRuntime, FlavorRuntimeCloudflared} {
		for _, tc := range []struct {
			name     string
			profile  bool
			env      string
			flags    []string
			disabled bool
			wantErr  string
		}{
			{name: "default"},
			{name: "profile disabled", profile: true, disabled: true},
			{name: "environment disabled", env: "disabled", disabled: true},
			{name: "environment over profile", profile: true, env: "auto"},
			{name: "flag over environment", env: "disabled", flags: []string{"--mcp.oauth-discovery=auto"}},
			{name: "flag over invalid environment", env: "typo", flags: []string{"--mcp.oauth-discovery=disabled"}, disabled: true},
			{name: "invalid environment", env: "typo", wantErr: "MCP_OAUTH_DISCOVERY"},
			{name: "empty flag", flags: []string{"--mcp.oauth-discovery="}, wantErr: "--mcp.oauth-discovery"},
		} {
			t.Run(string(flavor)+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				env := map[string]string{
					"CONTROL_PLANE_TUNNEL_ID": testTunnelID,
					"CONTROL_PLANE_API_KEY":   testAPIKey,
					"MCP_SERVER_URL":          "https://mcp.example.invalid/mcp",
				}
				if tc.env != "" {
					env["MCP_OAUTH_DISCOVERY"] = tc.env
				}
				var args []string
				if tc.profile {
					args = append(args, "--config", profile)
				}
				cfg, err := Load(append(args, tc.flags...), flavor, lookupEnvMap(env))
				if tc.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
						t.Fatalf("Load error = %v, want %s error", err, tc.wantErr)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if cfg.MCP.DisableOAuthDiscovery != tc.disabled {
					t.Errorf("DisableOAuthDiscovery = %t, want %t", cfg.MCP.DisableOAuthDiscovery, tc.disabled)
				}
			})
		}
	}
}

func TestValidateProfileRejectsUnknownOAuthDiscoveryMode(t *testing.T) {
	t.Parallel()

	err := ValidateProfileBytes("profile.yaml", []byte("mcp:\n  oauth_discovery: typo\n"))
	if err == nil || !strings.Contains(err.Error(), "mcp.oauth_discovery") {
		t.Fatalf("ValidateProfileBytes error = %v, want mcp.oauth_discovery error", err)
	}
}
