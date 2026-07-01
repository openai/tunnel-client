package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.openai.org/api/tunnel-client/pkg/codexplugin"
)

func TestCodexCommandHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CODEX_HELPER") != "1" {
		return
	}

	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	writef := func(format string, args ...any) {
		_, err := fmt.Fprintf(writer, format, args...)
		require.NoError(t, err)
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			continue
		}
		id := envelope["id"]
		method, _ := envelope["method"].(string)
		switch method {
		case "initialize":
			writef("{\"id\":%s,\"result\":{\"user_agent\":\"tunnel-client-test\",\"codex_home\":\"/tmp/.codex\",\"platform_family\":\"unix\",\"platform_os\":\"linux\"}}\n", marshalID(id))
		case "initialized":
			continue
		case "getAuthStatus":
			writef("{\"id\":%s,\"result\":{\"authMethod\":\"chatgpt\",\"requiresOpenaiAuth\":true}}\n", marshalID(id))
		case "account/read":
			writef("{\"id\":%s,\"result\":{\"account\":{\"type\":\"chatgpt\",\"email\":\"worker@example.com\",\"planType\":\"business\"},\"requiresOpenaiAuth\":true}}\n", marshalID(id))
		case "thread/start":
			maybeCaptureThreadStartParams(t, envelope["params"])
			if os.Getenv("GO_WANT_CODEX_STALL_THREAD_START") == "1" {
				_, _ = fmt.Fprintln(os.Stderr, "thread/start is stuck")
				require.NoError(t, writer.Flush())
				continue
			}
			writef("{\"id\":%s,\"result\":{\"thread\":{\"id\":\"thread_cli\",\"preview\":\"CLI assistant\",\"cwd\":\"/workspace/openai\",\"createdAt\":1713740000,\"updatedAt\":1713740001},\"model\":\"gpt-5.4\",\"modelProvider\":\"openai\",\"approvalPolicy\":\"never\",\"sandbox\":\"danger-full-access\"}}\n", marshalID(id))
			writef("{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"thread_cli\",\"preview\":\"CLI assistant\",\"cwd\":\"/workspace/openai\",\"createdAt\":1713740000,\"updatedAt\":1713740001}}}\n")
		case "thread/inject_items":
			maybeCaptureInjectItems(t, envelope["params"])
			writef("{\"id\":%s,\"result\":{}}\n", marshalID(id))
		case "turn/start":
			maybeCaptureTurnStartParams(t, envelope["params"])
			prompt := "unknown"
			if params, ok := envelope["params"].(map[string]any); ok {
				if input, ok := params["input"].([]any); ok && len(input) > 0 {
					if item, ok := input[0].(map[string]any); ok {
						if text, ok := item["text"].(string); ok {
							prompt = text
						}
					}
				}
			}
			response := fmt.Sprintf("assistant heard: %s", prompt)
			writef("{\"id\":%s,\"result\":{\"turn\":{\"id\":\"turn_cli\",\"status\":\"in_progress\"}}}\n", marshalID(id))
			writef("{\"method\":\"turn/started\",\"params\":{\"threadId\":\"thread_cli\",\"turn\":{\"id\":\"turn_cli\",\"status\":\"in_progress\"}}}\n")
			if os.Getenv("GO_WANT_CODEX_STALL_AFTER_TURN_START") == "1" {
				_, _ = fmt.Fprintln(os.Stderr, "turn started but no completion arrived")
				require.NoError(t, writer.Flush())
				continue
			}
			writef("{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread_cli\",\"turnId\":\"turn_cli\",\"delta\":%q}}\n", response)
			writef("{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread_cli\",\"turn\":{\"id\":\"turn_cli\",\"status\":\"completed\"}}}\n")
		default:
			writef("{\"id\":%s,\"result\":{}}\n", marshalID(id))
		}
		require.NoError(t, writer.Flush())
	}
	os.Exit(0)
}

func TestCodexStatusJSONReportsBridgeAndPluginState(t *testing.T) {
	codexHome := t.TempDir()
	fakeTunnelClient := filepath.Join(t.TempDir(), "tunnel-client")
	require.NoError(t, os.WriteFile(fakeTunnelClient, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	_, err := codexplugin.Install(codexHome, fakeTunnelClient)
	require.NoError(t, err)
	normalizedHint, err := codexplugin.NormalizeBinaryPath(fakeTunnelClient)
	require.NoError(t, err)

	codexBin := writeFakeCodexScript(t)
	t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, err := executeCommand(t, map[string]string{
		"CODEX_HOME": codexHome,
		"HOME":       t.TempDir(),
	}, "codex", "status", "--json")

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, `"state": "ready"`)
	require.Contains(t, stdout, `"app_server_supported": true`)
	require.Contains(t, stdout, `"plugin_installed": true`)
	require.Contains(t, stdout, `"plugin_binary_hint": "`+normalizedHint+`"`)
	require.Contains(t, stdout, `"plugin_matches_current_binary": false`)
	require.Contains(t, stdout, `"plugin_reinstall_command": "tunnel-client codex plugin install"`)
	require.Contains(t, stdout, `"version": "codex-cli 0.123.0-alpha.8"`)
	require.Contains(t, stdout, `"bridge_ready": true`)
	require.Contains(t, stdout, `"assistant_state": "ready"`)
	require.NotContains(t, stdout, `update check warning`)
	require.Contains(t, stdout, `"email": "worker@example.com"`)
}

func TestCodexStatusJSONReportsMarketplaceInstallAndStaleConfig(t *testing.T) {
	codexHome := t.TempDir()
	pluginDir := writeMarketplacePluginFixture(t, codexHome, "example-marketplace", false)
	config := `[plugins."tunnel-mcp@example-marketplace"]
enabled = true

[plugins."tunnel-mcp@debug"]
enabled = true
`
	require.NoError(t, os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o644))

	codexBin := writeFakeCodexScript(t)
	t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, err := executeCommand(t, map[string]string{
		"CODEX_HOME": codexHome,
		"HOME":       t.TempDir(),
	}, "codex", "status", "--json")

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, `"plugin_installed": true`)
	require.Contains(t, stdout, `"plugin_key": "tunnel-mcp@example-marketplace"`)
	require.Contains(t, stdout, `"plugin_marketplace": "example-marketplace"`)
	require.Contains(t, stdout, `"plugin_dir": "`+pluginDir+`"`)
	require.Contains(t, stdout, `"plugin_binary_hint_found": false`)
	require.Contains(t, stdout, `"enabled_plugin_config_keys": [`)
	require.Contains(t, stdout, `"tunnel-mcp@debug"`)
	require.Contains(t, stdout, `"stale_plugin_config_entries": [`)
	require.Contains(t, stdout, `no plugin manifest exists in the marketplace cache`)
}

func TestCodexDiagnoseJSONReportsPluginStateAndBridgeSeparately(t *testing.T) {
	codexHome := t.TempDir()
	pluginDir := writeMarketplacePluginFixture(t, codexHome, "example-marketplace", false)
	config := `[plugins."tunnel-mcp@example-marketplace"]
enabled = true
`
	require.NoError(t, os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o644))

	codexBin := writeFakeCodexScript(t)
	t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	stateRoot := filepath.Join(t.TempDir(), "state")
	profileDir := filepath.Join(t.TempDir(), "profiles")

	stdout, stderr, err := executeCommand(t, map[string]string{
		"CODEX_HOME":                codexHome,
		"HOME":                      t.TempDir(),
		"TUNNEL_CLIENT_STATE_DIR":   stateRoot,
		"TUNNEL_CLIENT_PROFILE_DIR": profileDir,
	}, "codex", "diagnose", "--plugin-root", pluginDir, "--json")

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, `"loaded_plugin_source": "`+pluginDir+`"`)
	require.Contains(t, stdout, `"enabled_plugin_config_keys": [`)
	require.Contains(t, stdout, `"cache_path": "`+pluginDir+`"`)
	require.Contains(t, stdout, `"plugin_binary_hint_path": "`+filepath.Join(pluginDir, ".tunnel-client-bin")+`"`)
	require.Contains(t, stdout, `"plugin_binary_hint_found": false`)
	require.Contains(t, stdout, `"state_root": "`+stateRoot+`"`)
	require.Contains(t, stdout, `"profile_dir": "`+profileDir+`"`)
	require.Contains(t, stdout, `"codex_bridge": {`)
	require.Contains(t, stdout, `"app_server_supported": true`)
	require.Contains(t, stdout, `"bridge_ready": true`)
}

func TestCodexStatusTextLabelsPluginStateAsOnDisk(t *testing.T) {
	codexHome := t.TempDir()
	fakeTunnelClient := filepath.Join(t.TempDir(), "tunnel-client")
	require.NoError(t, os.WriteFile(fakeTunnelClient, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	_, err := codexplugin.Install(codexHome, fakeTunnelClient)
	require.NoError(t, err)
	normalizedHint, err := codexplugin.NormalizeBinaryPath(fakeTunnelClient)
	require.NoError(t, err)

	codexBin := writeFakeCodexScript(t)
	t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, err := executeCommand(t, map[string]string{
		"CODEX_HOME": codexHome,
		"HOME":       t.TempDir(),
	}, "codex", "status")

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "Tunnel MCP plugin:\n  Status: installed")
	require.Contains(t, stdout, "Dir: "+codexplugin.PluginTargetDir(codexHome))
	require.Contains(t, stdout, "Binary hint: "+normalizedHint)
	require.Contains(t, stdout, "Matches current tunnel-client: false")
	require.Contains(t, stdout, "Reinstall plugin to use this binary: tunnel-client codex plugin install")
	require.NotContains(t, stdout, "Plugin: installed")
}

func TestCodexStatusTextClarifiesReadyCodexWithMissingOnDiskPlugin(t *testing.T) {
	codexHome := t.TempDir()

	codexBin := writeFakeCodexScript(t)
	t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, err := executeCommand(t, map[string]string{
		"CODEX_HOME": codexHome,
		"HOME":       t.TempDir(),
	}, "codex", "status")

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "Tunnel MCP plugin:\n  Status: not installed\n  Expected dir: "+codexplugin.PluginTargetDir(codexHome))
	require.Contains(t, stdout, "Note: Bridge and Assistant readiness reflect Codex itself, not plugin files on disk.")
}

func TestCodexStatusTextSeparatesPluginStateAfterUninstall(t *testing.T) {
	codexHome := t.TempDir()
	fakeTunnelClient := filepath.Join(t.TempDir(), "tunnel-client")
	require.NoError(t, os.WriteFile(fakeTunnelClient, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	_, err := codexplugin.Install(codexHome, fakeTunnelClient)
	require.NoError(t, err)

	codexBin := writeFakeCodexScript(t)
	t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, err := executeCommand(t, map[string]string{
		"CODEX_HOME": codexHome,
		"HOME":       t.TempDir(),
	}, "codex", "plugin", "uninstall", "--codex-home", codexHome)
	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "Removed on-disk tunnel-mcp plugin bundle")

	stdout, stderr, err = executeCommand(t, map[string]string{
		"CODEX_HOME": codexHome,
		"HOME":       t.TempDir(),
	}, "codex", "status")
	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "Codex:\n  State: ready")
	require.Contains(t, stdout, "\n\nTunnel MCP plugin:\n  Status: not installed\n  Expected dir: "+codexplugin.PluginTargetDir(codexHome))
	require.Contains(t, stdout, "\n\nCodex app / bridge:\n  app-server: supported\n  Bridge: ready\n  Assistant readiness: ready\n  Assistant: tunnel-client codex assistant\n  Account: worker@example.com (business)")
	require.Contains(t, stdout, "Note: Bridge and Assistant readiness reflect Codex itself, not plugin files on disk.")
	require.Contains(t, stdout, "\n\nCommands:\n  Install: ")
	require.NotContains(t, stdout, "Plugin on disk: not installed (")
	require.Less(t, strings.Index(stdout, "Tunnel MCP plugin:"), strings.Index(stdout, "Codex app / bridge:"))
}

func TestCodexStatusJSONReportsBridgeReadyWhenAssistantProbeStalls(t *testing.T) {
	originalTimeout := codexStatusAssistantProbeTimeout
	codexStatusAssistantProbeTimeout = 50 * time.Millisecond
	t.Cleanup(func() {
		codexStatusAssistantProbeTimeout = originalTimeout
	})
	t.Setenv("GO_WANT_CODEX_STALL_THREAD_START", "1")

	codexBin := writeFakeCodexScript(t)
	t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "codex", "status", "--json")

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, `"state": "bridge_ready"`)
	require.Contains(t, stdout, `"bridge_ready": true`)
	require.Contains(t, stdout, `"assistant_state": "unavailable"`)
	require.Regexp(t, `"assistant_error": "thread/start timed out after [0-9]+ms`, stdout)
	require.Contains(t, stdout, `recent stderr: thread/start is stuck`)
}

func TestCodexInstallPrefersHostDefaultWhenMultipleInstallersAreAvailable(t *testing.T) {
	binDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "brew"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "npm"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout bytes.Buffer
	root := newRootCommand(func(string) (string, bool) { return "", false }, &stdout, &bytes.Buffer{})
	root.SetArgs([]string{"codex", "install"})
	require.NoError(t, root.Execute())

	if runtime.GOOS == "darwin" {
		require.Contains(t, stdout.String(), "Preferred on this host: homebrew")
	} else {
		require.Contains(t, stdout.String(), "Preferred on this host: npm")
	}
	require.Contains(t, stdout.String(), "brew install codex")
	require.Contains(t, stdout.String(), "npm install -g @openai/codex")
}

func TestCodexAssistantRunsPromptArgument(t *testing.T) {
	codexBin := writeFakeCodexScript(t)
	t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "codex", "assistant", "describe", "the", "tunnel")

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "assistant heard: describe the tunnel")
	require.Contains(t, stderr, "assistant> ")
	require.Contains(t, stderr, "waiting for response")
}

func TestCodexAssistantReadsPromptFromStdin(t *testing.T) {
	codexBin := writeFakeCodexScript(t)
	t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, err := executeCommandWithInput(t, map[string]string{
		"HOME": t.TempDir(),
	}, "stdin prompt for codex\n", "codex", "assistant")

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "assistant heard: stdin prompt for codex")
	require.Contains(t, stderr, "assistant> ")
	require.Contains(t, stderr, "waiting for response")
}

func TestCodexAssistantReportsTurnStallDiagnostics(t *testing.T) {
	originalTimeout := codexAssistantTurnIdleTimeout
	codexAssistantTurnIdleTimeout = 50 * time.Millisecond
	t.Cleanup(func() {
		codexAssistantTurnIdleTimeout = originalTimeout
	})
	t.Setenv("GO_WANT_CODEX_STALL_AFTER_TURN_START", "1")

	codexBin := writeFakeCodexScript(t)
	t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "codex", "assistant", "describe", "the", "tunnel")

	require.Error(t, err)
	require.Contains(t, stderr, "waiting for response")
	require.ErrorContains(t, err, "assistant turn stalled after turn/start")
	require.ErrorContains(t, err, "turn turn_cli produced no completion or output for 50ms")
	require.ErrorContains(t, err, "recent stderr: turn started but no completion arrived")
}

func TestCodexAssistantUsesManagedCompatibleTurnDefaults(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "turn_start.json")
	t.Setenv("GO_WANT_CODEX_TURN_START_CAPTURE", capturePath)

	codexBin := writeFakeCodexScript(t)
	t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "codex", "assistant", "describe", "the", "tunnel")

	require.NoError(t, err, stderr)
	params := readCapturedTurnStartParams(t, capturePath)
	require.Equal(t, defaultCodexAssistantEffort, params.Effort)
	require.Equal(t, defaultCodexAssistantApprovalPolicy, params.ApprovalPolicy)
	require.Equal(t, "workspaceWrite", params.SandboxPolicy.Type)
}

func TestCodexAssistantInjectsPackagedKnowledgeWhenRepoDocsMayBeAbsent(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "inject_items.json")
	t.Setenv("GO_WANT_CODEX_INJECT_CAPTURE", capturePath)

	codexBin := writeFakeCodexScript(t)
	t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "codex", "assistant", "--cwd", "/tmp", "How", "do", "I", "connect", "this", "tunnel", "to", "ChatGPT?")

	require.NoError(t, err, stderr)
	payload := readCapturedInjectItems(t, capturePath)
	items, ok := payload["items"].([]any)
	require.True(t, ok)

	var found string
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		require.True(t, ok)
		content, ok := item["content"].([]any)
		require.True(t, ok)
		for _, partRaw := range content {
			part, ok := partRaw.(map[string]any)
			require.True(t, ok)
			text, _ := part["text"].(string)
			if strings.Contains(text, "Packaged tunnel-client knowledge base injected from the binary.") {
				found = text
			}
		}
	}
	require.NotEmpty(t, found)
	require.Contains(t, found, "docs/end-user-guide.md")
	require.Contains(t, found, "docs/onboarding.md")
	require.Contains(t, found, "Connect ChatGPT")
	require.Contains(t, found, "Connection: Tunnel")
	require.Contains(t, found, "paste the `tunnel_id`")
}

func TestCodexAssistantInjectsBundledPluginReferences(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "inject_items.json")
	t.Setenv("GO_WANT_CODEX_INJECT_CAPTURE", capturePath)

	codexBin := writeFakeCodexScript(t)
	t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	prompt := "How do I install the codex plugin and set up tunnel-client from the binary?"
	_, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "codex", "assistant", "--cwd", "/tmp", "How", "do", "I", "install", "the", "codex", "plugin", "and", "set", "up", "tunnel-client", "from", "the", "binary?")

	require.NoError(t, err, stderr)
	payload := readCapturedInjectItems(t, capturePath)
	items, ok := payload["items"].([]any)
	require.True(t, ok)

	var found string
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		require.True(t, ok)
		content, ok := item["content"].([]any)
		require.True(t, ok)
		for _, partRaw := range content {
			part, ok := partRaw.(map[string]any)
			require.True(t, ok)
			text, _ := part["text"].(string)
			if strings.Contains(text, "Curated tunnel-mcp plugin references injected from the binary.") {
				found = text
			}
		}
	}
	require.NotEmpty(t, found)
	require.Contains(t, found, "plugins/tunnel-mcp/skills/tunnel-mcp/references/setup-and-install.md")
	require.Contains(t, found, "tunnel-client codex plugin install")
	require.NotContains(t, found, prompt)
}

func TestCodexAssistantInjectsBundledBinaryAcquisitionGuidance(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "inject_items.json")
	t.Setenv("GO_WANT_CODEX_INJECT_CAPTURE", capturePath)

	codexBin := writeFakeCodexScript(t)
	t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	prompt := "The tunnel-mcp plugin is installed but tunnel-client is missing. How do I download and install the binary?"
	_, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "codex", "assistant", "--cwd", "/tmp", "The", "tunnel-mcp", "plugin", "is", "installed", "but", "tunnel-client", "is", "missing.", "How", "do", "I", "download", "and", "install", "the", "binary?")

	require.NoError(t, err, stderr)
	payload := readCapturedInjectItems(t, capturePath)
	items, ok := payload["items"].([]any)
	require.True(t, ok)

	var found string
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		require.True(t, ok)
		content, ok := item["content"].([]any)
		require.True(t, ok)
		for _, partRaw := range content {
			part, ok := partRaw.(map[string]any)
			require.True(t, ok)
			text, _ := part["text"].(string)
			if strings.Contains(text, "Curated tunnel-mcp plugin references injected from the binary.") {
				found = text
			}
		}
	}
	require.NotEmpty(t, found)
	require.Contains(t, found, "plugins/tunnel-mcp/skills/tunnel-mcp/references/binary.md")
	require.Contains(t, found, "https://github.com/openai/tunnel-client/releases/latest")
	require.Contains(t, found, "https://github.com/openai/tunnel-client")
	require.Contains(t, found, "git clone https://github.com/openai/tunnel-client.git")
	require.Contains(t, found, "TUNNEL_CLIENT_BIN")
	if runtime.GOOS == "windows" {
		require.Contains(t, found, `--tunnel-client-bin C:\\path\\to\\tunnel-client.exe`)
		require.NotContains(t, found, "--tunnel-client-bin /path/to/tunnel-client")
	} else {
		require.Contains(t, found, "--tunnel-client-bin /path/to/tunnel-client")
		require.NotContains(t, found, `--tunnel-client-bin C:\\path\\to\\tunnel-client.exe`)
	}
	require.NotContains(t, found, prompt)
}

func TestCodexHelpIncludesAssistantSubcommand(t *testing.T) {
	var stdout bytes.Buffer
	root := newRootCommand(func(string) (string, bool) { return "", false }, &stdout, &bytes.Buffer{})
	root.SetArgs([]string{"codex", "--help"})

	require.NoError(t, root.Execute())
	require.Contains(t, stdout.String(), "assistant")
	require.Contains(t, stdout.String(), "plugin")
}

func TestCodexPluginCommandInstallAndLegacyAliasBothWork(t *testing.T) {
	codexHome := t.TempDir()

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "codex", "plugin", "install", "--codex-home", codexHome)

	require.NoError(t, err, stderr)
	pluginDir := codexplugin.PluginTargetDir(codexHome)
	require.FileExists(t, filepath.Join(pluginDir, ".codex-plugin", "plugin.json"))
	require.Contains(t, stdout, "Installed tunnel-mcp")

	stdout, stderr, err = executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "plugin", "codex", "uninstall", "--codex-home", codexHome)

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "Removed on-disk tunnel-mcp plugin bundle")
}

func TestAssistantWorkingDirectoryPrefersNestedTunnelClientWorkspace(t *testing.T) {
	monorepoRoot := t.TempDir()
	workspace := filepath.Join(monorepoRoot, "api", "tunnel-client")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "cmd", "client"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.com/tunnel-client\n"), 0o644))

	t.Setenv("BUILD_WORKING_DIRECTORY", monorepoRoot)
	require.Equal(t, workspace, assistantWorkingDirectory(""))
}

func TestAssistantWorkingDirectoryPrefersStandaloneTunnelClientWorkspace(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "cmd", "client"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.com/tunnel-client\n"), 0o644))

	t.Setenv("BUILD_WORKING_DIRECTORY", workspace)
	require.Equal(t, workspace, assistantWorkingDirectory(""))
}

func TestAssistantWorkingDirectoryRespectsExplicitOverride(t *testing.T) {
	require.Equal(t, "/tmp/custom-cwd", assistantWorkingDirectory("/tmp/custom-cwd"))
}

func TestCodexAssistantUsesStandaloneWorkspaceAsThreadCWD(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "cmd", "client"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.com/tunnel-client\n"), 0o644))

	capturePath := filepath.Join(t.TempDir(), "thread_start.json")
	t.Setenv("BUILD_WORKING_DIRECTORY", workspace)
	t.Setenv("GO_WANT_CODEX_THREAD_START_CAPTURE", capturePath)

	codexBin := writeFakeCodexScript(t)
	t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "codex", "assistant", "describe", "the", "workspace")

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "assistant heard: describe the workspace")

	params := readCapturedThreadStartParams(t, capturePath)
	require.Equal(t, workspace, params.CWD)
	require.Equal(t, defaultCodexAssistantApprovalPolicy, params.ApprovalPolicy)
	require.Equal(t, defaultCodexAssistantSandboxType, params.Sandbox)
	require.Contains(t, params.DeveloperInstructions, "Stay focused on this workspace root: "+workspace)
}

func TestBuildCodexCLIDeveloperInstructionsNarrowsScopeToTunnelClient(t *testing.T) {
	instructions := buildCodexCLIDeveloperInstructions("/workspace/api/tunnel-client", "")

	require.Contains(t, instructions, "Treat the request as being about tunnel-client")
	require.Contains(t, instructions, "avoid broad repository scans")
	require.Contains(t, instructions, "Stay focused on this workspace root: /workspace/api/tunnel-client")
}

func TestHandleCodexAssistantSlashCommandShowsAndUpdatesModelSettings(t *testing.T) {
	options := codexAssistantOptions{Effort: defaultCodexAssistantEffort}
	var stderr bytes.Buffer

	handled, err := handleCodexAssistantSlashCommand(&stderr, &options, "/model")
	require.True(t, handled)
	require.NoError(t, err)
	require.Contains(t, stderr.String(), "model=default reasoning=medium")

	stderr.Reset()
	handled, err = handleCodexAssistantSlashCommand(&stderr, &options, "/model high")
	require.True(t, handled)
	require.NoError(t, err)
	require.Equal(t, "high", options.Effort)
	require.Contains(t, stderr.String(), "model=default reasoning=high")

	stderr.Reset()
	handled, err = handleCodexAssistantSlashCommand(&stderr, &options, "/model gpt-5.4 medium")
	require.True(t, handled)
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4", options.Model)
	require.Equal(t, "medium", options.Effort)
	require.Contains(t, stderr.String(), "model=gpt-5.4 reasoning=medium")
}

func TestHandleCodexAssistantSlashCommandRejectsUnknownReasoning(t *testing.T) {
	options := codexAssistantOptions{Effort: defaultCodexAssistantEffort}
	var stderr bytes.Buffer

	handled, err := handleCodexAssistantSlashCommand(&stderr, &options, "/model gpt-5 turbo")
	require.True(t, handled)
	require.EqualError(t, err, `unknown reasoning "turbo"; expected one of: low, medium, high`)
}

func writeFakeCodexScript(t *testing.T) string {
	t.Helper()
	scriptPath := filepath.Join(t.TempDir(), "codex")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
case "${1:-}" in
  --version)
    echo "update check warning" >&2
    echo "codex-cli 0.123.0-alpha.8"
    ;;
  app-server)
    if [ "${2:-}" = "--help" ]; then
      echo "usage: codex app-server"
      exit 0
    fi
    GO_WANT_CODEX_HELPER=1 exec %q -test.run=TestCodexCommandHelperProcess --
    ;;
  *)
    echo "unexpected codex args: $*" >&2
    exit 1
    ;;
esac
`, os.Args[0])
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))
	return scriptPath
}

func writeMarketplacePluginFixture(t *testing.T, codexHome string, marketplace string, withBinaryHint bool) string {
	t.Helper()
	pluginDir := codexplugin.PluginTargetDirFor(codexHome, marketplace, "tunnel-mcp", "0.1.0")
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, ".codex-plugin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, ".codex-plugin", "plugin.json"), []byte(`{"name":"tunnel-mcp"}`), 0o644))
	if withBinaryHint {
		fakeTunnelClient := filepath.Join(t.TempDir(), "tunnel-client")
		require.NoError(t, os.WriteFile(fakeTunnelClient, []byte("#!/bin/sh\nexit 0\n"), 0o755))
		normalizedHint, err := codexplugin.NormalizeBinaryPath(fakeTunnelClient)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(pluginDir, ".tunnel-client-bin"), []byte(normalizedHint+"\n"), 0o644))
	}
	return pluginDir
}

func marshalID(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func maybeCaptureThreadStartParams(t *testing.T, params any) {
	t.Helper()
	path := strings.TrimSpace(os.Getenv("GO_WANT_CODEX_THREAD_START_CAPTURE"))
	if path == "" {
		return
	}
	data, err := json.Marshal(params)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
}

type capturedThreadStartParams struct {
	CWD                   string `json:"cwd"`
	ApprovalPolicy        string `json:"approvalPolicy"`
	Sandbox               string `json:"sandbox"`
	DeveloperInstructions string `json:"developerInstructions"`
}

func readCapturedThreadStartParams(t *testing.T, path string) capturedThreadStartParams {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var params capturedThreadStartParams
	require.NoError(t, json.Unmarshal(data, &params))
	return params
}

func maybeCaptureTurnStartParams(t *testing.T, params any) {
	t.Helper()
	path := strings.TrimSpace(os.Getenv("GO_WANT_CODEX_TURN_START_CAPTURE"))
	if path == "" {
		return
	}
	data, err := json.Marshal(params)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
}

type capturedTurnStartParams struct {
	Model          string `json:"model"`
	Effort         string `json:"effort"`
	ApprovalPolicy string `json:"approvalPolicy"`
	SandboxPolicy  struct {
		Type string `json:"type"`
	} `json:"sandboxPolicy"`
}

func readCapturedTurnStartParams(t *testing.T, path string) capturedTurnStartParams {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var params capturedTurnStartParams
	require.NoError(t, json.Unmarshal(data, &params))
	return params
}

func maybeCaptureInjectItems(t *testing.T, params any) {
	t.Helper()
	path := strings.TrimSpace(os.Getenv("GO_WANT_CODEX_INJECT_CAPTURE"))
	if path == "" {
		return
	}
	data, err := json.Marshal(params)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
}

func readCapturedInjectItems(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	return payload
}

func executeCommandWithInput(
	t *testing.T,
	env map[string]string,
	input string,
	args ...string,
) (string, string, error) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	root := newRootCommand(func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}, &stdout, &stderr)
	root.SetIn(strings.NewReader(input))
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}
