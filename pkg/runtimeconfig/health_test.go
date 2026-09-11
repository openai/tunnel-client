package runtimeconfig

import (
	"strings"
	"testing"
)

func TestHealthDetailsPrecedenceAcrossFlavors(t *testing.T) {
	for _, flavor := range []Flavor{FlavorFull, FlavorRuntime, FlavorRuntimeCloudflared} {
		for _, tc := range []struct {
			name, yaml, env, flag string
			want                  bool
		}{
			{name: "legacy-default"},
			{name: "yaml", yaml: "true", want: true},
			{name: "environment", yaml: "false", env: "true", want: true},
			{name: "environment-false", yaml: "true", env: "false"},
			{name: "flag", yaml: "false", env: "false", flag: "true", want: true},
			{name: "flag-false", yaml: "true", env: "true", flag: "false"},
		} {
			t.Run(string(flavor)+"/"+tc.name, func(t *testing.T) {
				args := []string{}
				if tc.yaml != "" {
					args = append(args, "--config", writeRuntimeConfig(t, "health:\n  show_details: "+tc.yaml+"\n"))
				}
				if tc.flag != "" {
					args = append(args, "--health.show-details="+tc.flag)
				}
				env := map[string]string{"CONTROL_PLANE_TUNNEL_ID": testTunnelID, "CONTROL_PLANE_API_KEY": testAPIKey, "MCP_COMMAND": "echo"}
				if tc.env != "" {
					env["HEALTH_SHOW_DETAILS"] = tc.env
				}
				cfg, err := LoadFromFlagSet(runtimeFlagSet(t, flavor, args...), lookupEnvMap(env))
				if err != nil {
					t.Fatal(err)
				}
				if cfg.Health.ShowDetails != tc.want {
					t.Fatalf("ShowDetails = %v, want %v", cfg.Health.ShowDetails, tc.want)
				}
				if cfg.Health.ListenAddr != defaultHealthListenAddr || cfg.Health.UnixSocket != "" || cfg.Health.URLFile != "" {
					t.Fatalf("legacy listener defaults changed: %+v", cfg.Health)
				}
			})
		}
	}
}

func TestHealthDetailsInvalidEnvironment(t *testing.T) {
	env := map[string]string{"CONTROL_PLANE_TUNNEL_ID": testTunnelID, "CONTROL_PLANE_API_KEY": testAPIKey, "MCP_COMMAND": "echo", "HEALTH_SHOW_DETAILS": "sometimes"}
	_, err := LoadFromFlagSet(runtimeFlagSet(t, FlavorRuntime), lookupEnvMap(env))
	if err == nil || !strings.Contains(err.Error(), "HEALTH_SHOW_DETAILS") {
		t.Fatalf("expected configuration error, got %v", err)
	}
	// An explicit flag wins even over an invalid lower-precedence environment value.
	cfg, err := LoadFromFlagSet(runtimeFlagSet(t, FlavorRuntime, "--health.show-details=false"), lookupEnvMap(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Health.ShowDetails {
		t.Fatal("explicit false was lost")
	}
}
