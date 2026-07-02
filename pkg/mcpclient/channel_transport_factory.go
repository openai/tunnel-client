package mcpclient

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"
	"golang.org/x/sync/singleflight"

	"github.com/openai/tunnel-client/pkg/config"
	tclog "github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/proxy"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
)

// ChannelTransportFactory builds and caches MCP transports for configured
// channel bindings.
//
// A connector request can arrive on any logical tunnel-service channel. The
// dispatcher asks this factory for the binding-specific transport, and the
// factory keeps one cached transport/HTTP client per channel so session headers,
// proxy selection, mTLS config, and raw-HTTP logging remain stable across
// requests for that channel.
type ChannelTransportFactory struct {
	config        *config.MCPConfig
	logger        *slog.Logger
	logging       *config.LoggingConfig
	providers     []TransportProvider
	meterProvider *sdkmetric.MeterProvider
	tlsBundle     *tlsconfig.Bundle

	mu          sync.Mutex
	transports  map[string]mcp.Transport
	httpClients map[string]*http.Client

	transportGroup  singleflight.Group
	httpClientGroup singleflight.Group
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
	factory := &ChannelTransportFactory{
		config:        p.Config,
		logger:        p.Logger,
		logging:       p.Logging,
		meterProvider: p.MeterProvider,
		tlsBundle:     p.TLSBundle,
		providers:     p.TransportProviders,
		transports:    make(map[string]mcp.Transport),
		httpClients:   make(map[string]*http.Client),
	}
	factory.logProxyConfig()
	return factory, nil
}

// HTTPClientForBinding returns the HTTP client used for streamable MCP transports for a binding.
func (f *ChannelTransportFactory) HTTPClientForBinding(binding config.MCPChannelBinding) (*http.Client, error) {
	if f == nil {
		return nil, fmt.Errorf("mcpclient: channel transport factory is nil")
	}
	transportKind := binding.TransportKind
	if transportKind == "" {
		transportKind = config.MCPTransportHTTPStreamable
	}
	if transportKind != config.MCPTransportHTTPStreamable {
		return f.httpClientForKey("default", nil, "", nil, nil)
	}
	channelName := binding.Channel.Canonical()
	if channelName == "" {
		return nil, fmt.Errorf("mcpclient: invalid channel name")
	}
	return f.httpClientForKey(channelName.String(), binding.ServerURL, binding.UnixSocketPath, binding.HTTPProxy, binding.ClientCertificate)
}

// Build returns a cached transport for the requested binding. Concurrent first
// use of the same channel is collapsed with singleflight so duplicate connector
// traffic cannot race into multiple stdio child processes or independent HTTP
// transport wrappers.
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

	result, err, _ := f.transportGroup.Do(channelName.String(), func() (any, error) {
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
		httpClient, err := f.HTTPClientForBinding(binding)
		if err != nil {
			return nil, err
		}
		transport, err := provider.Build(TransportBuildParams{
			Config:     f.config,
			Binding:    binding,
			HTTPClient: httpClient,
		})
		if err != nil {
			return nil, err
		}
		transport = f.decorateTransport(transport)

		f.mu.Lock()
		f.transports[channelName.String()] = transport
		f.mu.Unlock()

		return transport, nil
	})
	if err != nil {
		return nil, err
	}
	transport, ok := result.(mcp.Transport)
	if !ok {
		return nil, fmt.Errorf("mcpclient: unexpected transport type %T", result)
	}
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

func (f *ChannelTransportFactory) httpClientForKey(key string, serverURL *url.URL, unixSocketPath string, proxyURL *url.URL, clientCertificate *tlsconfig.ClientCertificate) (*http.Client, error) {
	f.mu.Lock()
	if client, ok := f.httpClients[key]; ok {
		f.mu.Unlock()
		return client, nil
	}
	f.mu.Unlock()
	result, err, _ := f.httpClientGroup.Do(key, func() (any, error) {
		f.mu.Lock()
		if client, ok := f.httpClients[key]; ok {
			f.mu.Unlock()
			return client, nil
		}
		f.mu.Unlock()
		transport, err := buildMcpHTTPTransport(f.logger, f.logging, f.meterProvider, f.tlsBundle, clientCertificate, unixSocketPath, proxyURL, serverURL, f.config.ExtraHeaders, f.config.DiscoveryExtraHeaders)
		if err != nil {
			return nil, err
		}
		client := &http.Client{Transport: transport}
		f.mu.Lock()
		f.httpClients[key] = client
		f.mu.Unlock()
		return client, nil
	})
	if err != nil {
		return nil, err
	}
	client, ok := result.(*http.Client)
	if !ok {
		return nil, fmt.Errorf("mcpclient: unexpected http client type %T", result)
	}
	return client, nil
}

func (f *ChannelTransportFactory) logProxyConfig() {
	if f == nil || f.logger == nil || f.config == nil {
		return
	}
	logger := f.logger.With(tclog.FieldComponent, tclog.ComponentMcpClient)
	for _, binding := range f.config.ChannelBindings {
		channel := binding.Channel.Canonical()
		transportKind := binding.TransportKind
		if transportKind == "" {
			transportKind = config.MCPTransportHTTPStreamable
		}
		var targetURL *url.URL
		if transportKind == config.MCPTransportHTTPStreamable {
			targetURL = binding.ServerURL
		}
		route := proxy.ResolveRoute(proxy.RouteKindMCPChannel, channel.String(), targetURL, binding.HTTPProxy, binding.HTTPProxySource, os.LookupEnv)
		fields := []any{
			slog.String("channel", channel.String()),
			slog.String("transport", string(transportKind)),
			slog.Bool("mtls_enabled", binding.ClientCertificate != nil),
			slog.String("route_kind", string(route.Kind)),
			slog.String("route_name", route.Name),
			slog.String("target_host", route.TargetHostPort),
		}
		if binding.ClientCertificate != nil {
			fields = append(fields, slog.String("mtls_cert_path", binding.ClientCertificate.CertPath))
		}
		if binding.UnixSocketPath != "" {
			fields = append(fields, slog.String("unix_socket_path", binding.UnixSocketPath))
		}
		fields = append(fields, proxy.LogFields(route)...)
		logger.Info("mcp channel route resolved", fields...)
	}
}
