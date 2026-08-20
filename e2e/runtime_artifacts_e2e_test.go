package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	"github.com/openai/tunnel-client/testsupport/mockmcpserver"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

const (
	runtimeArtifactTunnelID               = "tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	runtimeArtifactAPIKey                 = "runtime-artifact-test-key"
	runtimeArtifactHealthListeningSignal  = "health server listening"
	runtimeArtifactPIDFileSignal          = "pid file written"
	runtimeArtifactMCPReadySignal         = "mcp session initialized"
	runtimeArtifactOAuthReadySignal       = "OAuth discovery ProtectedResourceMetaData fetched"
	runtimeArtifactStdioReadySignal       = "oauth discovery server URL is not configured"
	runtimeArtifactCloudflaredReadySignal = "bundled cloudflared ready"
	runtimeArtifactSignalTimeout          = 30 * time.Second
)

func TestRuntimeHTTPMCP(t *testing.T) {
	controlPlane, mcpServer := newRuntimeArtifactMocks(t)
	binary := buildRuntimeArtifact(t, "./cmd/client-runtime", "tunnel-client-runtime", "runtime")
	healthURLFile := filepath.Join(t.TempDir(), "health.url")

	proc := startRuntimeArtifact(t, binary, runtimeArtifactArgs(controlPlane, mcpServer, healthURLFile)...)
	healthBaseURL := waitForRuntimeArtifactHealthURL(t, proc, healthURLFile)
	waitForRuntimeArtifactIdle(t, proc, controlPlane)
	assertRuntimeArtifactHealthSurface(
		t,
		proc,
		healthBaseURL,
		runtimeArtifactMCPReadySignal,
		runtimeArtifactOAuthReadySignal,
	)
	assertRuntimeArtifactToolCall(t, controlPlane, mcpServer)

	_ = proc.stop()
}

func TestRuntimeOAuthStdioHarpoonMultiChannelConfigProfilePID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stdio test helper uses bash")
	}

	targetCalled := make(chan struct{}, 1)
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case targetCalled <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("runtime-harpoon-ok"))
	}))
	t.Cleanup(targetServer.Close)

	mcpServer := mockmcpserver.NewMockMCPServer(
		mockmcpserver.WithOAuthDiscoveryResources(),
	)
	mcpServer.Start(t)

	commands := []mocktunnelservice.CommandResponse{
		runtimeArtifactOAuthCommand(t),
		runtimeArtifactChannelCommandResponse(
			t,
			"runtime-artifact-stdio-init",
			"stdio",
			runtimeArtifactInitializePayload("stdio-init"),
			string(wiretypes.ResponsePayloadJSONRPC),
		),
		runtimeArtifactChannelCommandResponse(
			t,
			"runtime-artifact-stdio-ready",
			"stdio",
			runtimeArtifactInitializedPayload(),
			string(wiretypes.ResponsePayloadNotifyAck),
		),
		runtimeArtifactChannelCommandResponse(
			t,
			"runtime-artifact-stdio-tool",
			"stdio",
			json.RawMessage(`{"jsonrpc":"2.0","id":"stdio-tool","method":"tools/call","params":{"name":"hello","arguments":{"name":"Ada"}}}`),
			string(wiretypes.ResponsePayloadJSONRPC),
		),
		runtimeArtifactChannelCommandResponse(
			t,
			"runtime-artifact-harpoon-init",
			"harpoon",
			runtimeArtifactInitializePayload("harpoon-init"),
			string(wiretypes.ResponsePayloadJSONRPC),
		),
		runtimeArtifactChannelCommandResponse(
			t,
			"runtime-artifact-harpoon-ready",
			"harpoon",
			runtimeArtifactInitializedPayload(),
			string(wiretypes.ResponsePayloadNotifyAck),
		),
		runtimeArtifactChannelCommandResponse(
			t,
			"runtime-artifact-harpoon-call",
			"harpoon",
			json.RawMessage(`{"jsonrpc":"2.0","id":"harpoon-call","method":"tools/call","params":{"name":"call_target","arguments":{"label":"seed","method":"GET","headers":{}}}}`),
			string(wiretypes.ResponsePayloadJSONRPC),
		),
	}
	controlPlane := mocktunnelservice.NewMockTunnelService(
		mocktunnelservice.WithAPIKey(runtimeArtifactAPIKey),
		mocktunnelservice.WithTunnelID(runtimeArtifactTunnelID),
		mocktunnelservice.WithInitializationPhaseCommandsWithoutSessionHeaders(),
		mocktunnelservice.WithCommandResponses(commands...),
	)
	controlPlane.Start(t)

	binary := buildRuntimeArtifact(t, "./cmd/client-runtime", "tunnel-client-runtime", "runtime")
	profileDir := t.TempDir()
	profileName := "runtime-acceptance"
	profilePath := filepath.Join(profileDir, profileName+".yaml")
	healthURLFile := filepath.Join(t.TempDir(), "health.url")
	pidFile := filepath.Join(t.TempDir(), "runtime.pid")
	stdioArgs := mockmcpserver.StdioServerCommand(t)
	stdioCommand := strings.Join([]string{
		runtimeArtifactCommandQuote(stdioArgs[0]),
		runtimeArtifactCommandQuote(stdioArgs[1]),
	}, " ")
	profile := strings.Join([]string{
		"config_version: 1",
		"control_plane:",
		"  base_url: " + runtimeArtifactYAMLScalar(controlPlane.BaseURL().String()),
		"  tunnel_id: " + runtimeArtifactYAMLScalar(runtimeArtifactTunnelID),
		"  api_key: env:CONTROL_PLANE_API_KEY",
		"  poll_channels:",
		"    - main",
		"    - stdio",
		"    - harpoon",
		"mcp:",
		"  server_urls:",
		"    - channel: main",
		"      url: " + runtimeArtifactYAMLScalar(mcpServer.BaseURL().String()),
		"  commands:",
		"    - channel: stdio",
		"      command: " + runtimeArtifactYAMLScalar(stdioCommand),
		"harpoon:",
		"  allow_plaintext_http: true",
		"  targets:",
		"    - label: seed",
		"      url: " + runtimeArtifactYAMLScalar(targetServer.URL),
		"health:",
		"  listen_addr: 127.0.0.1:0",
		"  url_file: " + runtimeArtifactYAMLScalar(healthURLFile),
		"process:",
		"  pid_file: " + runtimeArtifactYAMLScalar(pidFile),
		"admin_ui:",
		"  open_browser: false",
		"log:",
		"  level: info",
		"  format: struct-text",
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(profilePath, []byte(profile), 0o600))

	proc := startRuntimeArtifact(
		t,
		binary,
		"run",
		"--profile",
		profileName,
		"--profile-dir",
		profileDir,
	)
	healthBaseURL := waitForRuntimeArtifactHealthURL(t, proc, healthURLFile)
	waitForRuntimeArtifactOutput(t, proc, "PID file", runtimeArtifactPIDFileSignal)
	pidBytes, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)
	require.Equal(t, proc.cmd.Process.Pid, pid)

	waitForRuntimeArtifactIdle(t, proc, controlPlane)
	assertRuntimeArtifactHealthSurface(
		t,
		proc,
		healthBaseURL,
		runtimeArtifactMCPReadySignal,
		runtimeArtifactOAuthReadySignal,
	)
	require.Len(
		t,
		controlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched),
		9,
		"main OAuth, stdio, and Harpoon channel commands should all complete",
	)
	select {
	case <-targetCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime Harpoon channel did not call its configured target")
	}

	_ = proc.stop()
	_, err = os.Stat(pidFile)
	require.ErrorIs(t, err, os.ErrNotExist, "runtime should remove its PID file on shutdown")
}

func TestRuntimeStdioCommandKeepsShellMetacharactersLiteral(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stdio test helper uses bash")
	}

	controlPlane := newRuntimeArtifactStdioControlPlane(t)
	binary := buildRuntimeArtifact(t, "./cmd/client-runtime", "tunnel-client-runtime", "runtime")
	healthURLFile := filepath.Join(t.TempDir(), "health.url")
	markerPath := filepath.Join(t.TempDir(), "shell-marker")
	stdioArgs := mockmcpserver.StdioServerCommand(t)
	rawCommand := strings.Join([]string{
		runtimeArtifactCommandQuote(stdioArgs[0]),
		runtimeArtifactCommandQuote(stdioArgs[1]),
		";",
		"touch",
		runtimeArtifactCommandQuote(markerPath),
	}, " ")

	proc := startRuntimeArtifact(t, binary, runtimeArtifactStdioArgs(controlPlane, healthURLFile, rawCommand)...)
	healthBaseURL := waitForRuntimeArtifactHealthURL(t, proc, healthURLFile)
	waitForRuntimeArtifactIdle(t, proc, controlPlane)
	assertRuntimeArtifactHealthSurface(t, proc, healthBaseURL, runtimeArtifactStdioReadySignal)
	assertRuntimeArtifactStdioToolCall(t, controlPlane)

	_, err := os.Stat(markerPath)
	require.ErrorIs(t, err, os.ErrNotExist, "stdio command metacharacters must remain literal argv")
	_ = proc.stop()
}

func TestRuntimeCloudflared(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake cloudflared wrapper uses a POSIX shell")
	}

	controlPlane, mcpServer := newRuntimeArtifactMocks(t)
	binary := buildRuntimeArtifact(t, "./cmd/client-runtime-cloudflared", "tunnel-client-runtime-cloudflared", "runtime-cloudflared")
	healthURLFile := filepath.Join(t.TempDir(), "health.url")
	exitFile := filepath.Join(t.TempDir(), "cloudflared.exit")
	startedFile := filepath.Join(t.TempDir(), "cloudflared.started")
	cloudflaredPath := writeRuntimeArtifactCloudflaredWrapper(t)

	args := append(runtimeArtifactArgs(controlPlane, mcpServer, healthURLFile),
		"--cloudflared.path", cloudflaredPath,
	)
	proc := startRuntimeArtifactWithEnv(t, binary, map[string]string{
		"CLOUDFLARED_TUNNEL_TOKEN":           "runtime-artifact-cloudflared-token",
		"GO_WANT_RUNTIME_CLOUDFLARED_HELPER": "1",
		"RUNTIME_E2E_HELPER_BINARY":          os.Args[0],
		"RUNTIME_CLOUDFLARED_EXIT_FILE":      exitFile,
		"RUNTIME_CLOUDFLARED_STARTED_FILE":   startedFile,
	}, args...)

	waitForRuntimeArtifactOutput(t, proc, "cloudflared readiness", runtimeArtifactCloudflaredReadySignal)
	_, err := os.Stat(startedFile)
	require.NoError(t, err, "fake cloudflared startup marker should exist after readiness")
	healthBaseURL := waitForRuntimeArtifactHealthURL(t, proc, healthURLFile)
	waitForRuntimeArtifactIdle(t, proc, controlPlane)
	assertRuntimeArtifactHealthSurface(
		t,
		proc,
		healthBaseURL,
		runtimeArtifactMCPReadySignal,
		runtimeArtifactOAuthReadySignal,
		runtimeArtifactCloudflaredReadySignal,
	)
	assertRuntimeArtifactToolCall(t, controlPlane, mcpServer)

	require.NoError(t, os.WriteFile(exitFile, []byte("exit\n"), 0o600))
	err, ok := proc.wait(10 * time.Second)
	require.Truef(t, ok, "runtime-cloudflared did not observe fake companion exit; output:\n%s", proc.output.String())
	require.Error(t, err)
	require.Contains(t, proc.output.String(), "cloudflared supervision failed")
}

func newRuntimeArtifactMocks(t *testing.T) (*mocktunnelservice.MockTunnelService, *mockmcpserver.MockMCPServer) {
	t.Helper()

	return newRuntimeArtifactMocksWithMCPOptions(t)
}

// newRuntimeArtifactMocksWithMCPOptions keeps the standard host-binary fixture
// script canonical while allowing compatibility scenarios to vary only the MCP
// transport surface under test (for example TLS or required headers).
func newRuntimeArtifactMocksWithMCPOptions(
	t *testing.T,
	extraMCPOptions ...mockmcpserver.Option,
) (*mocktunnelservice.MockTunnelService, *mockmcpserver.MockMCPServer) {
	t.Helper()

	mcpOptions := []mockmcpserver.Option{
		mockmcpserver.WithOAuthDiscoveryResources(),
		mockmcpserver.WithCalls(mockmcpserver.Call{
			Tool:   "echo",
			Result: json.RawMessage("{\"ok\":true}"),
		}),
	}
	mcpOptions = append(mcpOptions, extraMCPOptions...)
	mcpServer := mockmcpserver.NewMockMCPServer(mcpOptions...)
	mcpServer.Start(t)

	toolCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			"runtime-artifact-tool",
			json.RawMessage("{\"jsonrpc\":\"2.0\",\"id\":\"runtime-artifact-call\",\"method\":\"tools/call\",\"params\":{\"name\":\"echo\",\"arguments\":{\"name\":\"Ada\"}}}"),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: "runtime-artifact-tool",
			Assert: func(tb testing.TB, response mocktunnelservice.ReceivedResponse) {
				target := tb
				if target == nil {
					target = t
				}
				if response.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					target.Fatalf("tool response type = %q, want %q", response.ResponseType, wiretypes.ResponsePayloadJSONRPC)
				}
				if response.ResponseCode != http.StatusOK {
					target.Fatalf("tool response code = %d, want %d", response.ResponseCode, http.StatusOK)
				}
				if len(response.JSONResponse) == 0 {
					target.Fatal("tool response missing JSON-RPC payload")
				}
			},
		}},
	}
	controlPlane := mocktunnelservice.NewMockTunnelService(
		mocktunnelservice.WithAPIKey(runtimeArtifactAPIKey),
		mocktunnelservice.WithTunnelID(runtimeArtifactTunnelID),
		mocktunnelservice.WithSessionHeaderPropagation(),
		mocktunnelservice.WithInitializationPhaseCommands(),
		mocktunnelservice.WithCommandResponses(toolCommand),
	)
	controlPlane.Start(t)
	return controlPlane, mcpServer
}

func newRuntimeArtifactStdioControlPlane(t *testing.T) *mocktunnelservice.MockTunnelService {
	t.Helper()

	toolCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			"runtime-artifact-stdio-tool",
			json.RawMessage("{\"jsonrpc\":\"2.0\",\"id\":\"runtime-artifact-stdio-call\",\"method\":\"tools/call\",\"params\":{\"name\":\"hello\",\"arguments\":{\"name\":\"Ada\"}}}"),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: "runtime-artifact-stdio-tool",
			Assert: func(tb testing.TB, response mocktunnelservice.ReceivedResponse) {
				target := tb
				if target == nil {
					target = t
				}
				if response.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					target.Fatalf("stdio tool response type = %q, want %q", response.ResponseType, wiretypes.ResponsePayloadJSONRPC)
				}
				if response.ResponseCode != http.StatusOK {
					target.Fatalf("stdio tool response code = %d, want %d", response.ResponseCode, http.StatusOK)
				}
			},
		}},
	}
	controlPlane := mocktunnelservice.NewMockTunnelService(
		mocktunnelservice.WithAPIKey(runtimeArtifactAPIKey),
		mocktunnelservice.WithTunnelID(runtimeArtifactTunnelID),
		mocktunnelservice.WithInitializationPhaseCommandsWithoutSessionHeaders(),
		mocktunnelservice.WithCommandResponses(toolCommand),
	)
	controlPlane.Start(t)
	return controlPlane
}

func runtimeArtifactOAuthCommand(t *testing.T) mocktunnelservice.CommandResponse {
	t.Helper()
	const requestID = "runtime-artifact-oauth"
	return mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewOAuthDiscoveryCommand(requestID, nil),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: requestID,
			Assert: func(tb testing.TB, response mocktunnelservice.ReceivedResponse) {
				target := tb
				if target == nil {
					target = t
				}
				if response.ResponseType != string(wiretypes.ResponsePayloadOAuth) {
					target.Fatalf("OAuth response type = %q, want %q", response.ResponseType, wiretypes.ResponsePayloadOAuth)
				}
				if response.ResponseCode != http.StatusOK {
					target.Fatalf("OAuth response code = %d, want %d", response.ResponseCode, http.StatusOK)
				}
				var payload map[string]any
				if err := json.Unmarshal(response.JSONResponse, &payload); err != nil {
					target.Fatalf("decode OAuth response: %v", err)
				}
				if payload["resource"] == "" {
					target.Fatalf("OAuth response is missing resource metadata: %v", payload)
				}
			},
		}},
	}
}

func runtimeArtifactChannelCommandResponse(
	t *testing.T,
	requestID string,
	channel string,
	payload json.RawMessage,
	responseType string,
) mocktunnelservice.CommandResponse {
	t.Helper()
	return mocktunnelservice.CommandResponse{
		Command: runtimeArtifactChannelCommand(requestID, channel, payload),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: requestID,
			Assert: func(tb testing.TB, response mocktunnelservice.ReceivedResponse) {
				target := tb
				if target == nil {
					target = t
				}
				if response.ResponseType != responseType {
					target.Fatalf("response type for %s = %q, want %q", requestID, response.ResponseType, responseType)
				}
				if responseType == string(wiretypes.ResponsePayloadNotifyAck) {
					if response.ResponseCode != http.StatusOK && response.ResponseCode != http.StatusNoContent {
						target.Fatalf(
							"notification response code for %s = %d, want %d or %d",
							requestID,
							response.ResponseCode,
							http.StatusOK,
							http.StatusNoContent,
						)
					}
					return
				}
				if response.ResponseCode != http.StatusOK {
					target.Fatalf("response code for %s = %d, want %d", requestID, response.ResponseCode, http.StatusOK)
				}
			},
		}},
	}
}

func runtimeArtifactChannelCommand(requestID, channel string, payload json.RawMessage) json.RawMessage {
	command := map[string]any{
		"command_type": "jsonrpc",
		"request_id":   requestID,
		"jsonrpc":      payload,
		"created_at":   time.Now().UTC().Format(time.RFC3339),
		"shard_token":  requestID,
		"channel":      channel,
	}
	encoded, _ := json.Marshal(command)
	return json.RawMessage(encoded)
}

func runtimeArtifactInitializePayload(id string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%q,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"runtime-e2e","version":"0.0.1"}}}`,
		id,
	))
}

func runtimeArtifactInitializedPayload() json.RawMessage {
	return json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)
}

func runtimeArtifactYAMLScalar(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func runtimeArtifactArgs(controlPlane *mocktunnelservice.MockTunnelService, mcpServer *mockmcpserver.MockMCPServer, healthURLFile string) []string {
	return []string{
		"run",
		"--control-plane.base-url", controlPlane.BaseURL().String(),
		"--control-plane.tunnel-id", runtimeArtifactTunnelID,
		"--mcp.server-url", mcpServer.BaseURL().String(),
		"--health.listen-addr", "127.0.0.1:0",
		"--health.url-file", healthURLFile,
		"--log.level", "info",
		"--log.format", "struct-text",
	}
}

func runtimeArtifactStdioArgs(controlPlane *mocktunnelservice.MockTunnelService, healthURLFile, command string) []string {
	return []string{
		"run",
		"--control-plane.base-url", controlPlane.BaseURL().String(),
		"--control-plane.tunnel-id", runtimeArtifactTunnelID,
		"--mcp.command", command,
		"--health.listen-addr", "127.0.0.1:0",
		"--health.url-file", healthURLFile,
		"--log.level", "debug",
		"--log.format", "struct-text",
	}
}

func runtimeArtifactCommandQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func buildRuntimeArtifact(t *testing.T, packagePath, binaryName, flavor string) string {
	t.Helper()

	if binaryPath := runtimeArtifactBazelBinary(t, packagePath); binaryPath != "" {
		return binaryPath
	}

	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(t.TempDir(), binaryName)
	cmd := exec.Command(
		"go",
		"build",
		"-trimpath",
		"-buildvcs=false",
		"-ldflags",
		"-X github.com/openai/tunnel-client/pkg/version.Flavor="+flavor,
		"-o",
		binaryPath,
		packagePath,
	)
	cmd.Dir = runtimeArtifactModuleRoot(t)
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOCACHE="+filepath.Join(os.TempDir(), "tunnel-client-go-build-cache"),
	)
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "build %s:\n%s", packagePath, output)
	return binaryPath
}

func runtimeArtifactBazelBinary(t *testing.T, packagePath string) string {
	t.Helper()

	testSrcDir := os.Getenv("TEST_SRCDIR")
	if testSrcDir == "" {
		return ""
	}

	targetName := map[string]string{
		"./cmd/client":                     "client",
		"./cmd/client-runtime":             "client_runtime",
		"./cmd/client-runtime-cloudflared": "client_runtime_cloudflared",
	}[packagePath]
	require.NotEmptyf(t, targetName, "missing Bazel runtime artifact mapping for %q", packagePath)

	workspaces := []string{os.Getenv("TEST_WORKSPACE"), "_main"}
	for _, workspace := range workspaces {
		if workspace == "" {
			continue
		}
		binaryPath := filepath.Join(
			testSrcDir,
			workspace,
			"api",
			"tunnel-client",
			strings.TrimPrefix(packagePath, "./"),
			targetName,
		)
		if runtime.GOOS == "windows" {
			binaryPath += ".exe"
		}
		if info, err := os.Stat(binaryPath); err == nil && info.Mode().IsRegular() {
			return binaryPath
		}
	}

	t.Fatalf("Bazel runtime artifact for %s was not present in test runfiles", packagePath)
	return ""
}

func runtimeArtifactModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Dir(filepath.Dir(file))
}

type runtimeArtifactProcess struct {
	cmd    *exec.Cmd
	output *runtimeArtifactOutput
	done   chan struct{}

	mu      sync.Mutex
	waitErr error
}

func startRuntimeArtifact(t *testing.T, binary string, args ...string) *runtimeArtifactProcess {
	t.Helper()
	return startRuntimeArtifactWithEnv(t, binary, nil, args...)
}

func startRuntimeArtifactWithEnv(t *testing.T, binary string, overrides map[string]string, args ...string) *runtimeArtifactProcess {
	t.Helper()

	output := newRuntimeArtifactOutput()
	cmd := exec.Command(binary, args...)
	cmd.Env = runtimeArtifactEnvironment(overrides)
	cmd.Stdout = output
	cmd.Stderr = output
	require.NoErrorf(t, cmd.Start(), "start %s", binary)

	proc := &runtimeArtifactProcess{
		cmd:    cmd,
		output: output,
		done:   make(chan struct{}),
	}
	go func() {
		err := cmd.Wait()
		proc.mu.Lock()
		proc.waitErr = err
		proc.mu.Unlock()
		close(proc.done)
	}()
	t.Cleanup(func() {
		_ = proc.stop()
	})
	return proc
}

func runtimeArtifactEnvironment(overrides map[string]string) []string {
	blocked := map[string]struct{}{
		"ADMIN_UI_LOG_BUFFER_EVENTS":       {},
		"ALLOW_REMOTE_UI":                  {},
		"CLOUDFLARED_MANAGED":              {},
		"CLOUDFLARED_PATH":                 {},
		"CLOUDFLARED_READY_TIMEOUT":        {},
		"CLOUDFLARED_TUNNEL_TOKEN":         {},
		"CONTROL_PLANE_API_KEY":            {},
		"HARPOON_CAPTURE_PAYLOADS":         {},
		"LOG_FILE":                         {},
		"LOG_FORMAT":                       {},
		"LOG_LEVEL":                        {},
		"OPEN_WEB_UI":                      {},
		"OPENAI_API_KEY":                   {},
		"ALL_PROXY":                        {},
		"all_proxy":                        {},
		"HTTP_PROXY":                       {},
		"http_proxy":                       {},
		"HTTPS_PROXY":                      {},
		"https_proxy":                      {},
		"NO_PROXY":                         {},
		"no_proxy":                         {},
		"PROXY_CHECK_INTERVAL":             {},
		"RUNTIME_COMPAT_CONTROL_PLANE_URL": {},
		"RUNTIME_COMPAT_MCP_URL":           {},
		"TUNNEL_CLIENT_CONFIG":             {},
		"TUNNEL_CLIENT_PROFILE":            {},
		"TUNNEL_CLIENT_PROFILE_FILE":       {},
	}
	for key := range overrides {
		blocked[key] = struct{}{}
	}
	env := make([]string, 0, len(os.Environ())+len(overrides)+3)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, drop := blocked[key]; drop {
			continue
		}
		env = append(env, entry)
	}
	if _, ok := overrides["CONTROL_PLANE_API_KEY"]; !ok {
		env = append(env, "CONTROL_PLANE_API_KEY="+runtimeArtifactAPIKey)
	}
	if _, ok := overrides["NO_PROXY"]; !ok {
		env = append(env, "NO_PROXY=127.0.0.1,localhost")
	}
	if _, ok := overrides["no_proxy"]; !ok {
		env = append(env, "no_proxy=127.0.0.1,localhost")
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func (p *runtimeArtifactProcess) wait(timeout time.Duration) (error, bool) {
	if p == nil {
		return nil, true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.done:
		return p.err(), true
	case <-timer.C:
		return nil, false
	}
}

func (p *runtimeArtifactProcess) err() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

func (p *runtimeArtifactProcess) stop() error {
	if p == nil {
		return nil
	}
	select {
	case <-p.done:
		return p.err()
	default:
	}
	if p.cmd != nil && p.cmd.Process != nil {
		if runtime.GOOS != "windows" {
			_ = p.cmd.Process.Signal(os.Interrupt)
			if err, ok := p.wait(5 * time.Second); ok {
				return err
			}
		}
		_ = p.cmd.Process.Kill()
	}
	<-p.done
	return p.err()
}

type runtimeArtifactOutput struct {
	mu      sync.Mutex
	b       strings.Builder
	updated chan struct{}
}

func newRuntimeArtifactOutput() *runtimeArtifactOutput {
	return &runtimeArtifactOutput{updated: make(chan struct{}, 1)}
}

func (o *runtimeArtifactOutput) Write(payload []byte) (int, error) {
	o.mu.Lock()
	n, err := o.b.Write(payload)
	o.mu.Unlock()
	if n > 0 {
		select {
		case o.updated <- struct{}{}:
		default:
		}
	}
	return n, err
}

func (o *runtimeArtifactOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}

func waitForRuntimeArtifactHealthURL(t *testing.T, proc *runtimeArtifactProcess, path string) string {
	t.Helper()
	waitForRuntimeArtifactOutput(
		t,
		proc,
		"health URL file",
		runtimeArtifactHealthListeningSignal,
	)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	healthURL := strings.TrimSpace(string(data))
	require.NotEmpty(t, healthURL)
	return healthURL
}

func waitForRuntimeArtifactOutput(t *testing.T, proc *runtimeArtifactProcess, description string, signals ...string) {
	t.Helper()
	require.NotEmpty(t, signals)

	timer := time.NewTimer(runtimeArtifactSignalTimeout)
	defer timer.Stop()
	for {
		if runtimeArtifactOutputContainsAll(proc.output.String(), signals) {
			return
		}
		select {
		case <-proc.output.updated:
		case <-proc.done:
			if runtimeArtifactOutputContainsAll(proc.output.String(), signals) {
				return
			}
			t.Fatalf(
				"runtime artifact exited before %s signal %q; err=%v output:\n%s",
				description,
				signals,
				proc.err(),
				proc.output.String(),
			)
		case <-timer.C:
			if runtimeArtifactOutputContainsAll(proc.output.String(), signals) {
				return
			}
			t.Fatalf(
				"timed out waiting for %s signal %q; output:\n%s",
				description,
				signals,
				proc.output.String(),
			)
		}
	}
}

func runtimeArtifactOutputContainsAll(output string, signals []string) bool {
	for _, signal := range signals {
		if !strings.Contains(output, signal) {
			return false
		}
	}
	return true
}

func waitForRuntimeArtifactIdle(t *testing.T, proc *runtimeArtifactProcess, controlPlane *mocktunnelservice.MockTunnelService) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := controlPlane.WaitUntilIdle(ctx)
	require.NoErrorf(t, err, "runtime artifact did not drain control-plane script; output:\n%s", proc.output.String())
}

func assertRuntimeArtifactHealthSurface(
	t *testing.T,
	proc *runtimeArtifactProcess,
	baseURL string,
	readinessSignals ...string,
) {
	t.Helper()
	if len(readinessSignals) > 0 {
		waitForRuntimeArtifactOutput(t, proc, "readiness", readinessSignals...)
	}
	client := &http.Client{Timeout: 2 * time.Second}

	readyStatus, readyBody := runtimeArtifactResponse(t, client, baseURL+"/readyz")
	require.Equalf(t, http.StatusOK, readyStatus, "runtime artifact never became ready; body=%q output:\n%s", readyBody, proc.output.String())
	require.Equal(t, http.StatusOK, runtimeArtifactStatus(t, client, baseURL+"/healthz"))
	require.Equal(t, http.StatusOK, runtimeArtifactStatus(t, client, baseURL+"/metrics"))
	require.Equal(t, http.StatusNotFound, runtimeArtifactStatus(t, client, baseURL+"/ui"))
}

func runtimeArtifactStatus(t *testing.T, client *http.Client, url string) int {
	t.Helper()
	status, _ := runtimeArtifactResponse(t, client, url)
	return status
}

func runtimeArtifactResponse(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()
	response, err := client.Get(url)
	if err != nil {
		return 0, ""
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body)
}

func assertRuntimeArtifactToolCall(t *testing.T, controlPlane *mocktunnelservice.MockTunnelService, mcpServer *mockmcpserver.MockMCPServer) {
	t.Helper()
	responses := controlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	require.Len(t, responses, 3, "initialize, initialized, and tool responses should all be matched")
	requests := mcpServer.ReceivedRequests()
	require.Len(t, requests, 1)
	require.Equal(t, "echo", requests[0].Tool)
}

func assertRuntimeArtifactStdioToolCall(t *testing.T, controlPlane *mocktunnelservice.MockTunnelService) {
	t.Helper()
	responses := controlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	require.Len(t, responses, 3, "initialize, initialized, and stdio tool responses should all be matched")
}

func writeRuntimeArtifactCloudflaredWrapper(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cloudflared")
	script := strings.Join([]string{
		"#!/bin/sh",
		"exec \"$RUNTIME_E2E_HELPER_BINARY\" -test.run '^TestRuntimeCloudflaredHelperProcess$' -- \"$@\"",
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o700))
	return path
}

func TestRuntimeCloudflaredHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_RUNTIME_CLOUDFLARED_HELPER") != "1" {
		return
	}

	metricsAddr := runtimeArtifactArgValue(os.Args, "--metrics")
	if metricsAddr == "" {
		_, _ = fmt.Fprintln(os.Stderr, "missing --metrics")
		os.Exit(2)
	}
	listener, err := net.Listen("tcp", metricsAddr)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(2)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	server := &http.Server{Handler: mux}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)

	// Publish the startup marker only after the interrupt handler is armed,
	// and do it before /ready can be observed. Under -race the old order left
	// a window where the parent saw readiness and interrupted the helper before
	// it could write the shutdown marker.
	if startedFile := os.Getenv("RUNTIME_CLOUDFLARED_STARTED_FILE"); startedFile != "" {
		if err := os.WriteFile(startedFile, []byte("started\n"), 0o600); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(2)
		}
	}
	go func() { _ = server.Serve(listener) }()

	exitFile := os.Getenv("RUNTIME_CLOUDFLARED_EXIT_FILE")
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		if exitFile != "" {
			if _, err := os.Stat(exitFile); err == nil {
				_ = server.Close()
				os.Exit(23)
			}
		}
		select {
		case <-signals:
			if signalFile := os.Getenv("RUNTIME_CLOUDFLARED_SIGNAL_FILE"); signalFile != "" {
				if err := os.WriteFile(signalFile, []byte("signal\n"), 0o600); err != nil {
					_, _ = fmt.Fprintln(os.Stderr, err.Error())
					os.Exit(2)
				}
			}
			_ = server.Close()
			os.Exit(0)
		case <-deadline.C:
			_ = server.Close()
			os.Exit(24)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func runtimeArtifactArgValue(args []string, key string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}
