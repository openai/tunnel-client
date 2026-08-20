package runtimeconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

const (
	testTunnelID = "tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testAPIKey   = "sk_test_key"
)

func TestRegisterFlagsRuntimeOmitsFullOnlyAndCloudflared(t *testing.T) {
	fs := pflag.NewFlagSet("runtime", pflag.ContinueOnError)
	RegisterFlags(fs, FlavorRuntime)

	for _, name := range append(append([]string{}, fullOnlyFlags...), cloudflaredFlags...) {
		if got := fs.Lookup(name); got != nil {
			t.Fatalf("runtime flag set unexpectedly registers %q", name)
		}
	}
	for _, name := range []string{"control-plane.tunnel-id", "mcp.server-url", "mcp.command", "harpoon.target", "pid.file"} {
		if got := fs.Lookup(name); got == nil {
			t.Fatalf("runtime flag set does not register %q", name)
		}
	}
}

func TestRegisterFlagsCloudflaredAddsApprovedCompanionFlags(t *testing.T) {
	fs := pflag.NewFlagSet("runtime-cloudflared", pflag.ContinueOnError)
	RegisterFlags(fs, FlavorRuntimeCloudflared)

	for _, name := range cloudflaredFlags {
		if got := fs.Lookup(name); got == nil {
			t.Fatalf("runtime-cloudflared flag set does not register %q", name)
		}
	}
	for _, name := range fullOnlyFlags {
		if got := fs.Lookup(name); got != nil {
			t.Fatalf("runtime-cloudflared flag set unexpectedly registers %q", name)
		}
	}
}

func TestRuntimeSettingAllowlistsStayExplicitAndCurrent(t *testing.T) {
	assertExactRuntimeSettingList(t, "full-only flags", fullOnlyFlags, []string{
		"admin-ui.log-buffer-events",
		"allow-remote-ui",
		"harpoon.capture-payloads",
		"open-web-ui",
		"proxy.check-interval",
	})
	fullOnlyEnvironmentNames := make([]string, 0, len(fullOnlyEnv))
	for _, setting := range fullOnlyEnv {
		fullOnlyEnvironmentNames = append(fullOnlyEnvironmentNames, setting.name)
	}
	assertExactRuntimeSettingList(t, "full-only environment", fullOnlyEnvironmentNames, []string{
		"ADMIN_UI_LOG_BUFFER_EVENTS",
		"ALLOW_REMOTE_UI",
		"HARPOON_CAPTURE_PAYLOADS",
		"OPEN_WEB_UI",
		"PROXY_CHECK_INTERVAL",
	})
	assertExactRuntimeSettingList(t, "cloudflared flags", cloudflaredFlags, []string{
		"cloudflared.managed",
		"cloudflared.path",
		"cloudflared.ready-timeout",
		"cloudflared.token",
	})
	assertExactRuntimeSettingList(t, "cloudflared environment", cloudflaredEnv, []string{
		"CLOUDFLARED_MANAGED",
		"CLOUDFLARED_PATH",
		"CLOUDFLARED_READY_TIMEOUT",
		"CLOUDFLARED_TUNNEL_TOKEN",
	})
}

func TestLoadFromFlagSetPreservesFlagsEnvYAMLDefaultsPrecedence(t *testing.T) {
	configPath := writeRuntimeConfig(t, `
config_version: 1
control_plane:
  tunnel_id: tunnel_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
  api_key: env:YAML_API_KEY
  base_url: https://yaml.example.invalid
mcp:
  server_urls:
    - url: https://yaml-mcp.example.invalid/mcp
health:
  listen_addr: 127.0.0.1:7777
`)
	fs := runtimeFlagSet(t, FlavorRuntime,
		"--config", configPath,
		"--control-plane.tunnel-id", testTunnelID,
		"--mcp.server-url", "https://flag-mcp.example.invalid/mcp",
	)
	env := map[string]string{
		"CONTROL_PLANE_API_KEY":  testAPIKey,
		"YAML_API_KEY":           "yaml_api_key",
		"CONTROL_PLANE_BASE_URL": "https://env.example.invalid",
		"HEALTH_LISTEN_ADDR":     "127.0.0.1:9999",
	}

	cfg, err := LoadFromFlagSet(fs, lookupEnvMap(env))
	if err != nil {
		t.Fatalf("LoadFromFlagSet returned error: %v", err)
	}
	if got := cfg.ControlPlane.TunnelID.String(); got != testTunnelID {
		t.Fatalf("tunnel id = %q, want flag value %q", got, testTunnelID)
	}
	if got := cfg.ControlPlane.BaseURL.String(); got != "https://env.example.invalid" {
		t.Fatalf("base URL = %q, want environment value", got)
	}
	if got := cfg.MCP.ServerURL.String(); got != "https://flag-mcp.example.invalid/mcp" {
		t.Fatalf("MCP URL = %q, want flag value", got)
	}
	if got := cfg.Health.ListenAddr; got != "127.0.0.1:9999" {
		t.Fatalf("health listen address = %q, want environment value", got)
	}
	if got := cfg.Process.PIDFile; got != "" {
		t.Fatalf("PID default = %q, want empty", got)
	}
}

func TestRuntimeAcceptsDisabledGeneratedProfileAdminUICompatibilityField(t *testing.T) {
	configPath := writeRuntimeConfig(t, `
config_version: 1
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: env:CONTROL_PLANE_API_KEY
mcp:
  server_urls:
    - url: https://mcp.example.invalid/mcp
admin_ui:
  open_browser: false
`)
	fs := runtimeFlagSet(t, FlavorRuntime, "--config", configPath)
	if _, err := LoadFromFlagSet(fs, lookupEnvMap(map[string]string{"CONTROL_PLANE_API_KEY": testAPIKey})); err != nil {
		t.Fatalf("runtime rejected full-client-generated profile compatibility field: %v", err)
	}
}

func TestRuntimeRejectsNonDefaultAdminUIKeys(t *testing.T) {
	for key, value := range map[string]string{"allow_remote": "true", "log_buffer_events": "2001"} {
		t.Run(key, func(t *testing.T) {
			configPath := writeRuntimeConfig(t, `
config_version: 1
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: env:CONTROL_PLANE_API_KEY
mcp:
  server_urls:
    - url: https://mcp.example.invalid/mcp
admin_ui:
  `+key+`: `+value+`
`)
			fs := runtimeFlagSet(t, FlavorRuntime, "--config", configPath)
			_, err := LoadFromFlagSet(fs, lookupEnvMap(map[string]string{"CONTROL_PLANE_API_KEY": testAPIKey}))
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("error = %v, want rejection mentioning %q", err, key)
			}
		})
	}
}

func TestRuntimeAcceptsDefaultEquivalentFullOnlyEnvironment(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{name: "ALLOW_REMOTE_UI", value: "false"},
		{name: "OPEN_WEB_UI", value: "false"},
		{name: "ADMIN_UI_LOG_BUFFER_EVENTS", value: "2000"},
		{name: "PROXY_CHECK_INTERVAL", value: "60s"},
		{name: "PROXY_CHECK_INTERVAL", value: "1m"},
		{name: "PROXY_CHECK_INTERVAL", value: "1m0s"},
		{name: "HARPOON_CAPTURE_PAYLOADS", value: "false"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			fs := runtimeFlagSet(t, FlavorRuntime,
				"--control-plane.tunnel-id", testTunnelID,
				"--mcp.server-url", "https://mcp.example.invalid/mcp",
			)
			env := map[string]string{"CONTROL_PLANE_API_KEY": testAPIKey, tc.name: tc.value}
			if _, err := LoadFromFlagSet(fs, lookupEnvMap(env)); err != nil {
				t.Fatalf("runtime rejected default-equivalent %s=%q: %v", tc.name, tc.value, err)
			}
		})
	}
}

func TestRuntimeRejectsNonDefaultFullOnlyEnvironment(t *testing.T) {
	nonDefaultValue := map[string]string{
		"ALLOW_REMOTE_UI":            "true",
		"OPEN_WEB_UI":                "true",
		"ADMIN_UI_LOG_BUFFER_EVENTS": "2001",
		"PROXY_CHECK_INTERVAL":       "61s",
		"HARPOON_CAPTURE_PAYLOADS":   "true",
	}
	for _, setting := range fullOnlyEnv {
		name := setting.name
		t.Run(name, func(t *testing.T) {
			fs := runtimeFlagSet(t, FlavorRuntime,
				"--control-plane.tunnel-id", testTunnelID,
				"--mcp.server-url", "https://mcp.example.invalid/mcp",
			)
			env := map[string]string{"CONTROL_PLANE_API_KEY": testAPIKey, name: nonDefaultValue[name]}
			_, err := LoadFromFlagSet(fs, lookupEnvMap(env))
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("error = %v, want rejection mentioning %q", err, name)
			}
		})
	}
}

func TestRuntimeIgnoresUnrelatedEnvironment(t *testing.T) {
	fs := runtimeFlagSet(t, FlavorRuntime,
		"--control-plane.tunnel-id", testTunnelID,
		"--mcp.server-url", "https://mcp.example.invalid/mcp",
	)
	_, err := LoadFromFlagSet(fs, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": testAPIKey,
		"UNRELATED_PROCESS_ENV": "ignored",
	}))
	if err != nil {
		t.Fatalf("runtime rejected unrelated process environment: %v", err)
	}
}

func TestRuntimeRejectsHarpoonPayloadCaptureSurface(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		fs := pflag.NewFlagSet("runtime", pflag.ContinueOnError)
		RegisterFlags(fs, FlavorRuntime)
		if err := fs.Parse([]string{"--harpoon.capture-payloads"}); err == nil {
			t.Fatal("runtime accepted full-client-only Harpoon payload capture flag")
		}
	})
	t.Run("yaml", func(t *testing.T) {
		configPath := writeRuntimeConfig(t, `
config_version: 1
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: env:CONTROL_PLANE_API_KEY
mcp:
  server_urls:
    - url: https://mcp.example.invalid/mcp
harpoon:
  capture_payloads: true
`)
		fs := runtimeFlagSet(t, FlavorRuntime, "--config", configPath)
		_, err := LoadFromFlagSet(fs, lookupEnvMap(map[string]string{"CONTROL_PLANE_API_KEY": testAPIKey}))
		if err == nil || !strings.Contains(err.Error(), "capture_payloads") {
			t.Fatalf("error = %v, want payload capture YAML rejection", err)
		}
	})
}

func TestNormalRuntimeRejectsCloudflaredConfiguration(t *testing.T) {
	fs := runtimeFlagSet(t, FlavorRuntimeCloudflared,
		"--control-plane.tunnel-id", testTunnelID,
		"--mcp.server-url", "https://mcp.example.invalid/mcp",
		"--cloudflared.managed",
	)
	_, err := LoadFromFlagSet(fs, lookupEnvMap(map[string]string{"CONTROL_PLANE_API_KEY": testAPIKey}))
	if err == nil || !strings.Contains(err.Error(), "cloudflared") {
		t.Fatalf("error = %v, want normal runtime cloudflared rejection", err)
	}
}

func TestNormalRuntimeRejectsCloudflaredYAML(t *testing.T) {
	configPath := writeRuntimeConfig(t, `
config_version: 1
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: env:CONTROL_PLANE_API_KEY
mcp:
  server_urls:
    - url: https://mcp.example.invalid/mcp
cloudflared:
  managed: true
`)
	fs := runtimeFlagSet(t, FlavorRuntime, "--config", configPath)
	_, err := LoadFromFlagSet(fs, lookupEnvMap(map[string]string{"CONTROL_PLANE_API_KEY": testAPIKey}))
	if err == nil || !strings.Contains(err.Error(), "cloudflared") {
		t.Fatalf("error = %v, want normal runtime cloudflared YAML rejection", err)
	}
}

func TestCloudflaredLoaderAcceptsOnlyApprovedCompanionSettings(t *testing.T) {
	fs := runtimeFlagSet(t, FlavorRuntimeCloudflared,
		"--control-plane.tunnel-id", testTunnelID,
		"--mcp.server-url", "https://mcp.example.invalid/mcp",
		"--cloudflared.managed",
		"--cloudflared.path", "/tmp/cloudflared",
		"--cloudflared.ready-timeout", "17s",
	)
	cfg, err := LoadCloudflaredFromFlagSet(fs, lookupEnvMap(map[string]string{"CONTROL_PLANE_API_KEY": testAPIKey}))
	if err != nil {
		t.Fatalf("LoadCloudflaredFromFlagSet returned error: %v", err)
	}
	if !cfg.Cloudflared.Managed || cfg.Cloudflared.Path != "/tmp/cloudflared" || cfg.Cloudflared.ReadyTimeout.String() != "17s" {
		t.Fatalf("cloudflared settings = %+v", cfg.Cloudflared)
	}
	if got := cfg.Runtime.ControlPlane.TunnelID.String(); got != testTunnelID {
		t.Fatalf("runtime tunnel id = %q, want %q", got, testTunnelID)
	}
}

func TestRuntimeHarpoonConfigHasNoCapturePayloadsField(t *testing.T) {
	if _, ok := reflect.TypeOf(HarpoonConfig{}).FieldByName("CapturePayloads"); ok {
		t.Fatal("runtime HarpoonConfig unexpectedly exposes CapturePayloads")
	}
}

func TestRuntimeConfigHasNoFullOnlyFields(t *testing.T) {
	typ := reflect.TypeOf(Config{})
	for _, name := range []string{"AdminUI", "Cloudflared", "ProxyHealth"} {
		if _, ok := typ.FieldByName(name); ok {
			t.Fatalf("runtime Config unexpectedly exposes full-only field %q", name)
		}
	}
}

func runtimeFlagSet(t *testing.T, flavor Flavor, args ...string) *pflag.FlagSet {
	t.Helper()
	fs := pflag.NewFlagSet("runtime", pflag.ContinueOnError)
	RegisterFlags(fs, flavor)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	return fs
}

func lookupEnvMap(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func writeRuntimeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(contents)+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func assertExactRuntimeSettingList(t *testing.T, label string, got []string, want []string) {
	t.Helper()
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %v, want exactly %v", label, got, want)
	}
}
