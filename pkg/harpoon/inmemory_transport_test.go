package harpoon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/mcpclient"
)

func TestRestartableInMemoryTransportReconnectsAfterClose(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := mcp.NewServer(&mcp.Implementation{Name: "harpoon", Version: "test"}, nil)
	transport := mcpclient.NewSharedConnectionTransport(
		newRestartableInMemoryTransport(ctx, server, logger),
	)
	require.NotNil(t, transport)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)

	firstCtx, firstCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer firstCancel()
	first, err := client.Connect(firstCtx, transport, nil)
	require.NoError(t, err)
	require.NotNil(t, first.InitializeResult())
	require.NoError(t, first.Close())

	secondCtx, secondCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer secondCancel()
	second, err := client.Connect(secondCtx, transport, nil)
	require.NoError(t, err)
	require.NotNil(t, second.InitializeResult())
	require.NoError(t, second.Close())
}

func TestRestartableInMemoryTransportClosesActiveConnectionsOnStop(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := mcp.NewServer(&mcp.Implementation{Name: "harpoon", Version: "test"}, nil)
	base := newRestartableInMemoryTransport(ctx, server, logger)
	shared := mcpclient.NewSharedConnectionTransport(base)
	require.NotNil(t, shared)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	connectCtx, connectCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer connectCancel()
	session, err := client.Connect(connectCtx, shared, nil)
	require.NoError(t, err)

	waitDone := make(chan error, 1)
	go func() { waitDone <- session.Wait() }()
	cancel()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for active connection to close")
	}

	_, err = base.Connect(context.Background())
	require.ErrorIs(t, err, context.Canceled)
	_ = session.Close()
}

func TestRestartableInMemoryTransportReconnectsWhilePriorHandlerIsRunning(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := mcp.NewServer(&mcp.Implementation{Name: "harpoon", Version: "test"}, nil)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() {
		releaseOnce.Do(func() { close(release) })
	}
	t.Cleanup(releaseHandler)
	mcp.AddTool(server, &mcp.Tool{Name: "block"}, func(context.Context, *mcp.CallToolRequest, any) (*mcp.CallToolResult, any, error) {
		started <- struct{}{}
		<-release
		return &mcp.CallToolResult{}, nil, nil
	})

	shared := mcpclient.NewSharedConnectionTransport(
		newRestartableInMemoryTransport(ctx, server, logger),
	)
	require.NotNil(t, shared)

	firstTransport := &capturingTransport{base: shared}
	firstClient := mcp.NewClient(&mcp.Implementation{Name: "test-client-1", Version: "test"}, nil)
	firstCtx, firstCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer firstCancel()
	first, err := firstClient.Connect(firstCtx, firstTransport, nil)
	require.NoError(t, err)
	require.NotNil(t, firstTransport.conn)

	callDone := make(chan error, 1)
	go func() {
		_, err := first.CallTool(context.Background(), &mcp.CallToolParams{Name: "block"})
		callDone <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first handler to start")
	}

	// Dispatcher TTL expiry closes the shared transport connection directly,
	// while the server-side handler may still be unwinding.
	require.NoError(t, firstTransport.conn.Close())

	secondClient := mcp.NewClient(&mcp.Implementation{Name: "test-client-2", Version: "test"}, nil)
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer secondCancel()
	second, err := secondClient.Connect(secondCtx, shared, nil)
	require.NoError(t, err)
	require.NotNil(t, second.InitializeResult())
	require.NoError(t, second.Close())

	releaseHandler()
	select {
	case <-callDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first handler to finish")
	}
	_ = first.Close()
}

func TestRestartableInMemoryTransportCancelsPriorHandlerOnClose(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := mcp.NewServer(&mcp.Implementation{Name: "harpoon", Version: "test"}, nil)
	started := make(chan struct{}, 1)
	handlerCanceled := make(chan struct{})
	mcp.AddTool(server, &mcp.Tool{Name: "block-until-canceled"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
		started <- struct{}{}
		<-ctx.Done()
		close(handlerCanceled)
		return &mcp.CallToolResult{}, nil, ctx.Err()
	})

	shared := mcpclient.NewSharedConnectionTransport(
		newRestartableInMemoryTransport(ctx, server, logger),
	)
	require.NotNil(t, shared)

	capturing := &capturingTransport{base: shared}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	connectCtx, connectCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer connectCancel()
	session, err := client.Connect(connectCtx, capturing, nil)
	require.NoError(t, err)
	require.NotNil(t, capturing.conn)

	callDone := make(chan error, 1)
	go func() {
		_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "block-until-canceled"})
		callDone <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for handler to start")
	}

	require.NoError(t, capturing.conn.Close())
	select {
	case <-handlerCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for closed session to cancel handler")
	}
	select {
	case <-callDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for canceled call to return")
	}
	_ = session.Close()
}

func TestHarpoonAcceptsSelfContained20260728RequestsAcrossInstances(t *testing.T) {
	t.Parallel()

	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: false,
		MaxResponseBytes:   config.DefaultHarpoonMaxResponseBytes,
		MaxRedirects:       config.DefaultHarpoonMaxRedirects,
		Targets: []config.HarpoonTarget{{
			Label:       "svc",
			Description: "Static target shared by independent Harpoon instances",
			BaseURL:     mustParseURL(t, "https://example.invalid/"),
		}},
	}

	first := newRawHarpoonConnection(t, cfg)
	second := newRawHarpoonConnection(t, cfg)

	// Alternate requests between two independent embedded Harpoon servers.
	// None of these connections receives initialize or notifications/initialized:
	// each request carries the complete 2026-07-28 client context in _meta.
	discover := rawMCPCall(t, first, "discover-first", "server/discover", modernMCPParams(nil))
	versions, ok := discover["supportedVersions"].([]any)
	require.True(t, ok, "server/discover should return supportedVersions")
	require.Contains(t, versions, "2026-07-28")

	tools := rawMCPCall(t, second, "tools-second", "tools/list", modernMCPParams(nil))
	require.Equal(t, "complete", tools["resultType"])
	require.Contains(t, toolNames(t, tools), "list_targets")
	require.Contains(t, toolNames(t, tools), "call_target")

	firstCall := rawMCPCall(t, first, "call-first", "tools/call", modernMCPParams(map[string]any{
		"name":      "list_targets",
		"arguments": map[string]any{},
	}))
	secondCall := rawMCPCall(t, second, "call-second", "tools/call", modernMCPParams(map[string]any{
		"name":      "list_targets",
		"arguments": map[string]any{},
	}))
	for _, result := range []map[string]any{firstCall, secondCall} {
		require.Equal(t, "complete", result["resultType"])
		require.Contains(t, firstTextContent(t, result), "\"label\":\"svc\"")
	}
}

func TestHarpoonPreservesLegacyInitializeAndInitializedFlow(t *testing.T) {
	t.Parallel()

	cfg := &config.HarpoonConfig{
		MaxResponseBytes: config.DefaultHarpoonMaxResponseBytes,
		MaxRedirects:     config.DefaultHarpoonMaxRedirects,
		Targets: []config.HarpoonTarget{{
			Label:   "svc",
			BaseURL: mustParseURL(t, "https://example.invalid/"),
		}},
	}
	conn := newRawHarpoonConnection(t, cfg)

	// Old tunnel-service creates a new FastMCP client for each OAuth shim
	// request. Repeat the full lifecycle on the same dispatcher-facing
	// transport to prove the v1.7 compatibility reconnect happens before the
	// second initialize.
	for _, sessionName := range []string{"first", "second"} {
		initialize := rawMCPCall(t, conn, "legacy-"+sessionName+"-initialize", "initialize", map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "legacy-test-client-" + sessionName,
				"version": "1.0.0",
			},
		})
		require.Equal(t, "2025-11-25", initialize["protocolVersion"])

		rawMCPNotification(t, conn, "notifications/initialized", map[string]any{})

		tools := rawMCPCall(t, conn, "legacy-"+sessionName+"-tools", "tools/list", map[string]any{})
		require.Contains(t, toolNames(t, tools), "list_targets")
	}
}

func newRawHarpoonConnection(t *testing.T, cfg *config.HarpoonConfig) mcp.Connection {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := newTestServer(t, cfg)
	transport := mcpclient.NewInitializeRestartingSharedConnectionTransport(
		newRestartableInMemoryTransport(ctx, server.MCPServer(), logger),
	)
	require.NotNil(t, transport)

	conn, err := transport.Connect(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = conn.Close()
	})
	return conn
}

func modernMCPParams(extra map[string]any) map[string]any {
	params := map[string]any{
		"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion": "2026-07-28",
			"io.modelcontextprotocol/clientInfo": map[string]any{
				"name":    "modern-test-client",
				"version": "1.0.0",
			},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		},
	}
	for key, value := range extra {
		params[key] = value
	}
	return params
}

func rawMCPCall(t *testing.T, conn mcp.Connection, id, method string, params map[string]any) map[string]any {
	t.Helper()

	msg := decodeRawMCPMessage(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, conn.Write(ctx, msg))

	responseMsg, err := conn.Read(ctx)
	require.NoError(t, err)
	_, ok := responseMsg.(*jsonrpc.Response)
	require.True(t, ok, "expected JSON-RPC response, got %T", responseMsg)

	encoded, err := jsonrpc.EncodeMessage(responseMsg)
	require.NoError(t, err)
	var response map[string]any
	require.NoError(t, json.Unmarshal(encoded, &response))
	_, hasError := response["error"]
	require.False(t, hasError, "unexpected JSON-RPC error: %s", encoded)
	result, ok := response["result"].(map[string]any)
	require.True(t, ok, "response should have an object result: %s", encoded)
	return result
}

func rawMCPNotification(t *testing.T, conn mcp.Connection, method string, params map[string]any) {
	t.Helper()

	msg := decodeRawMCPMessage(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, conn.Write(ctx, msg))
}

func decodeRawMCPMessage(t *testing.T, payload map[string]any) jsonrpc.Message {
	t.Helper()

	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	msg, err := jsonrpc.DecodeMessage(encoded)
	require.NoError(t, err)
	return msg
}

func toolNames(t *testing.T, result map[string]any) []string {
	t.Helper()

	rawTools, ok := result["tools"].([]any)
	require.True(t, ok, "tools/list should return tools")
	names := make([]string, 0, len(rawTools))
	for _, rawTool := range rawTools {
		tool, ok := rawTool.(map[string]any)
		require.True(t, ok, "tool should be an object")
		name, ok := tool["name"].(string)
		require.True(t, ok, "tool should have a name")
		names = append(names, name)
	}
	return names
}

func firstTextContent(t *testing.T, result map[string]any) string {
	t.Helper()

	content, ok := result["content"].([]any)
	require.True(t, ok, "tools/call should return content")
	require.NotEmpty(t, content)
	first, ok := content[0].(map[string]any)
	require.True(t, ok, "first content block should be an object")
	text, ok := first["text"].(string)
	require.True(t, ok, "first content block should be text")
	return text
}

type capturingTransport struct {
	base mcp.Transport
	conn mcp.Connection
}

func (t *capturingTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	t.conn = conn
	return conn, nil
}
