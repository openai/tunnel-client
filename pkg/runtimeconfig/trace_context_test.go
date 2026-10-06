package runtimeconfig

import (
	"strings"
	"testing"
)

func TestForwardTraceContextPrecedenceAcrossFlavors(t *testing.T) {
	t.Parallel()

	for _, flavor := range []Flavor{FlavorFull, FlavorRuntime, FlavorRuntimeCloudflared} {
		for _, tc := range []struct {
			name, yaml, flag string
			env              *string
			want             bool
		}{
			{name: "legacy-default"},
			{name: "yaml-true", yaml: "true", want: true},
			{name: "yaml-false", yaml: "false"},
			{name: "environment-true", yaml: "false", env: traceContextEnvValue("true"), want: true},
			{name: "environment-false", yaml: "true", env: traceContextEnvValue("false")},
			{name: "environment-empty", yaml: "true", env: traceContextEnvValue("")},
			{name: "flag-true", yaml: "false", env: traceContextEnvValue("false"), flag: "true", want: true},
			{name: "flag-false", yaml: "true", env: traceContextEnvValue("true"), flag: "false"},
		} {
			t.Run(string(flavor)+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				var args []string
				if tc.yaml != "" {
					args = append(args, "--config", writeRuntimeConfig(t, "mcp:\n  forward_trace_context: "+tc.yaml+"\n"))
				}
				if tc.flag != "" {
					args = append(args, "--mcp.forward-trace-context="+tc.flag)
				}
				env := map[string]string{"CONTROL_PLANE_TUNNEL_ID": testTunnelID, "CONTROL_PLANE_API_KEY": testAPIKey, "MCP_COMMAND": "echo"}
				if tc.env != nil {
					env["MCP_FORWARD_TRACE_CONTEXT"] = *tc.env
				}
				cfg, err := Load(args, flavor, lookupEnvMap(env))
				if err != nil {
					t.Fatal(err)
				}
				if cfg.MCP.ForwardTraceContext != tc.want {
					t.Fatalf("ForwardTraceContext = %v, want %v", cfg.MCP.ForwardTraceContext, tc.want)
				}
			})
		}
	}
}

func TestForwardTraceContextRejectsInvalidEnvironmentUnlessFlagOverrides(t *testing.T) {
	t.Parallel()

	for _, flavor := range []Flavor{FlavorFull, FlavorRuntime, FlavorRuntimeCloudflared} {
		t.Run(string(flavor), func(t *testing.T) {
			t.Parallel()
			env := map[string]string{
				"CONTROL_PLANE_TUNNEL_ID":   testTunnelID,
				"CONTROL_PLANE_API_KEY":     testAPIKey,
				"MCP_COMMAND":               "echo",
				"MCP_FORWARD_TRACE_CONTEXT": "sometimes",
			}
			_, err := Load(nil, flavor, lookupEnvMap(env))
			if err == nil || !strings.Contains(err.Error(), "MCP_FORWARD_TRACE_CONTEXT") {
				t.Fatalf("expected invalid trace forwarding setting error, got %v", err)
			}
			cfg, err := Load([]string{"--mcp.forward-trace-context=false"}, flavor, lookupEnvMap(env))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MCP.ForwardTraceContext {
				t.Fatal("explicit false did not override invalid environment")
			}
		})
	}
}

func traceContextEnvValue(value string) *string { return &value }
