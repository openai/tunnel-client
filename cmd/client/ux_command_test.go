package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/codexplugin"
	"github.com/openai/tunnel-client/pkg/config"
)

func TestRootHelpAdvertisesAgentFirstTopicsAndCommands(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "--help")

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "Agent-first help topics:")
	require.Contains(t, stdout, "tunnel-client help quickstart")
	require.Contains(t, stdout, "init")
	require.Contains(t, stdout, "doctor")
	require.Contains(t, stdout, "dev")
	require.Contains(t, stdout, "codex")
	require.Contains(t, stdout, "tunnel-client codex assistant")
	require.Contains(t, stdout, "connect a local or private MCP server")
	require.Contains(t, stdout, canonicalTunnelsManagementURL)
	require.Contains(t, stdout, canonicalRuntimeAPIKeysURL)
	require.Contains(t, stdout, canonicalAdminAPIKeysURL)
	require.Contains(t, stdout, canonicalChatGPTConnectorSettingsURL)
}

func TestHelpTopicQuickstart(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "help", "quickstart")

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "What tunnel-client is for:")
	require.Contains(t, stdout, "tunnel-client run --embedded-mcp-stub")
	require.Contains(t, stdout, "sample_mcp_stdio_local")
	require.Contains(t, stdout, "sample_mcp_remote_no_auth")
	require.Contains(t, stdout, "tunnel-client doctor")
	require.Contains(t, stdout, "tunnel-client run")
	require.Contains(t, stdout, canonicalTunnelsManagementURL)
	require.Contains(t, stdout, canonicalRuntimeAPIKeysURL)
	require.Contains(t, stdout, canonicalAdminAPIKeysURL)
	require.Contains(t, stdout, canonicalChatGPTConnectorSettingsURL)
	require.Contains(t, stdout, "CONTROL_PLANE_TUNNEL_ID")
	require.Contains(t, stdout, "CONTROL_PLANE_API_KEY")
	require.Contains(t, stdout, "Do not give the admin key to the long-lived daemon.")
	require.Contains(t, stdout, "Use `tunnel-client run ...` when you intentionally want a foreground daemon")
	require.Contains(t, stdout, "For a long-lived local runtime managed by Codex")
	require.Contains(t, stdout, "Do not use `nohup` or `disown` as the tunnel-client supervision path.")
	require.Contains(t, stdout, "After `runtimes connect`, check `tunnel-client runtimes status <alias>`")
	require.Contains(t, stdout, "Only report success when status shows the managed")
	require.Contains(t, stdout, "Create or verify the connector in ChatGPT settings only while tunnel-client is running.")
	require.Contains(t, stdout, "Keep tunnel-client up for connector discovery and every MCP call from ChatGPT.")
}

func TestHelpTopicDoctor(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "help", "doctor")

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, canonicalTunnelsManagementURL)
	require.Contains(t, stdout, canonicalRuntimeAPIKeysURL)
	require.Contains(t, stdout, canonicalAdminAPIKeysURL)
	require.Contains(t, stdout, canonicalChatGPTConnectorSettingsURL)
	require.Contains(t, stdout, "CONTROL_PLANE_TUNNEL_ID")
	require.Contains(t, stdout, "CONTROL_PLANE_API_KEY")
	require.Contains(t, stdout, "OPENAI_ADMIN_KEY")
	require.Contains(t, stdout, "Create or verify the connector in ChatGPT settings only while tunnel-client is running.")
	require.Contains(t, stdout, "Keep tunnel-client up for connector discovery and every MCP call from ChatGPT.")
}

func TestEmbeddedHelpTopicsLoad(t *testing.T) {
	t.Parallel()

	topics := availableHelpTopics()
	require.NotEmpty(t, topics)
	for _, topic := range topics {
		body, ok := loadHelpTopic(topic)
		require.True(t, ok, topic)
		require.NotEmpty(t, body, topic)
	}
}

func TestHelpTopicPluginUsesCurrentOSInstallGuidance(t *testing.T) {
	t.Parallel()

	body, ok := loadHelpTopic("plugin")
	require.True(t, ok)
	if runtime.GOOS == "windows" {
		require.Contains(t, body, `Windows PowerShell: Set-Location C:\tmp\tunnel-plugin; powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\Install-Plugin.ps1 --tunnel-client-bin C:\path\to\tunnel-client.exe`)
		require.NotContains(t, body, "macOS/Linux: cd /tmp/tunnel-plugin && sh scripts/install_plugin.sh --tunnel-client-bin /path/to/tunnel-client")
	} else {
		require.Contains(t, body, "macOS/Linux: cd /tmp/tunnel-plugin && sh scripts/install_plugin.sh --tunnel-client-bin /path/to/tunnel-client")
		require.NotContains(t, body, `Windows PowerShell: Set-Location C:\tmp\tunnel-plugin; powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\Install-Plugin.ps1 --tunnel-client-bin C:\path\to\tunnel-client.exe`)
	}
}

func TestRenderHelpTopicForOSSwitchesBundleInstallCommand(t *testing.T) {
	t.Parallel()

	body := renderHelpTopicForOS(pluginBundleInstallCommandPlaceholder, "windows")
	require.Equal(t, `  Windows PowerShell: Set-Location C:\tmp\tunnel-plugin; powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\Install-Plugin.ps1 --tunnel-client-bin C:\path\to\tunnel-client.exe`, body)

	body = renderHelpTopicForOS(pluginBundleInstallCommandPlaceholder, "darwin")
	require.Equal(t, "  macOS/Linux: cd /tmp/tunnel-plugin && sh scripts/install_plugin.sh --tunnel-client-bin /path/to/tunnel-client", body)
}

func TestInitWritesValidatedProfile(t *testing.T) {
	t.Parallel()

	profileDir := t.TempDir()
	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "init",
		"--profile", "demo",
		"--profile-dir", profileDir,
		"--tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--control-plane-url-path", "/chatgpttunnelgateway/dev/us",
		"--mcp-server-url", "http://127.0.0.1:3001/mcp",
		"--open-web-ui",
	)

	require.NoError(t, err, stderr)
	path := filepath.Join(profileDir, "demo.yaml")
	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.NoError(t, config.ValidateProfileBytes(path, data))
	require.Contains(t, string(data), `url_path: "/chatgpttunnelgateway/dev/us"`)
	require.Contains(t, stdout, "Created profile demo")
	require.Contains(t, stdout, "tunnel-client doctor --profile demo")
	require.Contains(t, stdout, "tunnel-client run --profile demo")
	require.Contains(t, stdout, canonicalTunnelsManagementURL)
	require.Contains(t, stdout, canonicalRuntimeAPIKeysURL)
	require.Contains(t, stdout, canonicalAdminAPIKeysURL)
	require.Contains(t, stdout, canonicalChatGPTConnectorSettingsURL)
	require.Contains(t, stdout, "Create or verify the connector in https://chatgpt.com/#settings/Connectors only while `tunnel-client run --profile demo` is running.")
	require.Contains(t, stdout, "Keep the daemon up for connector discovery and every MCP call from ChatGPT.")
}

func TestInitWithoutTunnelIDPointsToQuickstartAndAdminFlows(t *testing.T) {
	t.Parallel()

	_, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "init")

	require.Error(t, err)
	require.Empty(t, stderr)
	require.Contains(t, err.Error(), "tunnel-client admin tunnels create --help")
	require.Contains(t, err.Error(), "tunnel-client help quickstart")
	require.Contains(t, err.Error(), canonicalTunnelsManagementURL)
	require.Contains(t, err.Error(), canonicalRuntimeAPIKeysURL)
	require.Contains(t, err.Error(), canonicalAdminAPIKeysURL)
	require.Contains(t, err.Error(), canonicalChatGPTConnectorSettingsURL)
}

func TestProfilesSamplesListAndShow(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "profiles", "samples", "list")

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "sample_mcp_with_dcr")
	require.Contains(t, stdout, "sample_mcp_stdio_local")
	require.Contains(t, stdout, "sample_mcp_remote_no_auth")
	require.Contains(t, stdout, "sample_mcp_enterprise_proxy")

	stdout, stderr, err = executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "profiles", "samples", "show", "sample_mcp_with_dcr")

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "Use when:")
	require.Contains(t, stdout, "Summary:")
	require.Contains(t, stdout, "Required:")
	require.Contains(t, stdout, "control_plane:")
}

func TestProfilesSamplesShowEnterpriseProxyMentionsEnvRefs(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "profiles", "samples", "show", "sample_mcp_enterprise_proxy")

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "env:HTTPS_PROXY")
	require.Contains(t, stdout, "env:ENTERPRISE_CA_BUNDLE")
	require.Contains(t, stdout, "OPENAI_ADMIN_KEY")
}

func TestInitCanUseExplicitStdioSample(t *testing.T) {
	t.Parallel()

	profileDir := t.TempDir()
	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "init",
		"--sample", "sample_mcp_stdio_local",
		"--profile", "local-stdio",
		"--profile-dir", profileDir,
		"--tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp-command", testExecutableCommand(),
	)

	require.NoError(t, err, stderr)
	path := filepath.Join(profileDir, "local-stdio.yaml")
	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.NoError(t, config.ValidateProfileBytes(path, data))
	require.Contains(t, string(data), `command: "`+testExecutableCommand()+`"`)
	require.Contains(t, stdout, "Sample: sample_mcp_stdio_local")
}

func TestInitRejectsNonExecutableStdioCommand(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("execute-bit preflight is Unix-specific")
	}

	profileDir := t.TempDir()
	commandPath := filepath.Join(t.TempDir(), "non-executable-server")
	require.NoError(t, os.WriteFile(commandPath, []byte("#!/bin/sh\n"), 0o600))

	_, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "init",
		"--sample", "sample_mcp_stdio_local",
		"--profile", "broken-stdio",
		"--profile-dir", profileDir,
		"--tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp-command", commandPath,
	)

	require.Error(t, err)
	require.Empty(t, stderr)
	require.Contains(t, err.Error(), "mcp-command preflight failed")
	require.Contains(t, err.Error(), "is not executable")
	require.NoFileExists(t, filepath.Join(profileDir, "broken-stdio.yaml"))
}

func TestInitSTDIO0305RejectsShellCWrapperForNonExecutableScript(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("execute-bit preflight is Unix-specific")
	}

	profileDir := t.TempDir()
	commandPath := filepath.Join(t.TempDir(), "stdio_server.sh")
	require.NoError(t, os.WriteFile(commandPath, []byte("#!/bin/sh\n"), 0o600))

	_, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "init",
		"--sample", "sample_mcp_stdio_local",
		"--profile", "broken-stdio-shell",
		"--profile-dir", profileDir,
		"--tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp-command", "/bin/sh -c "+shellQuote(commandPath),
	)

	require.Error(t, err)
	require.Empty(t, stderr)
	require.Contains(t, err.Error(), "mcp-command preflight failed")
	require.Contains(t, err.Error(), "chmod +x")
	require.NoFileExists(t, filepath.Join(profileDir, "broken-stdio-shell.yaml"))
}

func TestInitSTDIO0305RejectsDirectScriptWithMissingInterpreter(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shebang preflight is Unix-specific")
	}

	profileDir := t.TempDir()
	dir := t.TempDir()
	missingInterpreter := filepath.Join(dir, "missing-interpreter")
	commandPath := filepath.Join(dir, "stdio_server.sh")
	require.NoError(t, os.WriteFile(commandPath, []byte("#!"+missingInterpreter+"\n"), 0o700))

	_, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "init",
		"--sample", "sample_mcp_stdio_local",
		"--profile", "broken-stdio-interpreter",
		"--profile-dir", profileDir,
		"--tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp-command", commandPath,
	)

	require.Error(t, err)
	require.Empty(t, stderr)
	require.Contains(t, err.Error(), "mcp-command preflight failed")
	require.Contains(t, err.Error(), "uses an unavailable interpreter")
	require.Contains(t, err.Error(), "update the shebang")
	require.NoFileExists(t, filepath.Join(profileDir, "broken-stdio-interpreter.yaml"))
}

func TestEmbeddedProfileSamplesRenderAndValidate(t *testing.T) {
	t.Parallel()

	for _, sample := range profileSamples() {
		data, err := sample.Generate(sample.Example)
		require.NoError(t, err, sample.Name)
		path := filepath.Join(t.TempDir(), sample.Name+".yaml")
		require.NoError(t, config.ValidateProfileBytes(path, data), sample.Name)
	}
}

func TestPluginCodexInstallExportAndUninstallCommands(t *testing.T) {
	t.Parallel()

	codexHome := t.TempDir()
	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "plugin", "codex", "install", "--codex-home", codexHome)

	require.NoError(t, err, stderr)
	pluginDir := codexplugin.PluginTargetDir(codexHome)
	require.FileExists(t, filepath.Join(pluginDir, ".codex-plugin", "plugin.json"))
	require.FileExists(t, filepath.Join(pluginDir, ".tunnel-client-bin"))
	require.FileExists(t, filepath.Join(pluginDir, "scripts", "Install-Plugin.ps1"))
	require.FileExists(t, filepath.Join(pluginDir, "scripts", "install_plugin.py"))
	require.FileExists(t, filepath.Join(pluginDir, "scripts", "install_plugin.sh"))
	require.FileExists(t, filepath.Join(codexHome, "config.toml"))
	require.Contains(t, stdout, "Installed tunnel-mcp")
	require.Contains(t, stdout, "Persisted binary hint")
	require.Contains(t, stdout, "tunnel-client codex assistant")
	require.Contains(t, stdout, "tunnel-client help plugin")
	require.Contains(t, stdout, "start a new Codex session")

	exportDir := t.TempDir()
	stdout, stderr, err = executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "plugin", "codex", "export", "--dir", exportDir)

	require.NoError(t, err, stderr)
	require.FileExists(t, filepath.Join(exportDir, "scripts", "Install-Plugin.ps1"))
	require.FileExists(t, filepath.Join(exportDir, "scripts", "install_plugin.py"))
	require.FileExists(t, filepath.Join(exportDir, "scripts", "install_plugin.sh"))
	require.FileExists(t, filepath.Join(exportDir, ".codex-plugin", "plugin.json"))
	require.FileExists(t, filepath.Join(exportDir, ".tunnel-client-bin"))
	require.Contains(t, stdout, "Exported embedded Codex plugin bundle")
	require.Contains(t, stdout, "Persisted binary hint")
	require.Contains(t, stdout, "scripts/tunnel_mcp self-check")
	require.Contains(t, stdout, "sh scripts/install_plugin.sh --tunnel-client-bin /path/to/tunnel-client")
	require.Contains(t, stdout, `powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\Install-Plugin.ps1 --tunnel-client-bin C:\path\to\tunnel-client.exe`)

	stdout, stderr, err = executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "plugin", "codex", "uninstall", "--codex-home", codexHome)

	require.NoError(t, err, stderr)
	require.NoDirExists(t, pluginDir)
	configData, readErr := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	require.NoError(t, readErr)
	require.NotContains(t, string(configData), `[plugins."tunnel-mcp@debug"]`)
	require.Contains(t, stdout, "Removed on-disk tunnel-mcp plugin bundle")
	require.Contains(t, stdout, "Removed on-disk Codex config section")
	require.Contains(t, stdout, "tunnel-client codex plugin install")
	require.Contains(t, stdout, "restart any existing Codex session if the plugin was already loaded")
}

func TestPluginCodexInstallCanTargetMarketplaceCache(t *testing.T) {
	t.Parallel()

	codexHome := t.TempDir()
	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "codex", "plugin", "install", "--codex-home", codexHome, "--marketplace", "openai-internal-testing")

	require.NoError(t, err, stderr)
	pluginDir := codexplugin.PluginTargetDirFor(codexHome, "openai-internal-testing", "tunnel-mcp", "local")
	require.FileExists(t, filepath.Join(pluginDir, ".codex-plugin", "plugin.json"))
	require.FileExists(t, filepath.Join(pluginDir, ".tunnel-client-bin"))
	require.Contains(t, stdout, "Installed tunnel-mcp")
	require.Contains(t, stdout, "Persisted binary hint")
	configData, readErr := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	require.NoError(t, readErr)
	require.Contains(t, string(configData), `[plugins."tunnel-mcp@openai-internal-testing"]`)
}

func TestPluginCodexUninstallIsIdempotent(t *testing.T) {
	t.Parallel()

	codexHome := t.TempDir()
	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "plugin", "codex", "uninstall", "--codex-home", codexHome)

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "Tunnel MCP plugin is not installed")
}
