package main

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCloudflaredVersionCommandSurfacesPin(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	root := newRootCommand(func(string) (string, bool) { return "", false }, &stdout, io.Discard)
	root.SetArgs([]string{"cloudflared", "version"})

	require.NoError(t, root.Execute())
	require.Contains(t, stdout.String(), "bundled cloudflared 2026.7.2")
	require.Contains(t, stdout.String(), "module: github.com/cloudflare/cloudflared@v0.0.0-20260715110107-8679787525ed")
	require.Contains(t, stdout.String(), "release commit: 8679787525edc8575b2948a7c4a50b6292c6d426")
	require.Contains(t, stdout.String(), "security patch owner: tunnel-client maintainers")
}

func TestCloudflaredConfigCommandPrintsTokenFreeProductionConfig(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	root := newRootCommand(func(string) (string, bool) { return "", false }, &stdout, io.Discard)
	root.SetArgs([]string{
		"cloudflared",
		"config",
		"--token-file", "/run/secrets/cloudflared/token",
	})

	require.NoError(t, root.Execute())
	require.Contains(t, stdout.String(), `token-file: "/run/secrets/cloudflared/token"`)
	require.Contains(t, stdout.String(), "no-autoupdate: true")
	require.Contains(t, stdout.String(), `metrics: "127.0.0.1:20241"`)
	require.Contains(t, stdout.String(), `loglevel: "info"`)
	require.Contains(t, stdout.String(), `transport-loglevel: "warn"`)
	require.Contains(t, stdout.String(), `protocol: "auto"`)
	require.Contains(t, stdout.String(), `edge-ip-version: "auto"`)
	require.Contains(t, stdout.String(), "retries: 5")
	require.Contains(t, stdout.String(), `grace-period: "30s"`)
	require.NotContains(t, stdout.String(), "TUNNEL_TOKEN=")
}

func TestCloudflaredConfigCommandRejectsMissingTokenFile(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	root := newRootCommand(func(string) (string, bool) { return "", false }, &stdout, io.Discard)
	root.SetArgs([]string{"cloudflared", "config"})

	err := root.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "--token-file is required")
	require.Empty(t, stdout.String())
}

func TestCloudflaredConfigCommandSupportsProductionOverrides(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	root := newRootCommand(func(string) (string, bool) { return "", false }, &stdout, io.Discard)
	root.SetArgs([]string{
		"cloudflared",
		"config",
		"--token-file", "/run/secrets/cloudflared/token",
		"--metrics-address", "0.0.0.0:32123",
		"--log-level", "warn",
		"--transport-log-level", "error",
		"--protocol", "http2",
		"--edge-ip-version", "4",
		"--retries", "0",
		"--grace-period", "45s",
	})

	require.NoError(t, root.Execute())
	require.Contains(t, stdout.String(), `metrics: "0.0.0.0:32123"`)
	require.Contains(t, stdout.String(), `loglevel: "warn"`)
	require.Contains(t, stdout.String(), `transport-loglevel: "error"`)
	require.Contains(t, stdout.String(), `protocol: "http2"`)
	require.Contains(t, stdout.String(), `edge-ip-version: "4"`)
	require.Contains(t, stdout.String(), "retries: 0")
	require.Contains(t, stdout.String(), `grace-period: "45s"`)
}
