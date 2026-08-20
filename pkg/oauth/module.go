package oauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"go.uber.org/fx"

	tclog "github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon/hostbus"
)

// Module wires OAuth discovery state and fetcher.
var Module = fx.Module(
	"oauth",
	fx.Provide(NewDiscoveryState),
	fx.Invoke(startOAuthDiscovery),
)

type discoveryParams struct {
	fx.In

	Lifecycle  fx.Lifecycle
	Logger     *slog.Logger
	MCPConfig  *runtimeconfig.MCPConfig
	HTTPClient *http.Client `name:"mcp_client"`
	State      *DiscoveryState
	Bus        hostbus.HostRegistrationBus
	ProbeState *mcpclient.ProbeState `optional:"true"`
}

func startOAuthDiscovery(p discoveryParams) error {
	if p.Lifecycle == nil {
		return fmt.Errorf("oauth discovery: lifecycle is required")
	}
	if p.MCPConfig == nil {
		return fmt.Errorf("oauth discovery: mcp config is required")
	}
	if p.State == nil {
		return fmt.Errorf("oauth discovery: state is required")
	}
	if p.HTTPClient == nil {
		return fmt.Errorf("oauth discovery: http client is required")
	}
	if p.Logger == nil {
		return fmt.Errorf("oauth discovery: logger is required")
	}
	if p.Bus == nil {
		return fmt.Errorf("oauth discovery: host registration bus is required")
	}

	logger := p.Logger.With(tclog.FieldComponent, "oauth")
	ctx, cancel := context.WithCancel(context.Background())

	transportKind := p.MCPConfig.TransportKind
	serverURL := p.MCPConfig.ServerURL
	unixSocketPath := p.MCPConfig.UnixSocketPath
	if mainBinding := p.MCPConfig.MainChannelBinding(); mainBinding != nil {
		transportKind = mainBinding.TransportKind
		serverURL = mainBinding.ServerURL
		unixSocketPath = mainBinding.UnixSocketPath
	}
	if transportKind == "" {
		transportKind = runtimeconfig.MCPTransportHTTPStreamable
	}

	p.Lifecycle.Append(fx.Hook{
		OnStart: func(startCtx context.Context) error {
			if p.MCPConfig.AllowNoMain {
				const reason = "oauth discovery disabled because the main channel is not enabled"
				p.State.Set(nil, nil, nil, nil)
				logger.DebugContext(startCtx, reason)
				return nil
			}

			if transportKind != runtimeconfig.MCPTransportHTTPStreamable || serverURL == nil {
				reason := fmt.Sprintf("oauth discovery disabled for transport %q", transportKind)
				if serverURL == nil {
					reason = "oauth discovery server URL is not configured"
				}
				p.State.Set(nil, errors.New(reason), nil, nil)
				logger.DebugContext(startCtx, reason)
				return nil
			}

			go func() {
				if p.MCPConfig.StartupWaitTimeout > 0 {
					logger.InfoContext(ctx, "waiting for MCP startup probe before OAuth discovery",
						slog.Duration("timeout", p.MCPConfig.StartupWaitTimeout),
					)
				}
				if err := waitForMCPStartupProbe(ctx, p.MCPConfig, p.ProbeState); err != nil {
					if errors.Is(err, context.Canceled) {
						return
					}
					p.State.Set(nil, err, nil, nil)
					logger.WarnContext(ctx, "OAuth discovery disabled", slog.String("error", err.Error()))
					return
				}

				fetchCtx, fetchCancel := context.WithTimeout(ctx, DefaultDiscoveryTimeout)
				defer fetchCancel()

				start := time.Now()
				candidates, probe, err := BuildOAuthDiscoveryCandidates(fetchCtx, p.HTTPClient, serverURL, logger)
				if err != nil {
					p.State.Set(nil, err, nil, nil)
					logger.WarnContext(fetchCtx, "OAuth discovery disabled", slog.String("error", err.Error()))
					return
				}
				candidateStrings := candidatesToStrings(candidates)
				if len(candidates) == 0 {
					err := errors.New("oauth discovery metadata URLs are not configured")
					p.State.Set(nil, err, probe, candidateStrings)
					logger.WarnContext(fetchCtx, "OAuth discovery disabled", slog.String("error", err.Error()))
					return
				}

				resp, sourceURL, attempts, err := FetchOAuthMetadata(fetchCtx, p.HTTPClient, candidates, logger)
				result := BuildDiscoveryResult(resp, sourceURL, start, attempts)
				if err != nil {
					p.State.Set(result, err, probe, candidateStrings)
					logger.WarnContext(fetchCtx, "OAuth discovery failed", slog.String("error", err.Error()))
					return
				}
				if resp == nil {
					err := errors.New("oauth discovery returned nil response")
					p.State.Set(result, err, probe, candidateStrings)
					logger.WarnContext(fetchCtx, "OAuth discovery failed", slog.String("error", err.Error()))
					return
				}
				bundle, authServerMetaFetch, err := buildURLBundleFromPRMDWithAuthServerMetadata(
					fetchCtx,
					p.HTTPClient,
					resp.Payload(),
					start,
					sourceURL,
					URLBundleOptions{
						UnixSocketPath: unixSocketPath,
						UnixSocketURL:  serverURL,
						TrustedMCPURL:  serverURL,
					},
					logger,
				)
				if result != nil && authServerMetaFetch != nil {
					result.AuthServerMetadata = authServerMetaFetch
				}
				p.State.Set(result, nil, probe, candidateStrings)
				if err != nil {
					logger.ErrorContext(fetchCtx, "OAuth discovery bundle build failed", slog.String("error", err.Error()))
				} else {
					logDiscoveredURLs(logger, bundle)
					publishCtx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					if err := p.Bus.Publish(publishCtx, bundle); err != nil {
						logger.ErrorContext(fetchCtx, "OAuth discovery bundle publish failed", slog.String("error", err.Error()))
					}
				}
				logger.InfoContext(fetchCtx, "OAuth discovery ProtectedResourceMetaData fetched",
					slog.Int("status_code", resp.ResponseCode()),
					slog.Int64("latency_ms", time.Since(start).Milliseconds()),
				)
			}()

			return nil
		},
		OnStop: func(context.Context) error {
			cancel()
			return nil
		},
	})

	return nil
}

func waitForMCPStartupProbe(ctx context.Context, cfg *runtimeconfig.MCPConfig, probeState *mcpclient.ProbeState) error {
	if cfg == nil || cfg.StartupWaitTimeout <= 0 {
		return nil
	}
	if probeState == nil {
		return errors.New("oauth discovery: MCP probe state is required when startup wait is enabled")
	}
	return probeState.WaitUntilDone(ctx)
}

func logDiscoveredURLs(logger *slog.Logger, bundle hostbus.URLBundle) {
	if logger == nil || len(bundle.URLs) == 0 {
		return
	}
	fields := make([]any, 0, len(bundle.URLs)*3)
	for idx, record := range bundle.URLs {
		fields = append(fields,
			slog.String(fmt.Sprintf("url_%d", idx), tclog.RedactURL(record.URL)),
			slog.String(fmt.Sprintf("role_%d", idx), tagValue(record.Tags, hostbus.TagKeyRole)),
			slog.String(fmt.Sprintf("desc_%d", idx), record.Description),
		)
	}
	logger.Info("OAuth discovery URLs published", fields...)
}

func tagValue(tags []hostbus.Tag, key hostbus.TagKey) string {
	for _, tag := range tags {
		if tag.Key == key {
			return tag.Value
		}
	}
	return ""
}
