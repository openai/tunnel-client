package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.openai.org/api/tunnel-client/pkg/codexplugin"
	"go.openai.org/api/tunnel-client/pkg/config"
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

func TestInitWritesValidatedProfile(t *testing.T) {
	t.Parallel()

	profileDir := t.TempDir()
	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "init",
		"--profile", "demo",
		"--profile-dir", profileDir,
		"--tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp-server-url", "http://127.0.0.1:3001/mcp",
		"--open-web-ui",
	)

	require.NoError(t, err, stderr)
	path := filepath.Join(profileDir, "demo.yaml")
	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.NoError(t, config.ValidateProfileBytes(path, data))
	require.Contains(t, stdout, "Created profile demo")
	require.Contains(t, stdout, "tunnel-client doctor --profile demo")
	require.Contains(t, stdout, "tunnel-client run --profile demo")
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
		"--mcp-command", "python /path/to/server.py",
	)

	require.NoError(t, err, stderr)
	path := filepath.Join(profileDir, "local-stdio.yaml")
	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.NoError(t, config.ValidateProfileBytes(path, data))
	require.Contains(t, string(data), `command: "python /path/to/server.py"`)
	require.Contains(t, stdout, "Sample: sample_mcp_stdio_local")
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
	require.FileExists(t, filepath.Join(pluginDir, "scripts", "install_plugin.py"))
	require.FileExists(t, filepath.Join(codexHome, "config.toml"))
	require.Contains(t, stdout, "Installed tunnel-mcp")
	require.Contains(t, stdout, "tunnel-client codex assistant")
	require.Contains(t, stdout, "tunnel-client help plugin")
	require.Contains(t, stdout, "start a new Codex session")

	exportDir := t.TempDir()
	stdout, stderr, err = executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "plugin", "codex", "export", "--dir", exportDir)

	require.NoError(t, err, stderr)
	require.FileExists(t, filepath.Join(exportDir, "scripts", "install_plugin.py"))
	require.FileExists(t, filepath.Join(exportDir, ".codex-plugin", "plugin.json"))
	require.NoFileExists(t, filepath.Join(exportDir, ".tunnel-client-bin"))
	require.Contains(t, stdout, "Exported embedded Codex plugin bundle")
	require.Contains(t, stdout, "python3 scripts/install_plugin.py --tunnel-client-bin /path/to/tunnel-client")

	stdout, stderr, err = executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "plugin", "codex", "uninstall", "--codex-home", codexHome)

	require.NoError(t, err, stderr)
	require.NoDirExists(t, pluginDir)
	configData, readErr := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	require.NoError(t, readErr)
	require.NotContains(t, string(configData), `[plugins."tunnel-mcp@debug"]`)
	require.Contains(t, stdout, "Removed tunnel-mcp")
	require.Contains(t, stdout, "tunnel-client codex plugin install")
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
