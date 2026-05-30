package oauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"go.uber.org/fx"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/harpoon/hostbus"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
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
	MCPConfig  *config.MCPConfig
	HTTPClient *http.Client `name:"mcp_client"`
	State      *DiscoveryState
	Bus        hostbus.HostRegistrationBus
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

	transportKind := p.MCPConfig.TransportKind
	if transportKind == "" {
		transportKind = config.MCPTransportHTTPStreamable
	}
	serverURL := p.MCPConfig.ServerURL

	p.Lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if transportKind != config.MCPTransportHTTPStreamable || serverURL == nil {
				reason := fmt.Sprintf("oauth discovery disabled for transport %q", transportKind)
				if serverURL == nil {
					reason = "oauth discovery server URL is not configured"
				}
				p.State.Set(nil, errors.New(reason), nil, nil)
				logger.DebugContext(ctx, reason)
				return nil
			}

			go func() {
				fetchCtx, cancel := context.WithTimeout(context.Background(), DefaultDiscoveryTimeout)
				defer cancel()

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
						UnixSocketPath: p.MCPConfig.UnixSocketPath,
						UnixSocketURL:  p.MCPConfig.ServerURL,
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
	})

	return nil
}

func logDiscoveredURLs(logger *slog.Logger, bundle hostbus.URLBundle) {
	if logger == nil || len(bundle.URLs) == 0 {
		return
	}
	fields := make([]any, 0, len(bundle.URLs)*3)
	for idx, record := range bundle.URLs {
		fields = append(fields,
			slog.String(fmt.Sprintf("url_%d", idx), safeURL(record.URL)),
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

func safeURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.String()
}
