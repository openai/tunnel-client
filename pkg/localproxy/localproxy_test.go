package localproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/types"
	"github.com/openai/tunnel-client/testsupport/mockmcpserver"
)

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func TestStartFrontsHTTPMCPServerWithoutHealthListener(t *testing.T) {
	mcpServer := mockmcpserver.NewMockMCPServer(
		mockmcpserver.WithToolListChangedNotificationsDisabled(),
		mockmcpserver.WithCalls(mockmcpserver.Call{
			Tool: "echo",
			DynamicResult: func(arguments json.RawMessage) (json.RawMessage, error) {
				var payload struct {
					Name string `json:"name"`
				}
				require.NoError(t, json.Unmarshal(arguments, &payload))
				return json.RawMessage(fmt.Sprintf(`{"message":"hi, %s!"}`, payload.Name)), nil
			},
		}),
	)
	mcpServer.Start(t)

	var stderr lockedBuffer
	proxy, err := Start(context.Background(), Options{
		MCPServerURLs: []string{mcpServer.BaseURL().String()},
		Stderr:        &stderr,
	})
	require.NoError(t, err, stderr.String())
	t.Cleanup(func() {
		require.NoError(t, proxy.Stop(context.Background()))
	})
	info := proxy.Info()
	require.Empty(t, info.HealthURL)
	require.Equal(t, "go-in-memory", info.Backend)
	require.Equal(t, "tcp", info.MCPTransport)
	require.NotEmpty(t, info.MCPURL)
	require.Empty(t, info.MCPUnixSocket)
	require.Empty(t, info.MCPURLPath)
	if runtime.GOOS == "windows" {
		require.Equal(t, "tcp", info.ControlPlaneTransport)
		require.Empty(t, info.ControlPlaneUnixSocket)
	} else {
		require.Equal(t, "unix", info.ControlPlaneTransport)
		require.NotEmpty(t, info.ControlPlaneUnixSocket)
	}

	sessionID := postInitialize(t, info.MCPURL, nil)
	postInitializedNotification(t, info.MCPURL, sessionID)
	response := postToolCall(t, info.MCPURL, sessionID, nil)
	require.Equal(t, "hi, Ada!", response.Result.StructuredContent["message"])

	recorded := mcpServer.ReceivedRequests()
	require.Len(t, recorded, 1)
	require.Equal(t, "echo", recorded[0].Tool)
}

func TestStartFrontsHTTPMCPServerOverUnixIngress(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets are unavailable on Windows")
	}
	mcpServer := mockmcpserver.NewMockMCPServer(
		mockmcpserver.WithToolListChangedNotificationsDisabled(),
		mockmcpserver.WithCalls(mockmcpserver.Call{
			Tool:   "echo",
			Result: json.RawMessage(`{"message":"unix ingress"}`),
		}),
	)
	mcpServer.Start(t)

	socketPath := shortUnixSocketPath(t)
	proxy, err := Start(context.Background(), Options{
		ListenUnixSocket: socketPath,
		MCPServerURLs:    []string{mcpServer.BaseURL().String()},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, proxy.Stop(context.Background()))
	})

	info := proxy.Info()
	require.Equal(t, "go-in-memory", info.Backend)
	require.Equal(t, "unix", info.MCPTransport)
	require.Empty(t, info.MCPURL)
	require.Equal(t, socketPath, info.MCPUnixSocket)
	require.Equal(t, "/v1/mcp/"+info.TunnelID, info.MCPURLPath)

	client := unixHTTPClient(socketPath)
	mcpURL := "http://local-proxy" + info.MCPURLPath
	sessionID := postInitializeWithClient(t, client, mcpURL, nil)
	postInitializedNotificationWithClient(t, client, mcpURL, sessionID)
	response := postToolCallWithClient(t, client, mcpURL, sessionID, nil)
	require.Equal(t, "unix ingress", response.Result.StructuredContent["message"])

	require.NoError(t, proxy.Stop(context.Background()))
	_, err = os.Stat(socketPath)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestStartRejectsOccupiedUnixIngressPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets are unavailable on Windows")
	}
	mcpServer := mockmcpserver.NewMockMCPServer(mockmcpserver.WithToolListChangedNotificationsDisabled())
	mcpServer.Start(t)

	socketPath := shortUnixSocketPath(t)
	require.NoError(t, os.WriteFile(socketPath, []byte("occupied"), 0o600))

	_, err := Start(context.Background(), Options{
		ListenUnixSocket: socketPath,
		MCPServerURLs:    []string{mcpServer.BaseURL().String()},
	})
	require.ErrorContains(t, err, "listen on external MCP unix socket")
}

func TestStartFrontsStdioMCPServer(t *testing.T) {
	invocationLog := t.TempDir() + "/stdio-invocations.log"
	t.Setenv("MOCK_MCP_INVOCATION_LOG", invocationLog)

	proxy, err := Start(context.Background(), Options{
		MCPCommands: []string{strings.Join(mockmcpserver.StdioServerCommand(t), " ")},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, proxy.Stop(context.Background()))
	})

	sessionID := postInitializeOptionalSession(t, proxy.Info().MCPURL, nil)
	postInitializedNotification(t, proxy.Info().MCPURL, sessionID)
	response := postToolCall(t, proxy.Info().MCPURL, sessionID, nil)
	require.Equal(t, "hello Ada", response.Result.StructuredContent["message"])

	data, err := os.ReadFile(invocationLog)
	require.NoError(t, err)
	require.NotEmpty(t, data)
}

func TestStartSupportsChannelRouteAndHeaderFiltering(t *testing.T) {
	mcpServer := mockmcpserver.NewMockMCPServer(
		mockmcpserver.WithToolListChangedNotificationsDisabled(),
		mockmcpserver.WithCalls(mockmcpserver.Call{
			Tool:   "echo",
			Result: json.RawMessage(`{"message":"tools channel"}`),
		}),
	)
	mcpServer.Start(t)

	proxy, err := Start(context.Background(), Options{
		MCPServerURLs: []string{
			mcpServer.BaseURL().String(),
			"url=" + mcpServer.BaseURL().String() + ",channel=tools",
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, proxy.Stop(context.Background()))
	})

	sessionID := postInitialize(t, proxy.Info().MCPURL, nil)
	postInitializedNotification(t, proxy.Info().MCPURL, sessionID)

	headers := http.Header{
		"Mcp-Session-Id": {sessionID},
		"Authorization":  {"Bearer connector-user-token"},
		"Cookie":         {"session=blocked"},
	}
	channelURL := strings.TrimRight(proxy.Info().MCPURL, "/") + "/tools"
	response := postToolCall(t, channelURL, sessionID, headers)
	require.Equal(t, "tools channel", response.Result.StructuredContent["message"])

	recorded := mcpServer.ReceivedRequests()
	require.Len(t, recorded, 1)
	require.Equal(t, "Bearer connector-user-token", recorded[0].Headers.Get("Authorization"))
	require.Empty(t, recorded[0].Headers.Get("Cookie"))
}

func TestStartStreamsMultipleProgressNotificationsBeforeFinalResponse(t *testing.T) {
	mcpServer := mockmcpserver.NewMockMCPServer(
		mockmcpserver.WithToolListChangedNotificationsDisabled(),
		mockmcpserver.WithCalls(mockmcpserver.Call{
			Tool: "echo",
			Progress: []mockmcpserver.ProgressUpdate{
				{Percentage: 25, Message: "quarter"},
				{Percentage: 75, Message: "three quarters"},
			},
			Result: json.RawMessage(`{"message":"done"}`),
		}),
	)
	mcpServer.Start(t)

	proxy, err := Start(context.Background(), Options{
		MCPServerURLs: []string{mcpServer.BaseURL().String()},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, proxy.Stop(context.Background()))
	})

	sessionID := postInitialize(t, proxy.Info().MCPURL, nil)
	postInitializedNotification(t, proxy.Info().MCPURL, sessionID)
	received := postJSONRPCWithClient(t, http.DefaultClient, proxy.Info().MCPURL, sessionHeaders(sessionID), `{
		"jsonrpc":"2.0",
		"id":"tool-progress",
		"method":"tools/call",
		"params":{"name":"echo","arguments":{"name":"Ada"}}
	}`)

	require.Equal(t, "text/event-stream", received.headers.Get("Content-Type"))
	events := jsonRPCEventBodies(received.body)
	require.Len(t, events, 3, string(received.body))
	for _, event := range events[:2] {
		var notification struct {
			Method string `json:"method"`
		}
		require.NoError(t, json.Unmarshal(event, &notification), string(event))
		require.Equal(t, "notifications/progress", notification.Method)
	}
	var final toolCallResponse
	require.NoError(t, json.Unmarshal(events[2], &final), string(events[2]))
	require.Equal(t, "done", final.Result.StructuredContent["message"])
}

func TestHandleMCPBuffersProgressUntilFinalJSONForNonSSEClient(t *testing.T) {
	server := &localServer{
		tunnelID:        types.TunnelID("tunnel_jsonfallbackaaaaaaaaaaaaaaaaaa"),
		responseTimeout: time.Second,
		stateCh:         make(chan struct{}),
		inFlight:        make(map[string]*localRequest),
	}
	stateChanged := server.stateCh
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/mcp/"+server.tunnelID.String(),
		strings.NewReader(`{"jsonrpc":"2.0","id":"tool-json","method":"tools/call"}`),
	)
	request.Header.Set("Accept", "application/json")
	done := make(chan struct{})
	go func() {
		server.handleMCP(recorder, request)
		close(done)
	}()
	<-stateChanged

	server.mu.Lock()
	var pending *localRequest
	if len(server.pending) == 1 {
		pending = server.pending[0]
	}
	server.mu.Unlock()
	require.NotNil(t, pending)
	pending.responseCh <- localResponse{payload: wiretypes.TunnelResponsePayload{
		RequestID:    pending.id,
		JSONResponse: json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":50}}`),
		ResponseType: wiretypes.ResponsePayloadJSONRPCNotify,
	}}
	pending.responseCh <- localResponse{payload: wiretypes.TunnelResponsePayload{
		RequestID:    pending.id,
		JSONResponse: json.RawMessage(`{"jsonrpc":"2.0","id":"tool-json","result":{"structuredContent":{"message":"done"}}}`),
		ResponseCode: http.StatusOK,
		ResponseType: wiretypes.ResponsePayloadJSONRPC,
	}}
	<-done

	require.NotEqual(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	var final toolCallResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &final), recorder.Body.String())
	require.Equal(t, "done", final.Result.StructuredContent["message"])
}

func TestHandleResponsePreservesFinalWhenNotificationBufferIsFull(t *testing.T) {
	request := &localRequest{
		id:         "local-1",
		responseCh: make(chan localResponse, 1),
	}
	request.responseCh <- localResponse{payload: wiretypes.TunnelResponsePayload{
		RequestID:    request.id,
		JSONResponse: json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/progress"}`),
		ResponseType: wiretypes.ResponsePayloadJSONRPCNotify,
	}}
	server := &localServer{
		stateCh:  make(chan struct{}),
		inFlight: map[string]*localRequest{request.id: request},
	}
	final := wiretypes.TunnelResponsePayload{
		RequestID:    request.id,
		JSONResponse: json.RawMessage(`{"jsonrpc":"2.0","id":"tool-1","result":{"ok":true}}`),
		ResponseType: wiretypes.ResponsePayloadJSONRPC,
	}
	body, err := json.Marshal(final)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	server.handleResponse(
		recorder,
		httptest.NewRequest(http.MethodPost, "/v1/tunnels/local/response", bytes.NewReader(body)),
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	select {
	case response := <-request.responseCh:
		require.Equal(t, wiretypes.ResponsePayloadJSONRPC, response.payload.ResponseType)
	default:
		t.Fatal("terminal response was dropped")
	}
	require.Empty(t, server.inFlight)
}

func TestHandleTunnelRequiresExactBearerAuthorization(t *testing.T) {
	const tunnelID = "tunnel_authboundaryaaaaaaaaaaaaaaaaaa"
	server := &localServer{
		tunnelID: types.TunnelID(tunnelID),
		apiKey:   "local-secret",
	}

	tests := []struct {
		name          string
		authorization string
		wantStatus    int
	}{
		{name: "missing", wantStatus: http.StatusUnauthorized},
		{name: "wrong bearer", authorization: "Bearer wrong-secret", wantStatus: http.StatusUnauthorized},
		{name: "non bearer", authorization: "local-secret", wantStatus: http.StatusUnauthorized},
		{name: "extra token material", authorization: "Bearer local-secret extra", wantStatus: http.StatusUnauthorized},
		{name: "exact bearer", authorization: "Bearer local-secret", wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/v1/tunnels/"+tunnelID, nil)
			if tt.authorization != "" {
				request.Header.Set("Authorization", tt.authorization)
			}

			server.handleTunnel(recorder, request)

			require.Equal(t, tt.wantStatus, recorder.Code)
			if tt.wantStatus == http.StatusOK {
				require.JSONEq(t, `{
					"id":"`+tunnelID+`",
					"name":"local tunnel-client dev proxy",
					"description":"Pure-Go in-memory control plane for local MCP tests"
				}`, recorder.Body.String())
			} else {
				require.Contains(t, recorder.Body.String(), "unauthorized")
			}
		})
	}
}

func TestSanitizeForwardableRequestHeadersKeepsConnectorAuthorizationBoundary(t *testing.T) {
	input := http.Header{
		"Authorization":                {"Bearer connector-user-token"},
		"Mcp-Session-Id":               {"session-123"},
		"Mcp-Protocol-Version":         {"2025-06-18"},
		"X-OpenAI-Authorization":       {"Bearer service-token"},
		"X-OpenAI-Actor-Authorization": {"Bearer actor-token"},
		"X-OpenAI-Skip-Auth":           {"true"},
		"X-Tunnel-Traffic-Source":      {"spoofed"},
		"X-Forwarded-For":              {"203.0.113.10"},
		"Forwarded":                    {"for=203.0.113.10"},
		"Cookie":                       {"session=blocked"},
		"User-Agent":                   {"blocked-agent"},
		"Content-Type":                 {"application/json"},
		"Accept-Encoding":              {"gzip"},
		"X-Custom-Connector-Header":    {"kept"},
		"X-Empty-Connector-Header":     {""},
	}

	got := sanitizeForwardableRequestHeaders(input)

	require.Equal(t, "Bearer connector-user-token", got.Get("Authorization"))
	require.Equal(t, "session-123", got.Get("Mcp-Session-Id"))
	require.Equal(t, "2025-06-18", got.Get("Mcp-Protocol-Version"))
	require.Equal(t, "kept", got.Get("X-Custom-Connector-Header"))
	require.Empty(t, got.Values("X-Empty-Connector-Header"))
	for _, blocked := range []string{
		"X-OpenAI-Authorization",
		"X-OpenAI-Actor-Authorization",
		"X-OpenAI-Skip-Auth",
		"X-Tunnel-Traffic-Source",
		"X-Forwarded-For",
		"Forwarded",
		"Cookie",
		"User-Agent",
		"Content-Type",
		"Accept-Encoding",
	} {
		require.Empty(t, got.Values(blocked), "%s should not be forwarded to connector MCP servers", blocked)
	}
}

func TestHandleOAuthDiscoveryDoesNotForwardConnectorAuthorization(t *testing.T) {
	server := &localServer{
		tunnelID:        types.TunnelID("tunnel_oauthdiscoveryaaaaaaaaaaaaaaa"),
		responseTimeout: time.Second,
		stateCh:         make(chan struct{}),
		inFlight:        make(map[string]*localRequest),
	}
	stateChanged := server.stateCh
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/.well-known/oauth-protected-resource/v1/mcp/"+server.tunnelID.String(),
		nil,
	).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer connector-user-token")
	request.Header.Set("Cookie", "session=blocked")

	done := make(chan struct{})
	go func() {
		server.handleOAuthProtectedResource(recorder, request)
		close(done)
	}()
	<-stateChanged

	server.mu.Lock()
	var pending *localRequest
	if len(server.pending) == 1 {
		pending = server.pending[0]
	}
	server.mu.Unlock()
	require.NotNil(t, pending)

	var raw wiretypes.RawOauthDiscoveryPolledCommand
	require.NoError(t, json.Unmarshal(pending.command, &raw))
	require.Equal(t, wiretypes.CommandTypeOAuthDiscovery, raw.CommandType)
	require.Equal(t, types.DefaultChannel.String(), raw.Channel)
	require.Empty(t, raw.Headers.Values("Authorization"))
	require.Empty(t, raw.Headers.Values("Cookie"))
	require.Empty(t, raw.Headers)

	cancel()
	<-done
}

func TestWaitForMCPProbeAllowsOAuthRequiredProbeError(t *testing.T) {
	probeState := mcpclient.NewProbeState()
	probeState.Set(errors.New("401 unauthorized"))

	require.NoError(t, waitForMCPProbe(context.Background(), time.Second, probeState))
}

func TestStartWritesProxyInfoFile(t *testing.T) {
	mcpServer := mockmcpserver.NewMockMCPServer(mockmcpserver.WithToolListChangedNotificationsDisabled())
	mcpServer.Start(t)
	path := t.TempDir() + "/proxy.json"

	proxy, err := Start(context.Background(), Options{
		MCPServerURLs: []string{mcpServer.BaseURL().String()},
		URLFile:       path,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, proxy.Stop(context.Background()))
	})

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"mcp_url"`)
	require.Contains(t, string(data), proxy.Info().MCPURL)
	require.Contains(t, string(data), `"backend": "go-in-memory"`)
}

func TestStartEnablesEphemeralHealthListenerWhenURLFileRequested(t *testing.T) {
	mcpServer := mockmcpserver.NewMockMCPServer(mockmcpserver.WithToolListChangedNotificationsDisabled())
	mcpServer.Start(t)
	path := t.TempDir() + "/health.url"

	proxy, err := Start(context.Background(), Options{
		MCPServerURLs: []string{mcpServer.BaseURL().String()},
		HealthURLFile: path,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, proxy.Stop(context.Background()))
	})

	require.NotEmpty(t, proxy.Info().HealthURL)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	healthBaseURL := strings.TrimRight(strings.TrimSpace(string(data)), "/")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthBaseURL+"/healthz", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() {
		_ = resp.Body.Close()
	}()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestStartRejectsMissingMCPConfiguration(t *testing.T) {
	_, err := Start(context.Background(), Options{})
	require.ErrorContains(t, err, "set --mcp-server-url, --mcp-command, --profile, or --profile-file")
}

func TestStartRejectsUnavailableRustBackend(t *testing.T) {
	_, err := Start(context.Background(), Options{
		MCPServerURLs: []string{"http://127.0.0.1:1/mcp"},
		Backend:       BackendRust,
	})
	require.ErrorContains(t, err, "rust local proxy backend is unavailable in this build")
}

func TestResolveBackendAutoFallsBackToGo(t *testing.T) {
	factory, err := resolveBackendFactory(BackendAuto, QueueBackendInMemory, nil)
	require.NoError(t, err)
	require.Equal(t, BackendGo, factory.Name())
}

func TestResolveBackendAutoPrefersRegisteredRust(t *testing.T) {
	factory, err := resolveBackendFactory(BackendAuto, QueueBackendInMemory, []BackendFactory{
		fakeBackendFactory{name: BackendRust},
	})
	require.NoError(t, err)
	require.Equal(t, BackendRust, factory.Name())
}

func TestResolveBackendRejectsUnavailableRust(t *testing.T) {
	_, err := resolveBackendFactory(BackendRust, QueueBackendInMemory, nil)
	require.ErrorContains(t, err, "rust local proxy backend is unavailable in this build")
}

func TestResolveBackendRejectsUnknownBackend(t *testing.T) {
	_, err := resolveBackendFactory(BackendName("python"), QueueBackendInMemory, nil)
	require.ErrorContains(t, err, `unknown local proxy backend "python"`)
}

func TestResolveRedisQueueRequiresRust(t *testing.T) {
	_, err := resolveBackendFactory(BackendAuto, QueueBackendRedis, nil)
	require.ErrorContains(t, err, "redis engine queue backend requires the rust local proxy backend")
}

func TestResolveRedisQueueRejectsGo(t *testing.T) {
	_, err := resolveBackendFactory(BackendGo, QueueBackendRedis, nil)
	require.ErrorContains(t, err, "go local proxy backend does not support redis engine queue backend")
}

func TestResolveRedisQueueUsesRegisteredRust(t *testing.T) {
	factory, err := resolveBackendFactory(BackendAuto, QueueBackendRedis, []BackendFactory{
		fakeBackendFactory{name: BackendRust},
	})
	require.NoError(t, err)
	require.Equal(t, BackendRust, factory.Name())
}

func TestLocalServerStopClosesActivePollCombinedTCP(t *testing.T) {
	const tunnelID = "tunnel_22222222222222222222222222222222"
	server, err := startLocalServer(localServerOptions{
		TunnelID: tunnelID,
		APIKey:   localControlPlaneAPIKey,
	})
	require.NoError(t, err)

	pollCtx, cancelPoll := context.WithCancel(context.Background())
	defer cancelPoll()
	pollURL := server.ControlPlaneBaseURL().JoinPath("v1", "tunnels", tunnelID, "poll")
	query := pollURL.Query()
	query.Set("timeout_ms", "30000")
	pollURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(pollCtx, http.MethodGet, pollURL.String(), nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+localControlPlaneAPIKey)
	pollResult := doHTTPRequest(req, http.DefaultClient)
	require.NoError(t, server.WaitForPoll(context.Background(), time.Second))

	requireStopCompletes(t, server, cancelPoll)
	require.Error(t, <-pollResult, "active control-plane poll did not observe connection closure")
	requireEndpointClosed(t, http.DefaultClient, server.IngressBaseURL().JoinPath("readyz").String())
}

func TestLocalServerStopClosesActiveRequestsWithUnixControlPlane(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix control-plane listener is unavailable on Windows")
	}

	const tunnelID = "tunnel_22222222222222222222222222222222"
	controlPlaneSocket := shortUnixSocketPath(t)
	server, err := startLocalServer(localServerOptions{
		ControlPlaneUnixSocket: controlPlaneSocket,
		TunnelID:               tunnelID,
		APIKey:                 localControlPlaneAPIKey,
	})
	require.NoError(t, err)

	controlPlaneClient := unixHTTPClient(controlPlaneSocket)
	t.Cleanup(controlPlaneClient.CloseIdleConnections)
	requireStopClosesActiveIngressAndPoll(t, server, http.DefaultClient, controlPlaneClient)
}

func TestLocalServerStopClosesActiveRequestsWithUnixIngress(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix ingress listener is unavailable on Windows")
	}

	const tunnelID = "tunnel_22222222222222222222222222222222"
	ingressSocket := shortUnixSocketPath(t)
	server, err := startLocalServer(localServerOptions{
		ListenUnixSocket: ingressSocket,
		TunnelID:         tunnelID,
		APIKey:           localControlPlaneAPIKey,
	})
	require.NoError(t, err)

	ingressClient := unixHTTPClient(ingressSocket)
	t.Cleanup(ingressClient.CloseIdleConnections)
	requireStopClosesActiveIngressAndPoll(t, server, ingressClient, http.DefaultClient)
	_, err = os.Stat(ingressSocket)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func requireStopClosesActiveIngressAndPoll(
	t *testing.T,
	server *localServer,
	ingressClient *http.Client,
	controlPlaneClient *http.Client,
) {
	t.Helper()
	ingressCtx, cancelIngress := context.WithCancel(context.Background())
	defer cancelIngress()
	ingressURL := server.IngressBaseURL().JoinPath("v1", "mcp", server.tunnelID.String())
	ingressReq, err := http.NewRequestWithContext(
		ingressCtx,
		http.MethodPost,
		ingressURL.String(),
		strings.NewReader(`{"jsonrpc":"2.0","id":"active","method":"tools/list"}`),
	)
	require.NoError(t, err)
	ingressReq.Header.Set("Content-Type", "application/json")
	ingressResult := doHTTPRequest(ingressReq, ingressClient)
	require.Eventually(t, func() bool {
		server.mu.Lock()
		defer server.mu.Unlock()
		return len(server.pending) == 1
	}, time.Second, time.Millisecond, "ingress request did not become active")

	pollURL := server.ControlPlaneBaseURL().JoinPath("v1", "tunnels", server.tunnelID.String(), "poll")
	query := pollURL.Query()
	query.Set("timeout_ms", "30000")
	pollURL.RawQuery = query.Encode()
	dispatchCtx, cancelDispatch := context.WithTimeout(context.Background(), time.Second)
	defer cancelDispatch()
	dispatchReq, err := http.NewRequestWithContext(dispatchCtx, http.MethodGet, pollURL.String(), nil)
	require.NoError(t, err)
	dispatchReq.Header.Set("Authorization", "Bearer "+localControlPlaneAPIKey)
	require.NoError(t, <-doHTTPRequest(dispatchReq, controlPlaneClient))
	require.Eventually(t, func() bool {
		server.mu.Lock()
		defer server.mu.Unlock()
		return len(server.inFlight) == 1
	}, time.Second, time.Millisecond, "ingress request was not dispatched")

	server.mu.Lock()
	server.lastPoll = time.Time{}
	server.mu.Unlock()
	pollCtx, cancelPoll := context.WithCancel(context.Background())
	defer cancelPoll()
	pollReq, err := http.NewRequestWithContext(pollCtx, http.MethodGet, pollURL.String(), nil)
	require.NoError(t, err)
	pollReq.Header.Set("Authorization", "Bearer "+localControlPlaneAPIKey)
	pollResult := doHTTPRequest(pollReq, controlPlaneClient)
	require.NoError(t, server.WaitForPoll(context.Background(), time.Second))

	requireStopCompletes(t, server, func() {
		cancelIngress()
		cancelPoll()
	})
	require.Error(t, <-ingressResult, "active ingress request did not observe connection closure")
	require.Error(t, <-pollResult, "active control-plane poll did not observe connection closure")
	requireEndpointClosed(t, ingressClient, server.IngressBaseURL().JoinPath("readyz").String())
	requireEndpointClosed(t, controlPlaneClient, server.ControlPlaneBaseURL().JoinPath("readyz").String())
}

func doHTTPRequest(req *http.Request, client *http.Client) <-chan error {
	result := make(chan error, 1)
	go func() {
		resp, err := client.Do(req)
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			closeErr := resp.Body.Close()
			if err == nil {
				err = closeErr
			}
		}
		result <- err
	}()
	return result
}

func requireStopCompletes(t *testing.T, server *localServer, cancelRequests context.CancelFunc) {
	t.Helper()
	stopDone := make(chan error, 1)
	go func() {
		stopDone <- server.Stop(context.Background())
	}()
	select {
	case err := <-stopDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		cancelRequests()
		select {
		case err := <-stopDone:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("local server Stop remained blocked after active requests were canceled")
		}
		t.Fatal("local server Stop waited for active requests")
	}
}

func requireEndpointClosed(t *testing.T, client *http.Client, endpoint string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err, "endpoint %s still accepted connections after Stop", endpoint)
}

type fakeBackendFactory struct {
	name BackendName
}

func (f fakeBackendFactory) Name() BackendName {
	return f.name
}

func (fakeBackendFactory) StartBackend(context.Context, BackendOptions) (Backend, error) {
	return nil, errors.New("not implemented")
}

type toolCallResponse struct {
	Result struct {
		StructuredContent map[string]string `json:"structuredContent"`
	} `json:"result"`
}

func postInitialize(t *testing.T, mcpURL string, extraHeaders http.Header) string {
	t.Helper()
	return postInitializeWithClient(t, http.DefaultClient, mcpURL, extraHeaders)
}

func postInitializeWithClient(t *testing.T, client *http.Client, mcpURL string, extraHeaders http.Header) string {
	t.Helper()
	sessionID := postInitializeOptionalSessionWithClient(t, client, mcpURL, extraHeaders)
	require.NotEmpty(t, sessionID, "initialize response missing Mcp-Session-Id")
	return sessionID
}

func postInitializeOptionalSession(t *testing.T, mcpURL string, extraHeaders http.Header) string {
	t.Helper()
	return postInitializeOptionalSessionWithClient(t, http.DefaultClient, mcpURL, extraHeaders)
}

func postInitializeOptionalSessionWithClient(t *testing.T, client *http.Client, mcpURL string, extraHeaders http.Header) string {
	t.Helper()
	received := postJSONRPCWithClient(t, client, mcpURL, extraHeaders, `{
		"jsonrpc":"2.0",
		"id":"initialize-0",
		"method":"initialize",
		"params":{
			"protocolVersion":"2025-06-18",
			"capabilities":{"sampling":{},"roots":{"listChanged":true}},
			"clientInfo":{"name":"tunnel-client-local-proxy-test","version":"0.0.1"}
		}
	}`)
	return received.headers.Get("Mcp-Session-Id")
}

func postInitializedNotification(t *testing.T, mcpURL string, sessionID string) {
	t.Helper()
	postInitializedNotificationWithClient(t, http.DefaultClient, mcpURL, sessionID)
}

func postInitializedNotificationWithClient(t *testing.T, client *http.Client, mcpURL string, sessionID string) {
	t.Helper()
	_ = postJSONRPCWithClient(t, client, mcpURL, sessionHeaders(sessionID), `{
		"jsonrpc":"2.0",
		"method":"notifications/initialized",
		"params":{}
	}`)
}

func postToolCall(t *testing.T, mcpURL string, sessionID string, extraHeaders http.Header) toolCallResponse {
	t.Helper()
	return postToolCallWithClient(t, http.DefaultClient, mcpURL, sessionID, extraHeaders)
}

func postToolCallWithClient(t *testing.T, client *http.Client, mcpURL string, sessionID string, extraHeaders http.Header) toolCallResponse {
	t.Helper()
	headers := sessionHeaders(sessionID)
	for name, values := range extraHeaders {
		headers.Del(name)
		for _, value := range values {
			headers.Add(name, value)
		}
	}
	received := postJSONRPCWithClient(t, client, mcpURL, headers, `{
		"jsonrpc":"2.0",
		"id":"tool-1",
		"method":"tools/call",
		"params":{"name":"echo","arguments":{"name":"Ada"}}
	}`)
	var decoded toolCallResponse
	require.NoError(t, json.Unmarshal(jsonRPCResponseBody(received.body), &decoded), string(received.body))
	return decoded
}

func sessionHeaders(sessionID string) http.Header {
	headers := http.Header{}
	if sessionID != "" {
		headers.Set("Mcp-Session-Id", sessionID)
	}
	return headers
}

type jsonRPCIngressResponse struct {
	body    []byte
	headers http.Header
}

func postJSONRPCWithClient(t *testing.T, client *http.Client, mcpURL string, extraHeaders http.Header, body string) jsonRPCIngressResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpURL, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	for name, values := range extraHeaders {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() {
		_ = resp.Body.Close()
	}()
	responseBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.GreaterOrEqual(t, resp.StatusCode, 200, string(responseBody))
	require.Less(t, resp.StatusCode, 300, string(responseBody))
	return jsonRPCIngressResponse{body: responseBody, headers: resp.Header.Clone()}
}

func jsonRPCResponseBody(body []byte) []byte {
	events := jsonRPCEventBodies(body)
	if len(events) > 0 {
		return events[len(events)-1]
	}
	return body
}

func jsonRPCEventBodies(body []byte) [][]byte {
	var events [][]byte
	lines := bytes.Split(body, []byte("\n"))
	for _, rawLine := range lines {
		line := bytes.TrimSpace(rawLine)
		if payload, ok := bytes.CutPrefix(line, []byte("data: ")); ok {
			events = append(events, payload)
		}
	}
	return events
}

func unixHTTPClient(socketPath string) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{}
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socketPath)
	}
	return &http.Client{Transport: transport}
}

func shortUnixSocketPath(t *testing.T) string {
	t.Helper()
	file, err := os.CreateTemp("/tmp", "tc-mcp-*.sock")
	require.NoError(t, err)
	path := file.Name()
	require.NoError(t, file.Close())
	require.NoError(t, os.Remove(path))
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}
