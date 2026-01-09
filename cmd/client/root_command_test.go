package main

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRootCommandWithNoArgsPrintsHelp(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	root := newRootCommand(func(string) (string, bool) { return "", false }, &stdout, io.Discard)

	root.SetArgs([]string{})

	require.NoError(t, root.Execute())
	output := stdout.String()
	require.Contains(t, output, "Commands:")
	require.Contains(t, output, "run")
}

func TestRootHelpListsSubcommands(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	root := newRootCommand(func(string) (string, bool) { return "", false }, &stdout, io.Discard)

	root.SetArgs([]string{"--help"})

	require.NoError(t, root.Execute())
	output := stdout.String()
	require.Contains(t, output, "Commands:")
	require.Contains(t, output, "run")
	require.NotContains(t, output, "control-plane.base-url")
}
