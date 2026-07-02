package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
	"go.uber.org/fx/fxtest"

	"go.openai.org/api/tunnel-client/pkg/app"
	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/controlplane"
	"go.openai.org/api/tunnel-client/pkg/harpoon"
	"go.openai.org/api/tunnel-client/pkg/mcpclient"
	"go.openai.org/api/tunnel-client/pkg/tlsconfig"
	"go.openai.org/api/tunnel-client/pkg/types"
	"go.openai.org/api/tunnel-client/testsupport/mockmcpserver"
	"go.openai.org/api/tunnel-client/testsupport/mocktunnelservice"
	"go.openai.org/api/tunnel-client/testsupport/testctx"
)

// Leave room for failure state dumps and cleanup before the outer runner stops the test.
const testDeadlineReserve = 5 * time.Second

const tunnelIntegrationSocketEnv = "TUNNEL_INTEGRATION_TUNNEL_SERVICE_SOCKET_PATH"

// TestClientInstanceHeader identifies harnessed tunnel-client instances in mock control-plane requests.
const TestClientInstanceHeader = "X-Test-Tunnel-Client-Instance"

type harnessConfig struct {
	apiKey              string
	tunnelID            types.TunnelID
	controlPlaneOptions []mocktunnelservice.Option
	mcpOptions          []mockmcpserver.Option
	clientCustomizer    func(*config.Config)
	scenarioTimeout     time.Duration
	logWriter           io.Writer
	mcpTransportKind    config.MCPTransportKind
	mcpCommandArgs      []string
	useHarpoonTransport bool
	preserveClientURLs  bool
	useUnixControlPlane bool
	useUnixMCP          bool
	beforeClientStart   func(*Harness)
	afterClientStart    func(*Harness)
	beforeClientStop    func(*Harness)
}

// HarnessOption customizes the E2E harness configuration.
type HarnessOption func(*harnessConfig)

// WithAPIKey overrides the API key used between the client and mock control plane.
func WithAPIKey(key string) HarnessOption {
	return func(cfg *harnessConfig) {
		cfg.apiKey = key
	}
}

// WithTunnelID overrides the tunnel identifier advertised to the client.
func WithTunnelID(id types.TunnelID) HarnessOption {
	return func(cfg *harnessConfig) {
		cfg.tunnelID = id
	}
}

// WithControlPlaneOptions forwards additional options to the mock control plane.
func WithControlPlaneOptions(opts ...mocktunnelservice.Option) HarnessOption {
	return func(cfg *harnessConfig) {
		cfg.controlPlaneOptions = append(cfg.controlPlaneOptions, opts...)
	}
}

// WithMCPOptions forwards additional options to the mock MCP server.
func WithMCPOptions(opts ...mockmcpserver.Option) HarnessOption {
	return func(cfg *harnessConfig) {
		cfg.mcpOptions = append(cfg.mcpOptions, opts...)
	}
}

// WithClientConfig allows tests to customize the derived tunnel-client config.
func WithClientConfig(fn func(*config.Config)) HarnessOption {
	return func(cfg *harnessConfig) {
		cfg.clientCustomizer = fn
	}
}

// WithPreserveClientURLs prevents the harness from overwriting control-plane and MCP URLs.
func WithPreserveClientURLs() HarnessOption {
	return func(cfg *harnessConfig) {
		cfg.preserveClientURLs = true
	}
}

// WithUnixControlPlane routes tunnel-client control-plane HTTP over a Unix-domain socket.
func WithUnixControlPlane() HarnessOption {
	return func(cfg *harnessConfig) {
		cfg.useUnixControlPlane = true
	}
}

// WithUnixMCP routes tunnel-client MCP HTTP over a Unix-domain socket.
func WithUnixMCP() HarnessOption {
	return func(cfg *harnessConfig) {
		cfg.useUnixMCP = true
	}
}

// WithBeforeClientStart registers a hook that runs after mocks start but before tunnel-client starts.
func WithBeforeClientStart(fn func(*Harness)) HarnessOption {
	return func(cfg *harnessConfig) {
		cfg.beforeClientStart = fn
	}
}

// WithAfterClientStart registers a hook that runs after tunnel-client starts.
func WithAfterClientStart(fn func(*Harness)) HarnessOption {
	return func(cfg *harnessConfig) {
		cfg.afterClientStart = fn
	}
}

// WithBeforeClientStop registers a hook that runs after scripted commands drain and before tunnel-client stops.
func WithBeforeClientStop(fn func(*Harness)) HarnessOption {
	return func(cfg *harnessConfig) {
		cfg.beforeClientStop = fn
	}
}

// WithScenarioTimeout overrides the time ExecuteScenarious waits for the
// scripted tunnel commands to drain before failing the test.
func WithScenarioTimeout(timeout time.Duration) HarnessOption {
	return func(cfg *harnessConfig) {
		cfg.scenarioTimeout = timeout
	}
}

// WithLogWriter overrides the writer used by the tunnel-client logging module.
// The harness always tees writes into an internal buffer so logs can be dumped
// when ExecuteScenarious encounters an error.
func WithLogWriter(w io.Writer) HarnessOption {
	return func(cfg *harnessConfig) {
		cfg.logWriter = w
	}
}

// WithInMemoryMCPTransport uses the in-memory MCP transport for this harness.
func WithInMemoryMCPTransport() HarnessOption {
	return func(cfg *harnessConfig) {
		cfg.mcpTransportKind = config.MCPTransportInMemory
	}
}

// WithHarpoonInMemoryTransport routes MCP traffic to the embedded harpoon server.
func WithHarpoonInMemoryTransport() HarnessOption {
	return func(cfg *harnessConfig) {
		cfg.mcpTransportKind = config.MCPTransportInMemory
		cfg.useHarpoonTransport = true
	}
}

// WithMCPCommand configures the client to launch an MCP server over stdio.
func WithMCPCommand(commandArgs []string) HarnessOption {
	return func(cfg *harnessConfig) {
		cfg.mcpTransportKind = config.MCPTransportStdio
		cfg.mcpCommandArgs = append([]string{}, commandArgs...)
	}
}

// Harness wires together the mock control plane, mock MCP server, and a running tunnel-client.
type Harness struct {
	ControlPlane    *mocktunnelservice.MockTunnelService
	MCP             *mockmcpserver.MockMCPServer
	HarpoonRegistry *harpoon.Registry
	MCPProbeState   *mcpclient.ProbeState
	cfg             *config.Config
	app             *fxtest.App
	clients         []*TunnelClient
	waitTimeout     time.Duration
	tunnelStarted   bool
	mcpStarted      bool
	inMemoryMCP     *mcp.InMemoryTransport
	useHarpoon      bool
	preserveURLs    bool
	beforeStart     func(*Harness)
	afterStart      func(*Harness)
	beforeStop      func(*Harness)
	logWriter       io.Writer
	logBuffer       *lockedBuffer
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TunnelClient exposes deterministic poller control for one harnessed tunnel-client instance.
type TunnelClient struct {
	name   string
	app    *fxtest.App
	poller *pollerControl
}

// Name returns the deterministic instance label added to control-plane test requests.
func (c *TunnelClient) Name() string {
	if c == nil {
		return ""
	}
	return c.name
}

// PausePoller blocks future polls and cancels any active poll before returning.
func (c *TunnelClient) PausePoller(ctx context.Context) error {
	if c == nil || c.poller == nil {
		return errors.New("tunnel-client poller control not initialized")
	}
	return c.poller.Pause(ctx)
}

// UnpausePoller allows a paused poller to resume polling.
func (c *TunnelClient) UnpausePoller() {
	if c == nil || c.poller == nil {
		return
	}
	c.poller.Unpause()
}

// WaitForPolls blocks until this client has started at least want poll cycles.
func (c *TunnelClient) WaitForPolls(ctx context.Context, want int) error {
	if c == nil || c.poller == nil {
		return errors.New("tunnel-client poller control not initialized")
	}
	return c.poller.WaitForPolls(ctx, want)
}

// PollCount returns the number of poll cycles this client has started.
func (c *TunnelClient) PollCount() int {
	if c == nil || c.poller == nil {
		return 0
	}
	return c.poller.PollCount()
}

// NewHarness configures the mocks and client wiring using the provided options.
func NewHarness(t testing.TB, opts ...HarnessOption) *Harness {
	t.Helper()

	cfg := harnessConfig{
		apiKey:           "test-api-key",
		tunnelID:         types.TunnelID("tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		mcpTransportKind: config.MCPTransportHTTPStreamable,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	controlPlaneOpts := append([]mocktunnelservice.Option{},
		mocktunnelservice.WithAPIKey(cfg.apiKey),
		mocktunnelservice.WithTunnelID(string(cfg.tunnelID)),
	)
	if cfg.useUnixControlPlane {
		socketPath := newUnixSocketPath(t, "control-plane.sock")
		t.Setenv(tunnelIntegrationSocketEnv, socketPath)
		controlPlaneOpts = append(controlPlaneOpts, mocktunnelservice.WithUnixSocketPath(socketPath))
	}
	controlPlaneOpts = append(controlPlaneOpts, cfg.controlPlaneOptions...)
	controlPlane := mocktunnelservice.NewMockTunnelService(controlPlaneOpts...)

	mcpOpts := append([]mockmcpserver.Option{}, cfg.mcpOptions...)
	mcpSocketPath := ""
	if cfg.useUnixMCP {
		mcpSocketPath = newUnixSocketPath(t, "mcp.sock")
		mcpOpts = append(mcpOpts, mockmcpserver.WithUnixSocketPath(mcpSocketPath))
	}
	mcpServer := mockmcpserver.NewMockMCPServer(mcpOpts...)

	clientCfg := &config.Config{
		ControlPlane: config.ControlPlaneConfig{
			BaseURL:             nil,
			TunnelID:            cfg.tunnelID,
			APIKey:              cfg.apiKey,
			MaxInFlightRequests: 10,
			PollBackoffMin:      50 * time.Millisecond,
			PollBackoffMax:      300 * time.Millisecond,
		},
		Logging: config.LoggingConfig{
			Level:  slog.LevelError,
			Format: config.LogFormatStructText,
		},
		Health: config.HealthConfig{
			ListenAddr: "127.0.0.1:0",
		},
		MCP: config.MCPConfig{
			ServerURL:             nil,
			UnixSocketPath:        mcpSocketPath,
			TransportKind:         cfg.mcpTransportKind,
			ConnectionMaxTTL:      time.Minute,
			MaxConcurrentRequests: 1,
		},
	}
	if len(cfg.mcpCommandArgs) > 0 {
		clientCfg.MCP.CommandArgs = append([]string{}, cfg.mcpCommandArgs...)
		clientCfg.MCP.Command = strings.Join(cfg.mcpCommandArgs, " ")
		clientCfg.MCP.TransportKind = config.MCPTransportStdio
	}
	if cfg.clientCustomizer != nil {
		cfg.clientCustomizer(clientCfg)
	}

	var logBuf lockedBuffer
	var logWriter io.Writer = &logBuf
	if cfg.logWriter != nil {
		logWriter = io.MultiWriter(cfg.logWriter, &logBuf)
	}

	return &Harness{
		ControlPlane: controlPlane,
		MCP:          mcpServer,
		cfg:          clientCfg,
		waitTimeout:  cfg.scenarioTimeout,
		useHarpoon:   cfg.useHarpoonTransport,
		preserveURLs: cfg.preserveClientURLs,
		beforeStart:  cfg.beforeClientStart,
		afterStart:   cfg.afterClientStart,
		beforeStop:   cfg.beforeClientStop,
		logWriter:    logWriter,
		logBuffer:    &logBuf,
	}
}

func newUnixSocketPath(t testing.TB, socketName string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix control-plane E2E is not supported on Windows")
	}
	dir, err := os.MkdirTemp("/tmp", "tunnel-client-e2e-")
	if err != nil {
		t.Fatalf("create unix socket temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return filepath.Join(dir, socketName)
}

// ExecuteScenarious orchestrates the tunnel lifecycle, waits for the control
// plane queue to drain, and then shuts everything down before returning.
func (h *Harness) ExecuteScenarious(t testing.TB) {
	t.Helper()
	if h.ControlPlane == nil || h.MCP == nil || h.cfg == nil {
		t.Fatalf("harness not initialized")
	}
	defer h.shutdown(t)
	h.startControlPlane(t)
	h.startMCPServer(t)
	if h.beforeStart != nil {
		h.beforeStart(h)
	}
	h.startClient(t)
	if h.afterStart != nil {
		h.afterStart(h)
	}
	ctx, cancel := h.scenarioContext(t)
	defer cancel()
	if err := h.ControlPlane.WaitUntilIdle(ctx); err != nil {
		h.dumpFailureState(t, err)
		t.Fatalf("scenario did not complete: %v", err)
	}
	if h.beforeStop != nil {
		h.beforeStop(h)
	}
}

// ExecuteScenario returns the error (if any) instead of failing the test directly.
func (h *Harness) ExecuteScenario(t testing.TB) error {
	t.Helper()
	if h.ControlPlane == nil || h.MCP == nil || h.cfg == nil {
		return errors.New("harness not initialized")
	}
	defer h.shutdown(t)
	h.startControlPlane(t)
	h.startMCPServer(t)
	if h.beforeStart != nil {
		h.beforeStart(h)
	}
	h.startClient(t)
	if h.afterStart != nil {
		h.afterStart(h)
	}
	ctx, cancel := h.scenarioContext(t)
	defer cancel()
	if err := h.ControlPlane.WaitUntilIdle(ctx); err != nil {
		return fmt.Errorf("scenario did not complete: %w", err)
	}
	if h.beforeStop != nil {
		h.beforeStop(h)
	}
	return nil
}

// WaitForMCPProbe blocks until the startup MCP probe records a result or ctx is canceled.
func (h *Harness) WaitForMCPProbe(ctx context.Context) error {
	if h == nil || h.MCPProbeState == nil {
		return errors.New("mcp probe state not initialized")
	}
	return h.MCPProbeState.WaitUntilDone(ctx)
}

// PrimaryClient returns the first tunnel-client instance started by the harness.
func (h *Harness) PrimaryClient() *TunnelClient {
	if h == nil || len(h.clients) == 0 {
		return nil
	}
	return h.clients[0]
}

// StartAdditionalClient starts another tunnel-client against the harnessed mocks.
func (h *Harness) StartAdditionalClient(t testing.TB) *TunnelClient {
	t.Helper()
	if h == nil || h.ControlPlane == nil || h.MCP == nil || h.cfg == nil {
		t.Fatalf("harness not initialized")
		return nil
	}
	if !h.tunnelStarted || !h.mcpStarted {
		t.Fatalf("start harness mocks before starting another tunnel-client")
		return nil
	}
	return h.startTunnelClient(t)
}

func (h *Harness) scenarioContext(t testing.TB) (context.Context, context.CancelFunc) {
	ctx, cancel := testctx.WithDeadline(t, testDeadlineReserve)
	if h.waitTimeout <= 0 {
		return ctx, cancel
	}
	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, h.waitTimeout)
	return timeoutCtx, func() {
		timeoutCancel()
		cancel()
	}
}

func (h *Harness) dumpFailureState(t testing.TB, cause error) {
	if t == nil {
		return
	}
	t.Helper()
	if cause != nil {
		t.Logf("scenario error: %v", cause)
	}
	if h.ControlPlane != nil {
		pending, awaiting := h.ControlPlane.PendingScriptDebugInfo()
		for i, cmd := range pending {
			t.Logf("pending command[%d]: %s", i, string(cmd))
		}
		for i, cmd := range awaiting {
			t.Logf("awaiting response[%d]: %s", i, string(cmd))
		}
	}
	if h.logBuffer != nil {
		if h.logBuffer.Len() == 0 {
			t.Log("tunnel-client logs: <empty>")
		} else {
			t.Logf("tunnel-client logs:\n%s", h.logBuffer.String())
		}
	}
}

func (h *Harness) startControlPlane(t testing.TB) {
	t.Helper()
	if h.tunnelStarted {
		return
	}
	h.ControlPlane.Start(t)
	if h.ControlPlane.BaseURL() == nil {
		t.Fatalf("mock control plane failed to expose a base URL")
	}
	h.tunnelStarted = true
}

func (h *Harness) startMCPServer(t testing.TB) {
	t.Helper()
	if h.mcpStarted {
		return
	}
	switch h.cfg.MCP.TransportKind {
	case config.MCPTransportInMemory:
		if h.useHarpoon {
			return
		}
		h.inMemoryMCP = h.MCP.StartInMemory(t)
	case config.MCPTransportStdio:
		h.mcpStarted = true
		return
	default:
		h.MCP.Start(t)
		if h.MCP.BaseURL() == nil {
			t.Fatalf("mock MCP server failed to expose a base URL")
		}
	}
	h.mcpStarted = true
}

func (h *Harness) startClient(t testing.TB) {
	t.Helper()
	if h.app != nil {
		t.Fatalf("tunnel-client already running")
		return
	}
	client := h.startTunnelClient(t)
	if client == nil {
		return
	}
	h.app = client.app
}

func (h *Harness) startTunnelClient(t testing.TB) *TunnelClient {
	t.Helper()
	cfg := h.cloneConfig()
	if cfg == nil {
		t.Fatalf("missing tunnel-client configuration")
		return nil
	}
	ctrlURL := h.ControlPlane.BaseURL()
	if ctrlURL == nil {
		t.Fatalf("control plane must be started before the client")
		return nil
	}
	if !h.preserveURLs || cfg.ControlPlane.BaseURL == nil {
		cfg.ControlPlane.BaseURL = ctrlURL
	}
	transportKind := cfg.MCP.TransportKind
	if transportKind == "" {
		transportKind = config.MCPTransportHTTPStreamable
	}
	switch transportKind {
	case config.MCPTransportHTTPStreamable:
		if !h.preserveURLs || cfg.MCP.ServerURL == nil {
			mcpURL := h.MCP.BaseURL()
			if mcpURL == nil {
				t.Fatalf("mock MCP server must be started before the client")
				return nil
			}
			cfg.MCP.ServerURL = mcpURL
		}
	case config.MCPTransportInMemory, config.MCPTransportStdio:
		if transportKind == config.MCPTransportInMemory && h.inMemoryMCP == nil && !h.useHarpoon {
			t.Fatalf("mock MCP in-memory transport must be started before the client")
			return nil
		}
		if transportKind == config.MCPTransportStdio && len(cfg.MCP.CommandArgs) == 0 {
			t.Fatalf("mcp.command is required for stdio transport")
			return nil
		}
	default:
		t.Fatalf("unsupported MCP transport kind: %s", transportKind)
		return nil
	}
	if len(cfg.MCP.ChannelBindings) == 0 {
		cfg.MCP.ChannelBindings = []config.MCPChannelBinding{{
			Channel:        types.DefaultChannel,
			TransportKind:  cfg.MCP.TransportKind,
			ServerURL:      cfg.MCP.ServerURL,
			UnixSocketPath: cfg.MCP.UnixSocketPath,
			Command:        cfg.MCP.Command,
			CommandArgs:    cfg.MCP.CommandArgs,
		}}
	}
	clientName := fmt.Sprintf("client-%d", len(h.clients)+1)
	if cfg.ControlPlane.ExtraHeaders == nil {
		cfg.ControlPlane.ExtraHeaders = make(map[string]string)
	}
	cfg.ControlPlane.ExtraHeaders[TestClientInstanceHeader] = clientName
	logWriter := h.logWriter
	if logWriter == nil {
		logWriter = io.Discard
	}
	poller := newPollerControl()
	var (
		harpoonRegistry *harpoon.Registry
		mcpProbeState   *mcpclient.ProbeState
	)
	options := []fx.Option{
		fx.Provide(func() io.Writer { return logWriter }),
		fx.WithLogger(func(*slog.Logger) fxevent.Logger { return fxevent.NopLogger }),
		fx.Populate(&harpoonRegistry, &mcpProbeState),
		fx.Decorate(func(fetcher controlplane.Fetcher) controlplane.Fetcher {
			return poller.wrap(fetcher)
		}),
	}
	if h.useHarpoon {
		options = append(options, fx.Provide(fx.Annotate(
			func(transport mcp.Transport) mcp.Transport { return transport },
			fx.ParamTags(`name:"harpoon_in_memory_transport"`),
			fx.ResultTags(`name:"mcp_injected_transport"`),
		)))
	} else if h.inMemoryMCP != nil {
		options = append(options, fx.Provide(fx.Annotate(
			func() mcp.Transport { return h.inMemoryMCP },
			fx.ResultTags(`name:"mcp_injected_transport"`),
		)))
	}
	app := fxtest.New(t, app.Options(cfg, options...)...)
	app.RequireStart()
	client := &TunnelClient{
		name:   clientName,
		app:    app,
		poller: poller,
	}
	h.clients = append(h.clients, client)
	if len(h.clients) == 1 {
		h.HarpoonRegistry = harpoonRegistry
		h.MCPProbeState = mcpProbeState
	}
	return client
}

func (h *Harness) shutdown(t testing.TB) {
	t.Helper()
	for i := len(h.clients) - 1; i >= 0; i-- {
		client := h.clients[i]
		if client == nil || client.app == nil {
			continue
		}
		client.app.RequireStop()
		client.app = nil
	}
	h.clients = nil
	h.app = nil
	if h.MCP != nil && h.mcpStarted {
		h.MCP.Close()
		h.mcpStarted = false
	}
	if h.ControlPlane != nil && h.tunnelStarted {
		h.ControlPlane.Close()
		h.tunnelStarted = false
	}
}

type controlledFetcher struct {
	delegate controlplane.Fetcher
	control  *pollerControl
}

func (f *controlledFetcher) Poll(
	ctx context.Context,
	limit int,
) ([]controlplane.PolledCommand, types.TunnelServiceRequestID, error) {
	pollCtx, done, err := f.control.beginPoll(ctx)
	if err != nil {
		return nil, "", err
	}
	defer done()
	return f.delegate.Poll(pollCtx, limit)
}

type pollerControl struct {
	mu            sync.Mutex
	paused        bool
	resumeCh      chan struct{}
	activeCancels map[int]context.CancelFunc
	activeStateCh chan struct{}
	pollCount     int
	pollStateCh   chan struct{}
	nextPollID    int
}

func newPollerControl() *pollerControl {
	return &pollerControl{
		activeCancels: make(map[int]context.CancelFunc),
		activeStateCh: make(chan struct{}),
		pollStateCh:   make(chan struct{}),
	}
}

func (c *pollerControl) wrap(delegate controlplane.Fetcher) controlplane.Fetcher {
	return &controlledFetcher{delegate: delegate, control: c}
}

func (c *pollerControl) beginPoll(ctx context.Context) (context.Context, func(), error) {
	for {
		c.mu.Lock()
		if !c.paused {
			pollCtx, cancel := context.WithCancel(ctx)
			pollID := c.nextPollID
			c.nextPollID++
			c.activeCancels[pollID] = cancel
			c.pollCount++
			c.signalPollStateLocked()
			c.signalActiveStateLocked()
			c.mu.Unlock()
			return pollCtx, func() { c.finishPoll(pollID, cancel) }, nil
		}
		resumeCh := c.resumeCh
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-resumeCh:
		}
	}
}

func (c *pollerControl) finishPoll(pollID int, cancel context.CancelFunc) {
	cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.activeCancels, pollID)
	c.signalActiveStateLocked()
}

func (c *pollerControl) Pause(ctx context.Context) error {
	c.mu.Lock()
	if !c.paused {
		c.paused = true
		c.resumeCh = make(chan struct{})
	}
	cancels := make([]context.CancelFunc, 0, len(c.activeCancels))
	for _, cancel := range c.activeCancels {
		cancels = append(cancels, cancel)
	}
	c.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	return c.waitUntilIdle(ctx)
}

func (c *pollerControl) Unpause() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.paused {
		return
	}
	c.paused = false
	close(c.resumeCh)
	c.resumeCh = nil
}

func (c *pollerControl) WaitForPolls(ctx context.Context, want int) error {
	if want <= 0 {
		return nil
	}
	for {
		c.mu.Lock()
		if c.pollCount >= want {
			c.mu.Unlock()
			return nil
		}
		state := c.pollStateCh
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-state:
		}
	}
}

func (c *pollerControl) PollCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pollCount
}

func (c *pollerControl) waitUntilIdle(ctx context.Context) error {
	for {
		c.mu.Lock()
		if len(c.activeCancels) == 0 {
			c.mu.Unlock()
			return nil
		}
		state := c.activeStateCh
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-state:
		}
	}
}

func (c *pollerControl) signalActiveStateLocked() {
	close(c.activeStateCh)
	c.activeStateCh = make(chan struct{})
}

func (c *pollerControl) signalPollStateLocked() {
	close(c.pollStateCh)
	c.pollStateCh = make(chan struct{})
}

func (h *Harness) cloneConfig() *config.Config {
	if h.cfg == nil {
		return nil
	}
	clone := *h.cfg
	clone.ControlPlane = h.cfg.ControlPlane
	if h.cfg.ControlPlane.ExtraHeaders != nil {
		clone.ControlPlane.ExtraHeaders = make(map[string]string, len(h.cfg.ControlPlane.ExtraHeaders))
		for k, v := range h.cfg.ControlPlane.ExtraHeaders {
			clone.ControlPlane.ExtraHeaders[k] = v
		}
	}
	clone.Logging = h.cfg.Logging
	clone.Health = h.cfg.Health
	clone.Process = h.cfg.Process
	clone.MCP = h.cfg.MCP
	if h.cfg.MCP.ExtraHeaders != nil {
		clone.MCP.ExtraHeaders = make(map[string]string, len(h.cfg.MCP.ExtraHeaders))
		for k, v := range h.cfg.MCP.ExtraHeaders {
			clone.MCP.ExtraHeaders[k] = v
		}
	}
	if h.cfg.MCP.DiscoveryExtraHeaders != nil {
		clone.MCP.DiscoveryExtraHeaders = make(map[string]string, len(h.cfg.MCP.DiscoveryExtraHeaders))
		for k, v := range h.cfg.MCP.DiscoveryExtraHeaders {
			clone.MCP.DiscoveryExtraHeaders[k] = v
		}
	}
	clone.AdminUI = h.cfg.AdminUI
	clone.Harpoon = h.cfg.Harpoon
	clone.TLS = h.cfg.TLS
	return &clone
}

// SetTLSBundle updates the TLS bundle used by the harnessed tunnel-client.
func (h *Harness) SetTLSBundle(bundle *tlsconfig.Bundle) {
	if h == nil || h.cfg == nil {
		return
	}
	h.cfg.TLS = bundle
}
