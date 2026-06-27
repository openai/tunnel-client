package mcpclient

import (
	"context"
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

	"go.openai.org/api/tunnel-client/pkg/config"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
	"go.openai.org/api/tunnel-client/pkg/proxy"
	"go.openai.org/api/tunnel-client/pkg/tlsconfig"
	tctransport "go.openai.org/api/tunnel-client/pkg/transport"
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
	rootConfig    *config.Config
	config        *config.MCPConfig
	logger        *slog.Logger
	logging       *config.LoggingConfig
	providers     []TransportProvider
	meterProvider *sdkmetric.MeterProvider
	tlsBundle     *tlsconfig.Bundle

	mu                sync.Mutex
	transports        map[string]mcp.Transport
	httpClients       map[string]*http.Client
	dynamicTransports map[string]*tctransport.DynamicRoundTripper
	activeClientCerts map[string]*tlsconfig.ClientCertificate
	activeProxyURLs   map[string]*url.URL
	activeUnixSockets map[string]string
	activeServerURLs  map[string]*url.URL

	transportGroup  singleflight.Group
	httpClientGroup singleflight.Group
}

type channelTransportFactoryParams struct {
	fx.In

	Lifecycle          fx.Lifecycle
	RootConfig         *config.Config
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
		rootConfig:        p.RootConfig,
		config:            p.Config,
		logger:            p.Logger,
		logging:           p.Logging,
		meterProvider:     p.MeterProvider,
		tlsBundle:         p.TLSBundle,
		providers:         p.TransportProviders,
		transports:        make(map[string]mcp.Transport),
		httpClients:       make(map[string]*http.Client),
		dynamicTransports: make(map[string]*tctransport.DynamicRoundTripper),
		activeClientCerts: make(map[string]*tlsconfig.ClientCertificate),
		activeProxyURLs:   make(map[string]*url.URL),
		activeUnixSockets: make(map[string]string),
		activeServerURLs:  make(map[string]*url.URL),
	}

	if p.RootConfig != nil && p.RootConfig.Runtime.ConfigFile != "" {
		watcherCtx, watcherCancel := context.WithCancel(context.Background())
		w, err := config.NewWatcher(p.Logger)
		if err == nil {
			_ = w.Add(p.RootConfig.Runtime.ConfigFile)
			if p.TLSBundle != nil && p.TLSBundle.Path != "" {
				_ = w.Add(p.TLSBundle.Path)
			}
			go w.Start(watcherCtx, func() {
				factory.reloadConfig()
			})
			p.Lifecycle.Append(fx.Hook{
				OnStop: func(context.Context) error {
					watcherCancel()
					_ = w.Close()
					return nil
				},
			})
		} else {
			p.Logger.Warn("failed to initialize config watcher", slog.String("error", err.Error()))
		}
	}

	factory.logProxyConfig()
	return factory, nil
}

func (f *ChannelTransportFactory) reloadConfig() {
	newBundle, newProxy, err := config.ReloadDynamicMCPConfig(f.rootConfig.Runtime.ConfigFile, os.LookupEnv)
	if err != nil {
		f.logger.Error("failed to dynamically reload mcp config", slog.String("error", err.Error()))
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.tlsBundle = newBundle

	for key, dynRt := range f.dynamicTransports {
		// Try to resolve the proxy for this specific channel if a global proxy changed
		proxyURL := f.activeProxyURLs[key]
		if proxyURL == nil && newProxy != nil {
			proxyURL = newProxy
		} else if proxyURL != nil && proxyURL.String() != newProxy.String() {
			// If we had a specific proxy that wasn't the global one, keep it?
			// Actually, dynamic proxy reload targets the global MCP_HTTP_PROXY overrides.
			// Let's just use newProxy if they are using the default proxy.
			// But since we just want to update the transport:
			proxyURL = newProxy
		}

		newTransport, err := buildMcpHTTPTransport(f.logger, f.logging, f.meterProvider, f.tlsBundle, f.activeClientCerts[key], f.activeUnixSockets[key], proxyURL, f.activeServerURLs[key], f.config.ExtraHeaders, f.config.DiscoveryExtraHeaders)
		if err != nil {
			f.logger.Error("failed to rebuild dynamic transport", slog.String("channel", key), slog.String("error", err.Error()))
			continue
		}
		dynRt.Update(newTransport)
		f.logger.Info("dynamically reloaded mcp transport", slog.String("channel", key))
	}
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

		dynamicTransport := tctransport.NewDynamicRoundTripper(transport)

		client := &http.Client{Transport: dynamicTransport}
		f.mu.Lock()
		f.httpClients[key] = client
		f.dynamicTransports[key] = dynamicTransport
		f.activeClientCerts[key] = clientCertificate
		f.activeProxyURLs[key] = proxyURL
		f.activeUnixSockets[key] = unixSocketPath
		f.activeServerURLs[key] = serverURL
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
