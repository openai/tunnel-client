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

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane"
	dispatcherinternal "github.com/openai/tunnel-client/pkg/dispatcher/internal"
	"github.com/openai/tunnel-client/pkg/harpoon"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/types"
)

var requiredDispatcherChannels = []types.Channel{
	types.DefaultChannel,
	types.ChannelHarpoon,
}

// Params captures the dependencies needed to size the dispatcher work queue.
type Params struct {
	fx.In

	ControlPlane  *config.ControlPlaneConfig
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
	Channel                    types.Channel
	Priority                   int
	TransportKind              config.MCPTransportKind
	Transport                  mcp.Transport
	Routable                   func() bool
	SupportsMCP                bool
	SupportsOAuth              bool
	SupportsSessionTermination bool
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

func newConfiguredChannelBindings(cfg *config.MCPConfig, factory *mcpclient.ChannelTransportFactory) (configuredChannelBindingsResult, error) {
	if cfg == nil {
		return configuredChannelBindingsResult{}, fmt.Errorf("dispatcher: MCP config is required")
	}
	if factory == nil {
		return configuredChannelBindingsResult{}, fmt.Errorf("dispatcher: channel transport factory is required")
	}
	channelBindings := cfg.ChannelBindings
	if len(channelBindings) == 0 {
		mainBinding := config.MCPChannelBinding{
			Channel:       types.DefaultChannel,
			TransportKind: cfg.TransportKind,
			ServerURL:     cfg.ServerURL,
			Command:       cfg.Command,
			CommandArgs:   cfg.CommandArgs,
		}
		channelBindings = []config.MCPChannelBinding{mainBinding}
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
			transportKind = config.MCPTransportHTTPStreamable
		}
		bindings = append(bindings, dispatcherChannelBinding{
			Channel:                    channelName,
			Priority:                   0,
			TransportKind:              transportKind,
			Transport:                  transport,
			SupportsMCP:                true,
			SupportsOAuth:              channelName == types.DefaultChannel,
			SupportsSessionTermination: transportKind == config.MCPTransportHTTPStreamable,
		})
	}
	return configuredChannelBindingsResult{Bindings: bindings}, nil
}

type harpoonChannelBindingParams struct {
	fx.In

	HarpoonTransport mcp.Transport     `name:"harpoon_in_memory_transport" optional:"true"`
	HarpoonRegistry  *harpoon.Registry `optional:"true"`
}

func newHarpoonChannelBinding(p harpoonChannelBindingParams) dispatcherChannelBinding {
	transport := mcpclient.NewSharedConnectionTransport(p.HarpoonTransport)
	return dispatcherChannelBinding{
		Channel:                    types.ChannelHarpoon,
		Priority:                   0,
		TransportKind:              config.MCPTransportInMemory,
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

	Bindings []dispatcherChannelBinding `group:"dispatcher_channel_bindings"`
}

func newProcessorChannelBindings(p processorChannelBindingsParams) (map[types.Channel]dispatcherinternal.ChannelBinding, error) {
	out := make(map[types.Channel]dispatcherinternal.ChannelBinding, len(p.Bindings))
	originalByCanonical := make(map[types.Channel]types.Channel, len(p.Bindings))

	for _, binding := range p.Bindings {
		canonical := binding.Channel.Canonical()
		if canonical == "" {
			return nil, fmt.Errorf("dispatcher: channel name %q is invalid after normalization", binding.Channel)
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
			// consume another request's JSON-RPC response.
			if canonical == types.ChannelHarpoon || binding.TransportKind == config.MCPTransportStdio {
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

	missing := missingRequiredDispatcherChannels(out)
	if len(missing) > 0 {
		return nil, fmt.Errorf(
			"dispatcher: missing required channels %v (required channels: %v)",
			channelNames(missing),
			channelNames(requiredDispatcherChannels),
		)
	}
	for _, channelName := range requiredDispatcherChannels {
		binding := out[channelName]
		if !binding.SupportsMCP {
			return nil, fmt.Errorf(
				"dispatcher: required channel %q must set supportsMCP=true (required channels: %v)",
				channelName,
				channelNames(requiredDispatcherChannels),
			)
		}
	}

	return out, nil
}

func missingRequiredDispatcherChannels(channels map[types.Channel]dispatcherinternal.ChannelBinding) []types.Channel {
	missing := make([]types.Channel, 0, len(requiredDispatcherChannels))
	for _, required := range requiredDispatcherChannels {
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
