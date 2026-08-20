package mcpclient

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

// TransportProvider constructs an MCP transport for a specific transport kind.
type TransportProvider interface {
	Kind() runtimeconfig.MCPTransportKind
	Build(TransportBuildParams) (mcp.Transport, error)
}

// TransportBuildParams carries shared dependencies for transport construction.
type TransportBuildParams struct {
	Config     *runtimeconfig.MCPConfig
	Binding    runtimeconfig.MCPChannelBinding
	HTTPClient *http.Client
}

type streamableTransportProvider struct{}

func newStreamableTransportProvider() TransportProvider {
	return streamableTransportProvider{}
}

func (streamableTransportProvider) Kind() runtimeconfig.MCPTransportKind {
	return runtimeconfig.MCPTransportHTTPStreamable
}

func (streamableTransportProvider) Build(params TransportBuildParams) (mcp.Transport, error) {
	if params.Binding.ServerURL == nil {
		return nil, errors.New("mcpclient: server URL is required for http-streamable transport")
	}
	return &mcp.StreamableClientTransport{
		Endpoint:   params.Binding.ServerURL.String(),
		HTTPClient: params.HTTPClient,
	}, nil
}

type injectableTransportProvider struct {
	transport mcp.Transport
}

func (p injectableTransportProvider) Kind() runtimeconfig.MCPTransportKind {
	return runtimeconfig.MCPTransportInMemory
}

func (p injectableTransportProvider) Build(TransportBuildParams) (mcp.Transport, error) {
	if p.transport == nil {
		return nil, errors.New("mcpclient: in-memory transport requires injected transport")
	}
	return newSharedConnectionTransport(p.transport), nil
}

type stdioTransportProvider struct {
	commandFactory *stdioCommandTransportFactory
}

func (p stdioTransportProvider) Kind() runtimeconfig.MCPTransportKind {
	return runtimeconfig.MCPTransportStdio
}

func (p stdioTransportProvider) Build(params TransportBuildParams) (mcp.Transport, error) {
	if p.commandFactory == nil {
		return nil, errors.New("mcpclient: stdio transport requires mcp.command")
	}
	if len(params.Binding.CommandArgs) == 0 {
		return nil, errors.New("mcpclient: stdio transport requires mcp.command")
	}
	commandConfig := &runtimeconfig.MCPConfig{
		Command:     params.Binding.Command,
		CommandArgs: params.Binding.CommandArgs,
	}
	transport, err := p.commandFactory.transportForChannel(params.Binding.Channel).Transport(commandConfig)
	if err != nil {
		return nil, err
	}
	return newContextCancellationPreservingSharedConnectionTransport(transport), nil
}

func selectTransportProvider(kind runtimeconfig.MCPTransportKind, providers []TransportProvider) (TransportProvider, error) {
	if kind == "" {
		kind = runtimeconfig.MCPTransportHTTPStreamable
	}
	byKind := make(map[runtimeconfig.MCPTransportKind]TransportProvider, len(providers))
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		existing, ok := byKind[provider.Kind()]
		if ok && existing != nil {
			return nil, fmt.Errorf("mcpclient: multiple transport providers registered for %q", provider.Kind())
		}
		byKind[provider.Kind()] = provider
	}
	provider, ok := byKind[kind]
	if !ok || provider == nil {
		return nil, fmt.Errorf("mcpclient: no transport provider registered for %q", kind)
	}
	return provider, nil
}
