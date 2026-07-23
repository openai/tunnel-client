package tunnelclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	tunnelclient "github.com/openai/tunnel-client"
	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

const (
	testTunnelID = "tunnel_sdktestaaaaaaaaaaaaaaaaaaaaaaaaa"
	testAPIKey   = "sdk-test-api-key"
)

func TestNewValidatesRequiredInputs(t *testing.T) {
	_, clientTransport := mcp.NewInMemoryTransports()

	for _, tc := range []struct {
		name      string
		cfg       tunnelclient.Config
		transport mcp.Transport
		wantErr   string
	}{
		{
			name:    "transport",
			cfg:     tunnelclient.Config{TunnelID: testTunnelID, APIKey: testAPIKey},
			wantErr: "MCP transport is required",
		},
		{
			name:      "tunnel id",
			cfg:       tunnelclient.Config{APIKey: testAPIKey},
			transport: clientTransport,
			wantErr:   "tunnel ID is required",
		},
		{
			name:      "api key",
			cfg:       tunnelclient.Config{TunnelID: testTunnelID},
			transport: clientTransport,
			wantErr:   "API key is required",
		},
		{
			name:      "base url",
			cfg:       tunnelclient.Config{TunnelID: testTunnelID, APIKey: testAPIKey, ControlPlaneBaseURL: "://bad"},
			transport: clientTransport,
			wantErr:   "invalid control-plane base URL",
		},
		{
			name: "reserved control plane header",
			cfg: tunnelclient.Config{
				TunnelID:                 testTunnelID,
				APIKey:                   testAPIKey,
				ControlPlaneExtraHeaders: map[string]string{"Authorization": "Bearer override"},
			},
			transport: clientTransport,
			wantErr:   "cannot override authentication or client metadata",
		},
		{
			name: "max in-flight requests",
			cfg: tunnelclient.Config{
				TunnelID:            testTunnelID,
				APIKey:              testAPIKey,
				MaxInFlightRequests: 10001,
			},
			transport: clientTransport,
			wantErr:   "max in-flight requests must be less than or equal to 10000",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tunnelclient.New(tc.cfg, tc.transport)
			require.Error(t, err)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestClientForwardsMCPToolCallsOverInMemoryTransport(t *testing.T) {
	const (
		toolRequestID = "sdk-tool-request"
		callID        = "sdk-tool-call"
	)

	toolCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			toolRequestID,
			json.RawMessage("{\"jsonrpc\":\"2.0\",\"id\":\""+callID+"\",\"method\":\"tools/call\",\"params\":{\"name\":\"echo\",\"arguments\":{\"message\":\"hello from sdk\"}}}"),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: toolRequestID,
			Assert: func(tb testing.TB, response mocktunnelservice.ReceivedResponse) {
				tb.Helper()
				require.Equal(tb, http.StatusOK, response.ResponseCode)
				require.Equal(tb, string(wiretypes.ResponsePayloadJSONRPC), response.ResponseType)
				var payload struct {
					Result struct {
						StructuredContent struct {
							Message string `json:"message"`
						} `json:"structuredContent"`
					} `json:"result"`
				}
				require.NoError(tb, json.Unmarshal(response.JSONResponse, &payload))
				require.Equal(tb, "Echo: hello from sdk", payload.Result.StructuredContent.Message)
			},
		}},
	}
	controlPlane := mocktunnelservice.NewMockTunnelService(
		mocktunnelservice.WithAPIKey(testAPIKey),
		mocktunnelservice.WithTunnelID(testTunnelID),
		mocktunnelservice.WithInitializationPhaseCommandsWithoutSessionHeaders(),
		mocktunnelservice.WithCommandResponses(toolCommand),
	)
	controlPlane.Start(t)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server := mcp.NewServer(
		&mcp.Implementation{Name: "sdk-test-server", Version: "1.0.0"},
		&mcp.ServerOptions{
			// This test's scripted control plane validates the initialize,
			// initialized, and tool-call responses. Suppress unrelated tool-list
			// notifications so response arrival order is not scheduler-dependent
			// under the race detector.
			Capabilities: &mcp.ServerCapabilities{
				Tools: &mcp.ToolCapabilities{ListChanged: false},
			},
		},
	)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echo a message"}, func(
		_ context.Context,
		_ *mcp.CallToolRequest,
		args map[string]any,
	) (*mcp.CallToolResult, map[string]string, error) {
		message := fmt.Sprintf("Echo: %s", args["message"])
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: message}},
		}, map[string]string{"message": message}, nil
	})

	serverCtx, cancelServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Run(serverCtx, serverTransport)
	}()

	client, err := tunnelclient.New(tunnelclient.Config{
		TunnelID:            testTunnelID,
		APIKey:              testAPIKey,
		ControlPlaneBaseURL: controlPlane.BaseURL().String(),
		PollTimeout:         10 * time.Millisecond,
		LogWriter:           io.Discard,
	}, clientTransport)
	require.NoError(t, err)
	startCtx, cancelStart := context.WithTimeout(context.Background(), time.Second)
	defer cancelStart()
	require.NoError(t, client.Start(startCtx))

	readyCtx, cancelReady := context.WithTimeout(context.Background(), time.Second)
	defer cancelReady()
	require.NoError(t, client.WaitUntilReady(readyCtx))

	idleCtx, cancelIdle := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelIdle()
	require.NoError(t, controlPlane.WaitUntilIdle(idleCtx))

	canceledStopCtx, cancelCanceledStop := context.WithCancel(context.Background())
	cancelCanceledStop()
	require.ErrorContains(t, client.Stop(canceledStopCtx), "context canceled")

	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	require.NoError(t, client.Stop(stopCtx))
	require.ErrorIs(t, client.Start(context.Background()), tunnelclient.ErrClosed)

	cancelServer()
	select {
	case err := <-serverDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("MCP server stopped with error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("MCP server did not stop")
	}

	matched := controlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	require.Len(t, matched, 3)
}
