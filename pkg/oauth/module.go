package oauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jpillora/backoff"
	"go.uber.org/fx"

	tclog "github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon/hostbus"
)

var errStartupCatalogAcknowledgementUnavailable = errors.New("oauth discovery: startup catalog acknowledgement unavailable")

const (
	oauthDiscoveryRetryMin = time.Second
	oauthDiscoveryRetryMax = 30 * time.Second
)

// Module wires OAuth discovery state and fetcher.
var Module = fx.Module(
	"oauth",
	fx.Provide(NewDiscoveryState, hostbus.NewStartupCatalogState),
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

	// StartupCatalog is intentionally separate from DiscoveryState. Readiness
	// keeps its existing OAuth semantics, while startup catalog consumers wait
	// until any discovered URL bundle has also been processed by Harpoon.
	StartupCatalog *hostbus.StartupCatalogState `optional:"true"`
}

func startOAuthDiscovery(p discoveryParams) error {
	return startOAuthDiscoveryWithRetryPolicy(p, defaultOAuthDiscoveryRetryPolicy())
}

type oauthDiscoveryRetryPolicy struct {
	newBackoff func() *backoff.Backoff
	wait       func(context.Context, time.Duration) bool
}

func defaultOAuthDiscoveryRetryPolicy() oauthDiscoveryRetryPolicy {
	return oauthDiscoveryRetryPolicy{
		newBackoff: newOAuthDiscoveryBackoff,
		wait:       waitForOAuthDiscoveryRetry,
	}
}

func newOAuthDiscoveryBackoff() *backoff.Backoff {
	return &backoff.Backoff{
		Min:    oauthDiscoveryRetryMin,
		Max:    oauthDiscoveryRetryMax,
		Factor: 2,
		Jitter: true,
	}
}

func (p oauthDiscoveryRetryPolicy) backoff() *backoff.Backoff {
	if p.newBackoff != nil {
		return p.newBackoff()
	}
	return newOAuthDiscoveryBackoff()
}

func (p oauthDiscoveryRetryPolicy) waitForRetry(ctx context.Context, delay time.Duration) bool {
	if p.wait != nil {
		return p.wait(ctx, delay)
	}
	return waitForOAuthDiscoveryRetry(ctx, delay)
}

func startOAuthDiscoveryWithRetryPolicy(p discoveryParams, retryPolicy oauthDiscoveryRetryPolicy) error {
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
	startupCatalogBarrierEnabled := p.StartupCatalog != nil && hostbus.SupportsAcknowledgement(p.Bus)
	completeStartupCatalog := func(err error) {
		if p.StartupCatalog != nil {
			p.StartupCatalog.Complete(err)
		}
	}
	settleUnsupportedStartupCatalog := func() {
		if p.StartupCatalog != nil && !startupCatalogBarrierEnabled {
			// A custom legacy bus still receives discovered targets through the
			// historical Publish path, but it cannot prove registration completed.
			// Settle the stricter startup barrier with a generic failure so the
			// digest logger exits without emitting a false comparison signal.
			completeStartupCatalog(errStartupCatalogAcknowledgementUnavailable)
		}
	}
	finalizeStaticStartupCatalog := func() {
		if !startupCatalogBarrierEnabled {
			return
		}
		// Keep this asynchronous for disabled discovery paths so OAuth's
		// OnStart hook cannot wait on a registration subscriber whose own
		// OnStart hook has not run yet.
		go func() {
			if err := hostbus.PublishAndWait(ctx, p.Bus, hostbus.URLBundle{}); err != nil {
				completeStartupCatalog(err)
				logger.ErrorContext(ctx, "OAuth startup catalog finalization failed", slog.String("error", err.Error()))
				return
			}
			completeStartupCatalog(nil)
		}()
	}
	publishDiscoveredBundle := func(bundle hostbus.URLBundle) error {
		if startupCatalogBarrierEnabled {
			// Use the lifecycle context for acknowledgement waiting. A slow
			// registrar is not a hard startup failure merely because it takes
			// longer than the old delivery-only timeout.
			return hostbus.PublishAndWait(ctx, p.Bus, bundle)
		}
		// Isolated callers without the startup barrier retain the historical
		// fire-and-forget publication behavior.
		publishCtx, publishCancel := context.WithTimeout(ctx, time.Second)
		defer publishCancel()
		return p.Bus.Publish(publishCtx, bundle)
	}

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
			settleUnsupportedStartupCatalog()
			if p.MCPConfig.AllowNoMain {
				const reason = "oauth discovery disabled because the main channel is not enabled"
				p.State.Set(nil, nil, nil, nil)
				finalizeStaticStartupCatalog()
				logger.DebugContext(startCtx, reason)
				return nil
			}

			if transportKind != runtimeconfig.MCPTransportHTTPStreamable || serverURL == nil {
				reason := fmt.Sprintf("oauth discovery disabled for transport %q", transportKind)
				if serverURL == nil {
					reason = "oauth discovery server URL is not configured"
				}
				p.State.Set(nil, errors.New(reason), nil, nil)
				finalizeStaticStartupCatalog()
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
					completeStartupCatalog(err)
					logger.WarnContext(ctx, "OAuth discovery disabled", slog.String("error", err.Error()))
					return
				}

				retryBackoff := retryPolicy.backoff()
				// Retry the full discovery sequence with a fresh timeout so a
				// transient startup timeout can observe recovered probe and metadata
				// endpoints without terminally settling readiness.
				for {
					fetchCtx, fetchCancel := context.WithTimeout(ctx, DefaultDiscoveryTimeout)
					start := time.Now()
					candidates, probe, err := BuildOAuthDiscoveryCandidates(fetchCtx, p.HTTPClient, serverURL, logger)
					if err != nil {
						if errors.Is(err, context.Canceled) && ctx.Err() != nil {
							fetchCancel()
							return
						}
						p.State.Set(nil, err, nil, nil)
						completeStartupCatalog(err)
						logger.WarnContext(fetchCtx, "OAuth discovery disabled", slog.String("error", err.Error()))
						fetchCancel()
						return
					}
					candidateStrings := candidatesToStrings(candidates)
					if len(candidates) == 0 {
						err := errors.New("oauth discovery metadata URLs are not configured")
						p.State.Set(nil, err, probe, candidateStrings)
						completeStartupCatalog(err)
						logger.WarnContext(fetchCtx, "OAuth discovery disabled", slog.String("error", err.Error()))
						fetchCancel()
						return
					}

					resp, sourceURL, attempts, err := FetchOAuthMetadata(fetchCtx, p.HTTPClient, candidates, logger)
					result := BuildDiscoveryResult(resp, sourceURL, start, attempts)
					if err != nil {
						if errors.Is(err, context.Canceled) && ctx.Err() != nil {
							fetchCancel()
							return
						}
						if classifyDiscoveryFailure(err) == discoveryFailureTypeTimeoutOnly {
							retryIn := retryBackoff.Duration()
							logger.WarnContext(fetchCtx, "OAuth discovery timed out; retrying",
								slog.String("error", err.Error()),
								slog.Duration("retry_in", retryIn),
							)
							fetchCancel()
							if !retryPolicy.waitForRetry(ctx, retryIn) {
								return
							}
							continue
						}
						p.State.Set(result, err, probe, candidateStrings)
						if IsOptionalDiscoveryFailure(result, probe, err) {
							if !startupCatalogBarrierEnabled {
								completeStartupCatalog(nil)
							} else if finalizeErr := hostbus.PublishAndWait(ctx, p.Bus, hostbus.URLBundle{}); finalizeErr != nil {
								completeStartupCatalog(finalizeErr)
								logger.ErrorContext(fetchCtx, "OAuth startup catalog finalization failed", slog.String("error", finalizeErr.Error()))
							} else {
								completeStartupCatalog(nil)
							}
						} else {
							completeStartupCatalog(err)
						}
						logger.WarnContext(fetchCtx, "OAuth discovery failed", slog.String("error", err.Error()))
						fetchCancel()
						return
					}
					if resp == nil {
						err := errors.New("oauth discovery returned nil response")
						p.State.Set(result, err, probe, candidateStrings)
						completeStartupCatalog(err)
						logger.WarnContext(fetchCtx, "OAuth discovery failed", slog.String("error", err.Error()))
						fetchCancel()
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
						completeStartupCatalog(err)
						logger.ErrorContext(fetchCtx, "OAuth discovery bundle build failed", slog.String("error", err.Error()))
					} else {
						logDiscoveredURLs(logger, bundle)
						if err := publishDiscoveredBundle(bundle); err != nil {
							completeStartupCatalog(err)
							logger.ErrorContext(fetchCtx, "OAuth discovery bundle publish failed", slog.String("error", err.Error()))
						} else {
							completeStartupCatalog(nil)
						}
					}
					logger.InfoContext(fetchCtx, "OAuth discovery ProtectedResourceMetaData fetched",
						slog.Int("status_code", resp.ResponseCode()),
						slog.Int64("latency_ms", time.Since(start).Milliseconds()),
					)
					fetchCancel()
					return
				}
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

func waitForOAuthDiscoveryRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
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
