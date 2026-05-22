package mockmcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invopop/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

type headerContextKey struct{}

const wellKnownOAuthProtectedResourcePath = "/.well-known/oauth-protected-resource"

// Call defines a scripted tool invocation.
type Call struct {
	Tool            string
	Result          json.RawMessage
	DynamicResult   func(json.RawMessage) (json.RawMessage, error)
	Error           *CallError
	Progress        []ProgressUpdate
	ResponseHeaders http.Header
}

// CallError describes the error payload returned by the tool.
type CallError struct {
	Message string
}

// ProgressUpdate emits a notifications/progress event during a streaming response.
type ProgressUpdate struct {
	Percentage float64
	Message    string
}

// IncomingRequest captures the tool arguments and headers that reached the mock.
type IncomingRequest struct {
	Tool      string
	Arguments json.RawMessage
	Headers   http.Header
}

// IncomingHTTPRequest captures an HTTP request received by the mock server.
type IncomingHTTPRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
}

// MockMCPServer hosts a Streamable HTTP MCP server backed by scripted tool handlers.
type Option func(*MockMCPServer)

// WithCalls seeds the mock with scripted tool invocations.
func WithCalls(calls ...Call) Option {
	return func(m *MockMCPServer) {
		m.mu.Lock()
		defer m.mu.Unlock()
		for i := range calls {
			m.calls = append(m.calls, cloneCall(calls[i]))
		}
	}
}

// WithKeepalivePings injects SSE keepalive ping events ahead of streamable responses.
func WithKeepalivePings() Option {
	return func(m *MockMCPServer) {
		m.injectKeepalivePings = true
	}
}

// WithOAuthDiscoveryResources enables the well-known OAuth ProtectedResourceMetaData endpoint.
func WithOAuthDiscoveryResources() Option {
	return func(m *MockMCPServer) {
		m.enableOAuth = true
	}
}

// WithOAuthProtection enables OAuth discovery endpoints and protects the MCP server with
// auth.RequireBearerToken using a static in-memory API key set.
func WithOAuthProtection() Option {
	return func(m *MockMCPServer) {
		m.enableOAuth = true
		m.protectOAuth = true
		if m.apiKeys == nil {
			m.apiKeys = defaultAPIKeys()
		}
	}
}

// WithWWWAuthenticateProbe enables a 401 response with WWW-Authenticate header for empty POST probes.
func WithWWWAuthenticateProbe() Option {
	return func(m *MockMCPServer) {
		m.enableOAuth = true
		m.wwwAuthProbe = true
	}
}

// WithProtectedResourceMetadata sets the metadata returned by the well-known
// OAuth Protected Resource Metadata endpoint.
func WithProtectedResourceMetadata(meta oauthex.ProtectedResourceMetadata) Option {
	return func(m *MockMCPServer) {
		m.enableOAuth = true
		copyMeta := meta
		m.oauthMetadata = &copyMeta
	}
}

// WithRequiredHeader requires every HTTP request to carry name: value.
func WithRequiredHeader(name, value string) Option {
	return WithRequiredHeaderFor(name, value, nil)
}

// WithRequiredHeaderFor requires matching HTTP requests to carry name: value.
func WithRequiredHeaderFor(name, value string, predicate func(*http.Request) bool) Option {
	return func(m *MockMCPServer) {
		m.requiredHeaders = append(m.requiredHeaders, requiredHeader{
			name:      name,
			value:     value,
			predicate: predicate,
		})
	}
}

// WithToolListChangedNotificationsDisabled disables tool list changed notifications.
func WithToolListChangedNotificationsDisabled() Option {
	return func(m *MockMCPServer) {
		m.serverOptions = &mcp.ServerOptions{
			Capabilities: &mcp.ServerCapabilities{
				Tools: &mcp.ToolCapabilities{ListChanged: false},
			},
		}
	}
}

// WithTLSServer starts the mock MCP server with TLS enabled.
func WithTLSServer() Option {
	return func(m *MockMCPServer) {
		m.useTLS = true
	}
}

type MockMCPServer struct {
	mu       sync.Mutex
	calls    []*Call
	received []IncomingRequest
	httpSeen []IncomingHTTPRequest
	requests chan struct{}

	server     *mcp.Server
	httpServer *httptest.Server
	baseURL    *url.URL
	closeOnce  sync.Once

	transportCancel  context.CancelFunc
	transportDone    chan error
	transportCleanup func()

	enableOAuth  bool
	protectOAuth bool
	wwwAuthProbe bool
	wwwAuthURL   string
	apiKeys      map[string]*APIKey

	injectKeepalivePings bool

	oauthMetadata   *oauthex.ProtectedResourceMetadata
	serverOptions   *mcp.ServerOptions
	requiredHeaders []requiredHeader

	useTLS bool

	closing atomic.Bool
	tb      atomic.Value // testing.TB
}

type requiredHeader struct {
	name      string
	value     string
	predicate func(*http.Request) bool
}

var stdioLock sync.Mutex

// NewMockMCPServer constructs an empty mock server configured by optional options.
func NewMockMCPServer(opts ...Option) *MockMCPServer {
	mock := &MockMCPServer{
		requests: make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(mock)
	}
	return mock
}

// Start launches the mock MCP server and registers cleanup with t.
func (m *MockMCPServer) Start(t testing.TB) {
	t.Helper()
	m.mu.Lock()
	if m.httpServer != nil || m.transportDone != nil {
		m.mu.Unlock()
		t.Fatalf("mock MCP server already started")
		return
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		m.mu.Unlock()
		t.Skipf("mock MCP server listener unavailable: %v", err)
		return
	}

	server := m.newServerLocked()

	streamableHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return server }, nil)
	var protectedHandler http.Handler = streamableHandler
	scheme := "http"
	if m.useTLS {
		scheme = "https"
	}
	listenerHost := listener.Addr().String()
	metadataURL := fmt.Sprintf("%s://%s%s", scheme, listener.Addr().String(), wellKnownOAuthProtectedResourcePath)
	if m.protectOAuth {
		protectedHandler = auth.RequireBearerToken(m.tokenVerifier(), &auth.RequireBearerTokenOptions{
			Scopes:              []string{"read", "write"},
			ResourceMetadataURL: metadataURL,
		})(streamableHandler)
	}
	if m.wwwAuthProbe {
		m.wwwAuthURL = metadataURL
	}

	httpHandler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			m.recordHTTPRequest(req, nil)
			if !m.checkRequiredHeaders(w, req) {
				return
			}
		}
		if m.enableOAuth && req.URL.Path == wellKnownOAuthProtectedResourcePath {
			m.serveProtectedResourceMetadata(w, req)
			return
		}
		if req.Method == http.MethodPost {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				if m.closing.Load() || req.Context().Err() != nil {
					return
				}
				m.failf("mock MCP server read body: %v", err)
			}
			_ = req.Body.Close()
			req.Body = io.NopCloser(bytes.NewReader(body))
			m.recordHTTPRequest(req, body)
			if !m.checkRequiredHeaders(w, req) {
				return
			}
			if m.wwwAuthProbe && req.Header.Get("Authorization") == "" && len(body) == 0 && m.wwwAuthURL != "" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s"`, m.wwwAuthURL))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var methodProbe struct {
				Method string `json:"method"`
			}
			if err := json.Unmarshal(body, &methodProbe); err == nil && methodProbe.Method == "notifications/initialized" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			req.Body = io.NopCloser(bytes.NewReader(body))
			hw := &headerWriter{ResponseWriter: w}
			w = hw
			req = req.WithContext(context.WithValue(req.Context(), headerContextKey{}, hw))
		}
		if m.injectKeepalivePings && req.Method == http.MethodGet && acceptsEventStream(req) {
			w = &keepalivePingWriter{ResponseWriter: w}
		}
		// Proxy E2Es intentionally route synthetic remote hostnames through this
		// loopback-backed mock server. Normalize the Host header back to the
		// listener address so newer SDK localhost protections do not reject those
		// test requests.
		if localAddr, ok := req.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && localAddr != nil {
			if isLoopbackHost(localAddr.String()) && !isLoopbackHost(req.Host) {
				req.Host = listenerHost
			}
		}
		protectedHandler.ServeHTTP(w, req)
	})
	httpServer := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: httpHandler},
	}
	if m.useTLS {
		httpServer.StartTLS()
	} else {
		httpServer.Start()
	}

	parsed, err := url.Parse(httpServer.URL)
	if err != nil {
		m.mu.Unlock()
		httpServer.Close()
		t.Fatalf("mock MCP server parse URL: %v", err)
		return
	}
	m.server = server
	m.httpServer = httpServer
	m.baseURL = parsed
	if m.enableOAuth && m.oauthMetadata == nil {
		if meta := m.buildProtectedResourceMetadata(parsed); meta != nil {
			m.oauthMetadata = meta
		}
	}
	m.mu.Unlock()

	m.tb.Store(t)
	t.Cleanup(m.Close)
}

// Close shuts down the server and asserts all scripted calls were consumed.
func (m *MockMCPServer) Close() {
	m.closeOnce.Do(func() {
		m.closing.Store(true)
		m.mu.Lock()
		server := m.httpServer
		m.httpServer = nil
		m.baseURL = nil
		cancel := m.transportCancel
		done := m.transportDone
		cleanup := m.transportCleanup
		m.transportCancel = nil
		m.transportDone = nil
		m.transportCleanup = nil
		remaining := len(m.calls)
		m.calls = nil
		m.mu.Unlock()
		if server != nil {
			server.CloseClientConnections()
			server.Close()
		}
		if cancel != nil {
			cancel()
		}
		if done != nil {
			select {
			case err := <-done:
				if err != nil && !isExpectedTransportShutdownError(err) {
					m.failf("mock MCP server transport stopped with error: %v", err)
				}
			case <-time.After(time.Second):
				m.failf("mock MCP server transport did not stop before timeout")
			}
		}
		if cleanup != nil {
			cleanup()
		}
		if remaining != 0 {
			m.failf("mock MCP server stopped with %d pending call(s)", remaining)
		}
	})
}

// BaseURL returns the HTTP endpoint the MCP client should connect to.
func (m *MockMCPServer) BaseURL() *url.URL {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.baseURL == nil {
		return nil
	}
	copyURL := *m.baseURL
	return &copyURL
}

// TLSCertPEM returns the server certificate PEM for TLS-enabled servers.
func (m *MockMCPServer) TLSCertPEM() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.httpServer == nil || m.httpServer.TLS == nil || len(m.httpServer.TLS.Certificates) == 0 {
		return nil, errors.New("mock MCP server TLS certificate not available")
	}
	certs := m.httpServer.TLS.Certificates[0].Certificate
	if len(certs) == 0 {
		return nil, errors.New("mock MCP server TLS certificate not available")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certs[0]}), nil
}

// StartInMemory launches the mock MCP server on an in-memory transport.
func (m *MockMCPServer) StartInMemory(t testing.TB) *mcp.InMemoryTransport {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	m.startTransport(t, serverTransport, nil)
	return clientTransport
}

// StartStdio launches the mock MCP server over stdio and returns a client transport.
// Note that stdio is process-wide, so this method should not be used concurrently.
func (m *MockMCPServer) StartStdio(t testing.TB) *mcp.IOTransport {
	t.Helper()
	stdioLock.Lock()

	serverStdin, clientWriter, err := os.Pipe()
	if err != nil {
		stdioLock.Unlock()
		t.Fatalf("mock MCP server stdio pipe: %v", err)
		return nil
	}
	clientReader, serverStdout, err := os.Pipe()
	if err != nil {
		_ = serverStdin.Close()
		_ = clientWriter.Close()
		stdioLock.Unlock()
		t.Fatalf("mock MCP server stdio pipe: %v", err)
		return nil
	}

	origStdin := os.Stdin
	origStdout := os.Stdout
	os.Stdin = serverStdin
	os.Stdout = serverStdout

	cleanup := func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
		_ = serverStdin.Close()
		_ = serverStdout.Close()
		_ = clientReader.Close()
		_ = clientWriter.Close()
		stdioLock.Unlock()
	}

	m.startTransport(t, &mcp.StdioTransport{}, cleanup)
	return &mcp.IOTransport{
		Reader: clientReader,
		Writer: clientWriter,
	}
}

// WaitForRequests blocks until at least n tool calls have completed or ctx expires.
func (m *MockMCPServer) WaitForRequests(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	for {
		m.mu.Lock()
		count := len(m.received)
		m.mu.Unlock()
		if count >= n {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.requests:
		}
	}
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if parsedHost, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsedHost
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ReceivedRequests returns the recorded tool requests in order.
func (m *MockMCPServer) ReceivedRequests() []IncomingRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]IncomingRequest, len(m.received))
	for i, req := range m.received {
		out[i] = IncomingRequest{
			Tool:      req.Tool,
			Arguments: cloneJSON(req.Arguments),
			Headers:   cloneHeader(req.Headers),
		}
	}
	return out
}

// ReceivedHTTPRequests returns all HTTP requests observed by the mock server.
func (m *MockMCPServer) ReceivedHTTPRequests() []IncomingHTTPRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]IncomingHTTPRequest, len(m.httpSeen))
	for i, req := range m.httpSeen {
		out[i] = IncomingHTTPRequest{
			Method:  req.Method,
			Path:    req.Path,
			Headers: cloneHeader(req.Headers),
			Body:    bytes.Clone(req.Body),
		}
	}
	return out
}

func (m *MockMCPServer) recordHTTPRequest(req *http.Request, body []byte) {
	if m == nil || req == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	path := ""
	if req.URL != nil {
		path = req.URL.Path
	}
	m.httpSeen = append(m.httpSeen, IncomingHTTPRequest{
		Method:  req.Method,
		Path:    path,
		Headers: req.Header.Clone(),
		Body:    bytes.Clone(body),
	})
}

func (m *MockMCPServer) checkRequiredHeaders(w http.ResponseWriter, req *http.Request) bool {
	if m == nil || req == nil {
		return true
	}
	m.mu.Lock()
	requirements := append([]requiredHeader(nil), m.requiredHeaders...)
	m.mu.Unlock()
	for _, requirement := range requirements {
		if requirement.predicate != nil && !requirement.predicate(req) {
			continue
		}
		if req.Header.Get(requirement.name) == requirement.value {
			continue
		}
		http.Error(w, fmt.Sprintf("missing required header %s", requirement.name), http.StatusForbidden)
		return false
	}
	return true
}

func (m *MockMCPServer) uniqueToolsLocked() []string {
	seen := make(map[string]struct{})
	for _, call := range m.calls {
		if call.Tool != "" {
			seen[call.Tool] = struct{}{}
		}
	}
	tools := make([]string, 0, len(seen))
	for name := range seen {
		tools = append(tools, name)
	}
	sort.Strings(tools)
	return tools
}

func (m *MockMCPServer) newServerLocked() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mock-mcp-server", Version: "1.0.0"}, m.serverOptions)
	tools := m.uniqueToolsLocked()
	for _, toolName := range tools {
		tool := &mcp.Tool{
			Name:        toolName,
			Description: "mock tool",
			InputSchema: &jsonschema.Schema{Type: "object"},
		}
		mcp.AddTool(server, tool, m.toolHandler(toolName))
	}
	return server
}

func (m *MockMCPServer) startTransport(t testing.TB, transport mcp.Transport, cleanup func()) {
	t.Helper()
	m.mu.Lock()
	if m.httpServer != nil || m.transportDone != nil {
		m.mu.Unlock()
		if cleanup != nil {
			cleanup()
		}
		t.Fatalf("mock MCP server already started")
		return
	}
	server := m.newServerLocked()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	m.server = server
	m.transportCancel = cancel
	m.transportDone = done
	m.transportCleanup = cleanup
	m.mu.Unlock()

	m.tb.Store(t)
	go func() {
		done <- server.Run(ctx, transport)
	}()
	t.Cleanup(m.Close)
}

func (m *MockMCPServer) toolHandler(name string) mcp.ToolHandlerFor[map[string]any, map[string]any] {
	return func(ctx context.Context, req *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		call := m.popCall(name)
		if call == nil {
			return nil, nil, fmt.Errorf("no scripted response for tool %q", name)
		}
		if hw, ok := ctx.Value(headerContextKey{}).(*headerWriter); ok {
			hw.addHeaders(call.ResponseHeaders)
		}
		m.recordRequest(req, call)
		if call.DynamicResult != nil {
			payload, err := call.DynamicResult(req.Params.Arguments)
			if err != nil {
				return nil, nil, err
			}
			call.Result = cloneJSON(payload)
		}
		for _, update := range call.Progress {
			params := &mcp.ProgressNotificationParams{
				Progress: update.Percentage,
				Message:  update.Message,
			}
			if err := req.Session.NotifyProgress(ctx, params); err != nil {
				return nil, nil, err
			}
		}
		result, structured, err := buildResult(call)
		if err != nil {
			return nil, nil, err
		}
		if call.Error != nil {
			result.IsError = true
			if len(result.Content) == 0 {
				result.Content = []mcp.Content{&mcp.TextContent{Text: call.Error.Message}}
			}
		}
		return result, structured, nil
	}
}

func buildResult(call *Call) (*mcp.CallToolResult, map[string]any, error) {
	if call.Error != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: call.Error.Message}},
			IsError: true,
		}, nil, nil
	}
	if len(call.Result) == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "ok"}},
		}, map[string]any{}, nil
	}
	var structured map[string]any
	if err := json.Unmarshal(call.Result, &structured); err != nil {
		return nil, nil, fmt.Errorf("decode call result: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(call.Result)}},
	}, structured, nil
}

func (m *MockMCPServer) recordRequest(req *mcp.CallToolRequest, call *Call) {
	m.mu.Lock()
	args := cloneJSON(req.Params.Arguments)
	var headers http.Header
	if req.Extra != nil {
		headers = cloneHeader(req.Extra.Header)
	}
	m.received = append(m.received, IncomingRequest{
		Tool:      call.Tool,
		Arguments: args,
		Headers:   headers,
	})
	m.mu.Unlock()
	select {
	case m.requests <- struct{}{}:
	default:
	}
}

func (m *MockMCPServer) popCall(tool string) *Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, call := range m.calls {
		if call.Tool == tool {
			m.calls = append(m.calls[:i], m.calls[i+1:]...)
			return call
		}
	}
	return nil
}

func cloneCall(call Call) *Call {
	if call.ResponseHeaders != nil {
		call.ResponseHeaders = cloneHeader(call.ResponseHeaders)
	}
	if len(call.Result) > 0 {
		call.Result = cloneJSON(call.Result)
	}
	if len(call.Progress) > 0 {
		prog := make([]ProgressUpdate, len(call.Progress))
		copy(prog, call.Progress)
		call.Progress = prog
	}
	c := call
	return &c
}

func cloneJSON(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	out := make([]byte, len(raw))
	copy(out, raw)
	return json.RawMessage(out)
}

func cloneHeader(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	out := make(http.Header, len(h))
	for k, values := range h {
		copyVals := make([]string, len(values))
		copy(copyVals, values)
		out[k] = copyVals
	}
	return out
}

type headerWriter struct {
	http.ResponseWriter
	headers http.Header
	wrote   bool
}

func (h *headerWriter) WriteHeader(status int) {
	if !h.wrote {
		for key, values := range h.headers {
			for _, value := range values {
				h.ResponseWriter.Header().Add(key, value)
			}
		}
		h.wrote = true
	}
	h.ResponseWriter.WriteHeader(status)
}

func (h *headerWriter) Write(b []byte) (int, error) {
	if !h.wrote {
		h.WriteHeader(http.StatusOK)
	}
	return h.ResponseWriter.Write(b)
}

func (h *headerWriter) addHeaders(src http.Header) {
	if src == nil {
		return
	}
	if h.headers == nil {
		h.headers = make(http.Header)
	}
	for key, values := range src {
		h.headers[key] = append(h.headers[key], values...)
	}
}

func acceptsEventStream(req *http.Request) bool {
	accept := strings.Split(strings.Join(req.Header.Values("Accept"), ","), ",")
	for _, candidate := range accept {
		switch strings.TrimSpace(candidate) {
		case "text/event-stream", "text/*", "*/*":
			return true
		}
	}
	return false
}

type keepalivePingWriter struct {
	http.ResponseWriter
	injected bool
}

func (w *keepalivePingWriter) WriteHeader(status int) {
	w.ResponseWriter.WriteHeader(status)
	w.injectPing()
}

func (w *keepalivePingWriter) Write(p []byte) (int, error) {
	w.injectPing()
	return w.ResponseWriter.Write(p)
}

func (w *keepalivePingWriter) Flush() {
	w.injectPing()
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *keepalivePingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("response writer does not support hijacking")
}

func (w *keepalivePingWriter) injectPing() {
	if w.injected {
		return
	}
	w.injected = true
	_, _ = w.ResponseWriter.Write([]byte("event: ping\n"))
	_, _ = w.ResponseWriter.Write([]byte("data: ping\n\n"))
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (m *MockMCPServer) failf(format string, args ...any) {
	if tb, ok := m.tb.Load().(testing.TB); ok && tb != nil {
		tb.Helper()
		tb.Fatalf(format, args...)
		return
	}
	panic(fmt.Sprintf(format, args...))
}

func isExpectedTransportShutdownError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "server is closing") ||
		strings.Contains(lower, "broken pipe") ||
		strings.Contains(lower, "closed pipe") ||
		strings.Contains(lower, "use of closed network connection")
}

func (m *MockMCPServer) serveProtectedResourceMetadata(w http.ResponseWriter, req *http.Request) {
	handler := auth.ProtectedResourceMetadataHandler(m.getProtectedResourceMetadata())
	handler.ServeHTTP(w, req)
}

func (m *MockMCPServer) getProtectedResourceMetadata() *oauthex.ProtectedResourceMetadata {
	m.mu.Lock()
	meta := m.oauthMetadata
	base := m.baseURL
	enabled := m.enableOAuth
	m.mu.Unlock()

	if !enabled {
		return nil
	}
	if meta != nil {
		return meta
	}

	var resource string
	if base != nil {
		copyURL := *base
		copyURL.Path = ""
		copyURL.RawQuery = ""
		copyURL.Fragment = ""
		resource = copyURL.String()
	}
	meta = &oauthex.ProtectedResourceMetadata{
		Resource:        resource,
		ScopesSupported: []string{"read", "write"},
	}
	if resource != "" {
		m.mu.Lock()
		if m.oauthMetadata == nil {
			m.oauthMetadata = meta
		} else {
			meta = m.oauthMetadata
		}
		m.mu.Unlock()
	}
	return meta
}

func (m *MockMCPServer) buildProtectedResourceMetadata(base *url.URL) *oauthex.ProtectedResourceMetadata {
	if base == nil {
		return nil
	}
	copyURL := *base
	copyURL.Path = ""
	copyURL.RawQuery = ""
	copyURL.Fragment = ""
	return &oauthex.ProtectedResourceMetadata{
		Resource:        copyURL.String(),
		ScopesSupported: []string{"read", "write"},
	}
}

// APIKey is a static API key record for auth verification in tests.
type APIKey struct {
	Key    string
	UserID string
	Scopes []string
}

func defaultAPIKeys() map[string]*APIKey {
	return map[string]*APIKey{
		"sk-1234567890abcdef": {
			Key:    "sk-1234567890abcdef",
			UserID: "user1",
			Scopes: []string{"read", "write"},
		},
		"sk-abcdef1234567890": {
			Key:    "sk-abcdef1234567890",
			UserID: "user2",
			Scopes: []string{"read"},
		},
	}
}

func cloneAPIKeys(src map[string]*APIKey) map[string]*APIKey {
	if src == nil {
		return nil
	}
	out := make(map[string]*APIKey, len(src))
	for k, v := range src {
		if v == nil {
			continue
		}
		keyCopy := *v
		copyScopes := make([]string, len(v.Scopes))
		copy(copyScopes, v.Scopes)
		keyCopy.Scopes = copyScopes
		out[k] = &keyCopy
	}
	return out
}

func (m *MockMCPServer) tokenVerifier() auth.TokenVerifier {
	keys := cloneAPIKeys(m.apiKeys)
	return func(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		key, ok := keys[token]
		if !ok || key == nil {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{
			UserID:     key.UserID,
			Scopes:     append([]string{}, key.Scopes...),
			Expiration: time.Now().Add(time.Hour),
		}, nil
	}
}
