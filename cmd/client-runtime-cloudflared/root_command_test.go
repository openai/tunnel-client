package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/runtimecli"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/version"
)

func TestRuntimeCloudflaredRootCommandExposesOnlyRuntimeSurface(t *testing.T) {
	setLinkedRuntimeCloudflaredFlavor(t, version.FlavorRuntimeCloudflared)

	stdout, err := executeRuntimeCloudflaredCommand(t, nil, "--help")
	require.NoError(t, err)
	require.Contains(t, stdout, "tunnel-client-runtime-cloudflared")
	require.Contains(t, stdout, "run")
	require.NotContains(t, stdout, "\n  help ")
	for _, excluded := range []string{
		"admin",
		"codex",
		"dev",
		"doctor",
		"health",
		"init",
		"plugin",
		"profiles",
		"runtimes",
	} {
		require.NotContains(t, stdout, excluded)
	}

	stdout, err = executeRuntimeCloudflaredCommand(t, nil, "--version")
	require.NoError(t, err)
	require.Contains(t, stdout, "flavor=runtime-cloudflared")
}

func TestRuntimeCloudflaredRootCommandDisablesImplicitHelpSubcommand(t *testing.T) {
	setLinkedRuntimeCloudflaredFlavor(t, version.FlavorRuntimeCloudflared)

	root := newRootCommand(func(string) (string, bool) { return "", false }, io.Discard, io.Discard)
	root.InitDefaultHelpCmd()

	var visibleCommands []string
	for _, command := range root.Commands() {
		if command.IsAvailableCommand() {
			visibleCommands = append(visibleCommands, command.Name())
		}
	}
	require.Equal(t, []string{"run"}, visibleCommands)

	_, err := executeRuntimeCloudflaredCommand(t, nil, "help")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown command \"help\"")
}

func TestRuntimeCloudflaredRunHelpAddsOnlyCompanionFlags(t *testing.T) {
	setLinkedRuntimeCloudflaredFlavor(t, version.FlavorRuntimeCloudflared)

	stdout, err := executeRuntimeCloudflaredCommand(t, nil, "run", "--help")
	require.NoError(t, err)
	for _, included := range []string{
		"--control-plane.tunnel-id",
		"--mcp.server-url",
		"--cloudflared.managed",
		"--cloudflared.path",
		"--cloudflared.ready-timeout",
	} {
		require.Contains(t, stdout, included)
	}
	for _, excluded := range []string{
		"--allow-remote-ui",
		"--open-web-ui",
		"--admin-ui.log-buffer-events",
		"--harpoon.capture-payloads",
	} {
		require.NotContains(t, stdout, excluded)
	}
}

func TestRuntimeCloudflaredRunCommandMatchesCanonicalCobraFlagSurface(t *testing.T) {
	root := newRootCommand(func(string) (string, bool) { return "", false }, io.Discard, io.Discard)
	run, _, err := root.Find([]string{"run"})
	require.NoError(t, err)
	run.InitDefaultHelpFlag()

	expected := &cobra.Command{Use: "run"}
	runtimeconfig.RegisterFlags(expected.Flags(), runtimeconfig.FlavorRuntimeCloudflared)
	expected.InitDefaultHelpFlag()

	require.Equal(t, expected.Flags().FlagUsages(), run.Flags().FlagUsages())
}

func TestRuntimeCloudflaredRejectsUIFlagAndProfileKey(t *testing.T) {
	setLinkedRuntimeCloudflaredFlavor(t, version.FlavorRuntimeCloudflared)

	_, err := executeRuntimeCloudflaredCommand(t, nil, "run", "--open-web-ui")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown flag: --open-web-ui")

	profilePath := filepath.Join(t.TempDir(), "runtime-cloudflared-profile.yaml")
	profile := strings.Join([]string{
		"config_version: 1",
		"control_plane:",
		"  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"  api_key: env:CONTROL_PLANE_API_KEY",
		"mcp:",
		"  server_urls:",
		"    - url: https://mcp.example.invalid/mcp",
		"admin_ui:",
		"  log_buffer_events: 1",
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(profilePath, []byte(profile), 0o600))

	_, err = executeRuntimeCloudflaredCommand(t, map[string]string{
		"CONTROL_PLANE_API_KEY": "sk_test_key",
	}, "run", "--profile-file", profilePath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "log_buffer_events")
}

func TestRuntimeCloudflaredVersionRejectsMismatchedLinkedFlavor(t *testing.T) {
	setLinkedRuntimeCloudflaredFlavor(t, version.FlavorFull)

	output := runtimecli.Version(version.FlavorRuntimeCloudflared)
	require.Contains(t, output, "invalid runtime build metadata")
	require.Contains(t, output, "linked-flavor=full")
	require.NotContains(t, output, "flavor=runtime-cloudflared")
}

func setLinkedRuntimeCloudflaredFlavor(t *testing.T, flavor string) {
	t.Helper()
	originalFlavor := version.Flavor
	version.Flavor = flavor
	t.Cleanup(func() { version.Flavor = originalFlavor })
}

func executeRuntimeCloudflaredCommand(t *testing.T, env map[string]string, args ...string) (string, error) {
	t.Helper()
	var stdout bytes.Buffer
	root := newRootCommand(func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	}, &stdout, io.Discard)
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), err
}
