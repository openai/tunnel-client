package fx

import (
	"context"
	"errors"
	"log/slog"
	"os"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/controlplane"
	"go.openai.org/api/tunnel-client/pkg/controlplane/internal"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
	"go.openai.org/api/tunnel-client/pkg/proxy"
	"go.openai.org/api/tunnel-client/pkg/tlsconfig"
)

// Module wires control-plane polling into the Fx graph.
var Module = fx.Module(
	"controlplane",
	fx.Provide(newMetadataState, newTunnelServiceClient, newPoller),
	fx.Invoke(runMetadataFetch, runPoller),
)

type fetcherParams struct {
	fx.In

	Config        *config.ControlPlaneConfig
	TLSBundle     *tlsconfig.Bundle
	Logging       *config.LoggingConfig
	Logger        *slog.Logger
	MeterProvider *sdkmetric.MeterProvider
}

type clientResult struct {
	fx.Out

	Fetcher   controlplane.Fetcher
	Responder controlplane.Responder
	Client    *internal.TunnelServiceClient
}

func newTunnelServiceClient(p fetcherParams) (clientResult, error) {
	logger := p.Logger.With(tclog.FieldComponent, tclog.ComponentControlPlane)
	client, err := internal.NewTunnelServiceClient(context.Background(), p.Config, p.TLSBundle, logger, p.Logging, p.MeterProvider)
	if err != nil {
		return clientResult{}, err
	}
	route := proxy.ResolveRoute(proxy.RouteKindControlPlane, "control-plane", p.Config.BaseURL, p.Config.HTTPProxy, p.Config.HTTPProxySource, os.LookupEnv)
	logFields := []any{
		slog.String("route_kind", string(route.Kind)),
		slog.String("route_name", route.Name),
		slog.String("target_host", route.TargetHostPort),
	}
	logFields = append(logFields, proxy.LogFields(route)...)
	logger.InfoContext(context.Background(), "control-plane route resolved", logFields...)

	return clientResult{
		Fetcher:   client,
		Responder: client,
		Client:    client,
	}, nil
}

type pollerParams struct {
	fx.In

	Config             *config.ControlPlaneConfig
	PolledCommandQueue controlplane.PolledCommandQueue
	Fetcher            controlplane.Fetcher
	Logger             *slog.Logger
	MeterProvider      *sdkmetric.MeterProvider
}

func newPoller(p pollerParams) (internal.Poller, error) {
	logger := p.Logger.With(tclog.FieldComponent, tclog.ComponentControlPlane)
	if p.PolledCommandQueue == nil {
		panic("controlplane poller: dispatcher queue is nil")
	}
	queue := &queueAdapter{
		queue:  p.PolledCommandQueue,
		logger: logger,
	}
	meter := p.MeterProvider.Meter("controlplane")
	return internal.NewPoller(queue, p.Fetcher, logger, meter, p.Config.PollTimeout, p.Config.PollBackoffMin, p.Config.PollBackoffMax)
}

type runnerParams struct {
	fx.In

	Lifecycle fx.Lifecycle
	Logger    *slog.Logger
	Poller    internal.Poller
}

type metadataParams struct {
	fx.In

	Lifecycle     fx.Lifecycle
	Logger        *slog.Logger
	Client        *internal.TunnelServiceClient
	MetadataState *controlplane.MetadataState
}

func newMetadataState() *controlplane.MetadataState {
	return controlplane.NewMetadataState()
}

func runMetadataFetch(p metadataParams) error {
	logger := p.Logger.With(tclog.FieldComponent, tclog.ComponentControlPlane)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	p.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				metadata, err := p.Client.FetchTunnelMetadata(ctx)
				if err != nil {
					if errors.Is(err, context.Canceled) {
						return
					}
					var statusErr *internal.MetadataStatusError
					if errors.As(err, &statusErr) {
						logger.WarnContext(
							ctx,
							"tunnel metadata fetch failed",
							slog.Int("status_code", statusErr.StatusCode()),
							slog.String("status", statusErr.Status()),
						)
						p.MetadataState.Set(nil, err)
						return
					}
					logger.WarnContext(ctx, "tunnel metadata fetch failed", slog.String("error", err.Error()))
					p.MetadataState.Set(nil, err)
					return
				}

				p.MetadataState.Set(&controlplane.TunnelMetadata{
					ID:          metadata.ID,
					Name:        metadata.Name,
					Description: metadata.Description,
				}, nil)
				logger.InfoContext(
					ctx,
					"tunnel metadata fetched",
					slog.String("tunnel_id", metadata.ID),
					slog.String("name", metadata.Name),
					slog.String("description", metadata.Description),
				)
			}()
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			cancel()
			select {
			case <-done:
				return nil
			case <-stopCtx.Done():
				return stopCtx.Err()
			}
		},
	})

	return nil
}

func runPoller(p runnerParams) error {
	logger := p.Logger.With(tclog.FieldComponent, tclog.ComponentControlPlane)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	p.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			logger.InfoContext(ctx, "starting control-plane poller")
			go func() {
				defer close(done)
				p.Poller.Run(ctx)
			}()
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			logger.InfoContext(ctx, "stopping control-plane poller")
			cancel()
			select {
			case <-done:
				return nil
			case <-stopCtx.Done():
				return stopCtx.Err()
			}
		},
	})

	return nil
}

type queueAdapter struct {
	queue  controlplane.PolledCommandQueue
	logger *slog.Logger
}

func (q *queueAdapter) Capacity() int {
	return cap(q.queue)
}

func (q *queueAdapter) Length() int {
	return len(q.queue)
}

func (q *queueAdapter) Enqueue(ctx context.Context, cmd controlplane.PolledCommand) bool {
	select {
	case <-ctx.Done():
		return false
	case q.queue <- cmd:
		return true
	}
}
