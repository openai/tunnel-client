package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHelpTopicsUseRuntimesCommandSurface(t *testing.T) {
	t.Parallel()

	for _, topic := range availableHelpTopics() {
		body, ok := loadHelpTopic(topic)
		require.True(t, ok, "load help topic %s", topic)
		require.NotContains(t, body, "tunnel-client sessions", "help topic %s", topic)
	}

	plugin, ok := loadHelpTopic("plugin")
	require.True(t, ok)
	for _, snippet := range []string{
		"tunnel-client runtimes create ...",
		"tunnel-client runtimes connect ...",
		"tunnel-client runtimes list",
		"tunnel-client runtimes status <alias>",
		"do not use `nohup` or `disown` as the tunnel-client supervision path",
		"after connect, run `tunnel-client runtimes status <alias>` before reporting success",
		"tunnel-client admin-profiles list",
	} {
		require.Contains(t, plugin, snippet)
	}

	quickstart, ok := loadHelpTopic("quickstart")
	require.True(t, ok)
	require.Contains(t, quickstart, "tunnel-client runtimes list")
	require.Contains(t, quickstart, "Use `tunnel-client run ...` when you intentionally want a foreground daemon")
	require.Contains(t, quickstart, "For a long-lived local runtime managed by Codex")
	require.Contains(t, quickstart, "Do not use `nohup` or `disown` as the tunnel-client supervision path.")
	require.Contains(t, quickstart, "After `runtimes connect`, check `tunnel-client runtimes status <alias>`")
	require.Contains(t, quickstart, "Only report success when status shows the managed")
	require.Contains(t, quickstart, "tunnel-client help troubleshooting")
}

func TestRootHelpDoesNotExposeLegacySessionsCommand(t *testing.T) {
	t.Parallel()

	var stdout strings.Builder
	root := newRootCommand(func(string) (string, bool) { return "", false }, &stdout, &strings.Builder{})
	root.SetArgs([]string{"--help"})

	require.NoError(t, root.Execute())
	require.Contains(t, stdout.String(), "runtimes")
	require.NotContains(t, stdout.String(), "sessions")
}

func TestRootHelpCoversMCPStaticHeaders(t *testing.T) {
	t.Parallel()

	var stdout strings.Builder
	root := newRootCommand(func(string) (string, bool) { return "", false }, &stdout, &strings.Builder{})
	root.SetArgs([]string{"run", "--help"})

	require.NoError(t, root.Execute())
	require.Contains(t, stdout.String(), "--mcp.extra-headers")
	require.Contains(t, stdout.String(), "MCP_EXTRA_HEADERS")
	require.Contains(t, stdout.String(), "--mcp.discovery-extra-headers")
	require.Contains(t, stdout.String(), "MCP_DISCOVERY_EXTRA_HEADERS")
	require.Contains(t, stdout.String(), "values accept env:VAR or file:/path")
	require.Contains(t, stdout.String(), "--control-plane.extra-headers")
	require.Contains(t, stdout.String(), "CONTROL_PLANE_EXTRA_HEADERS")
}

func TestTroubleshootingHelpTopicIsDiscoverable(t *testing.T) {
	t.Parallel()

	require.Contains(t, availableHelpTopics(), "troubleshooting")

	var stdout strings.Builder
	root := newRootCommand(func(string) (string, bool) { return "", false }, &stdout, &strings.Builder{})
	root.SetArgs([]string{"--help"})

	require.NoError(t, root.Execute())
	require.Contains(t, stdout.String(), "tunnel-client help troubleshooting")
}

func TestTroubleshootingHelpTopicCoversRuntimeDebugging(t *testing.T) {
	t.Parallel()

	body, ok := loadHelpTopic("troubleshooting")
	require.True(t, ok)
	for _, snippet := range []string{
		"tunnel-client health --url-file /tmp/tunnel-client-health.url",
		"curl -fsS http://127.0.0.1:8080/healthz",
		"/readyz includes startup gates for OAuth discovery and MCP probing.",
		"If /readyz is 200 with a ready detail:",
		"tunnel-client doctor --profile <name> --explain",
		"tunnel-client runtimes status <alias>",
		"curl -fsSJO \"http://127.0.0.1:8080/api/logs/export?minutes=30\"",
		"docs/troubleshooting.md",
	} {
		require.Contains(t, body, snippet)
	}
}
