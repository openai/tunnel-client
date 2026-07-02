package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"go.openai.org/api/tunnel-client/pkg/config"
)

func TestRootCommandIncludesRun(t *testing.T) {
	t.Parallel()

	root := newRootCommand(func(string) (string, bool) { return "", false }, io.Discard, io.Discard)

	run, _, err := root.Find([]string{"run"})
	require.NoError(t, err)
	require.Equal(t, "run", run.Name())
	require.NotNil(t, run.Flags().Lookup("control-plane.base-url"))
}

func TestRunHelpIsScoped(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	root := newRootCommand(func(string) (string, bool) { return "", false }, &stdout, io.Discard)

	root.SetArgs([]string{"run", "--help"})

	require.NoError(t, root.Execute())
	output := stdout.String()
	require.Contains(t, output, "control-plane.base-url")
	require.Contains(t, output, "embedded-mcp-stub")
	require.NotContains(t, output, "Commands:")
}

func TestRunReportsTunnelIDBeforeMissingMCPBinding(t *testing.T) {
	t.Parallel()

	root := newRootCommand(func(key string) (string, bool) {
		switch key {
		case "LOG_FORMAT":
			return "struct-text", true
		case "OPENAI_API_KEY":
			return "dummy-key", true
		default:
			return "", false
		}
	}, io.Discard, io.Discard)

	root.SetArgs([]string{"run"})

	err := root.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "tunnel ID is required")
	require.Contains(t, err.Error(), "tunnel-client admin tunnels create --help")
	require.Contains(t, err.Error(), "tunnel-client init")
	require.Contains(t, err.Error(), "tunnel-client help quickstart")
}

func TestRunReportsHowToConfigureMainMCPChannel(t *testing.T) {
	t.Parallel()

	root := newRootCommand(func(key string) (string, bool) {
		switch key {
		case "LOG_FORMAT":
			return "struct-text", true
		case "OPENAI_API_KEY":
			return "dummy-key", true
		case "CONTROL_PLANE_TUNNEL_ID":
			return "tunnel_0123456789abcdef0123456789abcdef", true
		default:
			return "", false
		}
	}, io.Discard, io.Discard)

	root.SetArgs([]string{"run"})

	err := root.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "set --mcp.server-url or --mcp.command")
	require.Contains(t, err.Error(), "tunnel-client run --embedded-mcp-stub")
	require.Contains(t, err.Error(), "--health.listen-addr 127.0.0.1:0")
	require.Contains(t, err.Error(), "--health.url-file /tmp/tunnel-client-health.url")
	require.Contains(t, err.Error(), "tunnel-client init")
}

func TestRunEmbeddedMCPStubConfiguresMainChannel(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	run := newRunCommand(func(string) (string, bool) { return "", false })
	run.SetOut(&out)
	stub, err := configureRunEmbeddedMCPStub(run, runEmbeddedMCPStubOptions{
		Enabled: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, stub.Shutdown(context.TODO()))
	})

	cfg, err := config.LoadFromFlagSet(run.Flags(), func(key string) (string, bool) {
		switch key {
		case "OPENAI_API_KEY":
			return "dummy-key", true
		case "CONTROL_PLANE_TUNNEL_ID":
			return "tunnel_0123456789abcdef0123456789abcdef", true
		default:
			return "", false
		}
	})
	require.NoError(t, err)
	binding := cfg.MCP.MainChannelBinding()
	require.NotNil(t, binding)
	require.NotNil(t, binding.ServerURL)
	require.Equal(t, stub.MCPURL(), binding.ServerURL.String())
	require.Contains(t, out.String(), "These are the embedded demo MCP/OAuth endpoints")

	resp, err := http.Get(stub.ProtectedResourceMetadataURL())
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestRunEmbeddedMCPStubRejectsExplicitMainMCPFlags(t *testing.T) {
	t.Parallel()

	run := newRunCommand(func(string) (string, bool) { return "", false })
	require.NoError(t, run.Flags().Set("mcp.command", "command=python,channel=main"))

	_, err := configureRunEmbeddedMCPStub(run, runEmbeddedMCPStubOptions{
		Enabled: true,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--embedded-mcp-stub cannot be combined with --mcp.command")
}
