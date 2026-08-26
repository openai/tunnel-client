// Package dispatcher owns the bounded in-memory queue that decouples pollers
// from MCP workers.
package dispatcher

import (
	"context"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/controlplane"
	dispatcherinternal "github.com/openai/tunnel-client/pkg/dispatcher/internal"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon"
	"github.com/openai/tunnel-client/pkg/types"
)

var legacyRequiredDispatcherChannels = []types.Channel{
	types.DefaultChannel,
	types.ChannelHarpoon,
}

// Params captures the dependencies needed to size the dispatcher work queue.
type Params struct {
	fx.In

	ControlPlane  *runtimeconfig.ControlPlaneConfig
	MeterProvider *sdkmetric.MeterProvider
}

// Result exposes the bounded queue that downstream components consume.
type Result struct {
	fx.Out

	PolledCommandQueue controlplane.PolledCommandQueue
}

func newPolledCommandQueue(p Params) Result {
	size := 1
	if p.ControlPlane != nil && p.ControlPlane.MaxInFlightRequests > 0 {
		size = p.ControlPlane.MaxInFlightRequests
	}

	return Result{
		PolledCommandQueue: make(controlplane.PolledCommandQueue, size),
	}
}

type dispatcherChannelBinding struct {
	Channel                          types.Channel
	Priority                         int
	TransportKind                    runtimeconfig.MCPTransportKind
	Transport                        mcp.Transport
	StdioSendInitializedNotification bool
	Routable                         func() bool
	SupportsMCP                      bool
	SupportsOAuth                    bool
	SupportsSessionTermination       bool
}

// Module registers the dispatcher components with the Fx graph. It provides the
// bounded polled command queue sized according to ControlPlaneConfig, constructs
// the Processor that consumes commands from that queue and calls downstream MCP servers, and starts the listener
// goroutine that drains the queue when the app lifecycle begins.
var Module = fx.Module(
	"dispatcher",
	fx.Provide(
		newPolledCommandQueue,
		newConfiguredChannelBindings,
		fx.Annotate(newHarpoonChannelBinding, fx.ResultTags(`group:"dispatcher_channel_bindings"`)),
		newProcessorChannelBindings,
		dispatcherinternal.NewProcessor,
		dispatcherinternal.NewQueueListener,
	),
	fx.Invoke(startQueueListener),
)

type configuredChannelBindingsResult struct {
	fx.Out

	Bindings []dispatcherChannelBinding `group:"dispatcher_channel_bindings,flatten"`
}

func newConfiguredChannelBindings(cfg *runtimeconfig.MCPConfig, factory *mcpclient.ChannelTransportFactory) (configuredChannelBindingsResult, error) {
	if cfg == nil {
		return configuredChannelBindingsResult{}, fmt.Errorf("dispatcher: MCP config is required")
	}
	if factory == nil {
		return configuredChannelBindingsResult{}, fmt.Errorf("dispatcher: channel transport factory is required")
	}
	channelBindings := cfg.ChannelBindings
	if len(channelBindings) == 0 && !cfg.AllowNoMain {
		mainBinding := runtimeconfig.MCPChannelBinding{
			Channel:       types.DefaultChannel,
			TransportKind: cfg.TransportKind,
			ServerURL:     cfg.ServerURL,
			Command:       cfg.Command,
			CommandArgs:   cfg.CommandArgs,
		}
		channelBindings = []runtimeconfig.MCPChannelBinding{mainBinding}
	}
	bindings := make([]dispatcherChannelBinding, 0, len(channelBindings))
	for _, binding := range channelBindings {
		transport, err := factory.Build(binding)
		if err != nil {
			return configuredChannelBindingsResult{}, err
		}
		channelName := binding.Channel.Canonical()
		transportKind := binding.TransportKind
		if transportKind == "" {
			transportKind = runtimeconfig.MCPTransportHTTPStreamable
		}
		bindings = append(bindings, dispatcherChannelBinding{
			Channel:                          channelName,
			Priority:                         0,
			TransportKind:                    transportKind,
			Transport:                        transport,
			StdioSendInitializedNotification: cfg.StdioSendInitializedNotification,
			SupportsMCP:                      true,
			SupportsOAuth:                    channelName == types.DefaultChannel,
			SupportsSessionTermination:       transportKind == runtimeconfig.MCPTransportHTTPStreamable,
		})
	}
	return configuredChannelBindingsResult{Bindings: bindings}, nil
}

type harpoonChannelBindingParams struct {
	fx.In

	HarpoonTransport mcp.Transport                  `name:"harpoon_in_memory_transport" optional:"true"`
	HarpoonRegistry  runtimeharpoon.RegistryCounter `optional:"true"`
}

func newHarpoonChannelBinding(p harpoonChannelBindingParams) dispatcherChannelBinding {
	// Harpoon accepts 2026 self-contained requests directly, but legacy
	// tunnel-service OAuth shims still create a new initialize/initialized MCP
	// session for each call. Restart the shared in-memory SDK session on the
	// next initialize so both client generations remain compatible.
	transport := mcpclient.NewInitializeRestartingSharedConnectionTransport(p.HarpoonTransport)
	return dispatcherChannelBinding{
		Channel:                    types.ChannelHarpoon,
		Priority:                   0,
		TransportKind:              runtimeconfig.MCPTransportInMemory,
		Transport:                  transport,
		SupportsMCP:                true,
		SupportsOAuth:              false,
		SupportsSessionTermination: false,
		Routable: func() bool {
			return p.HarpoonRegistry != nil && p.HarpoonRegistry.Count() > 0
		},
	}
}

type processorChannelBindingsParams struct {
	fx.In

	Bindings     []dispatcherChannelBinding `group:"dispatcher_channel_bindings"`
	ControlPlane *runtimeconfig.ControlPlaneConfig
}

func newProcessorChannelBindings(p processorChannelBindingsParams) (map[types.Channel]dispatcherinternal.ChannelBinding, error) {
	out := make(map[types.Channel]dispatcherinternal.ChannelBinding, len(p.Bindings))
	originalByCanonical := make(map[types.Channel]types.Channel, len(p.Bindings))

	for _, binding := range p.Bindings {
		canonical := binding.Channel.Canonical()
		if canonical == "" {
			return nil, fmt.Errorf("dispatcher: channel name %q is invalid after normalization", binding.Channel)
		}
		if p.ControlPlane != nil && p.ControlPlane.PollChannelsConfigured && !containsChannel(p.ControlPlane.PollChannels, canonical) {
			continue
		}
		if original, exists := originalByCanonical[canonical]; exists {
			return nil, fmt.Errorf(
				"dispatcher: duplicate channel %q from bindings %q and %q",
				canonical,
				original,
				binding.Channel,
			)
		}
		if binding.SupportsMCP && binding.Transport == nil {
			return nil, fmt.Errorf("dispatcher: nil transport for channel %q with supportsMCP=true", canonical)
		}
		if canonical != types.DefaultChannel && binding.SupportsOAuth {
			return nil, fmt.Errorf("dispatcher: non-main channel %q must not set supportsOAuth=true", canonical)
		}

		var transport mcpclient.ForwardingTransport
		if binding.Transport != nil {
			transport = mcpclient.NewForwardingTransport(binding.Transport)
			// Stdio and Harpoon reuse a single underlying connection. Keep one
			// request lifecycle active at a time so concurrent workers cannot
			// consume another request's JSON-RPC response. Stdio also keeps its
			// child-process pipes alive when one request deadline expires and filters
			// that request's late response before the next lifecycle. Completing MCP
			// initialization for callers that omit notifications/initialized is an
			// explicit operator opt-in so legacy stdio servers keep verbatim behavior.
			if binding.TransportKind == runtimeconfig.MCPTransportStdio {
				if binding.StdioSendInitializedNotification {
					transport = mcpclient.NewStdioForwardingTransport(transport)
				} else {
					transport = mcpclient.NewSerializedForwardingTransportWithDeadlineRetirement(transport)
				}
			} else if canonical == types.ChannelHarpoon {
				transport = mcpclient.NewSerializedForwardingTransport(transport)
			}
		}
		out[canonical] = dispatcherinternal.ChannelBinding{
			Transport:                  transport,
			Priority:                   binding.Priority,
			Routable:                   binding.Routable,
			SupportsMCP:                binding.SupportsMCP,
			SupportsOAuth:              binding.SupportsOAuth,
			SupportsSessionTermination: binding.SupportsSessionTermination,
		}
		originalByCanonical[canonical] = binding.Channel
	}

	required := requiredDispatcherChannels(p.ControlPlane)
	missing := missingRequiredDispatcherChannels(out, required)
	if len(missing) > 0 {
		return nil, fmt.Errorf(
			"dispatcher: missing required channels %v (required channels: %v)",
			channelNames(missing),
			channelNames(required),
		)
	}
	for _, channelName := range required {
		binding := out[channelName]
		if !binding.SupportsMCP {
			return nil, fmt.Errorf(
				"dispatcher: required channel %q must set supportsMCP=true (required channels: %v)",
				channelName,
				channelNames(required),
			)
		}
	}

	return out, nil
}

func requiredDispatcherChannels(cfg *runtimeconfig.ControlPlaneConfig) []types.Channel {
	if cfg != nil && cfg.PollChannelsConfigured {
		return cfg.PollChannels
	}
	return legacyRequiredDispatcherChannels
}

func containsChannel(channels []types.Channel, want types.Channel) bool {
	for _, channel := range channels {
		if channel == want {
			return true
		}
	}
	return false
}

func missingRequiredDispatcherChannels(channels map[types.Channel]dispatcherinternal.ChannelBinding, requiredChannels []types.Channel) []types.Channel {
	missing := make([]types.Channel, 0, len(requiredChannels))
	for _, required := range requiredChannels {
		if _, ok := channels[required]; !ok {
			missing = append(missing, required)
		}
	}
	return missing
}

func channelNames(channels []types.Channel) []string {
	names := make([]string, 0, len(channels))
	for _, channelName := range channels {
		names = append(names, channelName.String())
	}
	sort.Strings(names)
	return names
}

type listenerParams struct {
	fx.In

	Lifecycle fx.Lifecycle
	Listener  *dispatcherinternal.QueueListener
}

func startQueueListener(p listenerParams) error {
	if p.Listener == nil {
		return fmt.Errorf("dispatcher: queue listener is nil")
	}

	ctx, cancel := context.WithCancel(context.Background())

	p.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			p.Listener.Start(ctx)
			return nil
		},
		OnStop: func(context.Context) error {
			cancel()
			p.Listener.Wait()
			return nil
		},
	})

	return nil
}
