package mcpclient

import (
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/fx"
)

type injectableProviderParams struct {
	fx.In

	Transport mcp.Transport `name:"mcp_injected_transport" optional:"true"`
}

func newInjectableTransportProvider(p injectableProviderParams) TransportProvider {
	return injectableTransportProvider{transport: p.Transport}
}

type stdioProviderParams struct {
	fx.In

	CommandTransport *stdioCommandTransport
}

func newStdioTransportProvider(p stdioProviderParams) TransportProvider {
	return stdioTransportProvider{
		commandTransport: p.CommandTransport,
	}
}

type stdioCommandTransportParams struct {
	fx.In

	Lifecycle  fx.Lifecycle
	Shutdowner fx.Shutdowner
	Logger     *slog.Logger
}

func newStdioCommandTransportProvider(p stdioCommandTransportParams) *stdioCommandTransport {
	return newStdioCommandTransport(p.Logger, p.Lifecycle, p.Shutdowner)
}

func newStdioRuntimeInfoProvider(transport *stdioCommandTransport) StdioRuntimeInfoProvider {
	return transport
}
