package mcpclient

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"

	"go.openai.org/api/tunnel-client/pkg/config"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
	"go.openai.org/api/tunnel-client/pkg/tlsconfig"
)

// ChannelTransportFactory builds MCP transports for configured channel bindings.
type ChannelTransportFactory struct {
	config     *config.MCPConfig
	logger     *slog.Logger
	logging    *config.LoggingConfig
	httpClient *http.Client
	providers  []TransportProvider

	mu         sync.Mutex
	transports map[string]mcp.Transport
}

type channelTransportFactoryParams struct {
	fx.In

	Config             *config.MCPConfig
	Logging            *config.LoggingConfig
	Logger             *slog.Logger
	MeterProvider      *sdkmetric.MeterProvider
	TLSBundle          *tlsconfig.Bundle
	TransportProviders []TransportProvider `group:"mcp_transport_providers"`
}

func newChannelTransportFactory(p channelTransportFactoryParams) (*ChannelTransportFactory, error) {
	if p.Config == nil || p.Logging == nil || p.Logger == nil || p.MeterProvider == nil {
		return nil, fmt.Errorf("mcpclient: channel transport factory requires config, logging, logger, and meter provider")
	}
	transport, err := buildMcpHTTPTransport(p.Logger, p.Logging, p.MeterProvider, p.TLSBundle)
	if err != nil {
		return nil, err
	}
	httpClient := &http.Client{Transport: transport}
	return &ChannelTransportFactory{
		config:     p.Config,
		logger:     p.Logger,
		logging:    p.Logging,
		httpClient: httpClient,
		providers:  p.TransportProviders,
		transports: make(map[string]mcp.Transport),
	}, nil
}

// HTTPClient returns the shared HTTP client used for streamable MCP transports.
func (f *ChannelTransportFactory) HTTPClient() *http.Client {
	if f == nil {
		return nil
	}
	return f.httpClient
}

// Build returns a cached transport for the requested binding.
func (f *ChannelTransportFactory) Build(binding config.MCPChannelBinding) (mcp.Transport, error) {
	if f == nil {
		return nil, fmt.Errorf("mcpclient: channel transport factory is nil")
	}
	channelName := binding.Channel.Canonical()
	if channelName == "" {
		return nil, fmt.Errorf("mcpclient: invalid channel name")
	}

	f.mu.Lock()
	if transport, ok := f.transports[channelName.String()]; ok {
		f.mu.Unlock()
		return transport, nil
	}
	f.mu.Unlock()

	transportKind := binding.TransportKind
	if transportKind == "" {
		transportKind = config.MCPTransportHTTPStreamable
	}
	provider, err := selectTransportProvider(transportKind, f.providers)
	if err != nil {
		return nil, err
	}
	transport, err := provider.Build(TransportBuildParams{
		Config:     f.config,
		Binding:    binding,
		HTTPClient: f.httpClient,
	})
	if err != nil {
		return nil, err
	}
	transport = f.decorateTransport(transport)

	f.mu.Lock()
	f.transports[channelName.String()] = transport
	f.mu.Unlock()

	return transport, nil
}

func (f *ChannelTransportFactory) decorateTransport(base mcp.Transport) mcp.Transport {
	if base == nil {
		return nil
	}
	if f.logging == nil || !f.logging.HTTPRawUnsafe || f.logging.Level > slog.LevelDebug {
		return base
	}
	logger := f.logger.With(tclog.FieldComponent, tclog.ComponentMcpClient, "transport", "raw_http")
	return &mcp.LoggingTransport{
		Transport: base,
		Writer:    slogWriter{logger: logger},
	}
}
