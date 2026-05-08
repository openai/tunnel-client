package pluginsbundle

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

//go:embed tunnel-mcp/.codex-plugin/plugin.json tunnel-mcp/README.md tunnel-mcp/AGENTS.md tunnel-mcp/assets/tunnel-mcp-logo.png tunnel-mcp/scripts/install_plugin.py tunnel-mcp/scripts/install_plugin.sh tunnel-mcp/scripts/Install-Plugin.ps1 tunnel-mcp/scripts/tunnel_mcp tunnel-mcp/scripts/tunnel_mcp.cmd tunnel-mcp/scripts/tunnel_mcp.ps1 tunnel-mcp/skills/tunnel-mcp/SKILL.md tunnel-mcp/skills/tunnel-mcp/references/*.md
var embeddedPluginFiles embed.FS

var tunnelMCPPluginFiles = []string{
	"tunnel-mcp/.codex-plugin/plugin.json",
	"tunnel-mcp/README.md",
	"tunnel-mcp/AGENTS.md",
	"tunnel-mcp/assets/tunnel-mcp-logo.png",
	"tunnel-mcp/scripts/install_plugin.py",
	"tunnel-mcp/scripts/install_plugin.sh",
	"tunnel-mcp/scripts/Install-Plugin.ps1",
	"tunnel-mcp/scripts/tunnel_mcp",
	"tunnel-mcp/scripts/tunnel_mcp.cmd",
	"tunnel-mcp/scripts/tunnel_mcp.ps1",
	"tunnel-mcp/skills/tunnel-mcp/SKILL.md",
	"tunnel-mcp/skills/tunnel-mcp/references/binary.md",
	"tunnel-mcp/skills/tunnel-mcp/references/profiles-state-and-keys.md",
	"tunnel-mcp/skills/tunnel-mcp/references/runtime-flows.md",
	"tunnel-mcp/skills/tunnel-mcp/references/setup-and-install.md",
	"tunnel-mcp/skills/tunnel-mcp/references/troubleshooting.md",
}

type PluginManifest struct {
	Name string `json:"name"`
}

var pluginSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func validatePluginSegment(value string, field string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%s must be a non-empty string", field)
	}
	if value != trimmed || trimmed == "." || trimmed == ".." || !pluginSegmentPattern.MatchString(trimmed) {
		return fmt.Errorf(
			"%s must use letters, numbers, '.', '_' or '-' and must not contain path separators",
			field,
		)
	}
	return nil
}

func TunnelMCPManifest() (PluginManifest, error) {
	data, err := embeddedPluginFiles.ReadFile("tunnel-mcp/.codex-plugin/plugin.json")
	if err != nil {
		return PluginManifest{}, fmt.Errorf("read tunnel-mcp manifest: %w", err)
	}
	var manifest PluginManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return PluginManifest{}, fmt.Errorf("parse tunnel-mcp manifest: %w", err)
	}
	if err := validatePluginSegment(manifest.Name, "tunnel-mcp manifest name"); err != nil {
		return PluginManifest{}, err
	}
	return manifest, nil
}

func TunnelMCPExportToDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("export directory is required")
	}
	for _, path := range tunnelMCPPluginFiles {
		data, err := embeddedPluginFiles.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded plugin file %s: %w", path, err)
		}
		targetPath := filepath.Join(dir, strings.TrimPrefix(path, "tunnel-mcp/"))
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("create embedded plugin directory for %s: %w", targetPath, err)
		}
		mode := fs.FileMode(0o644)
		if slices.Contains([]string{
			"tunnel-mcp/scripts/install_plugin.py",
			"tunnel-mcp/scripts/install_plugin.sh",
			"tunnel-mcp/scripts/Install-Plugin.ps1",
			"tunnel-mcp/scripts/tunnel_mcp",
			"tunnel-mcp/scripts/tunnel_mcp.cmd",
			"tunnel-mcp/scripts/tunnel_mcp.ps1",
		}, path) {
			mode = 0o755
		}
		if err := os.WriteFile(targetPath, data, mode); err != nil {
			return fmt.Errorf("write embedded plugin file %s: %w", targetPath, err)
		}
	}
	return nil
}
