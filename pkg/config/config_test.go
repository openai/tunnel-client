package config

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"go.openai.org/api/tunnel-client/pkg/types"
)

const (
	envTunnelID  = "tunnel_0123456789abcdef0123456789abcdef"
	flagTunnelID = "tunnel_fedcba9876543210fedcba9876543210"
)

func TestResolveControlPlanePathUsesSingleSeparator(t *testing.T) {
	t.Parallel()

	baseURL, err := url.Parse("https://gateway.example.com/")
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}

	target := ResolveControlPlanePath(baseURL, "/workspace/dev/us", "/v1/tunnel/tunnel_123/poll")
	if target == nil {
		t.Fatal("expected resolved control plane URL")
	}
	if got, want := target.String(), "https://gateway.example.com/workspace/dev/us/v1/tunnel/tunnel_123/poll"; got != want {
		t.Fatalf("unexpected resolved control plane URL: got %q want %q", got, want)
	}
}

func TestLoadUsesEnvWhenFlagsEmpty(t *testing.T) {
	lookup := map[string]string{
		"CONTROL_PLANE_BASE_URL":                "https://example",
		"CONTROL_PLANE_URL_PATH":                "/gateway/dev/us",
		"CONTROL_PLANE_TUNNEL_ID":               envTunnelID,
		"CONTROL_PLANE_API_KEY":                 "control-key",
		"CONTROL_PLANE_MAX_INFLIGHT_REQUESTS":   "15",
		"CONTROL_PLANE_POLL_TIMEOUT":            "45s",
		"CONTROL_PLANE_POLL_DEADLINE_GUARDRAIL": "500ms",
		"LOG_LEVEL":                             "debug",
		"LOG_FORMAT":                            "json",
		"LOG_FILE":                              "/tmp/log",
		"LOG_HTTP_RAW_UNSAFE":                   "true",
		"HEALTH_UNIX_SOCKET":                    "/tmp/health.sock",
		"HEALTH_URL_FILE":                       "/tmp/health-url",
		"ADMIN_UI_LOG_BUFFER_EVENTS":            "1234",
		"PID_FILE":                              "/tmp/pid-file",
		"MCP_SERVER_URL":                        "https://mcp.example",
		"MCP_EXTRA_HEADERS":                     "X-Internal-Auth: env-static",
		"MCP_DISCOVERY_EXTRA_HEADERS":           "X-Discovery-Auth: env-discovery",
		"MCP_CONNECTION_MAX_TTL":                "30s",
		"MCP_MAX_CONCURRENT_REQUESTS":           "12",
	}

	cfg, err := Load(nil, func(key string) (string, bool) {
		val, ok := lookup[key]
		return val, ok
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.ControlPlane.BaseURL == nil || cfg.ControlPlane.BaseURL.String() != "https://example" {
		t.Fatalf("unexpected control plane base url: %v", cfg.ControlPlane.BaseURL)
	}
	if cfg.ControlPlane.URLPath != "/gateway/dev/us" {
		t.Fatalf("unexpected control plane url path: %q", cfg.ControlPlane.URLPath)
	}
	if cfg.ControlPlane.TunnelID != envTunnelID {
		t.Fatalf("unexpected tunnel id: %s", cfg.ControlPlane.TunnelID)
	}
	if cfg.ControlPlane.MaxInFlightRequests != 15 {
		t.Fatalf("unexpected max in-flight requests: %d", cfg.ControlPlane.MaxInFlightRequests)
	}
	if cfg.ControlPlane.PollTimeout != 45*time.Second {
		t.Fatalf("unexpected poll timeout: %s", cfg.ControlPlane.PollTimeout)
	}
	if cfg.ControlPlane.PollDeadlineGuardrail != 500*time.Millisecond {
		t.Fatalf("unexpected poll deadline guardrail: %s", cfg.ControlPlane.PollDeadlineGuardrail)
	}
	if cfg.Logging.Level != slog.LevelDebug {
		t.Fatalf("unexpected log level: %s", cfg.Logging.Level.String())
	}
	if cfg.Logging.Format != LogFormatJSON {
		t.Fatalf("unexpected log format: %s", cfg.Logging.Format)
	}
	if cfg.Logging.File != "/tmp/log" {
		t.Fatalf("unexpected log file: %s", cfg.Logging.File)
	}
	if !cfg.Logging.HTTPRawUnsafe {
		t.Fatalf("expected raw HTTP logging to be enabled")
	}
	if cfg.ControlPlane.APIKey != "control-key" {
		t.Fatalf("expected control plane API key control-key, got %s", cfg.ControlPlane.APIKey)
	}
	if cfg.Health.URLFile != "/tmp/health-url" {
		t.Fatalf("unexpected health URL file: %s", cfg.Health.URLFile)
	}
	if cfg.Health.UnixSocket != "/tmp/health.sock" {
		t.Fatalf("unexpected health unix socket: %s", cfg.Health.UnixSocket)
	}
	if cfg.Health.ListenAddr != "127.0.0.1:8080" {
		t.Fatalf("expected default health listen addr 127.0.0.1:8080, got %s", cfg.Health.ListenAddr)
	}
	if cfg.AdminUI.LogBufferEvents != 1234 {
		t.Fatalf("unexpected admin UI log buffer events: %d", cfg.AdminUI.LogBufferEvents)
	}
	if cfg.Process.PIDFile != "/tmp/pid-file" {
		t.Fatalf("unexpected pid file: %s", cfg.Process.PIDFile)
	}
	if cfg.MCP.ServerURL == nil || cfg.MCP.ServerURL.String() != "https://mcp.example" {
		t.Fatalf("unexpected MCP server URL: %v", cfg.MCP.ServerURL)
	}
	if cfg.MCP.ExtraHeaders["X-Internal-Auth"] != "env-static" {
		t.Fatalf("unexpected MCP extra headers: %#v", cfg.MCP.ExtraHeaders)
	}
	if cfg.MCP.DiscoveryExtraHeaders["X-Discovery-Auth"] != "env-discovery" {
		t.Fatalf("unexpected MCP discovery extra headers: %#v", cfg.MCP.DiscoveryExtraHeaders)
	}
	if cfg.MCP.ConnectionMaxTTL != 30*time.Second {
		t.Fatalf("unexpected MCP connection ttl: %s", cfg.MCP.ConnectionMaxTTL)
	}
	if cfg.MCP.MaxConcurrentRequests != 12 {
		t.Fatalf("unexpected MCP max concurrent requests: %d", cfg.MCP.MaxConcurrentRequests)
	}
}

func TestControlPlanePollDefaultsStayBounded(t *testing.T) {
	t.Parallel()

	if defaultControlPlanePollTimeout <= 0 {
		t.Fatalf("default poll timeout must be greater than zero: %s", defaultControlPlanePollTimeout)
	}
	if defaultControlPlanePollTimeout+defaultControlPlanePollDeadlineGuardrail > maxControlPlanePollDeadline {
		t.Fatalf("default poll deadline must not exceed %s: %s", maxControlPlanePollDeadline, defaultControlPlanePollTimeout+defaultControlPlanePollDeadlineGuardrail)
	}
	if defaultControlPlanePollDeadlineGuardrail <= 0 {
		t.Fatalf("default poll deadline guardrail must be greater than zero: %s", defaultControlPlanePollDeadlineGuardrail)
	}
	if defaultControlPlanePollDeadlineGuardrail >= maxControlPlanePollDeadlineGuardrail {
		t.Fatalf("default poll deadline guardrail must stay below %s: %s", maxControlPlanePollDeadlineGuardrail, defaultControlPlanePollDeadlineGuardrail)
	}
}

func TestLoadRejectsPollTimingAboveExplicitBounds(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "poll timeout",
			args:    []string{"--control-plane.poll-timeout=10m1ms"},
			wantErr: "control-plane.poll-timeout plus control-plane.poll-deadline-guardrail must be less than or equal to 10m0s",
		},
		{
			name:    "poll deadline guardrail",
			args:    []string{"--control-plane.poll-deadline-guardrail=1m"},
			wantErr: "control-plane.poll-deadline-guardrail must be less than 1m0s",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(tc.args, lookupEnvMap(map[string]string{
				"CONTROL_PLANE_TUNNEL_ID": envTunnelID,
				"CONTROL_PLANE_API_KEY":   "control-key",
			}))
			if err == nil {
				t.Fatal("expected Load to reject invalid poll timing")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestControlPlanePollDeadlineCapsAtExplicitDeadline(t *testing.T) {
	t.Parallel()

	if got := (ControlPlaneConfig{
		PollTimeout:           maxControlPlanePollDeadline,
		PollDeadlineGuardrail: time.Second,
	}).PollDeadlineTimeoutOrDefault(); got != maxControlPlanePollDeadline {
		t.Fatalf("expected poll deadline cap %s, got %s", maxControlPlanePollDeadline, got)
	}
}

func TestLoadFlagsOverrideEnv(t *testing.T) {
	lookup := map[string]string{
		"CONTROL_PLANE_BASE_URL":              "https://env",
		"CONTROL_PLANE_URL_PATH":              "/env-path",
		"CONTROL_PLANE_TUNNEL_ID":             envTunnelID,
		"CONTROL_PLANE_API_KEY":               "control-env-key",
		"CONTROL_PLANE_MAX_INFLIGHT_REQUESTS": "25",
		"CONTROL_PLANE_POLL_TIMEOUT":          "1m",
		"OPENAI_API_KEY":                      "legacy-env-key",
		"LOG_LEVEL":                           "warn",
		"LOG_FORMAT":                          "json",
		"LOG_FILE":                            "/tmp/env",
		"LOG_HTTP_RAW_UNSAFE":                 "true",
		"HEALTH_UNIX_SOCKET":                  "/tmp/env-health.sock",
		"HEALTH_URL_FILE":                     "/tmp/env-health",
		"ADMIN_UI_LOG_BUFFER_EVENTS":          "111",
		"PID_FILE":                            "/tmp/env-pid",
		"MCP_SERVER_URL":                      "https://env-mcp",
		"MCP_EXTRA_HEADERS":                   "X-Internal-Auth: env-static",
		"MCP_DISCOVERY_EXTRA_HEADERS":         "X-Discovery-Auth: env-discovery",
		"MCP_CONNECTION_MAX_TTL":              "45m",
		"MCP_MAX_CONCURRENT_REQUESTS":         "5",
	}

	args := []string{
		"--control-plane.base-url", "https://flag",
		"--control-plane.url-path", "/flag-path",
		"--control-plane.tunnel-id", flagTunnelID,
		"--log.level", "info",
		"--log.format", "struct-text",
		"--log.file", "/tmp/flag",
		"--log.http-raw-unsafe=false",
		"--health.unix-socket", "/tmp/flag-health.sock",
		"--health.url-file", "/tmp/flag-health",
		"--admin-ui.log-buffer-events=456",
		"--pid.file", "/tmp/flag-pid",
		"--mcp.server-url", "https://flag-mcp",
		"--mcp.extra-headers", "X-Internal-Auth: flag-static",
		"--mcp.discovery-extra-headers", "X-Discovery-Auth: flag-discovery",
		"--control-plane.poll-timeout=5s",
		"--control-plane.poll-deadline-guardrail=250ms",
		"--mcp.connection-max-ttl=15m",
		"--mcp.max-concurrent-requests=20",
	}
	cfg, err := Load(args, func(key string) (string, bool) {
		val, ok := lookup[key]
		return val, ok
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.ControlPlane.BaseURL == nil || cfg.ControlPlane.BaseURL.String() != "https://flag" {
		t.Fatalf("expected flag control plane base url, got %v", cfg.ControlPlane.BaseURL)
	}
	if cfg.ControlPlane.URLPath != "/flag-path" {
		t.Fatalf("expected flag control plane url path, got %q", cfg.ControlPlane.URLPath)
	}
	if cfg.ControlPlane.TunnelID != flagTunnelID {
		t.Fatalf("expected flag tunnel id, got %s", cfg.ControlPlane.TunnelID)
	}
	if cfg.Logging.Level != slog.LevelInfo {
		t.Fatalf("expected log level info, got %s", cfg.Logging.Level.String())
	}
	if cfg.Logging.Format != LogFormatStructText {
		t.Fatalf("expected log format text, got %s", cfg.Logging.Format)
	}
	if cfg.Logging.File != "/tmp/flag" {
		t.Fatalf("expected log file /tmp/flag, got %s", cfg.Logging.File)
	}
	if cfg.Logging.HTTPRawUnsafe {
		t.Fatalf("expected raw HTTP logging to be disabled when flag sets false")
	}
	if cfg.ControlPlane.APIKey != "control-env-key" {
		t.Fatalf("expected control plane api key control-env-key, got %s", cfg.ControlPlane.APIKey)
	}
	if cfg.Health.URLFile != "/tmp/flag-health" {
		t.Fatalf("expected health URL file /tmp/flag-health, got %s", cfg.Health.URLFile)
	}
	if cfg.Health.UnixSocket != "/tmp/flag-health.sock" {
		t.Fatalf("expected health unix socket /tmp/flag-health.sock, got %s", cfg.Health.UnixSocket)
	}
	if cfg.AdminUI.LogBufferEvents != 456 {
		t.Fatalf("expected admin UI log buffer events 456, got %d", cfg.AdminUI.LogBufferEvents)
	}
	if cfg.Process.PIDFile != "/tmp/flag-pid" {
		t.Fatalf("expected pid file /tmp/flag-pid, got %s", cfg.Process.PIDFile)
	}
	if cfg.MCP.ServerURL == nil || cfg.MCP.ServerURL.String() != "https://flag-mcp" {
		t.Fatalf("expected MCP server URL https://flag-mcp, got %v", cfg.MCP.ServerURL)
	}
	if cfg.MCP.ExtraHeaders["X-Internal-Auth"] != "flag-static" {
		t.Fatalf("expected flag MCP extra header, got %#v", cfg.MCP.ExtraHeaders)
	}
	if cfg.MCP.DiscoveryExtraHeaders["X-Discovery-Auth"] != "flag-discovery" {
		t.Fatalf("expected flag MCP discovery extra header, got %#v", cfg.MCP.DiscoveryExtraHeaders)
	}
	if cfg.ControlPlane.MaxInFlightRequests != 25 {
		t.Fatalf("expected max in-flight requests 25, got %d", cfg.ControlPlane.MaxInFlightRequests)
	}
	if cfg.ControlPlane.PollTimeout != 5*time.Second {
		t.Fatalf("expected poll timeout 5s, got %s", cfg.ControlPlane.PollTimeout)
	}
	if cfg.ControlPlane.PollDeadlineGuardrail != 250*time.Millisecond {
		t.Fatalf("expected poll deadline guardrail 250ms, got %s", cfg.ControlPlane.PollDeadlineGuardrail)
	}
	if cfg.MCP.ConnectionMaxTTL != 15*time.Minute {
		t.Fatalf("expected connection ttl 15m, got %s", cfg.MCP.ConnectionMaxTTL)
	}
	if cfg.MCP.MaxConcurrentRequests != 20 {
		t.Fatalf("expected MCP max concurrent requests 20, got %d", cfg.MCP.MaxConcurrentRequests)
	}
}

func TestLoadUsesYAMLConfigWhenFlagsAndEnvUnset(t *testing.T) {
	controlHeaderPath := writeTempSecretFile(t, "yaml-control-header\n")
	discoveryHeaderPath := writeTempSecretFile(t, "yaml-discovery-from-file\n")
	healthListenAddrPath := writeTempSecretFile(t, "127.0.0.1:9090\n")
	healthUnixSocketPath := writeTempSecretFile(t, "/tmp/yaml-health.sock\n")
	toolsCommandPath := writeTempSecretFile(t, "python -m tools\n")
	harpoonTargetPath := writeTempSecretFile(t, "https://auth.example\n")
	configPath := writeTempConfigFile(t, `
config_version: 1
control_plane:
  base_url: env:YAML_CONTROL_PLANE_BASE_URL
  url_path: env:YAML_CONTROL_PLANE_URL_PATH
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: env:YAML_CONTROL_PLANE_API_KEY
  max_inflight_requests: 17
  poll_timeout: 55s
  poll_deadline_guardrail: 125ms
  extra_headers:
    X-Debug-Mode: yaml
    X-Control-Auth: file:`+controlHeaderPath+`
log:
  level: warn
  format: json
  file: /tmp/yaml-log.ndjson
  http_raw_unsafe: true
health:
  listen_addr: file:`+healthListenAddrPath+`
  unix_socket: file:`+healthUnixSocketPath+`
  url_file: /tmp/yaml-health-url
admin_ui:
  allow_remote: true
  open_browser: true
  log_buffer_events: 321
process:
  pid_file: /tmp/yaml.pid
mcp:
  server_urls:
    - channel: main
      url: env:YAML_MCP_MAIN_URL
  commands:
    - channel: tools
      command: file:`+toolsCommandPath+`
  extra_headers:
    X-Internal-Auth: env:YAML_MCP_STATIC_HEADER
  discovery_extra_headers:
    X-Discovery-Auth: file:`+discoveryHeaderPath+`
  connection_max_ttl: 2m
  max_concurrent_requests: 9
harpoon:
  targets:
    - label: auth
      url: file:`+harpoonTargetPath+`
      unix_socket: env:YAML_HARPOON_SOCKET
      description: Auth server
  additional_transports:
    - http-streamable
  capture_payloads: true
proxy:
  check_interval: 45s
`)

	cfg, err := Load([]string{"--config", configPath}, lookupEnvMap(map[string]string{
		"YAML_CONTROL_PLANE_BASE_URL": "https://yaml-control.example",
		"YAML_CONTROL_PLANE_URL_PATH": "/gateway/dev/us",
		"YAML_CONTROL_PLANE_API_KEY":  "yaml-control-key",
		"YAML_MCP_MAIN_URL":           "https://yaml-mcp.example/mcp",
		"YAML_MCP_STATIC_HEADER":      "yaml-static-from-env",
		"YAML_HARPOON_SOCKET":         "/tmp/yaml-harpoon.sock",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Runtime.ConfigFile != configPath {
		t.Fatalf("expected config file %q, got %q", configPath, cfg.Runtime.ConfigFile)
	}
	if !strings.Contains(string(cfg.Runtime.ConfigFileContents), "env:YAML_CONTROL_PLANE_BASE_URL") {
		t.Fatalf("expected runtime config file contents to contain startup YAML")
	}
	if cfg.ControlPlane.BaseURL == nil || cfg.ControlPlane.BaseURL.String() != "https://yaml-control.example" {
		t.Fatalf("unexpected control plane base url: %v", cfg.ControlPlane.BaseURL)
	}
	if cfg.ControlPlane.URLPath != "/gateway/dev/us" {
		t.Fatalf("unexpected control plane url path: %q", cfg.ControlPlane.URLPath)
	}
	if cfg.ControlPlane.TunnelID != "tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("unexpected tunnel id: %s", cfg.ControlPlane.TunnelID)
	}
	if cfg.ControlPlane.APIKey != "yaml-control-key" {
		t.Fatalf("expected resolved YAML control-plane API key, got %q", cfg.ControlPlane.APIKey)
	}
	if cfg.ControlPlane.MaxInFlightRequests != 17 {
		t.Fatalf("unexpected max in-flight requests: %d", cfg.ControlPlane.MaxInFlightRequests)
	}
	if cfg.ControlPlane.PollTimeout != 55*time.Second {
		t.Fatalf("unexpected poll timeout: %s", cfg.ControlPlane.PollTimeout)
	}
	if cfg.ControlPlane.PollDeadlineGuardrail != 125*time.Millisecond {
		t.Fatalf("unexpected poll deadline guardrail: %s", cfg.ControlPlane.PollDeadlineGuardrail)
	}
	if cfg.ControlPlane.ExtraHeaders["X-Debug-Mode"] != "yaml" {
		t.Fatalf("unexpected extra headers: %#v", cfg.ControlPlane.ExtraHeaders)
	}
	if cfg.ControlPlane.ExtraHeaders["X-Control-Auth"] != "yaml-control-header" {
		t.Fatalf("unexpected resolved control-plane extra headers: %#v", cfg.ControlPlane.ExtraHeaders)
	}
	if cfg.Logging.Level != slog.LevelWarn || cfg.Logging.Format != LogFormatJSON || cfg.Logging.File != "/tmp/yaml-log.ndjson" || !cfg.Logging.HTTPRawUnsafe {
		t.Fatalf("unexpected logging config: %#v", cfg.Logging)
	}
	if cfg.Health.ListenAddr != "127.0.0.1:9090" || cfg.Health.UnixSocket != "/tmp/yaml-health.sock" || cfg.Health.URLFile != "/tmp/yaml-health-url" {
		t.Fatalf("unexpected health config: %#v", cfg.Health)
	}
	if !cfg.AdminUI.AllowRemote || !cfg.AdminUI.OpenBrowser || cfg.AdminUI.LogBufferEvents != 321 {
		t.Fatalf("unexpected admin UI config: %#v", cfg.AdminUI)
	}
	if cfg.Process.PIDFile != "/tmp/yaml.pid" {
		t.Fatalf("unexpected pid file: %s", cfg.Process.PIDFile)
	}
	if cfg.MCP.ServerURL == nil || cfg.MCP.ServerURL.String() != "https://yaml-mcp.example/mcp" {
		t.Fatalf("unexpected main MCP server URL: %v", cfg.MCP.ServerURL)
	}
	if cfg.MCP.ConnectionMaxTTL != 2*time.Minute || cfg.MCP.MaxConcurrentRequests != 9 {
		t.Fatalf("unexpected MCP limits: ttl=%s max=%d", cfg.MCP.ConnectionMaxTTL, cfg.MCP.MaxConcurrentRequests)
	}
	if cfg.MCP.ExtraHeaders["X-Internal-Auth"] != "yaml-static-from-env" {
		t.Fatalf("unexpected MCP extra headers: %#v", cfg.MCP.ExtraHeaders)
	}
	if cfg.MCP.DiscoveryExtraHeaders["X-Discovery-Auth"] != "yaml-discovery-from-file" {
		t.Fatalf("unexpected MCP discovery extra headers: %#v", cfg.MCP.DiscoveryExtraHeaders)
	}
	if len(cfg.MCP.ChannelBindings) != 2 {
		t.Fatalf("expected two MCP channel bindings, got %d", len(cfg.MCP.ChannelBindings))
	}
	if tools := cfg.MCP.ChannelBindingFor(types.Channel("tools")); tools == nil || tools.Command != "python -m tools" {
		t.Fatalf("expected tools stdio binding, got %#v", tools)
	}
	if len(cfg.Harpoon.Targets) != 1 || cfg.Harpoon.Targets[0].Label != "auth" {
		t.Fatalf("unexpected harpoon targets: %#v", cfg.Harpoon.Targets)
	}
	if cfg.Harpoon.Targets[0].BaseURL == nil || cfg.Harpoon.Targets[0].BaseURL.String() != "https://auth.example" {
		t.Fatalf("unexpected resolved harpoon target url: %v", cfg.Harpoon.Targets[0].BaseURL)
	}
	if cfg.Harpoon.Targets[0].UnixSocketPath != "/tmp/yaml-harpoon.sock" {
		t.Fatalf("unexpected resolved harpoon target unix socket: %q", cfg.Harpoon.Targets[0].UnixSocketPath)
	}
	if !cfg.Harpoon.AdditionalTransportEnabled(HarpoonTransportHTTPStreamable) || !cfg.Harpoon.CapturePayloads {
		t.Fatalf("unexpected harpoon config: %#v", cfg.Harpoon)
	}
	if cfg.ProxyHealth.CheckInterval != 45*time.Second {
		t.Fatalf("unexpected proxy check interval: %s", cfg.ProxyHealth.CheckInterval)
	}
}

func TestLoadPrecedenceFlagsEnvYAMLDefaults(t *testing.T) {
	configPath := writeTempConfigFile(t, `
control_plane:
  base_url: https://yaml-control.example
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: yaml-control-key
mcp:
  server_urls:
    - channel: main
      url: https://yaml-mcp.example/mcp
`)

	cfg, err := Load([]string{
		"--config", configPath,
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "https://flag-mcp.example/mcp",
	}, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_BASE_URL": "https://env-control.example",
		"CONTROL_PLANE_API_KEY":  "env-control-key",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.ControlPlane.BaseURL == nil || cfg.ControlPlane.BaseURL.String() != "https://env-control.example" {
		t.Fatalf("expected env control plane base url, got %v", cfg.ControlPlane.BaseURL)
	}
	if cfg.ControlPlane.TunnelID != flagTunnelID {
		t.Fatalf("expected flag tunnel ID, got %s", cfg.ControlPlane.TunnelID)
	}
	if cfg.ControlPlane.APIKey != "env-control-key" {
		t.Fatalf("expected env API key, got %q", cfg.ControlPlane.APIKey)
	}
	if cfg.MCP.ServerURL == nil || cfg.MCP.ServerURL.String() != "https://flag-mcp.example/mcp" {
		t.Fatalf("expected flag MCP server URL, got %v", cfg.MCP.ServerURL)
	}
}

func TestLoadUsesYAMLConfigPathFromEnv(t *testing.T) {
	configPath := writeTempConfigFile(t, `
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: yaml-control-key
mcp:
  server_urls:
    - channel: main
      url: https://yaml-mcp.example/mcp
`)

	cfg, err := Load(nil, lookupEnvMap(map[string]string{
		"TUNNEL_CLIENT_CONFIG": configPath,
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Runtime.ConfigFile != configPath {
		t.Fatalf("expected config file %q, got %q", configPath, cfg.Runtime.ConfigFile)
	}
	if cfg.ControlPlane.APIKey != "yaml-control-key" {
		t.Fatalf("expected YAML API key, got %q", cfg.ControlPlane.APIKey)
	}
}

func TestLoadRejectsUnknownYAMLFields(t *testing.T) {
	configPath := writeTempConfigFile(t, `
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  unknown: true
mcp:
  server_urls:
    - channel: main
      url: https://yaml-mcp.example/mcp
`)

	_, err := Load([]string{"--config", configPath}, lookupEnvMap(nil))
	if err == nil {
		t.Fatalf("expected unknown YAML field error")
	}
	if !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestLoadCABundleFlag(t *testing.T) {
	bundlePath := writeTempCABundle(t)
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "https://mcp.example",
		"--ca-bundle", bundlePath,
	}
	cfg, err := Load(args, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.TLS == nil {
		t.Fatalf("expected TLS bundle to be populated")
	}
	if cfg.TLS.Path != bundlePath {
		t.Fatalf("expected bundle path %q, got %q", bundlePath, cfg.TLS.Path)
	}
	if cfg.TLS.RootCAs == nil {
		t.Fatalf("expected RootCAs to be set")
	}
}

func TestLoadCABundleEnvReference(t *testing.T) {
	bundlePath := writeTempCABundle(t)
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "https://mcp.example",
	}
	cfg, err := Load(args, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
		"CA_BUNDLE":             "env:ENTERPRISE_CA_BUNDLE",
		"ENTERPRISE_CA_BUNDLE":  bundlePath,
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.TLS == nil {
		t.Fatalf("expected TLS bundle to be populated")
	}
	if cfg.TLS.Path != bundlePath {
		t.Fatalf("expected bundle path %q, got %q", bundlePath, cfg.TLS.Path)
	}
}

func TestLoadCABundleRejectsInvalidPEM(t *testing.T) {
	dir := t.TempDir()
	bundlePath := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(bundlePath, []byte("not-a-cert"), 0o600); err != nil {
		t.Fatalf("write invalid bundle: %v", err)
	}
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "https://mcp.example",
		"--ca-bundle", bundlePath,
	}
	_, err := Load(args, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
	}))
	if err == nil {
		t.Fatalf("expected error for invalid PEM bundle")
	}
	if !strings.Contains(err.Error(), "invalid ca-bundle") {
		t.Fatalf("expected invalid ca-bundle error, got %v", err)
	}
}

func TestLoadFallsBackToOpenAIApiKey(t *testing.T) {
	cfg, err := Load(nil, func(key string) (string, bool) {
		if key == "OPENAI_API_KEY" {
			return "legacy-key", true
		}
		if key == "CONTROL_PLANE_TUNNEL_ID" {
			return envTunnelID, true
		}
		if key == "LOG_FORMAT" {
			return "struct-text", true
		}
		if key == "MCP_SERVER_URL" {
			return "https://mcp.default", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("unexpected error loading config with OPENAI_API_KEY: %v", err)
	}
	if cfg.ControlPlane.APIKey != "legacy-key" {
		t.Fatalf("expected control plane api key legacy-key, got %s", cfg.ControlPlane.APIKey)
	}
}

func TestLoadUsesControlPlaneAPIKeyFlag(t *testing.T) {
	args := []string{"--control-plane.api-key", "env:OPENAI_API_KEY_STAGING"}
	lookup := map[string]string{
		"CONTROL_PLANE_TUNNEL_ID": envTunnelID,
		"OPENAI_API_KEY_STAGING":  "staging-key",
		"LOG_FORMAT":              "struct-text",
		"MCP_SERVER_URL":          "https://mcp.default",
	}
	cfg, err := Load(args, func(key string) (string, bool) {
		val, ok := lookup[key]
		return val, ok
	})
	if err != nil {
		t.Fatalf("unexpected error when using control-plane.api-key flag: %v", err)
	}
	if cfg.ControlPlane.APIKey != "staging-key" {
		t.Fatalf("expected control plane API key staging-key, got %s", cfg.ControlPlane.APIKey)
	}
}

func TestLoadRejectsInvalidControlPlaneAPIKeyFlag(t *testing.T) {
	args := []string{"--control-plane.api-key", "OPENAI_API_KEY_STAGING"}
	_, err := Load(args, func(key string) (string, bool) {
		if key == "LOG_FORMAT" {
			return "struct-text", true
		}
		if key == "CONTROL_PLANE_TUNNEL_ID" {
			return envTunnelID, true
		}
		if key == "MCP_SERVER_URL" {
			return "https://mcp.default", true
		}
		return "", false
	})
	if err == nil {
		t.Fatalf("expected error when control-plane.api-key flag missing env: prefix")
	}
	if !strings.Contains(err.Error(), "env:") || !strings.Contains(err.Error(), "file:") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadParsesHarpoonTargetsFromFlags(t *testing.T) {
	args := []string{"--harpoon.target", "label=auth,url=https://example.com,unix-socket=env:HARPOON_SOCKET,desc=Auth server"}
	lookup := map[string]string{
		"CONTROL_PLANE_TUNNEL_ID": envTunnelID,
		"CONTROL_PLANE_API_KEY":   "control-key",
		"LOG_FORMAT":              "struct-text",
		"MCP_SERVER_URL":          "https://mcp.example",
		"HARPOON_SOCKET":          "/tmp/harpoon.sock",
	}

	cfg, err := Load(args, func(key string) (string, bool) {
		val, ok := lookup[key]
		return val, ok
	})
	if err != nil {
		t.Fatalf("unexpected error loading harpoon targets: %v", err)
	}
	if len(cfg.Harpoon.Targets) != 1 {
		t.Fatalf("expected 1 harpoon target, got %d", len(cfg.Harpoon.Targets))
	}
	if cfg.Harpoon.Targets[0].Label != "auth" {
		t.Fatalf("expected harpoon target label auth, got %s", cfg.Harpoon.Targets[0].Label)
	}
	if cfg.Harpoon.Targets[0].Description != "Auth server" {
		t.Fatalf("expected harpoon target description Auth server, got %s", cfg.Harpoon.Targets[0].Description)
	}
	if cfg.Harpoon.Targets[0].BaseURL == nil || cfg.Harpoon.Targets[0].BaseURL.String() != "https://example.com" {
		t.Fatalf("expected harpoon target url https://example.com, got %v", cfg.Harpoon.Targets[0].BaseURL)
	}
	if cfg.Harpoon.Targets[0].UnixSocketPath != "/tmp/harpoon.sock" {
		t.Fatalf("expected harpoon target unix socket /tmp/harpoon.sock, got %q", cfg.Harpoon.Targets[0].UnixSocketPath)
	}
}

func TestLoadRejectsInvalidHarpoonTargetLabel(t *testing.T) {
	args := []string{"--harpoon.target", "label=Auth-Prod,url=https://example.com,desc=Auth server"}
	lookup := map[string]string{
		"CONTROL_PLANE_TUNNEL_ID": envTunnelID,
		"CONTROL_PLANE_API_KEY":   "control-key",
		"LOG_FORMAT":              "struct-text",
		"MCP_SERVER_URL":          "https://mcp.example",
	}

	_, err := Load(args, func(key string) (string, bool) {
		val, ok := lookup[key]
		return val, ok
	})
	if err == nil {
		t.Fatalf("expected error loading invalid harpoon target label")
	}
	if !strings.Contains(err.Error(), "label must match") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadParsesHarpoonAdditionalTransport(t *testing.T) {
	args := []string{"--harpoon.additional-transport", "http-streamable"}
	lookup := map[string]string{
		"CONTROL_PLANE_TUNNEL_ID": envTunnelID,
		"CONTROL_PLANE_API_KEY":   "control-key",
		"LOG_FORMAT":              "struct-text",
		"MCP_SERVER_URL":          "https://mcp.example",
	}

	cfg, err := Load(args, func(key string) (string, bool) {
		val, ok := lookup[key]
		return val, ok
	})
	if err != nil {
		t.Fatalf("unexpected error loading harpoon transport: %v", err)
	}
	if !cfg.Harpoon.AdditionalTransportEnabled(HarpoonTransportHTTPStreamable) {
		t.Fatalf("expected harpoon http-streamable transport to be enabled")
	}
}

func TestLoadParsesHarpoonCapturePayloads(t *testing.T) {
	args := []string{"--harpoon.capture-payloads"}
	lookup := map[string]string{
		"CONTROL_PLANE_TUNNEL_ID": envTunnelID,
		"CONTROL_PLANE_API_KEY":   "control-key",
		"LOG_FORMAT":              "struct-text",
		"MCP_SERVER_URL":          "https://mcp.example",
	}

	cfg, err := Load(args, func(key string) (string, bool) {
		val, ok := lookup[key]
		return val, ok
	})
	if err != nil {
		t.Fatalf("unexpected error loading harpoon capture payloads: %v", err)
	}
	if !cfg.Harpoon.CapturePayloads {
		t.Fatalf("expected harpoon capture payloads to be enabled")
	}
}

func TestLoadHarpoonHostClassifierDefaults(t *testing.T) {
	lookup := map[string]string{
		"CONTROL_PLANE_TUNNEL_ID": envTunnelID,
		"CONTROL_PLANE_API_KEY":   "control-key",
		"LOG_FORMAT":              "struct-text",
		"MCP_SERVER_URL":          "https://mcp.example",
	}

	cfg, err := Load(nil, func(key string) (string, bool) {
		val, ok := lookup[key]
		return val, ok
	})
	if err != nil {
		t.Fatalf("unexpected error loading defaults: %v", err)
	}
	if !cfg.Harpoon.HostClassifier.IncludeLoopback {
		t.Fatalf("expected loopback default to be true")
	}
	if !cfg.Harpoon.HostClassifier.IncludePrivate {
		t.Fatalf("expected private default to be true")
	}
	if len(cfg.Harpoon.HostClassifier.IncludeSuffix) != 0 || len(cfg.Harpoon.HostClassifier.IncludeRegex) != 0 {
		t.Fatalf("expected empty suffix/regex defaults")
	}
}

func TestLoadHarpoonHostClassifierFlags(t *testing.T) {
	args := []string{
		"--harpoon.hosts-include-suffix", "internal",
		"--harpoon.hosts-include-regex", "^svc-.*",
		"--harpoon.hosts-include-loopback=false",
		"--harpoon.hosts-include-private=false",
	}
	lookup := map[string]string{
		"CONTROL_PLANE_TUNNEL_ID": envTunnelID,
		"CONTROL_PLANE_API_KEY":   "control-key",
		"LOG_FORMAT":              "struct-text",
		"MCP_SERVER_URL":          "https://mcp.example",
	}

	cfg, err := Load(args, func(key string) (string, bool) {
		val, ok := lookup[key]
		return val, ok
	})
	if err != nil {
		t.Fatalf("unexpected error loading host flags: %v", err)
	}
	if cfg.Harpoon.HostClassifier.IncludeLoopback || cfg.Harpoon.HostClassifier.IncludePrivate {
		t.Fatalf("expected loopback/private to be disabled")
	}
	if len(cfg.Harpoon.HostClassifier.IncludeSuffix) != 1 || cfg.Harpoon.HostClassifier.IncludeSuffix[0] != "internal" {
		t.Fatalf("unexpected suffix values: %#v", cfg.Harpoon.HostClassifier.IncludeSuffix)
	}
	if len(cfg.Harpoon.HostClassifier.IncludeRegex) != 1 || cfg.Harpoon.HostClassifier.IncludeRegex[0] != "^svc-.*" {
		t.Fatalf("unexpected regex values: %#v", cfg.Harpoon.HostClassifier.IncludeRegex)
	}
}

func TestLoadRejectsInvalidHarpoonHostRegex(t *testing.T) {
	lookup := map[string]string{
		"CONTROL_PLANE_TUNNEL_ID":     envTunnelID,
		"CONTROL_PLANE_API_KEY":       "control-key",
		"LOG_FORMAT":                  "struct-text",
		"MCP_SERVER_URL":              "https://mcp.example",
		"HARPOON_HOSTS_INCLUDE_REGEX": "[",
	}

	_, err := Load(nil, func(key string) (string, bool) {
		val, ok := lookup[key]
		return val, ok
	})
	if err == nil {
		t.Fatalf("expected error for invalid host regex")
	}
}

func TestLoadRejectsHarpoonMaxResponseBytesTooHigh(t *testing.T) {
	lookup := map[string]string{
		"CONTROL_PLANE_TUNNEL_ID":    envTunnelID,
		"CONTROL_PLANE_API_KEY":      "control-key",
		"LOG_FORMAT":                 "struct-text",
		"MCP_SERVER_URL":             "https://mcp.example",
		"HARPOON_MAX_RESPONSE_BYTES": "999999",
	}

	_, err := Load(nil, func(key string) (string, bool) {
		val, ok := lookup[key]
		return val, ok
	})
	if err == nil {
		t.Fatalf("expected error for oversized harpoon max response bytes")
	}
}

func TestLoadRejectsUnsetEnvForControlPlaneAPIKeyFlag(t *testing.T) {
	args := []string{"--control-plane.api-key", "env:OPENAI_API_KEY_STAGING"}
	_, err := Load(args, func(key string) (string, bool) {
		if key == "LOG_FORMAT" {
			return "struct-text", true
		}
		if key == "CONTROL_PLANE_TUNNEL_ID" {
			return envTunnelID, true
		}
		if key == "MCP_SERVER_URL" {
			return "https://mcp.default", true
		}
		return "", false
	})
	if err == nil {
		t.Fatalf("expected error when env referenced by control-plane.api-key flag not set")
	}
	if !strings.Contains(err.Error(), "not set") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadUsesControlPlaneAPIKeyFile(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "api_key.txt")
	if err := os.WriteFile(secretPath, []byte("file-key\n"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}

	args := []string{"--control-plane.api-key", "file:" + secretPath}
	lookup := map[string]string{
		"CONTROL_PLANE_TUNNEL_ID": envTunnelID,
		"LOG_FORMAT":              "struct-text",
		"MCP_SERVER_URL":          "https://mcp.default",
	}
	cfg, err := Load(args, func(key string) (string, bool) {
		val, ok := lookup[key]
		return val, ok
	})
	if err != nil {
		t.Fatalf("unexpected error when using file-backed control-plane.api-key flag: %v", err)
	}
	if cfg.ControlPlane.APIKey != "file-key" {
		t.Fatalf("expected control plane API key file-key, got %s", cfg.ControlPlane.APIKey)
	}
}

func TestLoadRejectsMissingControlPlaneAPIKeyFile(t *testing.T) {
	dir := t.TempDir()
	args := []string{"--control-plane.api-key", "file:" + filepath.Join(dir, "missing.txt")}
	_, err := Load(args, func(key string) (string, bool) {
		if key == "LOG_FORMAT" {
			return "struct-text", true
		}
		if key == "CONTROL_PLANE_TUNNEL_ID" {
			return envTunnelID, true
		}
		if key == "MCP_SERVER_URL" {
			return "https://mcp.default", true
		}
		return "", false
	})
	if err == nil {
		t.Fatalf("expected error when control-plane.api-key file does not exist")
	}
	if !strings.Contains(err.Error(), "read control-plane api key file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsEmptyControlPlaneAPIKeyFile(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "api_key.txt")
	if err := os.WriteFile(secretPath, []byte("\n"), 0o600); err != nil {
		t.Fatalf("write empty secret file: %v", err)
	}

	args := []string{"--control-plane.api-key", "file:" + secretPath}
	_, err := Load(args, func(key string) (string, bool) {
		if key == "LOG_FORMAT" {
			return "struct-text", true
		}
		if key == "CONTROL_PLANE_TUNNEL_ID" {
			return envTunnelID, true
		}
		if key == "MCP_SERVER_URL" {
			return "https://mcp.default", true
		}
		return "", false
	})
	if err == nil {
		t.Fatalf("expected error when control-plane.api-key file is empty")
	}
	if !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsNonPositiveMCPConnectionTTL(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "https://mcp.default",
		"--mcp.connection-max-ttl=0s",
	}
	_, err := Load(args, func(key string) (string, bool) {
		if key == "CONTROL_PLANE_API_KEY" {
			return "key", true
		}
		if key == "LOG_FORMAT" {
			return "struct-text", true
		}
		return "", false
	})
	if err == nil {
		t.Fatalf("expected error for non-positive connection ttl")
	}
	if !strings.Contains(err.Error(), "mcp.connection-max-ttl") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsEmptyControlPlaneAPIKey(t *testing.T) {
	_, err := Load(nil, func(key string) (string, bool) {
		if key == "CONTROL_PLANE_API_KEY" {
			return "", true
		}
		if key == "CONTROL_PLANE_TUNNEL_ID" {
			return envTunnelID, true
		}
		if key == "MCP_SERVER_URL" {
			return "https://mcp.default", true
		}
		return "", false
	})
	if err == nil {
		t.Fatalf("expected error when CONTROL_PLANE_API_KEY empty")
	}
	if !strings.Contains(err.Error(), "CONTROL_PLANE_API_KEY") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsLogLevelOverrideWithoutFormat(t *testing.T) {
	_, err := Load(nil, func(key string) (string, bool) {
		switch key {
		case "CONTROL_PLANE_API_KEY":
			return "key", true
		case "CONTROL_PLANE_TUNNEL_ID":
			return envTunnelID, true
		case "LOG_LEVEL":
			return "debug", true
		case "MCP_SERVER_URL":
			return "https://mcp.default", true
		default:
			return "", false
		}
	})
	if err == nil {
		t.Fatalf("expected error when overriding log level without specifying log format")
	}
	if !strings.Contains(err.Error(), "log level requires 'struct-text' or 'json' log format") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadDefaultsLogFileToStructTextWhenFormatUnset(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--log.file", "/tmp/tunnel.log",
	}
	cfg, err := Load(args, func(key string) (string, bool) {
		switch key {
		case "OPENAI_API_KEY":
			return "key", true
		case "MCP_SERVER_URL":
			return "https://mcp.default", true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Logging.Format != LogFormatStructText {
		t.Fatalf("expected log format struct-text, got %s", cfg.Logging.Format)
	}
	if cfg.Logging.File != "/tmp/tunnel.log" {
		t.Fatalf("expected log file /tmp/tunnel.log, got %s", cfg.Logging.File)
	}
}

func TestLoadKeepsExplicitJSONFormatWhenLogFileIsSet(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--log.file", "/tmp/tunnel.jsonl",
		"--log.format", "json",
	}
	cfg, err := Load(args, func(key string) (string, bool) {
		switch key {
		case "OPENAI_API_KEY":
			return "key", true
		case "MCP_SERVER_URL":
			return "https://mcp.default", true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Logging.Format != LogFormatJSON {
		t.Fatalf("expected log format json, got %s", cfg.Logging.Format)
	}
	if cfg.Logging.File != "/tmp/tunnel.jsonl" {
		t.Fatalf("expected log file /tmp/tunnel.jsonl, got %s", cfg.Logging.File)
	}
}

func TestLoadRejectsUnsupportedFormatWhenLogFileIsSet(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--log.file", "/tmp/tunnel.log",
		"--log.format", "yaml",
	}
	_, err := Load(args, func(key string) (string, bool) {
		switch key {
		case "OPENAI_API_KEY":
			return "key", true
		case "MCP_SERVER_URL":
			return "https://mcp.default", true
		default:
			return "", false
		}
	})
	if err == nil {
		t.Fatalf("expected error for unsupported log format when log.file is set")
	}
	if !strings.Contains(err.Error(), "unsupported log format") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRequiresTunnelID(t *testing.T) {
	cfgLookup := func(key string) (string, bool) {
		switch key {
		case "OPENAI_API_KEY":
			return "default-key", true
		case "LOG_FORMAT":
			return "struct-text", true
		case "MCP_SERVER_URL":
			return "https://mcp.default", true
		default:
			return "", false
		}
	}

	_, err := Load(nil, cfgLookup)
	if err == nil {
		t.Fatalf("expected error when tunnel id not provided")
	}
	if !strings.Contains(err.Error(), "tunnel ID is required") {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("rejects empty tunnel id flag", func(t *testing.T) {
		args := []string{"--control-plane.tunnel-id", ""}
		_, err := Load(args, func(key string) (string, bool) {
			if key == "OPENAI_API_KEY" {
				return "key", true
			}
			if key == "MCP_SERVER_URL" {
				return "https://mcp.default", true
			}
			return "", false
		})
		if err == nil {
			t.Fatalf("expected error when tunnel id flag empty")
		}
		if !strings.Contains(err.Error(), "tunnel ID is required") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("rejects empty CONTROL_PLANE_TUNNEL_ID env", func(t *testing.T) {
		_, err := Load(nil, func(key string) (string, bool) {
			if key == "CONTROL_PLANE_TUNNEL_ID" {
				return "", true
			}
			if key == "OPENAI_API_KEY" {
				return "key", true
			}
			if key == "MCP_SERVER_URL" {
				return "https://mcp.default", true
			}
			return "", false
		})
		if err == nil {
			t.Fatalf("expected error when CONTROL_PLANE_TUNNEL_ID env empty")
		}
		if !strings.Contains(err.Error(), "tunnel ID is required") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestLoadRequiresMCPServerURL(t *testing.T) {
	_, err := Load(nil, func(key string) (string, bool) {
		if key == "CONTROL_PLANE_TUNNEL_ID" {
			return envTunnelID, true
		}
		if key == "OPENAI_API_KEY" {
			return "key", true
		}
		if key == "LOG_FORMAT" {
			return "struct-text", true
		}
		return "", false
	})
	if err == nil {
		t.Fatalf("expected error when MCP server URL missing")
	}
	if !strings.Contains(err.Error(), "set --mcp.server-url or --mcp.command") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadPrioritizesTunnelIDErrorBeforeMissingMCPBinding(t *testing.T) {
	_, err := Load(nil, func(key string) (string, bool) {
		switch key {
		case "OPENAI_API_KEY":
			return "key", true
		case "LOG_FORMAT":
			return "struct-text", true
		default:
			return "", false
		}
	})
	if err == nil {
		t.Fatalf("expected error when tunnel id and MCP binding are both missing")
	}
	if !strings.Contains(err.Error(), "tunnel ID is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadUsesMCPCommand(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.command", `echo "hello world"`,
	}
	cfg, err := Load(args, func(key string) (string, bool) {
		switch key {
		case "OPENAI_API_KEY":
			return "key", true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.MCP.TransportKind != MCPTransportStdio {
		t.Fatalf("expected MCP transport stdio, got %s", cfg.MCP.TransportKind)
	}
	if cfg.MCP.ServerURL != nil {
		t.Fatalf("expected MCP server URL to be nil for stdio transport")
	}
	if cfg.MCP.Command == "" {
		t.Fatalf("expected MCP command to be set")
	}
	if got := cfg.MCP.CommandArgs; len(got) != 2 || got[0] != "echo" || got[1] != "hello world" {
		t.Fatalf("unexpected MCP command args: %v", got)
	}
}

func TestLoadRejectsMCPCommandAndServerURLSameChannel(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "channel=main,url=https://flag-mcp",
		"--mcp.command", "channel=main,command=echo hello",
	}
	_, err := Load(args, func(key string) (string, bool) {
		if key == "OPENAI_API_KEY" {
			return "key", true
		}
		return "", false
	})
	if err == nil {
		t.Fatalf("expected error when mcp.command and mcp.server-url target same channel")
	}
	if !strings.Contains(err.Error(), "duplicate channel") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadAllowsMCPCommandAndServerURLDifferentChannels(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "channel=main,url=https://flag-mcp",
		"--mcp.command", "channel=tools,command=echo hello",
	}
	cfg, err := Load(args, func(key string) (string, bool) {
		if key == "OPENAI_API_KEY" {
			return "key", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := cfg.MCP.ChannelBindingFor(types.Channel("tools")); got == nil || got.TransportKind != MCPTransportStdio {
		t.Fatalf("expected stdio binding for tools channel, got %v", got)
	}
	if got := cfg.MCP.ChannelBindingFor(types.DefaultChannel); got == nil || got.ServerURL == nil {
		t.Fatalf("expected main channel binding to include server url, got %v", got)
	}
}

func TestLoadParsesChannelQualifiedEntries(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "channel=main,url=https://flag-mcp",
		"--mcp.command", "channel=tools,command=echo hello",
	}
	cfg, err := Load(args, func(key string) (string, bool) {
		if key == "OPENAI_API_KEY" {
			return "key", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	mainBinding := cfg.MCP.ChannelBindingFor(types.DefaultChannel)
	if mainBinding == nil || mainBinding.ServerURL == nil {
		t.Fatalf("expected main binding URL, got %v", mainBinding)
	}
	toolsBinding := cfg.MCP.ChannelBindingFor(types.Channel("tools"))
	if toolsBinding == nil || toolsBinding.TransportKind != MCPTransportStdio {
		t.Fatalf("expected tools stdio binding, got %v", toolsBinding)
	}
}

func TestLoadParsesQualifiedMCPCommandWithCommaInCommandValue(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.command", `channel=main,command=python -c "print(1,2,3)"`,
	}
	cfg, err := Load(args, func(key string) (string, bool) {
		if key == "OPENAI_API_KEY" {
			return "key", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.MCP.Command != `python -c "print(1,2,3)"` {
		t.Fatalf("unexpected MCP command: %q", cfg.MCP.Command)
	}
	if got := cfg.MCP.CommandArgs; len(got) != 3 || got[0] != "python" || got[1] != "-c" || got[2] != "print(1,2,3)" {
		t.Fatalf("unexpected MCP command args: %v", got)
	}
}

func TestLoadParsesQualifiedMCPCommandWithTrailingChannelAndCommaInCommandValue(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.command", `command=python -c "print(1,2,3)",channel=main`,
	}
	cfg, err := Load(args, func(key string) (string, bool) {
		if key == "OPENAI_API_KEY" {
			return "key", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.MCP.Command != `python -c "print(1,2,3)"` {
		t.Fatalf("unexpected MCP command: %q", cfg.MCP.Command)
	}
	if got := cfg.MCP.CommandArgs; len(got) != 3 || got[2] != "print(1,2,3)" {
		t.Fatalf("unexpected MCP command args: %v", got)
	}
}

func TestLoadParsesEnvMCPEntries(t *testing.T) {
	cfg, err := Load(nil, func(key string) (string, bool) {
		switch key {
		case "OPENAI_API_KEY":
			return "key", true
		case "CONTROL_PLANE_TUNNEL_ID":
			return envTunnelID, true
		case "LOG_FORMAT":
			return "struct-text", true
		case "MCP_SERVER_URL":
			return "https://main.example.com/mcp\nchannel=foo,url=https://foo.example.com/mcp", true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := cfg.MCP.ChannelBindingFor(types.DefaultChannel); got == nil || got.ServerURL == nil {
		t.Fatalf("expected main binding from env, got %v", got)
	}
	if got := cfg.MCP.ChannelBindingFor(types.Channel("foo")); got == nil || got.ServerURL == nil {
		t.Fatalf("expected foo binding from env, got %v", got)
	}
}

func TestLoadAllowsSemicolonsInMCPCommandEnv(t *testing.T) {
	cfg, err := Load(nil, func(key string) (string, bool) {
		switch key {
		case "OPENAI_API_KEY":
			return "key", true
		case "CONTROL_PLANE_TUNNEL_ID":
			return envTunnelID, true
		case "LOG_FORMAT":
			return "struct-text", true
		case "MCP_COMMAND":
			return `bash -c "echo a; echo b"`, true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.MCP.Command != `bash -c "echo a; echo b"` {
		t.Fatalf("expected MCP command to preserve semicolons, got %q", cfg.MCP.Command)
	}
	if got := cfg.MCP.CommandArgs; len(got) != 3 || got[0] != "bash" || got[1] != "-c" || got[2] != "echo a; echo b" {
		t.Fatalf("unexpected MCP command args: %v", got)
	}
}

func TestLoadRejectsHarpoonChannelBinding(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "channel=harpoon,url=https://flag-mcp",
	}
	_, err := Load(args, func(key string) (string, bool) {
		if key == "OPENAI_API_KEY" {
			return "key", true
		}
		return "", false
	})
	if err == nil {
		t.Fatalf("expected error when configuring harpoon channel binding")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsHarpoonCommandBinding(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.command", "channel=harpoon,command=echo hello",
	}
	_, err := Load(args, func(key string) (string, bool) {
		if key == "OPENAI_API_KEY" {
			return "key", true
		}
		return "", false
	})
	if err == nil {
		t.Fatalf("expected error when configuring harpoon channel command binding")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsMissingMainChannel(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "channel=tools,url=https://flag-mcp",
	}
	_, err := Load(args, func(key string) (string, bool) {
		if key == "OPENAI_API_KEY" {
			return "key", true
		}
		return "", false
	})
	if err == nil {
		t.Fatalf("expected error when main channel binding missing")
	}
	if !strings.Contains(err.Error(), "add channel=main") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadParsesMCPClientCertificateFlags(t *testing.T) {
	certPath, keyPath := writeTempClientCertPair(t)
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "channel=main,url=https://mcp.example",
		"--mcp.client-cert", certPath,
		"--mcp.client-key", keyPath,
	}
	cfg, err := Load(args, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.MCP.ClientCertificate == nil {
		t.Fatalf("expected MCP client certificate to be configured")
	}
	if cfg.MCP.ClientCertificate.CertPath != certPath {
		t.Fatalf("expected cert path %q, got %q", certPath, cfg.MCP.ClientCertificate.CertPath)
	}
	mainBinding := cfg.MCP.MainChannelBinding()
	if mainBinding == nil || mainBinding.ClientCertificate == nil {
		t.Fatalf("expected main binding client certificate")
	}
	if mainBinding.ClientCertificate.KeyPath != keyPath {
		t.Fatalf("expected key path %q, got %q", keyPath, mainBinding.ClientCertificate.KeyPath)
	}
}

func TestLoadParsesControlPlaneClientCertificateFlagsAndSelectsMTLSBaseURL(t *testing.T) {
	certPath, keyPath := writeTempClientCertPair(t)
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "channel=main,url=https://mcp.example",
		"--control-plane.client-cert", certPath,
		"--control-plane.client-key", keyPath,
	}
	cfg, err := Load(args, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.ControlPlane.ClientCertificate == nil {
		t.Fatalf("expected control-plane client certificate to be configured")
	}
	if cfg.ControlPlane.ClientCertificate.CertPath != certPath {
		t.Fatalf("expected cert path %q, got %q", certPath, cfg.ControlPlane.ClientCertificate.CertPath)
	}
	if cfg.ControlPlane.ClientCertificate.KeyPath != keyPath {
		t.Fatalf("expected key path %q, got %q", keyPath, cfg.ControlPlane.ClientCertificate.KeyPath)
	}
	if cfg.ControlPlane.BaseURL == nil || cfg.ControlPlane.BaseURL.String() != "https://mtls.api.openai.com" {
		t.Fatalf("expected mTLS base URL, got %v", cfg.ControlPlane.BaseURL)
	}
}

func TestLoadParsesControlPlaneClientCertificateEnvReferences(t *testing.T) {
	certPath, keyPath := writeTempClientCertPair(t)
	cfg, err := Load([]string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "channel=main,url=https://mcp.example",
	}, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY":     "control-key",
		"CONTROL_PLANE_CLIENT_CERT": "env:CONTROL_CERT_FILE",
		"CONTROL_PLANE_CLIENT_KEY":  "env:CONTROL_KEY_FILE",
		"CONTROL_CERT_FILE":         certPath,
		"CONTROL_KEY_FILE":          keyPath,
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.ControlPlane.ClientCertificate == nil {
		t.Fatalf("expected control-plane client certificate")
	}
	if cfg.ControlPlane.ClientCertificate.CertPath != certPath {
		t.Fatalf("expected cert path %q, got %q", certPath, cfg.ControlPlane.ClientCertificate.CertPath)
	}
}

func TestLoadParsesControlPlaneClientCertificateYAMLFileReferences(t *testing.T) {
	certPath, keyPath := writeTempClientCertPair(t)
	configPath := writeTempConfigFile(t, `
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: env:YAML_CONTROL_PLANE_API_KEY
  client_cert: file:`+certPath+`
  client_key: file:`+keyPath+`
mcp:
  server_urls:
    - channel: main
      url: https://yaml-mcp.example/mcp
`)
	cfg, err := Load([]string{"--config", configPath}, lookupEnvMap(map[string]string{
		"YAML_CONTROL_PLANE_API_KEY": "yaml-control-key",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.ControlPlane.ClientCertificate == nil {
		t.Fatalf("expected control-plane client certificate")
	}
	if cfg.ControlPlane.ClientCertificate.CertPath != certPath {
		t.Fatalf("expected cert path %q, got %q", certPath, cfg.ControlPlane.ClientCertificate.CertPath)
	}
	if cfg.ControlPlane.BaseURL == nil || cfg.ControlPlane.BaseURL.String() != "https://mtls.api.openai.com" {
		t.Fatalf("expected default control-plane URL to switch to mTLS host, got %v", cfg.ControlPlane.BaseURL)
	}
}

func TestLoadParsesMCPUnixSocketYAMLReference(t *testing.T) {
	configPath := writeTempConfigFile(t, `
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: env:YAML_CONTROL_PLANE_API_KEY
mcp:
  server_urls:
    - channel: main
      url: http://localhost/mcp
      unix_socket: env:YAML_MCP_SOCKET
`)
	cfg, err := Load([]string{"--config", configPath}, lookupEnvMap(map[string]string{
		"YAML_CONTROL_PLANE_API_KEY": "yaml-control-key",
		"YAML_MCP_SOCKET":            "/tmp/yaml-mcp.sock",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	binding := cfg.MCP.MainChannelBinding()
	if binding == nil {
		t.Fatal("expected main MCP binding")
		return
	}
	if binding.UnixSocketPath != "/tmp/yaml-mcp.sock" {
		t.Fatalf("expected unix socket path /tmp/yaml-mcp.sock, got %q", binding.UnixSocketPath)
	}
	if cfg.MCP.UnixSocketPath != "/tmp/yaml-mcp.sock" {
		t.Fatalf("expected main unix socket path /tmp/yaml-mcp.sock, got %q", cfg.MCP.UnixSocketPath)
	}
}

func TestLoadRejectsMCPUnixSocketProxyCombination(t *testing.T) {
	_, err := Load([]string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "channel=main,url=http://localhost/mcp,unix-socket=/tmp/mcp.sock,http-proxy=http://proxy.example:8080",
	}, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
	}))
	if err == nil {
		t.Fatal("expected unix socket proxy combination to fail")
	}
	if !strings.Contains(err.Error(), "unix-socket cannot be combined with http-proxy") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadPreservesExplicitNonDefaultControlPlaneBaseURLWithMTLS(t *testing.T) {
	certPath, keyPath := writeTempClientCertPair(t)
	cfg, err := Load([]string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--control-plane.base-url", "https://local-control.example",
		"--control-plane.client-cert", certPath,
		"--control-plane.client-key", keyPath,
		"--mcp.server-url", "channel=main,url=https://mcp.example",
	}, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.ControlPlane.BaseURL == nil || cfg.ControlPlane.BaseURL.String() != "https://local-control.example" {
		t.Fatalf("expected explicit base URL to be preserved, got %v", cfg.ControlPlane.BaseURL)
	}
}

func TestLoadRejectsIncompleteControlPlaneClientCertificate(t *testing.T) {
	certPath, _ := writeTempClientCertPair(t)
	_, err := Load([]string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--control-plane.client-cert", certPath,
		"--mcp.server-url", "channel=main,url=https://mcp.example",
	}, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
	}))
	if err == nil {
		t.Fatalf("expected error for incomplete control-plane client certificate")
	}
	if !strings.Contains(err.Error(), "control-plane") || !strings.Contains(err.Error(), "client key path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsMismatchedControlPlaneClientCertificate(t *testing.T) {
	certPath, _ := writeTempClientCertPair(t)
	_, keyPath := writeTempClientCertPair(t)
	_, err := Load([]string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--control-plane.client-cert", certPath,
		"--control-plane.client-key", keyPath,
		"--mcp.server-url", "channel=main,url=https://mcp.example",
	}, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
	}))
	if err == nil {
		t.Fatalf("expected error for mismatched control-plane client certificate")
	}
	if !strings.Contains(err.Error(), "control-plane") || !strings.Contains(err.Error(), "private key does not match") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadParsesMCPClientCertificatePerChannel(t *testing.T) {
	certPath, keyPath := writeTempClientCertPair(t)
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", fmt.Sprintf("channel=main,url=https://mcp.example,client-cert=%s,client-key=%s", certPath, keyPath),
	}
	cfg, err := Load(args, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	binding := cfg.MCP.MainChannelBinding()
	if binding == nil || binding.ClientCertificate == nil {
		t.Fatalf("expected main binding client certificate")
	}
	if binding.ClientCertificate.CertPath != certPath {
		t.Fatalf("expected cert path %q, got %q", certPath, binding.ClientCertificate.CertPath)
	}
}

func TestLoadSupportsDistinctMCPClientCertificatesPerChannel(t *testing.T) {
	mainCertPath, mainKeyPath := writeTempClientCertPair(t)
	analyticsCertPath, analyticsKeyPath := writeTempClientCertPair(t)
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", fmt.Sprintf("channel=main,url=https://mcp-main.example,client-cert=%s,client-key=%s", mainCertPath, mainKeyPath),
		"--mcp.server-url", fmt.Sprintf("channel=analytics,url=https://mcp-analytics.example,client-cert=%s,client-key=%s", analyticsCertPath, analyticsKeyPath),
	}
	cfg, err := Load(args, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	mainBinding := cfg.MCP.ChannelBindingFor(types.DefaultChannel)
	if mainBinding == nil || mainBinding.ClientCertificate == nil {
		t.Fatalf("expected main binding client certificate")
	}
	analyticsBinding := cfg.MCP.ChannelBindingFor(types.Channel("analytics"))
	if analyticsBinding == nil || analyticsBinding.ClientCertificate == nil {
		t.Fatalf("expected analytics binding client certificate")
	}
	if mainBinding.ClientCertificate.CertPath == analyticsBinding.ClientCertificate.CertPath {
		t.Fatalf("expected distinct per-channel certificates")
	}
}

func TestLoadMCPClientCertificateFallbackAndOverride(t *testing.T) {
	defaultCertPath, defaultKeyPath := writeTempClientCertPair(t)
	overrideCertPath, overrideKeyPath := writeTempClientCertPair(t)
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.client-cert", defaultCertPath,
		"--mcp.client-key", defaultKeyPath,
		"--mcp.server-url", "channel=main,url=https://mcp-main.example",
		"--mcp.server-url", fmt.Sprintf("channel=analytics,url=https://mcp-analytics.example,client-cert=%s,client-key=%s", overrideCertPath, overrideKeyPath),
	}
	cfg, err := Load(args, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	mainBinding := cfg.MCP.ChannelBindingFor(types.DefaultChannel)
	if mainBinding == nil || mainBinding.ClientCertificate == nil {
		t.Fatalf("expected main binding client certificate")
	}
	if mainBinding.ClientCertificate.CertPath != defaultCertPath {
		t.Fatalf("expected default cert path %q, got %q", defaultCertPath, mainBinding.ClientCertificate.CertPath)
	}
	analyticsBinding := cfg.MCP.ChannelBindingFor(types.Channel("analytics"))
	if analyticsBinding == nil || analyticsBinding.ClientCertificate == nil {
		t.Fatalf("expected analytics binding client certificate")
	}
	if analyticsBinding.ClientCertificate.CertPath != overrideCertPath {
		t.Fatalf("expected override cert path %q, got %q", overrideCertPath, analyticsBinding.ClientCertificate.CertPath)
	}
}

func TestLoadRejectsIncompleteMCPClientCertificate(t *testing.T) {
	certPath, _ := writeTempClientCertPair(t)
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "channel=main,url=https://mcp.example",
		"--mcp.client-cert", certPath,
	}
	_, err := Load(args, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
	}))
	if err == nil {
		t.Fatalf("expected error for incomplete MCP client certificate")
	}
	if !strings.Contains(err.Error(), "client key path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsIncompletePerChannelMCPClientCertificate(t *testing.T) {
	certPath, _ := writeTempClientCertPair(t)
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", fmt.Sprintf("channel=main,url=https://mcp.example,client-cert=%s", certPath),
	}
	_, err := Load(args, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
	}))
	if err == nil {
		t.Fatalf("expected error for incomplete per-channel MCP client certificate")
	}
	if !strings.Contains(err.Error(), "missing client-key") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadParsesMCPClientCertificateEnvReferences(t *testing.T) {
	certPath, keyPath := writeTempClientCertPair(t)
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "channel=main,url=https://mcp.example",
		"--mcp.client-cert", "env:MCP_CLIENT_CERT_FILE",
		"--mcp.client-key", "env:MCP_CLIENT_KEY_FILE",
	}
	cfg, err := Load(args, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
		"MCP_CLIENT_CERT_FILE":  certPath,
		"MCP_CLIENT_KEY_FILE":   keyPath,
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.MCP.MainChannelBinding() == nil || cfg.MCP.MainChannelBinding().ClientCertificate == nil {
		t.Fatalf("expected main binding client certificate")
	}
}

func TestParseCommandArgv(t *testing.T) {
	testCases := map[string]struct {
		raw     string
		want    []string
		wantErr bool
	}{
		"simple": {
			raw:  "python -m server --flag value",
			want: []string{"python", "-m", "server", "--flag", "value"},
		},
		"double-quotes": {
			raw:  `node "path with spaces/app.js" --name="Ada Lovelace"`,
			want: []string{"node", "path with spaces/app.js", "--name=Ada Lovelace"},
		},
		"single-quotes": {
			raw:  "bash -c 'echo hello world'",
			want: []string{"bash", "-c", "echo hello world"},
		},
		"escaped-space": {
			raw:  `cmd hello\ world`,
			want: []string{"cmd", "hello world"},
		},
		"unterminated-quote": {
			raw:     `echo "unterminated`,
			wantErr: true,
		},
		"empty": {
			raw:     "   ",
			wantErr: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got, err := parseCommandArgv(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !equalStringSlices(got, tc.want) {
				t.Fatalf("unexpected argv: got %v want %v", got, tc.want)
			}
		})
	}
}

func TestLoadValidatesControlPlaneBaseURL(t *testing.T) {
	_, err := Load([]string{"--control-plane.base-url", "http://"}, func(key string) (string, bool) {
		if key == "OPENAI_API_KEY" {
			return "key", true
		}
		if key == "CONTROL_PLANE_TUNNEL_ID" {
			return envTunnelID, true
		}
		if key == "MCP_SERVER_URL" {
			return "https://mcp.default", true
		}
		return "", false
	})
	if err == nil {
		t.Fatalf("expected error when control plane base URL invalid")
	}
	if !strings.Contains(err.Error(), "invalid control-plane.base-url") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadValidatesControlPlaneURLPath(t *testing.T) {
	testCases := map[string]string{
		"must-start-with-slash":  "gateway/dev/us",
		"must-not-include-host":  "https://gateway.example/dev/us",
		"must-not-include-query": "/gateway/dev/us?workspace=prod",
	}
	for name, urlPath := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := Load([]string{"--control-plane.url-path", urlPath}, func(key string) (string, bool) {
				if key == "OPENAI_API_KEY" {
					return "key", true
				}
				if key == "CONTROL_PLANE_TUNNEL_ID" {
					return envTunnelID, true
				}
				if key == "MCP_SERVER_URL" {
					return "https://mcp.default", true
				}
				return "", false
			})
			if err == nil {
				t.Fatalf("expected error when control plane url path invalid")
			}
			if !strings.Contains(err.Error(), "invalid control-plane.url-path") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadValidatesTunnelIDFormat(t *testing.T) {
	t.Parallel()

	testCases := map[string]string{
		"contains-space":       "bad id",
		"missing-prefix":       "0123456789abcdef0123456789abcdef",
		"uppercase-characters": "tunnel_0123456789ABCDEF0123456789abcdef",
		"too-short":            "tunnel_1234",
	}

	for name, tunnelID := range testCases {
		name := name
		tunnelID := tunnelID
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := Load([]string{"--control-plane.tunnel-id", tunnelID}, func(key string) (string, bool) {
				if key == "OPENAI_API_KEY" {
					return "key", true
				}
				if key == "MCP_SERVER_URL" {
					return "https://mcp.default", true
				}
				return "", false
			})
			if err == nil {
				t.Fatalf("expected error when tunnel id %q violates format", tunnelID)
			}
			if !strings.Contains(err.Error(), "invalid tunnel ID") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadRejectsTunnelIDUnsafeForPath(t *testing.T) {
	_, err := Load([]string{"--control-plane.tunnel-id", "path/unsafe"}, func(key string) (string, bool) {
		switch key {
		case "OPENAI_API_KEY":
			return "key", true
		case "MCP_SERVER_URL":
			return "https://mcp.default", true
		default:
			return "", false
		}
	})
	if err == nil {
		t.Fatalf("expected error when tunnel id is not safe for path parameters")
	}
	if !strings.Contains(err.Error(), "path parameter") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsControlPlaneMaxInFlightAboveLimit(t *testing.T) {
	t.Run("flag value", func(t *testing.T) {
		args := []string{
			"--control-plane.tunnel-id", flagTunnelID,
			fmt.Sprintf("--control-plane.max-inflight=%d", maxControlPlaneMaxInFlight+1),
		}
		_, err := Load(args, func(key string) (string, bool) {
			if key == "CONTROL_PLANE_API_KEY" {
				return "key", true
			}
			if key == "MCP_SERVER_URL" {
				return "https://mcp.default", true
			}
			return "", false
		})
		if err == nil {
			t.Fatalf("expected error when max in-flight flag exceeds limit")
		}
		if !strings.Contains(err.Error(), "less than or equal") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("env value", func(t *testing.T) {
		_, err := Load([]string{"--control-plane.tunnel-id", flagTunnelID}, func(key string) (string, bool) {
			switch key {
			case "CONTROL_PLANE_API_KEY":
				return "key", true
			case "CONTROL_PLANE_MAX_INFLIGHT_REQUESTS":
				return fmt.Sprintf("%d", maxControlPlaneMaxInFlight+1), true
			case "MCP_SERVER_URL":
				return "https://mcp.default", true
			default:
				return "", false
			}
		})
		if err == nil {
			t.Fatalf("expected error when max in-flight env exceeds limit")
		}
		if !strings.Contains(err.Error(), "less than or equal") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestLoadRejectsUnsupportedFormat(t *testing.T) {
	args := []string{"--control-plane.tunnel-id", flagTunnelID, "--log.format", "yaml"}
	_, err := Load(args, func(key string) (string, bool) {
		if key == "OPENAI_API_KEY" {
			return "key", true
		}
		if key == "MCP_SERVER_URL" {
			return "https://mcp.default", true
		}
		return "", false
	})
	if err == nil {
		t.Fatalf("expected error for unsupported log format")
	}
}

func TestLoadRequiresAPIKey(t *testing.T) {
	_, err := Load(nil, func(key string) (string, bool) {
		if key == "MCP_SERVER_URL" {
			return "https://mcp.default", true
		}
		if key == "CONTROL_PLANE_TUNNEL_ID" {
			return envTunnelID, true
		}
		return "", false
	})
	if err == nil {
		t.Fatalf("expected error when control plane API key missing")
	}
	if !strings.Contains(err.Error(), "CONTROL_PLANE_API_KEY") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		key   string
		val   string
	}{
		{
			input: "extra-header: true",
			key:   "extra-header",
			val:   "true",
		},
		{
			input: "  X-Debug  :  1  ",
			key:   "X-Debug",
			val:   "1",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			key, val, err := parseHeader(tc.input)
			if err != nil {
				t.Fatalf("parseHeader(%q) returned error: %v", tc.input, err)
			}
			if key != tc.key || val != tc.val {
				t.Fatalf("parseHeader(%q) = (%q, %q), want (%q, %q)", tc.input, key, val, tc.key, tc.val)
			}
		})
	}
}

func TestParseHeaderRejectsInvalid(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"no-colon",
		": missing-key",
		"Missing-value:   ",
	} {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			if _, _, err := parseHeader(input); err == nil {
				t.Fatalf("expected error parsing %q, got nil", input)
			}
		})
	}
}

func TestBuildControlPlaneExtraHeadersFromEnv(t *testing.T) {
	t.Parallel()

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	RegisterFlags(fs)

	lookup := func(key string) (string, bool) {
		if key == "CONTROL_PLANE_EXTRA_HEADERS" {
			return "extra-header: true; x-debug:1; another-header: value", true
		}
		return "", false
	}

	headers, err := buildControlPlaneExtraHeaders(fs, lookup)
	if err != nil {
		t.Fatalf("buildControlPlaneExtraHeaders returned error: %v", err)
	}
	if len(headers) != 3 {
		t.Fatalf("expected 3 headers, got %d", len(headers))
	}
	if headers["extra-header"] != "true" {
		t.Fatalf("expected extra-header=true, got %q", headers["extra-header"])
	}
	if headers["x-debug"] != "1" {
		t.Fatalf("expected x-debug=1, got %q", headers["x-debug"])
	}
	if headers["another-header"] != "value" {
		t.Fatalf("expected another-header=value, got %q", headers["another-header"])
	}
}

func TestBuildControlPlaneExtraHeadersFromFlags(t *testing.T) {
	t.Parallel()

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	RegisterFlags(fs)

	if err := fs.Parse([]string{
		`--control-plane.extra-headers`, `extra-header: true`,
		`--control-plane.extra-headers`, `X-Trace-Id: abc123`,
	}); err != nil {
		t.Fatalf("flag parse failed: %v", err)
	}

	headers, err := buildControlPlaneExtraHeaders(fs, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("buildControlPlaneExtraHeaders returned error: %v", err)
	}
	if len(headers) != 2 {
		t.Fatalf("expected 2 headers, got %d", len(headers))
	}
	if headers["extra-header"] != "true" {
		t.Fatalf("expected extra-header=true, got %q", headers["extra-header"])
	}
	if headers["X-Trace-Id"] != "abc123" {
		t.Fatalf("expected X-Trace-Id=abc123, got %q", headers["X-Trace-Id"])
	}
}

func TestBuildControlPlaneExtraHeadersResolvesEnvAndFileValues(t *testing.T) {
	t.Parallel()

	headerFile := writeTempSecretFile(t, "file-secret\n")
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	RegisterFlags(fs)

	if err := fs.Parse([]string{
		`--control-plane.extra-headers`, `X-Env-Auth: env:CONTROL_HEADER_SECRET`,
		`--control-plane.extra-headers`, `X-File-Auth: file:` + headerFile,
	}); err != nil {
		t.Fatalf("flag parse failed: %v", err)
	}

	headers, err := buildControlPlaneExtraHeaders(fs, lookupEnvMap(map[string]string{
		"CONTROL_HEADER_SECRET": "env-secret",
	}))
	if err != nil {
		t.Fatalf("buildControlPlaneExtraHeaders returned error: %v", err)
	}
	if headers["X-Env-Auth"] != "env-secret" {
		t.Fatalf("expected X-Env-Auth from env, got %#v", headers)
	}
	if headers["X-File-Auth"] != "file-secret" {
		t.Fatalf("expected X-File-Auth from file, got %#v", headers)
	}
}

func TestBuildControlPlaneExtraHeadersRejectsReservedHeaders(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		flagValue string
		envValue  string
	}{
		{name: "authorization from flag", flagValue: "Authorization: Bearer attacker"},
		{name: "user agent from flag", flagValue: "User-Agent: custom-agent"},
		{name: "client version from flag", flagValue: "X-Tunnel-Client-Version: dev"},
		{name: "authorization from env", envValue: "authorization: Bearer attacker"},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
			RegisterFlags(fs)
			lookup := func(string) (string, bool) { return "", false }
			if tc.flagValue != "" {
				if err := fs.Parse([]string{`--control-plane.extra-headers`, tc.flagValue}); err != nil {
					t.Fatalf("flag parse failed: %v", err)
				}
			}
			if tc.envValue != "" {
				lookup = lookupEnvMap(map[string]string{"CONTROL_PLANE_EXTRA_HEADERS": tc.envValue})
			}

			_, err := buildControlPlaneExtraHeaders(fs, lookup)
			if err == nil {
				t.Fatalf("expected reserved control-plane header to be rejected")
			}
			if !strings.Contains(err.Error(), "cannot override control-plane authentication") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateFileConfigSyntaxRejectsReservedControlPlaneExtraHeaders(t *testing.T) {
	t.Parallel()

	err := validateFileConfigSyntax(fileConfig{
		ControlPlane: fileControlPlaneConfig{
			ExtraHeaders: map[string]string{
				"Authorization": "Bearer attacker",
			},
		},
	})
	if err == nil {
		t.Fatalf("expected reserved YAML control-plane header to be rejected")
	}
	if !strings.Contains(err.Error(), "cannot override control-plane authentication") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildMCPExtraHeadersFromEnvAndFlags(t *testing.T) {
	t.Parallel()

	t.Run("env", func(t *testing.T) {
		t.Parallel()
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		RegisterFlags(fs)
		lookup := lookupEnvMap(map[string]string{
			"MCP_EXTRA_HEADERS":           "X-Internal-Auth: env-static; X-Trace: env-trace",
			"MCP_DISCOVERY_EXTRA_HEADERS": "X-Discovery-Auth: env-discovery",
		})

		headers, err := buildExtraHeaders(fs, lookup, "mcp.extra-headers", "MCP_EXTRA_HEADERS")
		if err != nil {
			t.Fatalf("build mcp extra headers returned error: %v", err)
		}
		if headers["X-Internal-Auth"] != "env-static" || headers["X-Trace"] != "env-trace" {
			t.Fatalf("unexpected MCP extra headers: %#v", headers)
		}

		discoveryHeaders, err := buildExtraHeaders(fs, lookup, "mcp.discovery-extra-headers", "MCP_DISCOVERY_EXTRA_HEADERS")
		if err != nil {
			t.Fatalf("build mcp discovery extra headers returned error: %v", err)
		}
		if discoveryHeaders["X-Discovery-Auth"] != "env-discovery" {
			t.Fatalf("unexpected MCP discovery extra headers: %#v", discoveryHeaders)
		}
	})

	t.Run("flags", func(t *testing.T) {
		t.Parallel()
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		RegisterFlags(fs)
		if err := fs.Parse([]string{
			`--mcp.extra-headers`, `X-Internal-Auth: flag-static`,
			`--mcp.extra-headers`, `X-Trace: flag-trace`,
			`--mcp.discovery-extra-headers`, `X-Discovery-Auth: flag-discovery`,
		}); err != nil {
			t.Fatalf("flag parse failed: %v", err)
		}

		headers, err := buildExtraHeaders(fs, func(string) (string, bool) { return "", false }, "mcp.extra-headers", "MCP_EXTRA_HEADERS")
		if err != nil {
			t.Fatalf("build mcp extra headers returned error: %v", err)
		}
		if headers["X-Internal-Auth"] != "flag-static" || headers["X-Trace"] != "flag-trace" {
			t.Fatalf("unexpected MCP extra headers: %#v", headers)
		}

		discoveryHeaders, err := buildExtraHeaders(fs, func(string) (string, bool) { return "", false }, "mcp.discovery-extra-headers", "MCP_DISCOVERY_EXTRA_HEADERS")
		if err != nil {
			t.Fatalf("build mcp discovery extra headers returned error: %v", err)
		}
		if discoveryHeaders["X-Discovery-Auth"] != "flag-discovery" {
			t.Fatalf("unexpected MCP discovery extra headers: %#v", discoveryHeaders)
		}
	})
}

func TestBuildMCPExtraHeadersResolvesEnvFileAndRejectsInvalidReferences(t *testing.T) {
	t.Parallel()

	t.Run("envAndFile", func(t *testing.T) {
		t.Parallel()
		headerFile := writeTempSecretFile(t, "discovery-file-secret\n")
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		RegisterFlags(fs)
		lookup := lookupEnvMap(map[string]string{
			"MCP_EXTRA_HEADERS":           "X-Internal-Auth: env:MCP_RUNTIME_SECRET",
			"MCP_DISCOVERY_EXTRA_HEADERS": "X-Discovery-Auth: file:" + headerFile,
			"MCP_RUNTIME_SECRET":          "runtime-env-secret",
		})

		headers, err := buildExtraHeaders(fs, lookup, "mcp.extra-headers", "MCP_EXTRA_HEADERS")
		if err != nil {
			t.Fatalf("build mcp extra headers returned error: %v", err)
		}
		if headers["X-Internal-Auth"] != "runtime-env-secret" {
			t.Fatalf("unexpected MCP extra headers: %#v", headers)
		}

		discoveryHeaders, err := buildExtraHeaders(fs, lookup, "mcp.discovery-extra-headers", "MCP_DISCOVERY_EXTRA_HEADERS")
		if err != nil {
			t.Fatalf("build mcp discovery extra headers returned error: %v", err)
		}
		if discoveryHeaders["X-Discovery-Auth"] != "discovery-file-secret" {
			t.Fatalf("unexpected MCP discovery extra headers: %#v", discoveryHeaders)
		}
	})

	t.Run("missingEnv", func(t *testing.T) {
		t.Parallel()
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		RegisterFlags(fs)
		lookup := lookupEnvMap(map[string]string{
			"MCP_EXTRA_HEADERS": "X-Internal-Auth: env:MISSING_MCP_SECRET",
		})
		if _, err := buildExtraHeaders(fs, lookup, "mcp.extra-headers", "MCP_EXTRA_HEADERS"); err == nil {
			t.Fatalf("expected missing env reference error")
		}
	})

	t.Run("fileWithExtraNewline", func(t *testing.T) {
		t.Parallel()
		headerFile := writeTempSecretFile(t, "secret\nsecond-line\n")
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		RegisterFlags(fs)
		lookup := lookupEnvMap(map[string]string{
			"MCP_EXTRA_HEADERS": "X-Internal-Auth: file:" + headerFile,
		})
		if _, err := buildExtraHeaders(fs, lookup, "mcp.extra-headers", "MCP_EXTRA_HEADERS"); err == nil {
			t.Fatalf("expected CR/LF rejection for resolved file value")
		}
	})
}

func TestParseProxyReference(t *testing.T) {
	t.Run("envReference", func(t *testing.T) {
		t.Parallel()
		proxy, source, err := parseProxyReference("http-proxy", "env:PROXY_URL", lookupEnvMap(map[string]string{
			"PROXY_URL": "http://proxy.example:8080",
		}))
		if err != nil {
			t.Fatalf("parseProxyReference returned error: %v", err)
		}
		if source != ProxySource("env:PROXY_URL") {
			t.Fatalf("unexpected proxy source: %s", source)
		}
		if proxy == nil || proxy.String() != "http://proxy.example:8080" {
			t.Fatalf("unexpected proxy URL: %v", proxy)
		}
	})

	t.Run("missingEnv", func(t *testing.T) {
		t.Parallel()
		if _, _, err := parseProxyReference("http-proxy", "env:MISSING", lookupEnvMap(nil)); err == nil {
			t.Fatalf("expected error for missing env var")
		}
	})

	t.Run("invalidScheme", func(t *testing.T) {
		t.Parallel()
		if _, _, err := parseProxyReference("http-proxy", "socks5://proxy.example:1080", lookupEnvMap(nil)); err == nil {
			t.Fatalf("expected error for unsupported scheme")
		}
	})
}

func TestRedactProxyURL(t *testing.T) {
	t.Parallel()
	parsed, err := url.Parse("http://user:pass@proxy.example:8080/some/path")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	redacted := RedactProxyURL(parsed)
	if redacted != "http://proxy.example:8080" {
		t.Fatalf("unexpected redacted URL: %s", redacted)
	}
}

func TestLoadProxyPrecedence(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "channel=main,url=https://mcp.example,http-proxy=http://channel-proxy:8080",
		"--control-plane.http-proxy", "http://control-proxy:8080",
		"--mcp.http-proxy", "http://mcp-proxy:8080",
		"--harpoon.http-proxy", "http://harpoon-proxy:8080",
		"--http-proxy", "http://global-proxy:8080",
	}
	cfg, err := Load(args, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.ControlPlane.HTTPProxy == nil || cfg.ControlPlane.HTTPProxy.String() != "http://control-proxy:8080" {
		t.Fatalf("unexpected control plane proxy: %v", cfg.ControlPlane.HTTPProxy)
	}
	if cfg.ControlPlane.HTTPProxySource != ProxySource("control-plane.http-proxy") {
		t.Fatalf("unexpected control plane proxy source: %s", cfg.ControlPlane.HTTPProxySource)
	}
	if cfg.MCP.HTTPProxy == nil || cfg.MCP.HTTPProxy.String() != "http://mcp-proxy:8080" {
		t.Fatalf("unexpected MCP proxy: %v", cfg.MCP.HTTPProxy)
	}
	if cfg.MCP.HTTPProxySource != ProxySource("mcp.http-proxy") {
		t.Fatalf("unexpected MCP proxy source: %s", cfg.MCP.HTTPProxySource)
	}
	if cfg.Harpoon.HTTPProxy == nil || cfg.Harpoon.HTTPProxy.String() != "http://harpoon-proxy:8080" {
		t.Fatalf("unexpected harpoon proxy: %v", cfg.Harpoon.HTTPProxy)
	}
	if cfg.Harpoon.HTTPProxySource != ProxySource("harpoon.http-proxy") {
		t.Fatalf("unexpected harpoon proxy source: %s", cfg.Harpoon.HTTPProxySource)
	}

	binding := cfg.MCP.MainChannelBinding()
	if binding == nil {
		t.Fatalf("expected main channel binding")
		return
	}
	if binding.HTTPProxy == nil || binding.HTTPProxy.String() != "http://channel-proxy:8080" {
		t.Fatalf("unexpected channel proxy: %v", binding.HTTPProxy)
	}
	if binding.HTTPProxySource != ProxySource("mcp.server-url") {
		t.Fatalf("unexpected channel proxy source: %s", binding.HTTPProxySource)
	}
}

func TestLoadRejectsStdioProxy(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.command", "channel=main,command=echo hello,http-proxy=http://proxy.example:8080",
	}
	_, err := Load(args, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
	}))
	if err == nil {
		t.Fatalf("expected error for stdio http-proxy")
	}
}

func TestProxyCheckIntervalDefaults(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "channel=main,url=https://mcp.example",
	}
	cfg, err := Load(args, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.ProxyHealth.CheckInterval != 60*time.Second {
		t.Fatalf("unexpected proxy check interval: %v", cfg.ProxyHealth.CheckInterval)
	}
}

func TestProxyCheckIntervalFlag(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "channel=main,url=https://mcp.example",
		"--proxy.check-interval", "45s",
	}
	cfg, err := Load(args, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.ProxyHealth.CheckInterval != 45*time.Second {
		t.Fatalf("unexpected proxy check interval: %v", cfg.ProxyHealth.CheckInterval)
	}
}

func TestProxyCheckIntervalInvalid(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "channel=main,url=https://mcp.example",
	}
	_, err := Load(args, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY": "control-key",
		"PROXY_CHECK_INTERVAL":  "-5s",
	}))
	if err == nil {
		t.Fatalf("expected error for invalid proxy check interval")
	}
}

func TestCABundleHelpMentionsAdditive(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	RegisterFlags(fs)
	buf := &bytes.Buffer{}
	WriteUsage(fs, buf)
	if !strings.Contains(buf.String(), "additive to system trust") {
		t.Fatalf("expected additive CA bundle help text")
	}
}

func writeTempCABundle(t *testing.T) string {
	t.Helper()
	certPEM := generateTestCertPEM(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.pem")
	if err := os.WriteFile(path, certPEM, 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	return path
}

func writeTempConfigFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tunnel-client.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(contents)+"\n"), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	return path
}

func writeTempSecretFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	return path
}

func writeTempClientCertPair(t *testing.T) (string, string) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			CommonName: "test-client",
		},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})

	dir := t.TempDir()
	certPath := filepath.Join(dir, "client.crt")
	keyPath := filepath.Join(dir, "client.key")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	return certPath, keyPath
}

func generateTestCertPEM(t *testing.T) []byte {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "test-ca",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
