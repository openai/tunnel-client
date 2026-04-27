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
		"tunnel-client admin-profiles list",
	} {
		require.Contains(t, plugin, snippet)
	}

	quickstart, ok := loadHelpTopic("quickstart")
	require.True(t, ok)
	require.Contains(t, quickstart, "tunnel-client runtimes list")
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
