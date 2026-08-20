package mcpclient

import (
	"log/slog"
	"sync"

	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/types"
)

// ChannelStdioRuntimeInfoProvider exposes stdio process details per channel.
type ChannelStdioRuntimeInfoProvider interface {
	StdioRuntimeInfo(channel types.Channel) (StdioRuntimeInfo, bool)
}

type stdioCommandTransportFactory struct {
	logger     *slog.Logger
	lifecycle  fx.Lifecycle
	shutdowner fx.Shutdowner

	mu         sync.Mutex
	transports map[types.Channel]*stdioCommandTransport
}

func newStdioCommandTransportFactory(logger *slog.Logger, lifecycle fx.Lifecycle, shutdowner fx.Shutdowner) *stdioCommandTransportFactory {
	return &stdioCommandTransportFactory{
		logger:     logger,
		lifecycle:  lifecycle,
		shutdowner: shutdowner,
		transports: make(map[types.Channel]*stdioCommandTransport),
	}
}

func (f *stdioCommandTransportFactory) transportForChannel(channel types.Channel) *stdioCommandTransport {
	f.mu.Lock()
	defer f.mu.Unlock()
	canonical := channel.Canonical()
	if transport := f.transports[canonical]; transport != nil {
		return transport
	}
	transport := newStdioCommandTransport(f.logger, f.lifecycle, f.shutdowner)
	f.transports[canonical] = transport
	return transport
}

func (f *stdioCommandTransportFactory) StdioRuntimeInfo(channel types.Channel) (StdioRuntimeInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	canonical := channel.Canonical()
	transport := f.transports[canonical]
	if transport == nil {
		return StdioRuntimeInfo{}, false
	}
	return transport.StdioRuntimeInfo(), true
}
