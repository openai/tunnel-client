package pluginsbundle

import (
	assistantkb "github.com/openai/tunnel-client/docs"
	"runtime"
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

func BuildTunnelMCPPromptContext(prompt string) string {
	if isBinaryAcquisitionPrompt(prompt) {
		binaryExcerpt := buildBundledBinaryGuidanceExcerpt(runtime.GOOS)
		setupExcerpt := buildBundledSetupInstallExcerpt(runtime.GOOS)
		return assistantkb.FormatPromptContext([]string{
			"Curated tunnel-mcp plugin references injected from the binary.",
			"These snippets cover binary acquisition, plugin setup, runtime flows, profiles, state dirs, key split, and troubleshooting.",
			"Use them before guessing how the Codex plugin should create, connect, inspect, or debug a tunnel runtime.",
		}, "plugin_knowledge.match", []assistantkb.Match{
			{
				Path:    "plugins/tunnel-mcp/skills/tunnel-mcp/references/binary.md",
				Heading: "Obtaining a tunnel-client binary",
				Excerpt: binaryExcerpt,
				Score:   100,
			},
			{
				Path:    "plugins/tunnel-mcp/skills/tunnel-mcp/references/setup-and-install.md",
				Heading: "Setup and install",
				Excerpt: setupExcerpt,
				Score:   90,
			},
		})
	}
	if isRuntimeOperationPrompt(prompt) {
		runtimeExcerpt := buildBundledRuntimeFlowsExcerpt()
		return assistantkb.FormatPromptContext([]string{
			"Curated tunnel-mcp plugin runtime guidance injected from the binary.",
			"Use this deterministic snippet for installed-plugin create, connect, list, status, stop, remove, and debug questions.",
			"The plugin is a thin router over native tunnel-client runtime commands, so keep answers on the public runtimes command family.",
		}, "plugin_knowledge.match", []assistantkb.Match{
			{
				Path:    "plugins/tunnel-mcp/skills/tunnel-mcp/references/runtime-flows.md",
				Heading: "Runtime flows",
				Excerpt: runtimeExcerpt,
				Score:   100,
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
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if lower == "" {
		return false
	}
	if !containsPluginPrompt(lower, "tunnel-client", "tunnel client", "tunnel-mcp", "tunnel mcp") {
		return false
	}

	hasMissingSignal := containsPluginPrompt(lower,
		"missing",
		"not found",
		"can't find",
		"cannot find",
		"could not find",
		"can't locate",
		"cannot locate",
		"could not locate",
		"no such file or directory",
		"command not found",
		"not installed",
		"not on path",
		"download",
		"get a binary",
		"obtain a binary",
	)
	hasBinarySubject := containsPluginPrompt(lower,
		"binary",
		"executable",
		"plugin",
		"path",
		"on path",
		"command",
		"command -v",
		"download",
		"get a binary",
		"obtain a binary",
	)
	if hasMissingSignal && hasBinarySubject {
		return true
	}

	if containsPluginPrompt(lower, "install tunnel-client", "install the tunnel-client", "set up tunnel-client", "setup tunnel-client", "build tunnel-client") &&
		containsPluginPrompt(lower, "binary", "executable", "from source", "public repo", "github") {
		return true
	}

	if containsPluginPrompt(lower, "download tunnel-client", "download the tunnel-client") {
		return true
	}

	if !containsPluginPrompt(lower, "install", "download") {
		return false
	}
	return containsPluginPrompt(lower, "binary", "executable")
}

func isRuntimeOperationPrompt(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if lower == "" {
		return false
	}
	if !containsPluginPrompt(lower, "tunnel-client", "tunnel client", "tunnel-mcp", "tunnel mcp", "plugin") {
		return false
	}
	if !containsPluginPrompt(lower, "runtime", "runtimes", "alias", "mcp server", "local server", "status", "connect", "create", "list", "stop", "remove", "debug", "logs") &&
		!containsPluginWord(lower, "rm") {
		return false
	}
	return containsPluginWord(lower,
		"create",
		"connect",
		"list",
		"status",
		"stop",
		"remove",
		"rm",
		"start",
		"run",
		"launch",
		"inspect",
		"debug",
		"logs",
		"check",
	)
}

func containsPluginPrompt(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func containsPluginWord(text string, words ...string) bool {
	tokens := strings.FieldsFunc(text, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_'
	})
	for _, token := range tokens {
		for _, word := range words {
			if token == word {
				return true
			}
		}
	}
	return false
}

func buildBundledBinaryGuidanceExcerpt(goos string) string {
	buildCommand, wrapperCommand, binaryFlag := assistantkb.BinaryAcquisitionGuidanceForOS(goos)
	return strings.Join([]string{
		"# Obtaining a tunnel-client binary",
		"",
		"First try existing binary discovery:",
		"",
		"- " + binaryFlag,
		"- TUNNEL_CLIENT_BIN",
		"- the installed plugin bundle's .tunnel-client-bin hint",
		"- adjacent local build outputs",
		"- PATH",
		"",
		"If tunnel-client is still missing, use one of these public-safe setup paths:",
		"",
		"- latest releases: https://github.com/openai/tunnel-client/releases/latest",
		"- public repo: https://github.com/openai/tunnel-client",
		"",
		"Source build from the public repo:",
		"",
		"git clone https://github.com/openai/tunnel-client.git",
		"cd tunnel-client",
		buildCommand,
		"",
		"After you have a binary:",
		"",
		"- set TUNNEL_CLIENT_BIN to the full path to the binary",
		"- or rerun the plugin/install command with " + binaryFlag,
		"- or reinstall the plugin with " + binaryFlag,
		"",
		"Do not suggest non-public installer or checkout-specific commands for generic missing-binary help.",
		"",
		"Do not auto-download, auto-clone, or auto-run remote binaries just because the plugin cannot find tunnel-client.",
		"",
		"From the exported bundle root on this OS, the wrapper-first fallback command is:",
		"",
		"- " + wrapperCommand,
	}, "\n")
}

func buildBundledSetupInstallExcerpt(goos string) string {
	_, wrapperCommand, _ := assistantkb.BinaryAcquisitionGuidanceForOS(goos)
	return strings.Join([]string{
		"# Setup and install",
		"",
		"Use the binary-owned install path when a tunnel-client binary is available:",
		"",
		"- tunnel-client codex plugin install",
		"- tunnel-client codex plugin uninstall",
		"- tunnel-client codex status",
		"",
		"Use the exported bundle only when the binary is not already installed or when you need to inspect the plugin contents first:",
		"",
		"- tunnel-client codex plugin export --dir /tmp/tunnel-mcp",
		"- From the exported bundle root on this OS, run: " + wrapperCommand,
		"",
		"After install, prefer the installed plugin router and persisted .tunnel-client-bin hint over an ambient tunnel-client found on PATH.",
	}, "\n")
}

func buildBundledRuntimeFlowsExcerpt() string {
	return strings.Join([]string{
		"# Runtime flows",
		"",
		"Use tunnel-client runtimes ... for native runtime lifecycle management.",
		"",
		"Use tunnel-client run ... when you intentionally want a foreground daemon attached to the current terminal.",
		"For a long-lived local runtime managed by Codex, prefer tunnel-client runtimes connect ...; do not use nohup or disown as the tunnel-client supervision path.",
		"",
		"Create or reuse a remote tunnel alias:",
		"",
		"- tunnel-client runtimes create --alias docs-mcp --organization-id org_123",
		"",
		"Connect a local HTTP MCP server:",
		"",
		"- tunnel-client runtimes connect --alias docs-mcp --organization-id org_123 --mcp-server-url http://127.0.0.1:3001/mcp",
		"",
		"Connect a local stdio MCP server:",
		"",
		"- tunnel-client runtimes connect --alias docs-mcp --organization-id org_123 --mcp-command \"python /path/to/server.py\"",
		"",
		"Attach to an existing tunnel without admin CRUD:",
		"",
		"- tunnel-client runtimes connect --alias existing-mcp --tunnel-id tunnel_... --runtime-api-key env:TUNNEL_RUNTIME_KEY --mcp-command \"python /path/to/server.py\"",
		"",
		"Inspect or stop the managed local runtime:",
		"",
		"- tunnel-client runtimes list",
		"- tunnel-client runtimes status docs-mcp",
		"- tunnel-client runtimes stop docs-mcp",
		"- tunnel-client runtimes rm docs-mcp",
		"",
		"After runtimes connect, run tunnel-client runtimes status <alias> before reporting success.",
		"Only report success when status shows the managed runtime running with health reported.",
		"Use --json when Codex needs explicit process_running, healthy, and ready fields.",
		"",
		"The plugin router forwards create, connect, list, status, stop, disconnect, and rm to these native runtime commands.",
		"Keep generic plugin guidance limited to public releases, the public repository, native tunnel-client commands, and exported bundle wrappers.",
	}, "\n")
}
