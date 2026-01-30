package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

const (
	envTunnelID  = "tunnel_0123456789abcdef0123456789abcdef"
	flagTunnelID = "tunnel_fedcba9876543210fedcba9876543210"
)

func TestLoadUsesEnvWhenFlagsEmpty(t *testing.T) {
	lookup := map[string]string{
		"CONTROL_PLANE_BASE_URL":              "https://example",
		"CONTROL_PLANE_TUNNEL_ID":             envTunnelID,
		"CONTROL_PLANE_API_KEY":               "control-key",
		"CONTROL_PLANE_MAX_INFLIGHT_REQUESTS": "15",
		"CONTROL_PLANE_POLL_TIMEOUT":          "45s",
		"LOG_LEVEL":                           "debug",
		"LOG_FORMAT":                          "json",
		"LOG_FILE":                            "/tmp/log",
		"LOG_HTTP_RAW_UNSAFE":                 "true",
		"HEALTH_URL_FILE":                     "/tmp/health-url",
		"PID_FILE":                            "/tmp/pid-file",
		"MCP_SERVER_URL":                      "https://mcp.example",
		"MCP_CONNECTION_MAX_TTL":              "30s",
		"MCP_MAX_CONCURRENT_REQUESTS":         "12",
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
	if cfg.ControlPlane.TunnelID != envTunnelID {
		t.Fatalf("unexpected tunnel id: %s", cfg.ControlPlane.TunnelID)
	}
	if cfg.ControlPlane.MaxInFlightRequests != 15 {
		t.Fatalf("unexpected max in-flight requests: %d", cfg.ControlPlane.MaxInFlightRequests)
	}
	if cfg.ControlPlane.PollTimeout != 45*time.Second {
		t.Fatalf("unexpected poll timeout: %s", cfg.ControlPlane.PollTimeout)
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
	if cfg.Process.PIDFile != "/tmp/pid-file" {
		t.Fatalf("unexpected pid file: %s", cfg.Process.PIDFile)
	}
	if cfg.MCP.ServerURL == nil || cfg.MCP.ServerURL.String() != "https://mcp.example" {
		t.Fatalf("unexpected MCP server URL: %v", cfg.MCP.ServerURL)
	}
	if cfg.MCP.ConnectionMaxTTL != 30*time.Second {
		t.Fatalf("unexpected MCP connection ttl: %s", cfg.MCP.ConnectionMaxTTL)
	}
	if cfg.MCP.MaxConcurrentRequests != 12 {
		t.Fatalf("unexpected MCP max concurrent requests: %d", cfg.MCP.MaxConcurrentRequests)
	}
}

func TestLoadFlagsOverrideEnv(t *testing.T) {
	lookup := map[string]string{
		"CONTROL_PLANE_BASE_URL":              "https://env",
		"CONTROL_PLANE_TUNNEL_ID":             envTunnelID,
		"CONTROL_PLANE_API_KEY":               "control-env-key",
		"CONTROL_PLANE_MAX_INFLIGHT_REQUESTS": "25",
		"CONTROL_PLANE_POLL_TIMEOUT":          "1m",
		"OPENAI_API_KEY":                      "legacy-env-key",
		"LOG_LEVEL":                           "warn",
		"LOG_FORMAT":                          "json",
		"LOG_FILE":                            "/tmp/env",
		"LOG_HTTP_RAW_UNSAFE":                 "true",
		"HEALTH_URL_FILE":                     "/tmp/env-health",
		"PID_FILE":                            "/tmp/env-pid",
		"MCP_SERVER_URL":                      "https://env-mcp",
		"MCP_CONNECTION_MAX_TTL":              "45m",
		"MCP_MAX_CONCURRENT_REQUESTS":         "5",
	}

	args := []string{
		"--control-plane.base-url", "https://flag",
		"--control-plane.tunnel-id", flagTunnelID,
		"--log.level", "info",
		"--log.format", "struct-text",
		"--log.file", "/tmp/flag",
		"--log.http-raw-unsafe=false",
		"--health.url-file", "/tmp/flag-health",
		"--pid.file", "/tmp/flag-pid",
		"--mcp.server-url", "https://flag-mcp",
		"--control-plane.poll-timeout=5s",
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
	if cfg.Process.PIDFile != "/tmp/flag-pid" {
		t.Fatalf("expected pid file /tmp/flag-pid, got %s", cfg.Process.PIDFile)
	}
	if cfg.MCP.ServerURL == nil || cfg.MCP.ServerURL.String() != "https://flag-mcp" {
		t.Fatalf("expected MCP server URL https://flag-mcp, got %v", cfg.MCP.ServerURL)
	}
	if cfg.ControlPlane.MaxInFlightRequests != 25 {
		t.Fatalf("expected max in-flight requests 25, got %d", cfg.ControlPlane.MaxInFlightRequests)
	}
	if cfg.ControlPlane.PollTimeout != 5*time.Second {
		t.Fatalf("expected poll timeout 5s, got %s", cfg.ControlPlane.PollTimeout)
	}
	if cfg.MCP.ConnectionMaxTTL != 15*time.Minute {
		t.Fatalf("expected connection ttl 15m, got %s", cfg.MCP.ConnectionMaxTTL)
	}
	if cfg.MCP.MaxConcurrentRequests != 20 {
		t.Fatalf("expected MCP max concurrent requests 20, got %d", cfg.MCP.MaxConcurrentRequests)
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
	args := []string{"--harpoon-target", "label=auth,url=https://example.com,desc=Auth server"}
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
}

func TestLoadRejectsInvalidHarpoonTargetLabel(t *testing.T) {
	args := []string{"--harpoon-target", "label=Auth-Prod,url=https://example.com,desc=Auth server"}
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
	args := []string{"--harpoon-additional-transport", "http-streamable"}
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
	args := []string{"--harpoon-capture-payloads"}
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
	if !strings.Contains(err.Error(), "MCP server URL or command is required") {
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

func TestLoadRejectsMCPCommandAndServerURL(t *testing.T) {
	args := []string{
		"--control-plane.tunnel-id", flagTunnelID,
		"--mcp.server-url", "https://flag-mcp",
		"--mcp.command", "echo hello",
	}
	_, err := Load(args, func(key string) (string, bool) {
		if key == "OPENAI_API_KEY" {
			return "key", true
		}
		return "", false
	})
	if err == nil {
		t.Fatalf("expected error when mcp.command and mcp.server-url are both set")
	}
	if !strings.Contains(err.Error(), "mcp.command and mcp.server-url are mutually exclusive") {
		t.Fatalf("unexpected error: %v", err)
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
