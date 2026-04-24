package pluginsbundle

import (
	assistantkb "go.openai.org/api/tunnel-client/docs"
	"strings"
)

const (
	tunnelMCPPromptMatchLimit   = 2
	tunnelMCPPromptExcerptChars = 700
)

var tunnelMCPReferenceFiles = []string{
	"tunnel-mcp/skills/tunnel-mcp/references/binary.md",
	"tunnel-mcp/skills/tunnel-mcp/references/setup-and-install.md",
	"tunnel-mcp/skills/tunnel-mcp/references/profiles-state-and-keys.md",
	"tunnel-mcp/skills/tunnel-mcp/references/runtime-flows.md",
	"tunnel-mcp/skills/tunnel-mcp/references/troubleshooting.md",
}

const bundledBinaryGuidanceExcerpt = `# Obtaining a tunnel-client binary

First try existing binary discovery:

- --tunnel-client-bin /path/to/tunnel-client
- TUNNEL_CLIENT_BIN
- the installed plugin bundle's .tunnel-client-bin hint
- adjacent local build outputs
- PATH

If tunnel-client is still missing, use one of these public-safe setup paths:

- latest releases: https://github.com/openai/tunnel-client/releases/latest
- public repo: https://github.com/openai/tunnel-client

Source build from the public repo:

git clone https://github.com/openai/tunnel-client.git
cd tunnel-client
go build -o bin/tunnel-client ./cmd/client

Windows source build:

git clone https://github.com/openai/tunnel-client.git
cd tunnel-client
go build -o bin/tunnel-client.exe ./cmd/client

After you have a binary:

- set TUNNEL_CLIENT_BIN to the full path to the binary
- or rerun the plugin/install command with --tunnel-client-bin /path/to/tunnel-client
- or reinstall the plugin with --tunnel-client-bin /path/to/tunnel-client

Do not auto-download, auto-clone, or auto-run remote binaries just because the plugin cannot find tunnel-client.`

const bundledSetupInstallExcerpt = `# Setup and install

Use the binary-owned install path when a tunnel-client binary is available:

- tunnel-client codex plugin install
- tunnel-client codex plugin uninstall
- tunnel-client codex status

Use the exported bundle only when the binary is not already installed or when you need to inspect the plugin contents first:

- tunnel-client codex plugin export --dir /tmp/tunnel-mcp
- cd /tmp/tunnel-mcp && python3 scripts/install_plugin.py --tunnel-client-bin /path/to/tunnel-client

After install, prefer the installed plugin router and persisted .tunnel-client-bin hint over an ambient tunnel-client found on PATH.`

func BuildTunnelMCPPromptContext(prompt string) string {
	if isBinaryAcquisitionPrompt(prompt) {
		return assistantkb.FormatPromptContext([]string{
			"Curated tunnel-mcp plugin references injected from the binary.",
			"These snippets cover binary acquisition, plugin setup, runtime flows, profiles, state dirs, key split, and troubleshooting.",
			"Use them before guessing how the Codex plugin should create, connect, inspect, or debug a tunnel runtime.",
		}, "plugin_knowledge.match", []assistantkb.Match{
			{
				Path:    "plugins/tunnel-mcp/skills/tunnel-mcp/references/binary.md",
				Heading: "Obtaining a tunnel-client binary",
				Excerpt: bundledBinaryGuidanceExcerpt,
				Score:   100,
			},
			{
				Path:    "plugins/tunnel-mcp/skills/tunnel-mcp/references/setup-and-install.md",
				Heading: "Setup and install",
				Excerpt: bundledSetupInstallExcerpt,
				Score:   90,
			},
		})
	}
	matches := assistantkb.SearchFS(
		prompt,
		embeddedPluginFiles,
		tunnelMCPReferenceFiles,
		"plugins/",
		tunnelMCPPromptMatchLimit,
		tunnelMCPPromptExcerptChars,
	)
	return assistantkb.FormatPromptContext([]string{
		"Curated tunnel-mcp plugin references injected from the binary.",
		"These snippets cover binary acquisition, plugin setup, runtime flows, profiles, state dirs, key split, and troubleshooting.",
		"Use them before guessing how the Codex plugin should create, connect, inspect, or debug a tunnel runtime.",
	}, "plugin_knowledge.match", matches)
}

func isBinaryAcquisitionPrompt(prompt string) bool {
	lower := strings.ToLower(prompt)
	signals := []string{
		"tunnel-client was not found",
		"cannot find a `tunnel-client` binary",
		"cannot find a tunnel-client binary",
		"command -v tunnel-client",
		"tunnel-client is missing",
		"install the binary",
		"download and install the binary",
		"how do i get a binary",
		"plugin cannot find a `tunnel-client` binary",
		"plugin cannot find tunnel-client",
	}
	for _, signal := range signals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}
