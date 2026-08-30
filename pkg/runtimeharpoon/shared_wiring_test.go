package runtimeharpoon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

func TestAdditionalStreamableTransportAcceptsSelfContained20260728POSTs(t *testing.T) {
	t.Parallel()

	endpoint := newAdditionalStreamableTransportEndpoint(t)

	discover := postSelfContainedRequest(t, endpoint, "server/discover", "", nil)
	versions, ok := discover["supportedVersions"].([]any)
	require.True(t, ok, "server/discover should return supportedVersions")
	require.Contains(t, versions, "2026-07-28")

	tools := postSelfContainedRequest(t, endpoint, "tools/list", "", nil)
	require.Equal(t, "complete", tools["resultType"])

	call := postSelfContainedRequest(t, endpoint, "tools/call", "list_targets", map[string]any{
		"name":      "list_targets",
		"arguments": map[string]any{},
	})
	require.Equal(t, "complete", call["resultType"])

}

func TestAdditionalStreamableTransportRejectsOversizedPOSTBeforeClassification(t *testing.T) {
	t.Parallel()

	endpoint := newAdditionalStreamableTransportEndpoint(t)
	body := bytes.Repeat([]byte("x"), mcp.DefaultMaxRequestBodyBytes+1)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	require.NoError(t, err)
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
}

func TestAdditionalStreamableTransportPreservesLegacySDKInitializeFlow(t *testing.T) {
	t.Parallel()

	endpoint := newAdditionalStreamableTransportEndpoint(t)
	fallback := &legacyFallbackRoundTripper{base: http.DefaultTransport}
	httpClient := &http.Client{Transport: fallback}
	transport := &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: httpClient,
	}
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "legacy-sdk-test-client",
		Version: "1.0.0",
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	require.NotNil(t, session.InitializeResult())
	require.Equal(t, "2025-11-25", session.InitializeResult().ProtocolVersion)

	tools, err := session.ListTools(ctx, nil)
	require.NoError(t, err)
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	require.Contains(t, names, "list_targets")

	// The public v1.7 client defaults to server/discover and does not expose a
	// legacy-version knob. Reject only that probe in the test transport so its
	// documented fallback exercises the same initialize/initialized path used
	// by older SDK clients; every legacy POST reaches the real Harpoon handler.
	methods := fallback.postMethods()
	require.Contains(t, methods, "server/discover")
	require.Contains(t, methods, "initialize")
	require.Contains(t, methods, "notifications/initialized")
	require.Contains(t, methods, "tools/list")

	require.NoError(t, session.Close())
	httpMethods := fallback.httpMethods()
	require.Contains(t, httpMethods, http.MethodGet)
	require.Contains(t, httpMethods, http.MethodDelete)
}

func TestLegacyProtocolForTestingRejectsDiscoverAndPreservesLegacySession(t *testing.T) {
	t.Parallel()

	server := mcp.NewServer(&mcp.Implementation{Name: "legacy-harpoon", Version: "test"}, nil)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverSession, err := server.Connect(ctx, &legacyProtocolForTestingTransport{base: serverTransport}, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, serverSession.Close())
	})

	recordingTransport := &recordingTransport{base: clientTransport}
	client := mcp.NewClient(&mcp.Implementation{Name: "modern-test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, recordingTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, clientSession.Close())
	})
	require.NotNil(t, clientSession.InitializeResult())
	require.Equal(t, "2025-11-25", clientSession.InitializeResult().ProtocolVersion)

	_, err = clientSession.ListTools(ctx, nil)
	require.NoError(t, err)

	require.Equal(t, []string{
		"server/discover",
		"initialize",
		"notifications/initialized",
		"tools/list",
	}, recordingTransport.requestMethods())
}

func TestLegacyProtocolForTestingDoesNotLetSelfContainedRequestCreateSession(t *testing.T) {
	t.Parallel()

	server := mcp.NewServer(&mcp.Implementation{Name: "legacy-harpoon", Version: "test"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverSession, err := server.Connect(ctx, &legacyProtocolForTestingTransport{base: serverTransport}, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, serverSession.Close())
	})

	connection, err := clientTransport.Connect(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, connection.Close())
	})

	modernID, err := jsonrpc.MakeID("modern-tools")
	require.NoError(t, err)
	modernParams, err := json.Marshal(map[string]any{
		"_meta": map[string]any{
			mcp.MetaKeyProtocolVersion:    "2026-07-28",
			mcp.MetaKeyClientInfo:         map[string]any{"name": "modern-test-client", "version": "test"},
			mcp.MetaKeyClientCapabilities: map[string]any{},
		},
	})
	require.NoError(t, err)
	require.NoError(t, connection.Write(ctx, &jsonrpc.Request{
		ID:     modernID,
		Method: "tools/list",
		Params: modernParams,
	}))
	modernResponse := readJSONRPCResponse(t, ctx, connection)
	var protocolErr *jsonrpc.Error
	require.ErrorAs(t, modernResponse.Error, &protocolErr)
	require.EqualValues(t, jsonrpc.CodeInvalidRequest, protocolErr.Code)

	legacyID, err := jsonrpc.MakeID("legacy-tools-before-initialize")
	require.NoError(t, err)
	require.NoError(t, connection.Write(ctx, &jsonrpc.Request{
		ID:     legacyID,
		Method: "tools/list",
		Params: json.RawMessage(`{}`),
	}))
	legacyResponse := readJSONRPCResponse(t, ctx, connection)
	require.Error(t, legacyResponse.Error, "a rejected modern request must not initialize the legacy session")

	initializeID, err := jsonrpc.MakeID("legacy-initialize")
	require.NoError(t, err)
	initializeParams, err := json.Marshal(map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "legacy-test-client", "version": "test"},
	})
	require.NoError(t, err)
	require.NoError(t, connection.Write(ctx, &jsonrpc.Request{
		ID:     initializeID,
		Method: "initialize",
		Params: initializeParams,
	}))
	initializeResponse := readJSONRPCResponse(t, ctx, connection)
	require.NoError(t, initializeResponse.Error)

	require.NoError(t, connection.Write(ctx, &jsonrpc.Request{
		Method: "notifications/initialized",
		Params: json.RawMessage("{}"),
	}))

	postInitializeID, err := jsonrpc.MakeID("modern-tools-after-initialize")
	require.NoError(t, err)
	require.NoError(t, connection.Write(ctx, &jsonrpc.Request{
		ID:     postInitializeID,
		Method: "tools/list",
		Params: modernParams,
	}))
	postInitializeResponse := readJSONRPCResponse(t, ctx, connection)
	require.NoError(t, postInitializeResponse.Error, "an established legacy session should accept request metadata")
}

func newAdditionalStreamableTransportEndpoint(t *testing.T) string {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &runtimeconfig.HarpoonConfig{
		MaxResponseBytes: 1024,
		MaxRedirects:     5,
		AdditionalTransports: []runtimeconfig.HarpoonTransportKind{
			runtimeconfig.HarpoonTransportHTTPStreamable,
		},
	}
	registry, err := NewRegistry(logger, false, nil)
	require.NoError(t, err)
	server, err := NewServer(cfg, registry, logger)
	require.NoError(t, err)

	lifecycle := new(additionalTransportTestLifecycle)
	adminMux := http.NewServeMux()
	err = RegisterAdditionalTransport(AdditionalTransportParams{
		Lifecycle:  lifecycle,
		GuardedMux: NewGuardedMux(adminMux),
		Config:     cfg,
		Server:     server,
		Logger:     logger,
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(adminMux)
	t.Cleanup(func() {
		httpServer.Close()
		lifecycle.stop(t)
	})
	return httpServer.URL + "/harpoon/mcp"
}

func postSelfContainedRequest(t *testing.T, endpoint, method, name string, extra map[string]any) map[string]any {
	t.Helper()

	params := map[string]any{
		"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion": "2026-07-28",
			"io.modelcontextprotocol/clientInfo": map[string]any{
				"name":    "modern-http-test-client",
				"version": "1.0.0",
			},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		},
	}
	for key, value := range extra {
		params[key] = value
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      method,
		"method":  method,
		"params":  params,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Method", method)
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	if name != "" {
		req.Header.Set("Mcp-Name", name)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	wire := readStreamableJSONRPCResponse(t, resp)
	var response struct {
		Result map[string]any
		Error  map[string]any
	}
	require.NoError(t, json.Unmarshal(wire, &response))
	require.Empty(t, response.Error, "unexpected JSON-RPC error: %s", wire)
	require.NotNil(t, response.Result, "response should have an object result: %s", wire)
	return response.Result
}

func readStreamableJSONRPCResponse(t *testing.T, resp *http.Response) []byte {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	require.NoError(t, err)
	if mediaType == "application/json" {
		return body
	}
	require.Equal(t, "text/event-stream", mediaType)

	scanner := bufio.NewScanner(bytes.NewReader(body))
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" && len(data) > 0 {
			break
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	require.NoError(t, scanner.Err())
	require.NotEmpty(t, data, "SSE response should contain a data event: %s", body)
	return []byte(strings.Join(data, "\n"))
}

func readJSONRPCResponse(t *testing.T, ctx context.Context, connection mcp.Connection) *jsonrpc.Response {
	t.Helper()
	message, err := connection.Read(ctx)
	require.NoError(t, err)
	response, ok := message.(*jsonrpc.Response)
	require.True(t, ok, "message = %T, want *jsonrpc.Response", message)
	return response
}

type recordingTransport struct {
	base mcp.Transport

	mu      sync.Mutex
	methods []string
}

func (t *recordingTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	connection, err := t.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &recordingConnection{base: connection, record: t.recordMethod}, nil
}

func (t *recordingTransport) recordMethod(method string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.methods = append(t.methods, method)
}

func (t *recordingTransport) requestMethods() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.methods...)
}

type recordingConnection struct {
	base   mcp.Connection
	record func(string)
}

func (c *recordingConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	return c.base.Read(ctx)
}

func (c *recordingConnection) Write(ctx context.Context, message jsonrpc.Message) error {
	if request, ok := message.(*jsonrpc.Request); ok && c.record != nil {
		c.record(request.Method)
	}
	return c.base.Write(ctx, message)
}

func (c *recordingConnection) Close() error {
	return c.base.Close()
}

func (c *recordingConnection) SessionID() string {
	return c.base.SessionID()
}

type additionalTransportTestLifecycle struct {
	hooks []fx.Hook
}

func (l *additionalTransportTestLifecycle) Append(hook fx.Hook) {
	l.hooks = append(l.hooks, hook)
}

func (l *additionalTransportTestLifecycle) stop(t *testing.T) {
	t.Helper()

	for _, hook := range slices.Backward(l.hooks) {
		if hook.OnStop != nil {
			require.NoError(t, hook.OnStop(context.Background()))
		}
	}
}

type legacyFallbackRoundTripper struct {
	base http.RoundTripper

	mu                 sync.Mutex
	rejectedDiscover   bool
	methods            []string
	httpRequestMethods []string
}

func (t *legacyFallbackRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.httpRequestMethods = append(t.httpRequestMethods, req.Method)
	t.mu.Unlock()

	if req.Method != http.MethodPost {
		return t.base.RoundTrip(req)
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	if err := req.Body.Close(); err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))

	var rpcRequest struct {
		ID     json.RawMessage
		Method string
	}
	if err := json.Unmarshal(body, &rpcRequest); err != nil {
		return nil, err
	}

	t.mu.Lock()
	t.methods = append(t.methods, rpcRequest.Method)
	rejectDiscover := rpcRequest.Method == "server/discover" && !t.rejectedDiscover
	if rejectDiscover {
		t.rejectedDiscover = true
	}
	t.mu.Unlock()

	if !rejectDiscover {
		return t.base.RoundTrip(req)
	}

	responseBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      rpcRequest.ID,
		"error": map[string]any{
			"code":    -32601,
			"message": "method not found",
		},
	})
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(responseBody)),
		ContentLength: int64(len(responseBody)),
		Request:       req,
	}, nil
}

func (t *legacyFallbackRoundTripper) postMethods() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.methods...)
}

func (t *legacyFallbackRoundTripper) httpMethods() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.httpRequestMethods...)
}
