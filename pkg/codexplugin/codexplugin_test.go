package codexplugin

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstallExportsPluginAndUpdatesConfig(t *testing.T) {
	t.Parallel()

	codexHome := t.TempDir()
	tunnelClientBin := filepath.Join(t.TempDir(), "tunnel-client")
	require.NoError(t, os.WriteFile(tunnelClientBin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	detection, err := Install(codexHome, tunnelClientBin)
	require.NoError(t, err)

	require.True(t, detection.PluginInstalled)
	require.FileExists(t, filepath.Join(detection.PluginDir, ".codex-plugin", "plugin.json"))
	require.FileExists(t, filepath.Join(detection.PluginDir, "scripts", "Install-Plugin.ps1"))
	require.FileExists(t, filepath.Join(detection.PluginDir, "scripts", "install_plugin.py"))
	require.FileExists(t, filepath.Join(detection.PluginDir, "scripts", "install_plugin.sh"))
	require.FileExists(t, filepath.Join(detection.PluginDir, "scripts", "tunnel_mcp"))
	require.FileExists(t, filepath.Join(detection.PluginDir, ".tunnel-client-bin"))

	configData, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	require.NoError(t, err)
	require.Contains(t, string(configData), `[plugins."tunnel-mcp@debug"]`)
	require.Contains(t, string(configData), "enabled = true")
}

func TestInstallForMarketplacePersistsBinaryHintAndConfigKey(t *testing.T) {
	t.Parallel()

	codexHome := t.TempDir()
	tunnelClientBin := filepath.Join(t.TempDir(), "tunnel-client")
	require.NoError(t, os.WriteFile(tunnelClientBin, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	detection, err := InstallForMarketplace(codexHome, "openai-internal-testing", tunnelClientBin)
	require.NoError(t, err)

	require.True(t, detection.PluginInstalled)
	require.Equal(t, "openai-internal-testing", detection.PluginMarketplace)
	require.Equal(t, "tunnel-mcp@openai-internal-testing", detection.PluginKey)
	require.FileExists(t, filepath.Join(detection.PluginDir, ".tunnel-client-bin"))
	normalized, err := NormalizeBinaryPath(tunnelClientBin)
	require.NoError(t, err)
	require.Equal(t, normalized, detection.PluginBinaryHint)
	configData, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	require.NoError(t, err)
	require.Contains(t, string(configData), `[plugins."tunnel-mcp@openai-internal-testing"]`)
}

func TestExportWritesBinaryHintWhenProvided(t *testing.T) {
	t.Parallel()

	exportDir := t.TempDir()
	tunnelClientBin := filepath.Join(t.TempDir(), "tunnel-client")
	require.NoError(t, os.WriteFile(tunnelClientBin, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	err := Export(exportDir, tunnelClientBin)
	require.NoError(t, err)

	require.FileExists(t, filepath.Join(exportDir, "scripts", "Install-Plugin.ps1"))
	require.FileExists(t, filepath.Join(exportDir, "scripts", "install_plugin.sh"))
	data, err := os.ReadFile(filepath.Join(exportDir, ".tunnel-client-bin"))
	require.NoError(t, err)
	normalized, err := NormalizeBinaryPath(tunnelClientBin)
	require.NoError(t, err)
	require.Equal(t, normalized+"\n", string(data))
}

func TestDetectReportsMissingPluginInstallHint(t *testing.T) {
	t.Parallel()

	codexHome := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("[features]\nplugins = true\n"), 0o644))

	detection := Detect(func(key string) (string, bool) {
		if key == "CODEX_HOME" {
			return codexHome, true
		}
		return "", false
	})

	require.True(t, detection.Detected)
	require.False(t, detection.PluginInstalled)
	require.Equal(t, "tunnel-client codex plugin install", detection.InstallHint)
}

func TestDetectReportsInstalledBinaryHint(t *testing.T) {
	t.Parallel()

	codexHome := t.TempDir()
	tunnelClientBin := filepath.Join(t.TempDir(), "tunnel-client")
	require.NoError(t, os.WriteFile(tunnelClientBin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	_, err := Install(codexHome, tunnelClientBin)
	require.NoError(t, err)

	detection := Detect(func(key string) (string, bool) {
		if key == "CODEX_HOME" {
			return codexHome, true
		}
		return "", false
	})

	normalized, err := NormalizeBinaryPath(tunnelClientBin)
	require.NoError(t, err)
	require.Equal(t, normalized, detection.PluginBinaryHint)
}

func TestDetectReportsMarketplaceInstalledBundle(t *testing.T) {
	t.Parallel()

	codexHome := t.TempDir()
	config := `[plugins."tunnel-mcp@example-marketplace"]
enabled = true
`
	require.NoError(t, os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o644))
	pluginDir := PluginTargetDirFor(codexHome, "example-marketplace", "tunnel-mcp", "0.1.0")
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, ".codex-plugin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, ".codex-plugin", "plugin.json"), []byte(`{"name":"tunnel-mcp"}`), 0o644))
	tunnelClientBin := filepath.Join(t.TempDir(), "tunnel-client")
	require.NoError(t, os.WriteFile(tunnelClientBin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	normalized, err := NormalizeBinaryPath(tunnelClientBin)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, ".tunnel-client-bin"), []byte(normalized+"\n"), 0o644))

	detection := Detect(func(key string) (string, bool) {
		if key == "CODEX_HOME" {
			return codexHome, true
		}
		return "", false
	})

	require.True(t, detection.PluginInstalled)
	require.Equal(t, "tunnel-mcp@example-marketplace", detection.PluginKey)
	require.Equal(t, "example-marketplace", detection.PluginMarketplace)
	require.Equal(t, "0.1.0", detection.PluginVersion)
	require.Equal(t, pluginDir, detection.PluginDir)
	require.Equal(t, normalized, detection.PluginBinaryHint)
	require.True(t, detection.PluginBinaryHintFound)
	require.Equal(t, []string{"tunnel-mcp@example-marketplace"}, detection.EnabledConfigKeys)
	require.Empty(t, detection.StaleConfigEntries)
}

func TestDetectPrefersHighestSemverMarketplaceBundle(t *testing.T) {
	t.Parallel()

	codexHome := t.TempDir()
	config := `[plugins."tunnel-mcp@example-marketplace"]
enabled = true
`
	require.NoError(t, os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o644))

	for _, version := range []string{"0.9.0", "0.10.0"} {
		pluginDir := PluginTargetDirFor(codexHome, "example-marketplace", "tunnel-mcp", version)
		require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, ".codex-plugin"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(pluginDir, ".codex-plugin", "plugin.json"), []byte(`{"name":"tunnel-mcp"}`), 0o644))
	}

	detection := Detect(func(key string) (string, bool) {
		if key == "CODEX_HOME" {
			return codexHome, true
		}
		return "", false
	})

	require.True(t, detection.PluginInstalled)
	require.Equal(t, "0.10.0", detection.PluginVersion)
	require.Equal(t, PluginTargetDirFor(codexHome, "example-marketplace", "tunnel-mcp", "0.10.0"), detection.PluginDir)
}

func TestDetectFlagsStalePluginConfigEntry(t *testing.T) {
	t.Parallel()

	codexHome := t.TempDir()
	config := `[plugins."tunnel-mcp@example-marketplace"]
enabled = true
`
	require.NoError(t, os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o644))

	detection := Detect(func(key string) (string, bool) {
		if key == "CODEX_HOME" {
			return codexHome, true
		}
		return "", false
	})

	require.False(t, detection.PluginInstalled)
	require.Len(t, detection.StaleConfigEntries, 1)
	require.Equal(t, "tunnel-mcp@example-marketplace", detection.StaleConfigEntries[0].Key)
	require.Contains(t, detection.StaleConfigEntries[0].Reason, "no plugin manifest")
}

func TestDetectInstalledBundleMissingBinaryHint(t *testing.T) {
	t.Parallel()

	codexHome := t.TempDir()
	config := `[plugins."tunnel-mcp@example-marketplace"]
enabled = true
`
	require.NoError(t, os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o644))
	pluginDir := PluginTargetDirFor(codexHome, "example-marketplace", "tunnel-mcp", "0.1.0")
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, ".codex-plugin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, ".codex-plugin", "plugin.json"), []byte(`{"name":"tunnel-mcp"}`), 0o644))

	detection := Detect(func(key string) (string, bool) {
		if key == "CODEX_HOME" {
			return codexHome, true
		}
		return "", false
	})

	require.True(t, detection.PluginInstalled)
	require.Equal(t, pluginDir, detection.PluginDir)
	require.Empty(t, detection.PluginBinaryHint)
	require.False(t, detection.PluginBinaryHintFound)
	require.Equal(t, filepath.Join(pluginDir, ".tunnel-client-bin"), detection.PluginBinaryHintPath)
}

func TestInstallNormalizesSymlinkedBinaryHint(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not reliably available in Windows test environments")
	}

	codexHome := t.TempDir()
	realBin := filepath.Join(t.TempDir(), "tunnel-client-real")
	require.NoError(t, os.WriteFile(realBin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	symlinkBin := filepath.Join(t.TempDir(), "tunnel-client-link")
	require.NoError(t, os.Symlink(realBin, symlinkBin))

	detection, err := Install(codexHome, symlinkBin)
	require.NoError(t, err)
	normalized, err := NormalizeBinaryPath(realBin)
	require.NoError(t, err)
	require.Equal(t, normalized, detection.PluginBinaryHint)
}

func TestUninstallRemovesPluginBundleAndConfigSection(t *testing.T) {
	t.Parallel()

	codexHome := t.TempDir()
	tunnelClientBin := filepath.Join(t.TempDir(), "tunnel-client")
	require.NoError(t, os.WriteFile(tunnelClientBin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	detection, err := Install(codexHome, tunnelClientBin)
	require.NoError(t, err)

	result, err := Uninstall(codexHome)
	require.NoError(t, err)
	require.True(t, result.RemovedPluginDir)
	require.True(t, result.RemovedConfigSection)
	require.NoDirExists(t, detection.PluginDir)

	configData, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	require.NoError(t, err)
	require.NotContains(t, string(configData), `[plugins."tunnel-mcp@debug"]`)
}

func TestUninstallIsIdempotentWhenPluginMissing(t *testing.T) {
	t.Parallel()

	codexHome := t.TempDir()
	result, err := Uninstall(codexHome)
	require.NoError(t, err)
	require.False(t, result.RemovedPluginDir)
	require.False(t, result.RemovedConfigSection)
}
