// Package tunnelclient exposes an embeddable Secure MCP Tunnel client.
//
// Applications can connect an MCP server to the OpenAI Tunnel control plane
// without binding a local port or launching a stdio child process. Create an
// in-memory transport pair with the MCP Go SDK, run the server on one side, and
// pass the other side to New.
package tunnelclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/openai/tunnel-client/pkg/app"
	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane"
	"github.com/openai/tunnel-client/pkg/types"
)

const (
	// DefaultControlPlaneBaseURL is the OpenAI Tunnel control-plane host used
	// when Config.ControlPlaneBaseURL is empty.
	DefaultControlPlaneBaseURL = "https://api.openai.com"

	defaultMaxInFlightRequests   = 20
	maxMaxInFlightRequests       = 10000
	defaultMCPConnectionMaxTTL   = 10 * time.Minute
	defaultMCPConcurrentRequests = 10
	defaultStopTimeout           = 5 * time.Second
)

var (
	// ErrClosed is returned when a stopped Client is started again.
	ErrClosed = errors.New("tunnel-client: client is closed")
)

// Config contains the settings needed by an embedded Tunnel client.
//
// TunnelID and APIKey are required. ControlPlaneBaseURL defaults to
// DefaultControlPlaneBaseURL. The SDK intentionally does not start the
// tunnel-client health/admin listener; the caller owns its application's
// process lifecycle and observability surface.
type Config struct {
	TunnelID            string
	APIKey              string
	ControlPlaneBaseURL string
	ControlPlaneURLPath string
	OrganizationID      string
	// ControlPlaneExtraHeaders adds non-authentication headers to poll,
	// response, and metadata requests. Names and values must be valid for HTTP;
	// names are case-insensitive, and conflicting case variants are rejected.
	ControlPlaneExtraHeaders map[string]string

	// MaxInFlightRequests controls the bounded control-plane command queue.
	// Zero uses the same default as the tunnel-client CLI.
	MaxInFlightRequests int
	// PollTimeout controls how long one empty control-plane poll may wait.
	// Zero uses the tunnel-client runtime default.
	PollTimeout time.Duration
	// PollDeadlineGuardrail is added to PollTimeout for the client-side HTTP
	// deadline. Zero uses the tunnel-client runtime default.
	PollDeadlineGuardrail time.Duration

	// LogLevel defaults to slog.LevelInfo. LogWriter defaults to os.Stderr.
	LogLevel  slog.Level
	LogWriter io.Writer
}

// Client owns one embedded tunnel-client runtime.
type Client struct {
	app *fx.App

	ready     chan struct{}
	readyOnce sync.Once

	mu      sync.Mutex
	started bool
	closed  bool
}

// New constructs an embedded tunnel-client runtime using transport as the MCP
// client side of an in-memory transport pair.
//
// The caller remains responsible for running its MCP server on the matching
// server-side transport. New constructs the runtime but does not start it; call
// Start or Run.
func New(cfg Config, transport mcp.Transport) (*Client, error) {
	if transport == nil {
		return nil, errors.New("tunnel-client: MCP transport is required")
	}
	internalCfg, err := buildConfig(cfg)
	if err != nil {
		return nil, err
	}

	client := &Client{ready: make(chan struct{})}
	writer := cfg.LogWriter
	if writer == nil {
		writer = os.Stderr
	}

	client.app = app.NewWithRuntime(
		internalCfg,
		app.RuntimeOptions{DisableHealthAdmin: true},
		fx.Provide(func() io.Writer { return writer }),
		fx.Provide(fx.Annotate(
			func() mcp.Transport { return transport },
			fx.ResultTags(`name:"mcp_injected_transport"`),
		)),
		fx.Decorate(func(fetcher controlplane.Fetcher) controlplane.Fetcher {
			return &readyFetcher{delegate: fetcher, ready: client.markReady}
		}),
		fx.WithLogger(func(*slog.Logger) fxevent.Logger { return fxevent.NopLogger }),
	)
	if err := client.app.Err(); err != nil {
		return nil, fmt.Errorf("tunnel-client: construct runtime: %w", err)
	}
	return client, nil
}

// Start starts control-plane polling and MCP forwarding. It is safe to call
// Start more than once before Stop; later calls are no-ops.
func (c *Client) Start(ctx context.Context) error {
	if c == nil || c.app == nil {
		return errors.New("tunnel-client: client is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if c.started {
		return nil
	}
	if err := c.app.Start(ctx); err != nil {
		return fmt.Errorf("tunnel-client: start runtime: %w", err)
	}
	c.started = true
	return nil
}

// Ready is closed after the first successful control-plane poll completes.
// It lets embedders distinguish process startup from a verified control-plane
// round trip without exposing an HTTP health listener.
func (c *Client) Ready() <-chan struct{} {
	if c == nil || c.ready == nil {
		never := make(chan struct{})
		return never
	}
	return c.ready
}

// WaitUntilReady waits for the first successful control-plane poll or for ctx
// cancellation.
func (c *Client) WaitUntilReady(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-c.Ready():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Done exposes the runtime shutdown signal channel.
func (c *Client) Done() <-chan os.Signal {
	if c == nil || c.app == nil {
		return nil
	}
	return c.app.Done()
}

// Stop stops polling and MCP forwarding. It is safe to call Stop more than
// once. A stopped Client cannot be restarted.
func (c *Client) Stop(ctx context.Context) error {
	if c == nil || c.app == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	if !c.started {
		c.closed = true
		return nil
	}
	if err := c.app.Stop(ctx); err != nil {
		return fmt.Errorf("tunnel-client: stop runtime: %w", err)
	}
	c.started = false
	c.closed = true
	return nil
}

// Run starts the runtime, blocks until ctx is canceled or the runtime receives
// a shutdown signal, and then stops it. Context cancellation is a normal
// shutdown path and is not returned as an error.
func (c *Client) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.Start(ctx); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
	case <-c.Done():
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), defaultStopTimeout)
	defer cancel()
	return c.Stop(stopCtx)
}

func (c *Client) markReady() {
	if c == nil {
		return
	}
	c.readyOnce.Do(func() {
		close(c.ready)
	})
}

type readyFetcher struct {
	delegate controlplane.Fetcher
	ready    func()
}

func (f *readyFetcher) Poll(
	ctx context.Context,
	limit int,
) ([]controlplane.PolledCommand, types.TunnelServiceRequestID, error) {
	commands, requestID, err := f.delegate.Poll(ctx, limit)
	if err == nil && f.ready != nil {
		f.ready()
	}
	return commands, requestID, err
}

func buildConfig(cfg Config) (*config.Config, error) {
	tunnelID := strings.TrimSpace(cfg.TunnelID)
	if err := config.ValidateTunnelID(tunnelID); err != nil {
		return nil, fmt.Errorf("tunnel-client: %w", err)
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("tunnel-client: API key is required")
	}

	baseURLRaw := strings.TrimSpace(cfg.ControlPlaneBaseURL)
	if baseURLRaw == "" {
		baseURLRaw = DefaultControlPlaneBaseURL
	}
	baseURL, err := url.Parse(baseURLRaw)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		if err == nil {
			err = errors.New("URL must include scheme and host")
		}
		return nil, fmt.Errorf("tunnel-client: invalid control-plane base URL: %w", err)
	}

	urlPath, err := config.NormalizeControlPlaneURLPath(cfg.ControlPlaneURLPath)
	if err != nil {
		return nil, fmt.Errorf("tunnel-client: %w", err)
	}
	if cfg.MaxInFlightRequests < 0 {
		return nil, errors.New("tunnel-client: max in-flight requests must be greater than zero")
	}
	maxInFlight := cfg.MaxInFlightRequests
	if maxInFlight == 0 {
		maxInFlight = defaultMaxInFlightRequests
	}
	if maxInFlight > maxMaxInFlightRequests {
		return nil, fmt.Errorf("tunnel-client: max in-flight requests must be less than or equal to %d", maxMaxInFlightRequests)
	}
	if cfg.PollTimeout < 0 {
		return nil, errors.New("tunnel-client: poll timeout must be greater than zero")
	}
	if cfg.PollDeadlineGuardrail < 0 {
		return nil, errors.New("tunnel-client: poll deadline guardrail must be greater than zero")
	}
	extraHeaders, err := config.NormalizeExtraHeaders("tunnel-client: control-plane extra headers", cfg.ControlPlaneExtraHeaders)
	if err != nil {
		return nil, err
	}
	if err := validateControlPlaneExtraHeaders(extraHeaders); err != nil {
		return nil, err
	}

	return &config.Config{
		ControlPlane: config.ControlPlaneConfig{
			BaseURL:               baseURL,
			URLPath:               urlPath,
			TunnelID:              types.TunnelID(tunnelID),
			OrganizationID:        strings.TrimSpace(cfg.OrganizationID),
			APIKey:                cfg.APIKey,
			MaxInFlightRequests:   maxInFlight,
			PollTimeout:           cfg.PollTimeout,
			PollDeadlineGuardrail: cfg.PollDeadlineGuardrail,
			ExtraHeaders:          extraHeaders,
		},
		Logging: config.LoggingConfig{
			Level:  cfg.LogLevel,
			Format: config.LogFormatStructText,
		},
		MCP: config.MCPConfig{
			TransportKind:         config.MCPTransportInMemory,
			ConnectionMaxTTL:      defaultMCPConnectionMaxTTL,
			MaxConcurrentRequests: defaultMCPConcurrentRequests,
			ChannelBindings: []config.MCPChannelBinding{{
				Channel:       types.DefaultChannel,
				TransportKind: config.MCPTransportInMemory,
			}},
		},
	}, nil
}

func validateControlPlaneExtraHeaders(headers map[string]string) error {
	for name := range headers {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "":
			return errors.New("tunnel-client: control-plane extra header name is required")
		case "accept", "authorization", "user-agent", "x-tunnel-client-name", "x-tunnel-client-version", "x-tunnel-mcp-server-info":
			return fmt.Errorf("tunnel-client: control-plane extra header %q cannot override authentication or client metadata", name)
		}
	}
	return nil
}
