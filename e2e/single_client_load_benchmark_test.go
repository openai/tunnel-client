package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openai/tunnel-client/pkg/localproxy"
)

const (
	loadWorkersEnv        = "TUNNEL_CLIENT_LOAD_WORKERS"
	loadMaxInFlightEnv    = "TUNNEL_CLIENT_LOAD_MAX_INFLIGHT"
	loadMCPConcurrencyEnv = "TUNNEL_CLIENT_LOAD_MCP_CONCURRENCY"
	loadPayloadBytesEnv   = "TUNNEL_CLIENT_LOAD_PAYLOAD_BYTES"
	loadRequestTimeoutEnv = "TUNNEL_CLIENT_LOAD_REQUEST_TIMEOUT"
)

type singleClientLoadConfig struct {
	workers        int
	maxInFlight    int
	mcpConcurrency int
	payloadBytes   int
	requestTimeout time.Duration
}

func defaultSingleClientLoadConfig() singleClientLoadConfig {
	return singleClientLoadConfig{
		workers:        64,
		maxInFlight:    256,
		mcpConcurrency: 64,
		payloadBytes:   1024,
		requestTimeout: 30 * time.Second,
	}
}

// TestSingleTunnelClientLoadHarnessSmoke keeps the benchmark path alive in
// ordinary go test and CI runs without performing a real load test.
func TestSingleTunnelClientLoadHarnessSmoke(t *testing.T) {
	t.Parallel()

	cfg := singleClientLoadConfig{
		workers:        2,
		maxInFlight:    4,
		mcpConcurrency: 2,
		payloadBytes:   16,
		requestTimeout: 10 * time.Second,
	}
	harness := newSingleClientLoadHarness(t, cfg)
	run := harness.runRequests(4, cfg.workers, cfg.payloadBytes, nil, nil)
	if len(run.failures) != 0 {
		t.Fatalf("load harness smoke failed: %v", run.failures[0])
	}
	if run.succeeded != run.attempted {
		t.Fatalf("load harness completed %d/%d requests", run.succeeded, run.attempted)
	}
	if got, want := harness.echoCalls.Load(), int64(run.attempted+1); got != want {
		t.Fatalf("MCP echo calls = %d, want %d (warmup plus measured calls)", got, want)
	}
}

func TestFinalJSONRPCBodyUsesLastSSEEvent(t *testing.T) {
	body := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"tool-1\",\"result\":{\"ok\":true}}\n\n")
	want := []byte("{\"jsonrpc\":\"2.0\",\"id\":\"tool-1\",\"result\":{\"ok\":true}}")
	if got := finalJSONRPCBody(body); !bytes.Equal(got, want) {
		t.Fatalf("final SSE JSON-RPC body = %s, want %s", got, want)
	}
}

// BenchmarkSingleTunnelClient starts one real tunnel-client runtime through
// localproxy, then drives concurrent JSON-RPC tool calls through its loopback
// MCP ingress. It is intentionally a benchmark rather than a normal test so
// CI compiles the load harness but does not run load by default.
func BenchmarkSingleTunnelClient(b *testing.B) {
	cfg, err := singleClientLoadConfigFromEnv()
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.SetBytes(int64(cfg.payloadBytes))

	harness := newSingleClientLoadHarness(b, cfg)
	run := harness.runRequests(
		b.N,
		cfg.workers,
		cfg.payloadBytes,
		b.ResetTimer,
		b.StopTimer,
	)
	if len(run.failures) != 0 {
		b.Fatalf("%d/%d load requests failed; first error: %v", len(run.failures), run.attempted, run.failures[0])
	}

	latencies := run.successfulLatencies()
	b.ReportMetric(float64(run.succeeded)/run.elapsed.Seconds(), "req/s")
	b.ReportMetric(durationMilliseconds(percentile(latencies, 0.50)), "p50_ms")
	b.ReportMetric(durationMilliseconds(percentile(latencies, 0.95)), "p95_ms")
	b.ReportMetric(durationMilliseconds(percentile(latencies, 0.99)), "p99_ms")
	b.ReportMetric(durationMilliseconds(percentile(latencies, 1.00)), "max_ms")
	b.ReportMetric(float64(cfg.workers), "workers")
	b.ReportMetric(float64(cfg.maxInFlight), "max_inflight")
	b.ReportMetric(float64(cfg.mcpConcurrency), "mcp_concurrency")
	b.ReportMetric(float64(cfg.payloadBytes), "payload_B")
	b.ReportMetric(float64(runtime.NumCPU()), "cpus")
	b.ReportMetric(float64(runtime.GOMAXPROCS(0)), "gomaxprocs")
	b.Logf(
		"single tunnel-client loopback load: attempted=%d succeeded=%d elapsed=%s backend=%s ingress=%s control_plane=%s payload_bytes=%d GOOS=%s GOARCH=%s CPUs=%d GOMAXPROCS=%d",
		run.attempted,
		run.succeeded,
		run.elapsed.Round(time.Millisecond),
		harness.info.Backend,
		harness.info.MCPTransport,
		harness.info.ControlPlaneTransport,
		cfg.payloadBytes,
		runtime.GOOS,
		runtime.GOARCH,
		runtime.NumCPU(),
		runtime.GOMAXPROCS(0),
	)
}

type singleClientLoadHarness struct {
	client         *http.Client
	endpoint       string
	sessionID      string
	requestTimeout time.Duration
	info           localproxy.Info
	echoCalls      *atomic.Int64
}

func newSingleClientLoadHarness(tb testing.TB, cfg singleClientLoadConfig) *singleClientLoadHarness {
	tb.Helper()

	mcpURL, echoCalls := startSingleClientLoadMCPServer(tb)
	proxy, err := localproxy.Start(context.Background(), localproxy.Options{
		Backend:          localproxy.BackendGo,
		MCPServerURLs:    []string{mcpURL},
		ReadinessTimeout: 15 * time.Second,
		LookupEnv:        loadConfigLookupEnv(cfg),
		Stdout:           io.Discard,
		Stderr:           io.Discard,
		DisableFXLogging: true,
	})
	if err != nil {
		tb.Fatalf("start single tunnel-client load harness: %v", err)
	}
	tb.Cleanup(func() {
		if err := proxy.Stop(context.Background()); err != nil {
			tb.Errorf("stop single tunnel-client load harness: %v", err)
		}
	})

	info := proxy.Info()
	if info.MCPURL == "" {
		tb.Fatalf("single tunnel-client load harness missing TCP MCP URL (transport=%s)", info.MCPTransport)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = cfg.workers*2 + 16
	transport.MaxIdleConnsPerHost = cfg.workers + 8
	client := &http.Client{Transport: transport}
	tb.Cleanup(transport.CloseIdleConnections)

	harness := &singleClientLoadHarness{
		client:         client,
		endpoint:       info.MCPURL,
		requestTimeout: cfg.requestTimeout,
		info:           info,
		echoCalls:      echoCalls,
	}
	sessionID, err := harness.initializeSession(context.Background())
	if err != nil {
		tb.Fatalf("initialize single tunnel-client load session: %v", err)
	}
	harness.sessionID = sessionID
	if _, err := harness.postToolCall(context.Background(), "warmup", "x"); err != nil {
		tb.Fatalf("warm up single tunnel-client load session: %v", err)
	}
	return harness
}

func startSingleClientLoadMCPServer(tb testing.TB) (string, *atomic.Int64) {
	tb.Helper()

	var echoCalls atomic.Int64
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "tunnel-client-load-test",
		Version: "0.0.1",
	}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "Return a tiny response for tunnel-client load tests.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		echoCalls.Add(1)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "ok"}},
		}, map[string]any{"ok": true}, nil
	})

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, nil)
	httpServer := httptest.NewServer(handler)
	tb.Cleanup(httpServer.Close)
	return httpServer.URL, &echoCalls
}

func (h *singleClientLoadHarness) initializeSession(ctx context.Context) (string, error) {
	response, err := h.postJSONRPC(ctx, nil, []byte(`{
		"jsonrpc":"2.0",
		"id":"initialize-0",
		"method":"initialize",
		"params":{
			"protocolVersion":"2025-06-18",
			"capabilities":{"sampling":{},"roots":{"listChanged":true}},
			"clientInfo":{"name":"tunnel-client-load-test","version":"0.0.1"}
		}
	}`))
	if err != nil {
		return "", err
	}
	sessionID := response.headers.Get("Mcp-Session-Id")
	if sessionID == "" {
		return "", fmt.Errorf("initialize response missing Mcp-Session-Id")
	}

	if _, err := h.postJSONRPC(ctx, sessionHeaders(sessionID), []byte(`{
		"jsonrpc":"2.0",
		"method":"notifications/initialized",
		"params":{}
	}`)); err != nil {
		return "", fmt.Errorf("send initialized notification: %w", err)
	}
	return sessionID, nil
}

func (h *singleClientLoadHarness) postToolCall(ctx context.Context, requestID string, payload string) (time.Duration, error) {
	body := []byte(`{"jsonrpc":"2.0","id":"` + requestID + `","method":"tools/call","params":{"name":"echo","arguments":{"payload":"` + payload + `"}}}`)

	started := time.Now()
	response, err := h.postJSONRPC(ctx, sessionHeaders(h.sessionID), body)
	latency := time.Since(started)
	if err != nil {
		return latency, err
	}

	finalBody := finalJSONRPCBody(response.body)
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(finalBody, &envelope); err != nil {
		return latency, fmt.Errorf("decode tool response: %w", err)
	}
	if len(bytes.TrimSpace(envelope.Error)) != 0 && !bytes.Equal(bytes.TrimSpace(envelope.Error), []byte("null")) {
		return latency, fmt.Errorf("tool response contained JSON-RPC error: %s", envelope.Error)
	}
	if len(envelope.Result) == 0 {
		return latency, fmt.Errorf("tool response missing result")
	}
	return latency, nil
}

type loadHTTPResponse struct {
	headers http.Header
	body    []byte
}

func (h *singleClientLoadHarness) postJSONRPC(ctx context.Context, headers http.Header, body []byte) (loadHTTPResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(ctx, h.requestTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, h.endpoint, bytes.NewReader(body))
	if err != nil {
		return loadHTTPResponse{}, fmt.Errorf("create JSON-RPC request: %w", err)
	}
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}

	response, err := h.client.Do(request)
	if err != nil {
		return loadHTTPResponse{}, fmt.Errorf("send JSON-RPC request: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return loadHTTPResponse{}, fmt.Errorf("read JSON-RPC response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return loadHTTPResponse{}, fmt.Errorf("JSON-RPC response status %d: %s", response.StatusCode, truncateResponse(responseBody))
	}
	return loadHTTPResponse{headers: response.Header.Clone(), body: responseBody}, nil
}

func sessionHeaders(sessionID string) http.Header {
	headers := http.Header{}
	if sessionID != "" {
		headers.Set("Mcp-Session-Id", sessionID)
	}
	return headers
}

func finalJSONRPCBody(body []byte) []byte {
	var final []byte
	for rawLine := range bytes.SplitSeq(body, []byte("\n")) {
		line := bytes.TrimSpace(rawLine)
		if payload, ok := bytes.CutPrefix(line, []byte("data: ")); ok {
			final = append(final[:0], payload...)
		}
	}
	if len(final) != 0 {
		return final
	}
	return bytes.TrimSpace(body)
}

func truncateResponse(body []byte) string {
	const limit = 256
	body = bytes.TrimSpace(body)
	if len(body) <= limit {
		return string(body)
	}
	return string(body[:limit]) + "..."
}

type singleClientLoadRun struct {
	attempted int
	succeeded int
	elapsed   time.Duration
	latencies []time.Duration
	failures  []error
}

func (h *singleClientLoadHarness) runRequests(
	requests int,
	workers int,
	payloadBytes int,
	beforeStart func(),
	afterStop func(),
) singleClientLoadRun {
	if requests <= 0 {
		return singleClientLoadRun{}
	}
	if workers <= 0 {
		workers = 1
	}

	payload := strings.Repeat("x", payloadBytes)
	latencies := make([]time.Duration, requests)
	failures := make([]error, requests)
	var next atomic.Int64
	startGate := make(chan struct{})
	var workerGroup sync.WaitGroup
	workerGroup.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer workerGroup.Done()
			<-startGate
			for {
				index := int(next.Add(1) - 1)
				if index >= requests {
					return
				}
				latency, err := h.postToolCall(context.Background(), "load-"+strconv.Itoa(index), payload)
				latencies[index] = latency
				failures[index] = err
			}
		}()
	}

	if beforeStart != nil {
		beforeStart()
	}
	started := time.Now()
	close(startGate)
	workerGroup.Wait()
	elapsed := time.Since(started)
	if afterStop != nil {
		afterStop()
	}

	run := singleClientLoadRun{
		attempted: requests,
		elapsed:   elapsed,
		latencies: latencies,
	}
	for _, err := range failures {
		if err != nil {
			run.failures = append(run.failures, err)
			continue
		}
		run.succeeded++
	}
	return run
}

func (r singleClientLoadRun) successfulLatencies() []time.Duration {
	if r.succeeded == 0 {
		return nil
	}
	latencies := make([]time.Duration, 0, r.succeeded)
	for _, latency := range r.latencies {
		if latency > 0 {
			latencies = append(latencies, latency)
		}
	}
	slices.Sort(latencies)
	return latencies
}

func percentile(sorted []time.Duration, quantile float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if quantile <= 0 {
		return sorted[0]
	}
	if quantile >= 1 {
		return sorted[len(sorted)-1]
	}
	index := int(float64(len(sorted)-1) * quantile)
	return sorted[index]
}

func durationMilliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}

func singleClientLoadConfigFromEnv() (singleClientLoadConfig, error) {
	cfg := defaultSingleClientLoadConfig()
	var err error
	if cfg.workers, err = positiveIntEnv(loadWorkersEnv, cfg.workers); err != nil {
		return singleClientLoadConfig{}, err
	}
	if cfg.maxInFlight, err = positiveIntEnv(loadMaxInFlightEnv, cfg.maxInFlight); err != nil {
		return singleClientLoadConfig{}, err
	}
	if cfg.mcpConcurrency, err = positiveIntEnv(loadMCPConcurrencyEnv, cfg.mcpConcurrency); err != nil {
		return singleClientLoadConfig{}, err
	}
	if cfg.payloadBytes, err = positiveIntEnv(loadPayloadBytesEnv, cfg.payloadBytes); err != nil {
		return singleClientLoadConfig{}, err
	}
	if cfg.maxInFlight > 10_000 {
		return singleClientLoadConfig{}, fmt.Errorf("%s must be less than or equal to 10000", loadMaxInFlightEnv)
	}
	if raw := strings.TrimSpace(os.Getenv(loadRequestTimeoutEnv)); raw != "" {
		cfg.requestTimeout, err = time.ParseDuration(raw)
		if err != nil {
			return singleClientLoadConfig{}, fmt.Errorf("invalid %s: %w", loadRequestTimeoutEnv, err)
		}
		if cfg.requestTimeout <= 0 {
			return singleClientLoadConfig{}, fmt.Errorf("%s must be greater than zero", loadRequestTimeoutEnv)
		}
	}
	return cfg, nil
}

func positiveIntEnv(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", name)
	}
	return value, nil
}

func loadConfigLookupEnv(cfg singleClientLoadConfig) func(string) (string, bool) {
	values := map[string]string{
		"CONTROL_PLANE_MAX_INFLIGHT_REQUESTS": strconv.Itoa(cfg.maxInFlight),
		"MCP_MAX_CONCURRENT_REQUESTS":         strconv.Itoa(cfg.mcpConcurrency),
		"LOG_FORMAT":                          "struct-text",
		"LOG_LEVEL":                           "error",
	}
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
