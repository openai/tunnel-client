package adminui

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/oauth"
	"github.com/openai/tunnel-client/pkg/types"
	"github.com/openai/tunnel-client/pkg/version"
)

func TestHandleLogsExportReturnsRedactedTarGz(t *testing.T) {
	t.Parallel()

	buf := NewLogBufferWithCapacity(10)
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "request Authorization: Bearer sk-proj-abcdefg1234567890", 0)
	r.AddAttrs(
		slog.String("api_key", "sk-proj-secretvalue123456"),
		slog.String("raw", "standalone sk-proj-standalone123456"),
	)
	buf.Handle(context.Background(), r)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/logs/export?minutes=30", nil)
	handleLogsExport(
		buf,
		func() logExportRuntime {
			return collectLogExportRuntime(
				[]string{"tunnel-client", "run", "--control-plane.api-key=env:OPENAI_TUNNEL_KEY_PROD"},
				[]string{
					"CONTROL_PLANE_TUNNEL_ID=tunnel_0123456789abcdef0123456789abcdef",
					"OPENAI_TUNNEL_KEY_PROD=sk-proj-runtime-secret123456",
				},
			)
		},
		func() (metricsSnapshot, error) {
			return metricsSnapshot{
				Filename: metricsSnapshotFile,
				Body:     []byte("# HELP test_metric test\n# TYPE test_metric counter\ntest_metric 7\n"),
			}, nil
		},
		func() logExportAdminSnapshots {
			return logExportAdminSnapshots{
				Status: statusResponse{
					ControlPlaneTunnelID: "tunnel_0123456789abcdef0123456789abcdef",
					MCPServerURL:         "https://example.test/mcp",
				},
				System: systemResponse{
					MainChannelProbeStatus: "ok",
				},
				OAuth: oauthStatusResponse{
					DiscoveryURLs: []string{"https://example.test/.well-known/oauth-protected-resource/mcp"},
					Pending:       true,
				},
				Harpoon: logExportHarpoonData{
					Status: harpoonStatusResponse{
						Enabled:          true,
						MaxResponseBytes: 2048,
					},
					Targets: harpoonTargetsResponse{Targets: []harpoonTargetResponse{{
						Label:       "auth",
						URL:         "https://alice:secret@auth.example/token/opaque-path-token?access_token=secret-token",
						Description: "Auth target",
						Category:    "oauth",
					}}},
					Calls: harpoonCallsResponse{Calls: []harpoonCallResponse{{
						Timestamp:           time.Unix(1, 0).UTC(),
						Label:               "auth",
						URL:                 "https://alice:secret@auth.example/token/opaque-path-token?access_token=secret-token",
						Method:              http.MethodPost,
						Status:              200,
						LatencyMS:           17,
						ResponseContentType: "application/json",
						ReqBytes:            12,
						RespBytes:           21,
					}}},
				},
			}
		},
	)(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/gzip", rec.Header().Get("Content-Type"))
	require.Regexp(t, `^attachment; filename="tunnel-client-logs-\d{8}T\d{6}Z\.tar\.gz"$`, rec.Header().Get("Content-Disposition"))
	require.NotEmpty(t, rec.Header().Get("Content-Length"))

	files := readTarGzForTest(t, rec.Body.Bytes())
	require.Contains(t, files, "manifest.json")
	require.Contains(t, files, "README.txt")
	require.Contains(t, files, runtimeSnapshotFile)
	require.Contains(t, files, "tunnel-client.logs.ndjson")
	require.Contains(t, files, metricsSnapshotFile)
	require.Contains(t, files, "admin/status.json")
	require.Contains(t, files, "admin/system.json")
	require.Contains(t, files, "admin/oauth.json")
	require.Contains(t, files, "admin/harpoon.json")

	require.Contains(t, files["tunnel-client.logs.ndjson"], "sk-REDACTED")
	require.Contains(t, files["tunnel-client.logs.ndjson"], "Authorization: Bearer [REDACTED]")
	require.Contains(t, files["tunnel-client.logs.ndjson"], `"api_key":"[REDACTED]"`)
	require.NotContains(t, files["tunnel-client.logs.ndjson"], "secretvalue")
	require.Contains(t, files[metricsSnapshotFile], "test_metric 7")

	var manifest logExportManifest
	require.NoError(t, json.Unmarshal([]byte(files["manifest.json"]), &manifest))
	require.True(t, manifest.Redacted)
	require.Equal(t, 1, manifest.EventCount)
	require.Equal(t, 10, manifest.LogBufferCapacity)
	require.Equal(t, len(files[metricsSnapshotFile]), manifest.MetricsBytes)
	require.Contains(t, manifest.Files, metricsSnapshotFile)
	require.Contains(t, manifest.Files, runtimeSnapshotFile)
	require.Contains(t, manifest.Runtime.Argv, "--control-plane.api-key=env:OPENAI_TUNNEL_KEY_PROD")
	require.Equal(t, "[REDACTED]", manifest.Runtime.Environment["OPENAI_TUNNEL_KEY_PROD"])
	require.Contains(t, manifest.Files, "admin/status.json")
	require.Contains(t, manifest.Files, "admin/system.json")
	require.Contains(t, manifest.Files, "admin/oauth.json")
	require.Contains(t, manifest.Files, "admin/harpoon.json")
	require.Equal(t, version.ClientName, manifest.Runtime.Client.ClientName)
	require.Equal(t, version.SemanticVersion, manifest.Runtime.Client.SemanticVersion)
	require.Equal(t, version.Version, manifest.Runtime.Client.Version)
	require.Equal(t, version.UserAgent, manifest.Runtime.Client.UserAgent)
	require.Contains(t, files[runtimeSnapshotFile], "argv:")
	require.Contains(t, files[runtimeSnapshotFile], "environment:")
	require.Contains(t, files[runtimeSnapshotFile], "client:")
	require.Contains(t, files[runtimeSnapshotFile], "client_name: "+version.ClientName)
	require.Contains(t, files[runtimeSnapshotFile], "semantic_version: "+version.SemanticVersion)
	require.Contains(t, files[runtimeSnapshotFile], "user_agent: "+version.UserAgent)
	require.Contains(t, files[runtimeSnapshotFile], "OPENAI_TUNNEL_KEY_PROD: '[REDACTED]'")
	require.NotContains(t, files[runtimeSnapshotFile], "sk-proj-runtime-secret123456")

	var status statusResponse
	require.NoError(t, json.Unmarshal([]byte(files["admin/status.json"]), &status))
	require.Equal(t, "tunnel_0123456789abcdef0123456789abcdef", status.ControlPlaneTunnelID)
	require.Equal(t, "https://example.test/mcp", status.MCPServerURL)

	var system systemResponse
	require.NoError(t, json.Unmarshal([]byte(files["admin/system.json"]), &system))
	require.Equal(t, "ok", system.MainChannelProbeStatus)

	var oauth oauthStatusResponse
	require.NoError(t, json.Unmarshal([]byte(files["admin/oauth.json"]), &oauth))
	require.Equal(t, []string{"https://example.test/.well-known/oauth-protected-resource/mcp"}, oauth.DiscoveryURLs)
	require.True(t, oauth.Pending)

	var harpoonExport logExportHarpoonData
	require.NoError(t, json.Unmarshal([]byte(files["admin/harpoon.json"]), &harpoonExport))
	require.True(t, harpoonExport.Status.Enabled)
	require.Len(t, harpoonExport.Targets.Targets, 1)
	require.Equal(t, "https://auth.example/token/[REDACTED]", harpoonExport.Targets.Targets[0].URL)
	require.Len(t, harpoonExport.Calls.Calls, 1)
	require.Equal(t, "application/json", harpoonExport.Calls.Calls[0].ResponseContentType)
	require.NotContains(t, files["admin/harpoon.json"], "alice:secret")
	require.NotContains(t, files["admin/harpoon.json"], "opaque-path-token")
	require.NotContains(t, files["admin/harpoon.json"], "secret-token")
}

func TestRedactSnapshotStringRedactsURLPathSecrets(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		"https://auth.example/oauth/token/[REDACTED]/callback",
		redactSnapshotString("https://alice:secret@auth.example/oauth/token/path-token-value/callback?client_secret=query-secret"),
	)
	require.Equal(
		t,
		"https://auth.example/oauth/[REDACTED]/callback",
		redactSnapshotString("https://auth.example/oauth/embedded-secret-token/callback"),
	)
	require.Equal(
		t,
		"https://auth.example/token",
		redactSnapshotString("https://auth.example/token?access_token=query-secret"),
	)
}

func TestBuildLogsArchiveFiltersBeforeCallSite(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	archive, err := buildLogsArchive([]LogEvent{
		{Seq: 7, Time: now, Level: "INFO", Message: "hello"},
	}, now, 30*time.Minute, 2000, logExportRuntime{Argv: []string{"tunnel-client", "run"}}, metricsSnapshot{
		Filename: metricsSnapshotFile,
		Body:     []byte("commands_poll_cycles_total 42\n"),
	}, logExportAdminSnapshots{})
	require.NoError(t, err)

	files := readTarGzForTest(t, archive)
	require.Contains(t, files["tunnel-client.logs.ndjson"], `"seq":7`)
	require.Contains(t, files["tunnel-client.logs.ndjson"], "hello")
	require.Equal(t, "commands_poll_cycles_total 42\n", files[metricsSnapshotFile])
	require.Contains(t, files, "admin/status.json")
	require.Contains(t, files, "admin/system.json")
	require.Contains(t, files, "admin/oauth.json")
	require.Contains(t, files, "admin/harpoon.json")
}

func TestBuildLogsArchiveOmitsMetricsFileWhenSnapshotUnavailable(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	archive, err := buildLogsArchive(nil, now, 15*time.Minute, 128, logExportRuntime{}, metricsSnapshot{}, logExportAdminSnapshots{})
	require.NoError(t, err)

	files := readTarGzForTest(t, archive)
	require.NotContains(t, files, metricsSnapshotFile)
	require.Contains(t, files, "admin/status.json")
	require.Contains(t, files, "admin/system.json")
	require.Contains(t, files, "admin/oauth.json")
	require.Contains(t, files, "admin/harpoon.json")

	var manifest logExportManifest
	require.NoError(t, json.Unmarshal([]byte(files["manifest.json"]), &manifest))
	require.Zero(t, manifest.MetricsBytes)
	require.NotContains(t, manifest.Files, metricsSnapshotFile)
}

func TestBuildLogsArchiveRedactsAdminSnapshots(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	archive, err := buildLogsArchive(nil, now, 15*time.Minute, 128, logExportRuntime{}, metricsSnapshot{}, logExportAdminSnapshots{
		Status: statusResponse{
			MCPServerURL:            "https://alice:secret@example.test/mcp?code=secret-code&access_token=secret-token",
			MCPResourceMetadataURLs: []string{"https://alice:secret@example.test/.well-known/oauth-protected-resource/mcp?resource_metadata=secret-code"},
		},
		OAuth: oauthStatusResponse{
			DiscoveryURLs: []string{"https://alice:secret@example.test/.well-known/oauth-protected-resource/mcp?resource_metadata=secret-code"},
			Metadata: &oauth.DiscoveryResult{
				URL:        "https://alice:secret@example.test/.well-known/oauth-protected-resource/mcp?code=secret-code",
				Headers:    http.Header{"Authorization": []string{"Bearer secret-bearer"}, "Set-Cookie": []string{"sid=session-secret"}},
				Body:       json.RawMessage(`{"access_token":"secret-access-token","issuer":"https://example.test/issuer"}`),
				BodyText:   "metadata available",
				StatusCode: http.StatusOK,
			},
			WWWAuthenticateProbe: &oauth.WWWAuthenticateProbeStatus{
				Attempted: true,
				URL:       "https://alice:secret@example.test/mcp?code=secret-code",
				Error:     "authorization failed for https://alice:secret@example.test/mcp?code=secret-code",
			},
			SelectedAuthServer: "https://alice:secret@example.test/auth?access_token=secret-token",
		},
	})
	require.NoError(t, err)

	files := readTarGzForTest(t, archive)

	require.NotContains(t, files["admin/status.json"], "alice:secret@")
	require.NotContains(t, files["admin/status.json"], "secret-token")
	require.NotContains(t, files["admin/status.json"], "secret-code")
	require.Contains(t, files["admin/status.json"], `"mcp_server_url": "https://example.test/mcp"`)
	require.Contains(t, files["admin/status.json"], `"mcp_resource_metadata_urls": [`)
	require.Contains(t, files["admin/status.json"], `https://example.test/.well-known/oauth-protected-resource/mcp`)

	require.NotContains(t, files["admin/oauth.json"], "alice:secret@")
	require.NotContains(t, files["admin/oauth.json"], "secret-bearer")
	require.NotContains(t, files["admin/oauth.json"], "session-secret")
	require.NotContains(t, files["admin/oauth.json"], "secret-access-token")
	require.NotContains(t, files["admin/oauth.json"], "secret-token")
	require.NotContains(t, files["admin/oauth.json"], "secret-code")
	require.Contains(t, files["admin/oauth.json"], `"Authorization": "[REDACTED]"`)
	require.Contains(t, files["admin/oauth.json"], `"Set-Cookie": "[REDACTED]"`)
	require.Contains(t, files["admin/oauth.json"], `"access_token": "[REDACTED]"`)
	require.Contains(t, files["admin/oauth.json"], "https://example.test/.well-known/oauth-protected-resource/mcp")
	require.Contains(t, files["admin/oauth.json"], `"selected_authorization_server": "https://example.test/auth"`)
}

func TestCollectLogExportRuntimeKeepsReproMetadataAndRedactsSecrets(t *testing.T) {
	t.Parallel()

	got := collectLogExportRuntime(
		[]string{
			"tunnel-client",
			"run",
			"--control-plane.api-key=env:OPENAI_TUNNEL_KEY_PROD",
			"--cloudflared.token=env:CLOUDFLARED_TOKEN_REF",
			"--mcp.server-url",
			"https://example.test/mcp?code=secret-code",
			"--harpoon.target=url=https://target.test?access_token=target-token",
			"--control-plane.extra-headers=X-Tunnel-Shard-Token: shard-secret",
			"--mcp.extra-headers=X-Internal-Auth: env:MCP_RUNTIME_SECRET",
			"--mcp.discovery-extra-headers=X-Discovery-Auth: file:/run/secrets/mcp-discovery-header",
			"--admin-token",
			"literal-admin-token",
			"--unrelated",
			"sk-proj-argv-secret123456",
		},
		[]string{
			"CONTROL_PLANE_TUNNEL_ID=tunnel_0123456789abcdef0123456789abcdef",
			"LOG_LEVEL=debug",
			"MCP_SERVER_URL=https://env.example/mcp",
			"CONTROL_PLANE_EXTRA_HEADERS=X-Control-Auth: env:CONTROL_HEADER_SECRET",
			"MCP_EXTRA_HEADERS=X-Internal-Auth: env-runtime-secret",
			"MCP_DISCOVERY_EXTRA_HEADERS=X-Discovery-Auth: env-discovery-secret",
			"HTTPS_PROXY=http://proxy-user:proxy-pass@proxy.example:8080",
			"OPENAI_TUNNEL_KEY_PROD=sk-proj-env-secret123456",
			"CLOUDFLARED_TOKEN_REF=cloudflared-secret-value",
			"CLOUDFLARED_TUNNEL_TOKEN=cloudflared-direct-secret",
			"UNRELATED_SECRET=should-not-be-exported-because-not-relevant",
		},
	)

	require.Contains(t, got.Argv, "--control-plane.api-key=env:OPENAI_TUNNEL_KEY_PROD")
	require.Contains(t, got.Argv, "--cloudflared.token=env:CLOUDFLARED_TOKEN_REF")
	require.Contains(t, got.Argv, "https://example.test/mcp?code=[REDACTED]")
	require.Contains(t, got.Argv, "--harpoon.target=url=https://target.test?access_token=[REDACTED]")
	require.Contains(t, got.Argv, "--control-plane.extra-headers=X-Tunnel-Shard-Token: [REDACTED]")
	require.Contains(t, got.Argv, "--mcp.extra-headers=X-Internal-Auth: [REDACTED]")
	require.Contains(t, got.Argv, "--mcp.discovery-extra-headers=X-Discovery-Auth: [REDACTED]")
	require.NotContains(t, got.Argv, "MCP_RUNTIME_SECRET")
	require.NotContains(t, got.Argv, "/run/secrets/mcp-discovery-header")
	require.Contains(t, got.Argv, "[REDACTED]")
	require.Contains(t, got.Argv, "sk-REDACTED")
	require.NotContains(t, got.Argv, "literal-admin-token")
	require.NotContains(t, got.Argv, "sk-proj-argv-secret123456")

	require.Equal(t, "tunnel_0123456789abcdef0123456789abcdef", got.Environment["CONTROL_PLANE_TUNNEL_ID"])
	require.Equal(t, "debug", got.Environment["LOG_LEVEL"])
	require.Equal(t, "https://env.example/mcp", got.Environment["MCP_SERVER_URL"])
	require.Equal(t, "X-Control-Auth: [REDACTED]", got.Environment["CONTROL_PLANE_EXTRA_HEADERS"])
	require.Equal(t, "X-Internal-Auth: [REDACTED]", got.Environment["MCP_EXTRA_HEADERS"])
	require.Equal(t, "X-Discovery-Auth: [REDACTED]", got.Environment["MCP_DISCOVERY_EXTRA_HEADERS"])
	require.Equal(t, "http://[REDACTED]@proxy.example:8080", got.Environment["HTTPS_PROXY"])
	require.Equal(t, "[REDACTED]", got.Environment["OPENAI_TUNNEL_KEY_PROD"])
	require.Equal(t, "[REDACTED]", got.Environment["CLOUDFLARED_TOKEN_REF"])
	require.Equal(t, "[REDACTED]", got.Environment["CLOUDFLARED_TUNNEL_TOKEN"])
	require.NotContains(t, got.Environment, "UNRELATED_SECRET")
	require.NotContains(t, got.Environment, "should-not-be-exported-because-not-relevant")
	require.Equal(t, version.ClientName, got.Client.ClientName)
	require.Equal(t, version.SemanticVersion, got.Client.SemanticVersion)
	require.Equal(t, version.Version, got.Client.Version)
	require.Equal(t, version.UserAgent, got.Client.UserAgent)
}

func TestCollectLogExportRuntimeRedactsSplitHeaderFlagValues(t *testing.T) {
	t.Parallel()

	got := collectLogExportRuntime(
		[]string{
			"tunnel-client",
			"run",
			"--control-plane.extra-headers",
			"X-Control-Auth: literal-control-secret",
			"--mcp.extra-headers",
			"X-Internal-Auth: env:MCP_RUNTIME_SECRET",
			"--mcp.discovery-extra-headers",
			"X-Discovery-Auth: file:/run/secrets/mcp-discovery-header",
		},
		[]string{
			"MCP_RUNTIME_SECRET=runtime-secret",
		},
	)

	require.Contains(t, got.Argv, "--control-plane.extra-headers")
	require.Contains(t, got.Argv, "X-Control-Auth: [REDACTED]")
	require.Contains(t, got.Argv, "--mcp.extra-headers")
	require.Contains(t, got.Argv, "X-Internal-Auth: [REDACTED]")
	require.Contains(t, got.Argv, "--mcp.discovery-extra-headers")
	require.Contains(t, got.Argv, "X-Discovery-Auth: [REDACTED]")
	argv := strings.Join(got.Argv, "\n")
	require.NotContains(t, argv, "literal-control-secret")
	require.NotContains(t, argv, "MCP_RUNTIME_SECRET")
	require.NotContains(t, argv, "/run/secrets/mcp-discovery-header")
	require.Equal(t, "[REDACTED]", got.Environment["MCP_RUNTIME_SECRET"])
}

func TestCollectLogExportRuntimeRedactsOpaqueCloudflaredTokenReferences(t *testing.T) {
	t.Parallel()

	fromArgs := collectLogExportRuntime(
		[]string{
			"tunnel-client",
			"run",
			"--cloudflared.token",
			"env:CLOUDFLARED_CREDENTIAL",
		},
		[]string{
			"CLOUDFLARED_CREDENTIAL=opaque-cloudflared-secret",
		},
	)
	require.Equal(t, "[REDACTED]", fromArgs.Environment["CLOUDFLARED_CREDENTIAL"])

	cfg := &config.Config{
		Runtime: config.RuntimeConfig{
			ConfigFileContents: []byte(`
cloudflared:
  token: env:CLOUDFLARED_CREDENTIAL
`),
		},
	}
	fromConfig := collectLogExportRuntime(
		nil,
		[]string{
			"CLOUDFLARED_CREDENTIAL=opaque-cloudflared-secret",
		},
		sensitiveRuntimeEnvReferencesFromConfig(cfg),
	)
	require.Equal(t, "[REDACTED]", fromConfig.Environment["CLOUDFLARED_CREDENTIAL"])
}

func TestRuntimeSnapshotProviderIncludesRedactedEffectiveConfig(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		ControlPlane: config.ControlPlaneConfig{
			BaseURL:             mustURLForTest(t, "https://api.example"),
			TunnelID:            types.TunnelID("tunnel_0123456789abcdef0123456789abcdef"),
			APIKey:              "sk-proj-control-secret123456",
			MaxInFlightRequests: 20,
			PollTimeout:         30 * time.Second,
			ExtraHeaders:        map[string]string{"X-Tunnel-Shard-Token": "secret-shard-token"},
		},
		Logging: config.LoggingConfig{
			Level:  slog.LevelInfo,
			Format: config.LogFormatJSON,
			File:   "/tmp/tunnel-client.log",
		},
		Health:  config.HealthConfig{ListenAddr: "127.0.0.1:8080"},
		Process: config.ProcessConfig{PIDFile: "/tmp/tunnel-client.pid"},
		Cloudflared: config.CloudflaredConfig{
			Token:        "cloudflared-secret-value",
			Path:         "/opt/tunnel-client/cloudflared",
			ReadyTimeout: 30 * time.Second,
		},
		MCP: config.MCPConfig{
			ServerURL:             mustURLForTest(t, "https://alice:secret@mcp.example/mcp?access_token=secret-token"),
			TransportKind:         config.MCPTransportHTTPStreamable,
			ConnectionMaxTTL:      time.Minute,
			MaxConcurrentRequests: 5,
			ExtraHeaders:          map[string]string{"X-Internal-Auth": "runtime-secret"},
			DiscoveryExtraHeaders: map[string]string{"X-Discovery-Auth": "discovery-secret"},
			ChannelBindings: []config.MCPChannelBinding{
				{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportHTTPStreamable,
					ServerURL:     mustURLForTest(t, "https://alice:secret@mcp.example/mcp?access_token=secret-token"),
				},
			},
		},
		AdminUI:     config.AdminUIConfig{LogBufferEvents: 10},
		ProxyHealth: config.ProxyHealthConfig{CheckInterval: time.Minute},
		Runtime:     config.RuntimeConfig{ConfigFile: "/tmp/tunnel-client.yaml"},
	}

	runtime := NewRuntimeSnapshotProvider(cfg)()
	archive, err := buildLogsArchive(nil, time.Now().UTC(), time.Minute, 10, runtime, metricsSnapshot{}, logExportAdminSnapshots{})
	require.NoError(t, err)

	files := readTarGzForTest(t, archive)
	var payload map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(files[runtimeSnapshotFile]), &payload))
	require.Contains(t, payload, "effective_config")
	require.NotContains(t, files[runtimeSnapshotFile], "sk-proj-control-secret123456")
	require.NotContains(t, files[runtimeSnapshotFile], "secret-token")
	require.NotContains(t, files[runtimeSnapshotFile], "secret-shard-token")
	require.NotContains(t, files[runtimeSnapshotFile], "runtime-secret")
	require.NotContains(t, files[runtimeSnapshotFile], "discovery-secret")
	require.NotContains(t, files[runtimeSnapshotFile], "cloudflared-secret-value")
	require.Contains(t, files[runtimeSnapshotFile], "api_key: '[REDACTED]'")
	require.Contains(t, files[runtimeSnapshotFile], "cloudflared:")
	require.Contains(t, files[runtimeSnapshotFile], "token: '[REDACTED]'")
	require.Contains(t, files[runtimeSnapshotFile], "X-Internal-Auth: '[REDACTED]'")
	require.Contains(t, files[runtimeSnapshotFile], "X-Discovery-Auth: '[REDACTED]'")
	require.Contains(t, files[runtimeSnapshotFile], "https://mcp.example/mcp")
}

func TestRuntimeSnapshotProviderIncludesRedactedActualConfigSnapshot(t *testing.T) {
	t.Parallel()

	configPath := "/tmp/tunnel-client.yaml"
	configContents := []byte(`
config_version: 1
control_plane:
  base_url: https://alice:secret@api.example/v1?access_token=control-token
  tunnel_id: tunnel_0123456789abcdef0123456789abcdef
  api_key: sk-proj-config-secret123456
  client_cert: /tmp/control-plane-client.crt
  client_key: /tmp/control-plane-private.pem
  extra_headers:
    Authorization: Bearer config-bearer
    X-Tunnel-Shard-Token: config-shard-token
    X-Control-Env-Ref: env:CONTROL_HEADER_SECRET
    X-Debug: safe-value
mcp:
  extra_headers:
    X-Internal-Auth: mcp-runtime-secret
    X-Internal-Ref: env:MCP_HEADER_SECRET
  discovery_extra_headers:
    X-Discovery-Auth: mcp-discovery-secret
    X-Discovery-Ref: file:/run/secrets/mcp-discovery-header
  server_urls:
    - channel: main
      url: https://bob:secret@mcp.example/mcp?code=mcp-code
      client_cert: /tmp/client.crt
      client_key: /tmp/client.key
  commands:
    - channel: tools
      command: python -m tools --api-key sk-proj-command-secret123456
`)
	cfg := &config.Config{
		Runtime: config.RuntimeConfig{
			ConfigFile:         configPath,
			ConfigFileContents: configContents,
		},
	}

	runtime := NewRuntimeSnapshotProvider(cfg)()
	archive, err := buildLogsArchive(nil, time.Now().UTC(), time.Minute, 10, runtime, metricsSnapshot{}, logExportAdminSnapshots{})
	require.NoError(t, err)

	files := readTarGzForTest(t, archive)
	var payload map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(files[runtimeSnapshotFile]), &payload))
	require.Contains(t, payload, "actual_config")

	runtimeYAML := files[runtimeSnapshotFile]
	require.Contains(t, runtimeYAML, "actual_config:")
	require.Contains(t, runtimeYAML, "path: /tmp/tunnel-client.yaml")
	require.Contains(t, runtimeYAML, "base_url: https://api.example/v1")
	require.Contains(t, runtimeYAML, "url: https://mcp.example/mcp")
	require.Contains(t, runtimeYAML, "client_cert: /tmp/client.crt")
	require.Contains(t, runtimeYAML, "client_cert: /tmp/control-plane-client.crt")
	require.Contains(t, runtimeYAML, "X-Debug: '[REDACTED]'")
	require.Contains(t, runtimeYAML, "api_key: '[REDACTED]'")
	require.Contains(t, runtimeYAML, "Authorization: '[REDACTED]'")
	require.Contains(t, runtimeYAML, "X-Tunnel-Shard-Token: '[REDACTED]'")
	require.Contains(t, runtimeYAML, "X-Control-Env-Ref: '[REDACTED]'")
	require.Contains(t, runtimeYAML, "X-Internal-Auth: '[REDACTED]'")
	require.Contains(t, runtimeYAML, "X-Internal-Ref: '[REDACTED]'")
	require.Contains(t, runtimeYAML, "X-Discovery-Auth: '[REDACTED]'")
	require.Contains(t, runtimeYAML, "X-Discovery-Ref: '[REDACTED]'")
	require.Contains(t, runtimeYAML, "client_key: '[REDACTED]'")
	require.Contains(t, runtimeYAML, "python -m tools --api-key sk-REDACTED")
	require.NotContains(t, runtimeYAML, "alice:secret")
	require.NotContains(t, runtimeYAML, "bob:secret")
	require.NotContains(t, runtimeYAML, "control-token")
	require.NotContains(t, runtimeYAML, "mcp-code")
	require.NotContains(t, runtimeYAML, "config-secret")
	require.NotContains(t, runtimeYAML, "config-bearer")
	require.NotContains(t, runtimeYAML, "config-shard-token")
	require.NotContains(t, runtimeYAML, "safe-value")
	require.NotContains(t, runtimeYAML, "mcp-runtime-secret")
	require.NotContains(t, runtimeYAML, "mcp-discovery-secret")
	require.NotContains(t, runtimeYAML, "CONTROL_HEADER_SECRET")
	require.NotContains(t, runtimeYAML, "MCP_HEADER_SECRET")
	require.NotContains(t, runtimeYAML, "/run/secrets/mcp-discovery-header")
	require.NotContains(t, runtimeYAML, "client.key")
	require.NotContains(t, runtimeYAML, "control-plane-private.pem")
	require.NotContains(t, runtimeYAML, "command-secret")
}

func TestHandleLogsExportReturnsInternalServerErrorWhenMetricsSnapshotFails(t *testing.T) {
	t.Parallel()

	buf := NewLogBufferWithCapacity(4)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/logs/export?minutes=30", nil)

	handleLogsExport(buf, nil, func() (metricsSnapshot, error) {
		return metricsSnapshot{}, errors.New("boom")
	}, nil)(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "capture metrics snapshot")
}

func TestNewMetricsSnapshotProviderCapturesHandlerOutput(t *testing.T) {
	t.Parallel()

	provider := NewMetricsSnapshotProvider(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("queue_depth 3\n"))
	}))

	got, err := provider()
	require.NoError(t, err)
	require.Equal(t, metricsSnapshotFile, got.Filename)
	require.Equal(t, []byte("queue_depth 3\n"), got.Body)
}

func TestNewMetricsSnapshotProviderRejectsUnexpectedStatus(t *testing.T) {
	t.Parallel()

	provider := NewMetricsSnapshotProvider(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))

	_, err := provider()
	require.EqualError(t, err, "capture metrics snapshot: unexpected status 503")
}

func mustURLForTest(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	return parsed
}

func readTarGzForTest(t *testing.T, data []byte) map[string]string {
	t.Helper()

	gz, err := gzip.NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, gz.Close())
	}()

	tr := tar.NewReader(gz)
	files := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		body, err := io.ReadAll(tr)
		require.NoError(t, err)
		files[hdr.Name] = string(body)
	}
	return files
}
