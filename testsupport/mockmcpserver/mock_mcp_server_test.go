package mockmcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

func TestMockMCPServerUsage(t *testing.T) {
	t.Parallel()

	server := NewMockMCPServer(
		WithCalls(
			Call{
				Tool:   "ping",
				Result: json.RawMessage(`{"message":"pong"}`),
				ResponseHeaders: http.Header{
					"X-Mcp-Mode": {"simple"},
				},
			},
			Call{
				Tool: "stream",
				Progress: []ProgressUpdate{
					{Percentage: 50, Message: "halfway"},
				},
				Result: json.RawMessage(`{"complete":true}`),
				ResponseHeaders: http.Header{
					"X-Mcp-Mode": {"stream"},
				},
			},
		),
	)
	server.Start(t)

	baseURL := server.BaseURL()
	if baseURL == nil {
		t.Fatal("mock MCP server did not expose a base URL")
	}

	var (
		progressMu sync.Mutex
		progress   []float64
		progressCh = make(chan struct{}, 1)
	)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.1"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			progressMu.Lock()
			progress = append(progress, req.Params.Progress)
			if len(progress) == 1 {
				select {
				case progressCh <- struct{}{}:
				default:
				}
			}
			progressMu.Unlock()
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   baseURL.String(),
		HTTPClient: httpClientForServer(t, server),
	}, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	defer func() {
		_ = session.Close()
	}()

	pingRes, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ping"})
	if err != nil {
		t.Fatalf("call ping: %v", err)
	}
	var pingStructured map[string]any
	rawPing, err := json.Marshal(pingRes.StructuredContent)
	if err == nil {
		_ = json.Unmarshal(rawPing, &pingStructured)
	}
	if pingStructured["message"] != "pong" {
		t.Fatalf("unexpected ping result: %v", pingStructured)
	}

	progressMu.Lock()
	progress = progress[:0]
	progressMu.Unlock()

	streamParams := &mcp.CallToolParams{Name: "stream", Arguments: map[string]any{"topic": "demo"}}
	streamParams.SetProgressToken("stream-progress")
	streamRes, err := session.CallTool(ctx, streamParams)
	if err != nil {
		t.Fatalf("call stream: %v", err)
	}
	var streamStructured map[string]any
	if raw, err := json.Marshal(streamRes.StructuredContent); err == nil {
		_ = json.Unmarshal(raw, &streamStructured)
	}
	if streamStructured["complete"] != true {
		t.Fatalf("unexpected stream result: %v", streamStructured)
	}
	select {
	case <-progressCh:
	case <-time.After(time.Second):
		progressMu.Lock()
		defer progressMu.Unlock()
		t.Fatalf("expected progress notification, got %v", progress)
	}
	progressMu.Lock()
	firstProgress := progress[0]
	progressMu.Unlock()
	if firstProgress != 50 {
		t.Fatalf("unexpected progress updates: %v", progress)
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	if err := server.WaitForRequests(waitCtx, 2); err != nil {
		t.Fatalf("wait for requests: %v", err)
	}
	reqs := server.ReceivedRequests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 recorded requests, got %d", len(reqs))
	}
	if reqs[0].Tool != "ping" || reqs[1].Tool != "stream" {
		t.Fatalf("unexpected request order: %+v", reqs)
	}
}

func TestMockMCPServerCloseTerminatesActiveStreamableConnection(t *testing.T) {
	t.Parallel()

	server := NewMockMCPServer()
	server.Start(t)

	baseURL := server.BaseURL()
	if baseURL == nil {
		t.Fatal("mock MCP server did not expose a base URL")
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   baseURL.String(),
		HTTPClient: httpClientForServer(t, server),
	}, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	defer func() {
		_ = session.Close()
	}()

	closed := make(chan struct{})
	go func() {
		server.Close()
		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(time.Second):
		_ = session.Close()
		select {
		case <-closed:
		case <-time.After(time.Second):
		}
		t.Fatal("mock MCP server Close blocked with an active streamable connection")
	}
}

func TestMockMCPServerInMemory(t *testing.T) {
	t.Parallel()

	server := NewMockMCPServer(
		WithCalls(
			Call{
				Tool:   "ping",
				Result: json.RawMessage(`{"ok":true}`),
			},
		),
	)
	clientTransport := server.StartInMemory(t)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	defer func() {
		_ = session.Close()
	}()

	pingRes, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ping"})
	if err != nil {
		t.Fatalf("call ping: %v", err)
	}
	var pingStructured map[string]any
	if raw, err := json.Marshal(pingRes.StructuredContent); err == nil {
		_ = json.Unmarshal(raw, &pingStructured)
	}
	if pingStructured["ok"] != true {
		t.Fatalf("unexpected ping result: %v", pingStructured)
	}
}

func TestMockMCPServerStdio(t *testing.T) {
	server := NewMockMCPServer(
		WithCalls(
			Call{
				Tool:   "ping",
				Result: json.RawMessage(`{"ok":true}`),
			},
		),
	)
	clientTransport := server.StartStdio(t)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	defer func() {
		_ = session.Close()
	}()

	pingRes, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ping"})
	if err != nil {
		t.Fatalf("call ping: %v", err)
	}
	var pingStructured map[string]any
	if raw, err := json.Marshal(pingRes.StructuredContent); err == nil {
		_ = json.Unmarshal(raw, &pingStructured)
	}
	if pingStructured["ok"] != true {
		t.Fatalf("unexpected ping result: %v", pingStructured)
	}
}

func TestMockMCPServerWWWAuthenticateProbe(t *testing.T) {
	t.Parallel()

	server := NewMockMCPServer(
		WithWWWAuthenticateProbe(),
		WithOAuthDiscoveryResources(),
	)
	server.Start(t)

	baseURL := server.BaseURL()
	if baseURL == nil {
		t.Fatal("mock MCP server did not expose a base URL")
	}

	req, err := http.NewRequest(http.MethodPost, baseURL.String(), http.NoBody)
	if err != nil {
		t.Fatalf("build probe request: %v", err)
	}

	resp, err := httpClientForServer(t, server).Do(req)
	if err != nil {
		t.Fatalf("probe request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for probe, got %d", resp.StatusCode)
	}
	header := resp.Header.Get("WWW-Authenticate")
	if header == "" || !strings.Contains(header, "resource_metadata") {
		t.Fatalf("missing resource_metadata in WWW-Authenticate header: %q", header)
	}
}

func TestMockMCPServerIgnoresCanceledPostBodyRead(t *testing.T) {
	t.Parallel()

	server := NewMockMCPServer()
	server.Start(t)

	server.mu.Lock()
	var handler http.Handler
	if server.httpServer != nil {
		handler = server.httpServer.Config.Handler
	}
	server.mu.Unlock()
	baseURL := server.BaseURL()
	if handler == nil || baseURL == nil {
		t.Fatal("mock MCP server did not expose an HTTP handler")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL.String(), unexpectedEOFReader{})
	if err != nil {
		t.Fatalf("build canceled POST request: %v", err)
	}

	handler.ServeHTTP(httptest.NewRecorder(), req)
}

func TestMockMCPServerOAuthMetadata(t *testing.T) {
	t.Parallel()

	server := NewMockMCPServer(
		WithOAuthDiscoveryResources(),
		WithCalls(
			Call{
				Tool:   "ping",
				Result: json.RawMessage(`{"ok":true}`),
			},
		),
	)
	server.Start(t)

	baseURL := server.BaseURL()
	if baseURL == nil {
		t.Fatal("mock MCP server did not expose a base URL")
	}
	metadataURL := baseURL.ResolveReference(&url.URL{Path: wellKnownOAuthProtectedResourcePath})

	resp, err := httpClientForServer(t, server).Get(metadataURL.String())
	if err != nil {
		t.Fatalf("GET metadata: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metadata status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var metadata oauthex.ProtectedResourceMetadata
	if err := json.NewDecoder(resp.Body).Decode(&metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadata.Resource != baseURL.String() {
		t.Fatalf("metadata resource = %q, want %q", metadata.Resource, baseURL.String())
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "*")
	}

	optionsReq, err := http.NewRequest(http.MethodOptions, metadataURL.String(), nil)
	if err != nil {
		t.Fatalf("create OPTIONS request: %v", err)
	}
	optionsResp, err := httpClientForServer(t, server).Do(optionsReq)
	if err != nil {
		t.Fatalf("OPTIONS metadata: %v", err)
	}
	defer func() {
		_ = optionsResp.Body.Close()
	}()
	if optionsResp.StatusCode != http.StatusNoContent {
		t.Fatalf("OPTIONS metadata status = %d, want %d", optionsResp.StatusCode, http.StatusNoContent)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   baseURL.String(),
		HTTPClient: httpClientForServer(t, server),
	}, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	defer func() {
		_ = session.Close()
	}()
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ping"}); err != nil {
		t.Fatalf("call ping: %v", err)
	}
	if err := server.WaitForRequests(ctx, 1); err != nil {
		t.Fatalf("wait for requests: %v", err)
	}
}

func TestMockMCPServerOAuthProtection(t *testing.T) {
	t.Parallel()

	server := NewMockMCPServer(
		WithOAuthProtection(),
		WithCalls(
			Call{
				Tool:   "ping",
				Result: json.RawMessage(`{"ok":true}`),
			},
		),
	)
	server.Start(t)

	baseURL := server.BaseURL()
	if baseURL == nil {
		t.Fatal("mock MCP server did not expose a base URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Unauthorized initialize should return 401 with WWW-Authenticate header.
	initPayload := `{"jsonrpc":"2.0","id":"init-unauth","method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
	resp, err := httpClientForServer(t, server).Post(baseURL.String(), "application/json", strings.NewReader(initPayload))
	if err != nil {
		t.Fatalf("POST initialize without auth: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized init status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	if authz := resp.Header.Get("WWW-Authenticate"); authz == "" || !strings.Contains(authz, wellKnownOAuthProtectedResourcePath) {
		t.Fatalf("WWW-Authenticate header missing metadata URL: %q", authz)
	}

	// Metadata remains public.
	metadataURL := baseURL.ResolveReference(&url.URL{Path: wellKnownOAuthProtectedResourcePath})
	metadataResp, err := httpClientForServer(t, server).Get(metadataURL.String())
	if err != nil {
		t.Fatalf("GET metadata: %v", err)
	}
	defer func() {
		_ = metadataResp.Body.Close()
	}()
	if metadataResp.StatusCode != http.StatusOK {
		t.Fatalf("metadata status = %d, want %d", metadataResp.StatusCode, http.StatusOK)
	}

	// Authorized client can initialize and call tools.
	baseClient := httpClientForServer(t, server)
	authClient := *baseClient
	authClient.Transport = roundTripperWithBearer{token: "sk-1234567890abcdef", base: baseClient.Transport}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.1"}, nil)
	session, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   baseURL.String(),
		HTTPClient: &authClient,
	}, nil)
	if err != nil {
		t.Fatalf("connect MCP client with auth: %v", err)
	}
	defer func() {
		_ = session.Close()
	}()

	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ping"}); err != nil {
		t.Fatalf("call ping with auth: %v", err)
	}
	if err := server.WaitForRequests(ctx, 1); err != nil {
		t.Fatalf("wait for requests: %v", err)
	}
}

func httpClientForServer(t testing.TB, server *MockMCPServer) *http.Client {
	t.Helper()

	server.mu.Lock()
	defer server.mu.Unlock()
	if server.httpServer == nil {
		t.Fatal("mock MCP server did not expose an HTTP client")
	}
	client := server.httpServer.Client()
	client.Timeout = 3 * time.Second
	return client
}

type roundTripperWithBearer struct {
	token string
	base  http.RoundTripper
}

type unexpectedEOFReader struct{}

func (unexpectedEOFReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func (r roundTripperWithBearer) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+r.token)
	return r.base.RoundTrip(clone)
}
