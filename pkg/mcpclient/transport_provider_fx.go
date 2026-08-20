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

	CommandFactory *stdioCommandTransportFactory
}

func newStdioTransportProvider(p stdioProviderParams) TransportProvider {
	return stdioTransportProvider{
		commandFactory: p.CommandFactory,
	}
}

type stdioCommandTransportParams struct {
	fx.In

	Lifecycle  fx.Lifecycle
	Shutdowner fx.Shutdowner
	Logger     *slog.Logger
}

func newStdioCommandTransportFactoryProvider(p stdioCommandTransportParams) *stdioCommandTransportFactory {
	return newStdioCommandTransportFactory(p.Logger, p.Lifecycle, p.Shutdowner)
}

func newChannelStdioRuntimeInfoProvider(factory *stdioCommandTransportFactory) ChannelStdioRuntimeInfoProvider {
	if factory == nil {
		return nil
	}
	return factory
}
