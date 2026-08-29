package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jpillora/backoff"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/openai/tunnel-client/pkg/controlplane"
	"github.com/openai/tunnel-client/pkg/controlplane/apierror"
	wiretypes "github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	tclog "github.com/openai/tunnel-client/pkg/log"
	tcmetrics "github.com/openai/tunnel-client/pkg/metrics"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
	tctransport "github.com/openai/tunnel-client/pkg/transport"
	"github.com/openai/tunnel-client/pkg/tunnelctx"
	"github.com/openai/tunnel-client/pkg/types"
	"github.com/openai/tunnel-client/pkg/version"
)

const (
	pollPathFormat                        = "/v1/tunnels/%s/poll"
	responsePathFormat                    = "/v1/tunnels/%s/response"
	metadataPathFormat                    = "/v1/tunnels/%s"
	managedCloudflarePathFormat           = "/v1/tunnels/%s/cloudflare/runtime"
	maxControlPlaneErrorBodySize          = 64 * 1024
	tunnelIntegrationSocketEnv            = "TUNNEL_INTEGRATION_TUNNEL_SERVICE_SOCKET_PATH"
	defaultResponseRetryAttempts          = 3
	defaultManagedCloudflareRetryAttempts = 3
	// The default tunnel-service route clamps requested poll waits below five
	// seconds, so a learned proxy cutoff cannot safely reduce the wire timeout
	// below this in the supported deployment.
	minimumAdaptiveProxyPollTimeout = 5 * time.Second
	// Keep enough distance from an observed idle cutoff even when operators
	// configure a smaller client deadline guardrail.
	minimumAdaptiveProxyPollMargin = 5 * time.Second
)

var errMissingConfig = errors.New("controlplane client: config is required")

// TunnelServiceClient implements the Fetcher and Responder interfaces backed by
// the control-plane HTTP API.
type TunnelServiceClient struct {
	client                         *http.Client
	secretClient                   *http.Client
	pollEndpoint                   *url.URL
	responseEndpoint               *url.URL
	metadataEndpoint               *url.URL
	managedCloudflareEndpoint      *url.URL
	logger                         *slog.Logger
	tunnelID                       types.TunnelID
	apiKey                         string
	userAgent                      string
	pollTimeout                    time.Duration
	pollGuardrail                  time.Duration
	usesProxy                      bool
	learnedProxyPollTimeout        atomic.Int64
	pollChannels                   []types.Channel
	pollChannelsConfigured         bool
	now                            func() time.Time
	pollElapsedSince               func(time.Time) time.Duration
	responseRetryAttempts          int
	newResponseBackoff             func() *backoff.Backoff
	managedCloudflareRetryAttempts int
	newManagedCloudflareBackoff    func() *backoff.Backoff
	retrySleep                     func(context.Context, time.Duration) bool
}

// NewTunnelServiceClient constructs an HTTP-backed client using the provided runtimeconfig.
func NewTunnelServiceClient(ctx context.Context, cfg *runtimeconfig.ControlPlaneConfig, tlsBundle *tlsconfig.Bundle, logger *slog.Logger, loggingCfg *runtimeconfig.LoggingConfig, meterProvider otelmetric.MeterProvider) (*TunnelServiceClient, error) {
	if cfg == nil {
		return nil, errMissingConfig
	}
	if cfg.BaseURL == nil {
		return nil, errors.New("controlplane client: control-plane.base-url is required")
	}
	if cfg.TunnelID == "" {
		return nil, errors.New("controlplane client: control-plane.tunnel-id is required")
	}
	if cfg.APIKey == "" {
		return nil, errors.New("controlplane client: control-plane.api-key is required")
	}
	if err := runtimeconfig.ValidateControlPlaneAPIKey(cfg.APIKey); err != nil {
		return nil, fmt.Errorf("controlplane client: %w", err)
	}
	if meterProvider == nil {
		return nil, errors.New("controlplane client: meter provider is required")
	}

	if logger == nil {
		return nil, errors.New("controlplane client: logger is required")
	}

	tunnelIDSegment := url.PathEscape(cfg.TunnelID.String())
	pollEndpoint := runtimeconfig.ResolveControlPlanePath(cfg.BaseURL, cfg.URLPath, fmt.Sprintf(pollPathFormat, tunnelIDSegment))
	responseEndpoint := runtimeconfig.ResolveControlPlanePath(cfg.BaseURL, cfg.URLPath, fmt.Sprintf(responsePathFormat, tunnelIDSegment))
	metadataEndpoint := runtimeconfig.ResolveControlPlanePath(cfg.BaseURL, cfg.URLPath, fmt.Sprintf(metadataPathFormat, tunnelIDSegment))
	managedCloudflareEndpoint := runtimeconfig.ResolveControlPlanePath(cfg.BaseURL, cfg.URLPath, fmt.Sprintf(managedCloudflarePathFormat, tunnelIDSegment))

	pollTimeout := cfg.PollTimeoutOrDefault()
	pollGuardrail := cfg.PollDeadlineGuardrailOrDefault()
	pollDeadline := cfg.PollDeadlineTimeoutOrDefault()
	usesProxy := controlPlaneUsesProxy(cfg, http.ProxyFromEnvironment, os.LookupEnv)

	transport, err := buildControlPlaneHTTPTransport(cfg, tlsBundle, logger, loggingCfg, meterProvider)
	if err != nil {
		return nil, err
	}
	secretTransport, err := buildSecretControlPlaneHTTPTransport(cfg, tlsBundle, logger, meterProvider)
	if err != nil {
		return nil, err
	}
	tlsconfig.LogBundleUsage(logger, tlsBundle)

	client := &TunnelServiceClient{
		client: &http.Client{
			Timeout:   pollDeadline,
			Transport: transport,
		},
		secretClient: &http.Client{
			Timeout:   pollDeadline,
			Transport: secretTransport,
		},
		pollEndpoint:                   pollEndpoint,
		responseEndpoint:               responseEndpoint,
		metadataEndpoint:               metadataEndpoint,
		managedCloudflareEndpoint:      managedCloudflareEndpoint,
		logger:                         logger,
		tunnelID:                       cfg.TunnelID,
		apiKey:                         cfg.APIKey,
		userAgent:                      version.UserAgent,
		pollTimeout:                    pollTimeout,
		pollGuardrail:                  pollGuardrail,
		usesProxy:                      usesProxy,
		pollChannels:                   append([]types.Channel(nil), cfg.PollChannels...),
		pollChannelsConfigured:         cfg.PollChannelsConfigured,
		now:                            time.Now,
		pollElapsedSince:               time.Since,
		responseRetryAttempts:          defaultResponseRetryAttempts,
		newResponseBackoff:             newControlPlaneBackoff,
		managedCloudflareRetryAttempts: defaultManagedCloudflareRetryAttempts,
		newManagedCloudflareBackoff:    newControlPlaneBackoff,
		retrySleep:                     sleepWithContext,
	}
	logger.InfoContext(ctx, "TunnelServiceClient created",
		slog.String("tunnel_id", client.tunnelID.String()),
		slog.String("poll_endpoint", client.pollEndpoint.String()),
		slog.String("response_endpoint", client.responseEndpoint.String()),
		slog.String("metadata_endpoint", client.metadataEndpoint.String()),
		slog.Int64("poll_timeout_ms", pollTimeoutMilliseconds(pollTimeout)),
		slog.Int64("poll_deadline_guardrail_ms", pollGuardrail.Milliseconds()),
		slog.Int64("poll_deadline_ms", pollDeadline.Milliseconds()),
		slog.Bool("uses_proxy", usesProxy),
	)

	return client, nil
}

func controlPlaneUsesProxy(cfg *runtimeconfig.ControlPlaneConfig, proxyFromEnvironment func(*http.Request) (*url.URL, error), lookupEnv func(string) (string, bool)) bool {
	if cfg == nil || cfg.BaseURL == nil || cfg.UnixSocketPath != "" {
		return false
	}
	if lookupEnv != nil {
		if socketPath, ok := lookupEnv(tunnelIntegrationSocketEnv); ok && socketPath != "" {
			return false
		}
	}
	if cfg.HTTPProxy != nil {
		return true
	}
	if proxyFromEnvironment == nil {
		return false
	}

	req := &http.Request{URL: cfg.BaseURL}
	proxyURL, err := proxyFromEnvironment(req)
	return err == nil && proxyURL != nil
}

func (c *TunnelServiceClient) effectivePollTimeout() time.Duration {
	if c == nil {
		return 0
	}
	if learned := time.Duration(c.learnedProxyPollTimeout.Load()); learned > 0 && learned < c.pollTimeout {
		return learned
	}
	return c.pollTimeout
}

func (c *TunnelServiceClient) pollDeadlineFor(timeout time.Duration) time.Duration {
	return (runtimeconfig.ControlPlaneConfig{
		PollTimeout:           timeout,
		PollDeadlineGuardrail: c.pollGuardrail,
	}).PollDeadlineTimeoutOrDefault()
}

func (c *TunnelServiceClient) elapsedSince(start time.Time) time.Duration {
	if c != nil && c.pollElapsedSince != nil {
		return c.pollElapsedSince(start)
	}
	return time.Since(start)
}

// maybeLearnProxyPollTimeout lowers future proxy-routed polls after a
// pre-header transport disconnect that happened before either request
// deadline fired. The learned value is process-local and only ever decreases.
func (c *TunnelServiceClient) maybeLearnProxyPollTimeout(
	ctx context.Context,
	pollCtx context.Context,
	attemptedTimeout time.Duration,
	elapsed time.Duration,
	receivedHeaders bool,
	err error,
) {
	if c == nil || !c.usesProxy || receivedHeaders || err == nil {
		return
	}
	if ctx.Err() != nil || pollCtx.Err() != nil || !isProxyIdleDisconnectError(err) {
		return
	}
	learned := learnedProxyPollTimeoutFromDisconnect(elapsed, attemptedTimeout, c.pollGuardrail)
	if learned <= 0 {
		return
	}

	for {
		currentNanos := c.learnedProxyPollTimeout.Load()
		current := time.Duration(currentNanos)
		if current > 0 && current <= learned {
			return
		}
		if !c.learnedProxyPollTimeout.CompareAndSwap(currentNanos, int64(learned)) {
			continue
		}
		if c.logger != nil {
			c.logger.WarnContext(ctx, "control-plane proxy closed long poll; lowering future poll timeout",
				slog.String("error", err.Error()),
				slog.Int64("observed_disconnect_ms", elapsed.Milliseconds()),
				slog.Int64("attempted_poll_timeout_ms", pollTimeoutMilliseconds(attemptedTimeout)),
				slog.Int64("learned_poll_timeout_ms", pollTimeoutMilliseconds(learned)),
			)
		}
		return
	}
}

func learnedProxyPollTimeoutFromDisconnect(elapsed, attemptedTimeout, guardrail time.Duration) time.Duration {
	// A proxy can close just after the service-side wait expires but before the
	// client-side deadline does. Treat the full poll deadline as the upper
	// bound, not just the requested service wait.
	pollDeadline := attemptedTimeout + guardrail
	if elapsed <= minimumAdaptiveProxyPollMargin || elapsed >= pollDeadline || attemptedTimeout <= minimumAdaptiveProxyPollTimeout {
		return 0
	}
	margin := guardrail
	if margin < minimumAdaptiveProxyPollMargin {
		margin = minimumAdaptiveProxyPollMargin
	}
	if halfElapsed := elapsed / 2; margin > halfElapsed {
		margin = halfElapsed
	}
	learned := (elapsed - margin).Truncate(time.Millisecond)
	if learned < minimumAdaptiveProxyPollTimeout {
		learned = minimumAdaptiveProxyPollTimeout
	}
	if learned >= attemptedTimeout {
		return 0
	}
	return learned
}

func isProxyIdleDisconnectError(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

// TunnelMetadata captures the minimal tunnel metadata needed for boot logging.
type TunnelMetadata struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type APIStatusError struct {
	prefix        string
	statusCode    int
	status        string
	info          apierror.Info
	retryAfter    time.Duration
	hasRetryAfter bool
}

type MetadataStatusError = APIStatusError

func (e *APIStatusError) Error() string {
	if e == nil {
		return ""
	}
	message := fmt.Sprintf("%s %d", e.prefix, e.statusCode)
	if detail := e.Detail(); detail != "" {
		message += ": " + detail
	}
	return message
}

func (e *APIStatusError) StatusCode() int {
	return e.statusCode
}

func (e *APIStatusError) Status() string {
	return e.status
}

func (e *APIStatusError) Code() string {
	return e.info.Code
}

func (e *APIStatusError) Type() string {
	return e.info.Type
}

func (e *APIStatusError) Message() string {
	return e.info.Message
}

func (e *APIStatusError) Detail() string {
	if e == nil {
		return ""
	}
	return apierror.Detail(e.info)
}

func (e *APIStatusError) Mitigation() string {
	if e == nil {
		return ""
	}
	return e.info.Mitigation
}

func (e *APIStatusError) RetryAfter() (time.Duration, bool) {
	if e == nil {
		return 0, false
	}
	return e.retryAfter, e.hasRetryAfter
}

func newAPIStatusError(prefix string, resp *http.Response, now time.Time) *APIStatusError {
	statusErr := &APIStatusError{
		prefix:     prefix,
		statusCode: resp.StatusCode,
		status:     resp.Status,
	}
	statusErr.retryAfter, statusErr.hasRetryAfter = parseRetryAfter(
		resp.Header.Get("Retry-After"),
		now,
	)

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxControlPlaneErrorBodySize))
	_, _ = io.Copy(io.Discard, resp.Body)
	if readErr != nil {
		statusErr.info.Message = "read error body: " + readErr.Error()
		return statusErr
	}
	populateAPIStatusError(statusErr, body)
	return statusErr
}

func populateAPIStatusError(statusErr *APIStatusError, body []byte) {
	statusErr.info = apierror.Parse(body)
}

// FetchTunnelMetadata requests the tunnel metadata record for the configured tunnel.
func (c *TunnelServiceClient) FetchTunnelMetadata(ctx context.Context) (*TunnelMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.metadataEndpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("controlplane client: build tunnel metadata request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("controlplane client: fetch tunnel metadata: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, newAPIStatusError("controlplane client: unexpected metadata status", resp, c.nowTime())
	}

	var metadata TunnelMetadata
	if err := json.NewDecoder(resp.Body).Decode(&metadata); err != nil {
		return nil, fmt.Errorf("controlplane client: decode tunnel metadata: %w", err)
	}

	return &metadata, nil
}

type managedCloudflareRuntimeResponse struct {
	CloudflareTunnel *controlplane.ManagedCloudflareTunnelMetadata `json:"cloudflare_tunnel"`
	RuntimeToken     string                                        `json:"runtime_token"`
}

// FetchManagedCloudflareTunnel retrieves the runtime-only Cloudflare
// connection material for the configured logical tunnel. It intentionally uses
// a client that never enables raw HTTP body logging because the successful
// response contains the runtime token.
func (c *TunnelServiceClient) FetchManagedCloudflareTunnel(ctx context.Context) (*controlplane.ManagedCloudflareTunnelRuntime, error) {
	if c == nil || c.secretClient == nil || c.managedCloudflareEndpoint == nil {
		return nil, errors.New("controlplane client: managed Cloudflare runtime client is unavailable")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.managedCloudflareEndpoint.String(), nil)
	if err != nil {
		return nil, errors.New("controlplane client: build managed Cloudflare runtime request")
	}

	attempts := c.managedCloudflareRetryAttempts
	if attempts <= 0 {
		attempts = defaultManagedCloudflareRetryAttempts
	}
	runtimeBackoff := c.managedCloudflareBackoff()
	for attempt := 1; attempt <= attempts; attempt++ {
		resp, err := c.secretClient.Do(req.Clone(ctx))
		if err != nil {
			if attempt == attempts {
				return nil, errors.New("controlplane client: fetch managed Cloudflare runtime")
			}
			if !c.waitForRetry(ctx, runtimeBackoff.Duration()) {
				return nil, managedCloudflareRetryWaitError(ctx)
			}
			continue
		}

		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			statusErr := newSecretAPIStatusError(
				"controlplane client: unexpected managed Cloudflare runtime status",
				resp,
				c.nowTime(),
			)
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if !isRetryableManagedCloudflareStatus(resp.StatusCode) || attempt == attempts {
				return nil, statusErr
			}
			retryAfter, hasRetryAfter := statusErr.RetryAfter()
			delay := retryDelay(runtimeBackoff.Duration(), retryAfter, hasRetryAfter)
			if !c.waitForRetry(ctx, delay) {
				return nil, managedCloudflareRetryWaitError(ctx)
			}
			continue
		}

		var payload managedCloudflareRuntimeResponse
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			_ = resp.Body.Close()
			return nil, errors.New("controlplane client: decode managed Cloudflare runtime response")
		}
		_ = resp.Body.Close()
		if payload.CloudflareTunnel == nil ||
			strings.TrimSpace(payload.CloudflareTunnel.TunnelID) == "" ||
			strings.TrimSpace(payload.CloudflareTunnel.Name) == "" ||
			strings.TrimSpace(payload.CloudflareTunnel.AccountID) == "" ||
			strings.TrimSpace(payload.RuntimeToken) == "" {
			return nil, errors.New("controlplane client: invalid managed Cloudflare runtime response")
		}

		return &controlplane.ManagedCloudflareTunnelRuntime{
			CloudflareTunnel: *payload.CloudflareTunnel,
			RuntimeToken:     payload.RuntimeToken,
		}, nil
	}

	return nil, errors.New("controlplane client: exhausted managed Cloudflare runtime retries")
}

// newSecretAPIStatusError intentionally skips the response body. Runtime-token
// endpoints must not copy arbitrary server payloads into errors or logs.
func newSecretAPIStatusError(prefix string, resp *http.Response, now time.Time) *APIStatusError {
	statusErr := &APIStatusError{
		prefix:     prefix,
		statusCode: resp.StatusCode,
		status:     resp.Status,
	}
	statusErr.retryAfter, statusErr.hasRetryAfter = parseRetryAfter(
		resp.Header.Get("Retry-After"),
		now,
	)
	return statusErr
}

func buildControlPlaneHTTPTransport(cfg *runtimeconfig.ControlPlaneConfig, tlsBundle *tlsconfig.Bundle, logger *slog.Logger, loggingCfg *runtimeconfig.LoggingConfig, meterProvider otelmetric.MeterProvider) (http.RoundTripper, error) {
	return buildControlPlaneHTTPTransportWithLogging(
		cfg,
		tlsBundle,
		logger,
		loggingCfg,
		meterProvider,
		true,
	)
}

func buildSecretControlPlaneHTTPTransport(cfg *runtimeconfig.ControlPlaneConfig, tlsBundle *tlsconfig.Bundle, logger *slog.Logger, meterProvider otelmetric.MeterProvider) (http.RoundTripper, error) {
	return buildControlPlaneHTTPTransportWithLogging(
		cfg,
		tlsBundle,
		logger,
		nil,
		meterProvider,
		false,
	)
}

func buildControlPlaneHTTPTransportWithLogging(cfg *runtimeconfig.ControlPlaneConfig, tlsBundle *tlsconfig.Bundle, logger *slog.Logger, loggingCfg *runtimeconfig.LoggingConfig, meterProvider otelmetric.MeterProvider, includeRawHTTPLogging bool) (http.RoundTripper, error) {
	// Order matters (outermost to innermost):
	//   1. Control-plane round tripper applies auth headers before anything else.
	//   2. Optional logging wraps otel instrumentation so dumps include the final headers.
	//   3. otelhttp instrumentation and its route labeler sit close to the network for accurate metrics.
	//   4. Response receipt recording sits closest to the network so outer response
	//      middleware cannot delay the timestamp by consuming the response body.
	extraHeaders, err := runtimeconfig.NormalizeExtraHeaders("controlplane client extra headers", cfg.ExtraHeaders)
	if err != nil {
		return nil, fmt.Errorf("controlplane client: %w", err)
	}
	base, err := tctransport.CloneDefaultWithBundle(tlsBundle)
	if err != nil {
		return nil, fmt.Errorf("controlplane client: %w", err)
	}
	base, err = tctransport.ApplyClientCertificate(base, cfg.ClientCertificate)
	if err != nil {
		return nil, fmt.Errorf("controlplane client: %w", err)
	}
	base, err = tctransport.ApplyProxy(base, cfg.HTTPProxy)
	if err != nil {
		return nil, fmt.Errorf("controlplane client: %w", err)
	}
	socketPath := cfg.UnixSocketPath
	if socketPath == "" {
		socketPath = os.Getenv(tunnelIntegrationSocketEnv)
	}
	base, err = tctransport.ApplyUnixSocketPath(base, socketPath)
	if err != nil {
		return nil, fmt.Errorf("controlplane client: %w", err)
	}
	base = newResponseReceiptRoundTripper(base)
	base = tcmetrics.WithHTTPClientMetricAttributes(base)
	base = otelhttp.NewTransport(
		base,
		otelhttp.WithMeterProvider(meterProvider),
	)
	if includeRawHTTPLogging {
		base = tclog.NewRoundTripper(base, logger, loggingCfg, tclog.ComponentControlPlane)
	}

	return newControlPlaneRoundTripper(
		base,
		cfg.APIKey,
		version.UserAgent,
		cfg.OrganizationID,
		cfg.MCPServerInfoHeader,
		extraHeaders,
		logger,
	), nil
}

// PostResponse acknowledges the provided request with the JSON-RPC response.
func (c *TunnelServiceClient) PostResponse(ctx context.Context, requestID types.RequestID, response *types.TunnelResponse) (types.TunnelServiceRequestID, error) {
	if requestID == "" {
		return "", errors.New("controlplane responder: requestID is required")
	}
	if response == nil {
		return "", errors.New("controlplane responder: response is required")
	}

	if err := response.Validate(); err != nil {
		return "", fmt.Errorf("controlplane responder: %w", err)
	}

	channel := response.Channel()
	if channel == "" {
		return "", errors.New("controlplane responder: channel is required")
	}

	responseType := wiretypes.ResponsePayloadJSONRPC
	switch response.Type() {
	case types.ResponseTypeJSONRPCNotification:
		responseType = wiretypes.ResponsePayloadJSONRPCNotify
	case types.ResponseTypeNotificationAcknowledgment:
		responseType = wiretypes.ResponsePayloadNotifyAck
	case types.ResponseTypeOAuthDiscovery:
		responseType = wiretypes.ResponsePayloadOAuth
	case types.ResponseTypeSessionTermination:
		responseType = wiretypes.ResponsePayloadSessionTermination
	}

	responseHeaders := response.Headers()
	// OAuth discovery responses are local-dev-proxy traffic with a separate response-header contract.
	if responseType != wiretypes.ResponsePayloadOAuth {
		responseHeaders = sanitizeMCPResponseHeaders(responseHeaders)
	}
	payload := wiretypes.TunnelResponsePayload{
		RequestID:       requestID.String(),
		ResponseHeaders: responseHeaders,
		ResponseCode:    response.ResponseCode(),
		ResponseType:    responseType,
	}
	payload.Channel = channel.String()
	if rawResponse := response.Payload(); len(rawResponse) > 0 {
		payload.JSONResponse = rawResponse
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("controlplane responder: marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.responseEndpoint.String(), bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("controlplane responder: build request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if controlPlaneRequestID, ok := tunnelctx.ControlPlaneCommandRequestIDFromContext(ctx); ok {
		req.Header.Set("X-Client-Request-Id", controlPlaneRequestID.String())
	}
	shardToken, ok := tunnelctx.ShardTokenFromContext(ctx)
	if !ok || shardToken == "" {
		return "", errors.New("controlplane responder: shard token is required")
	}
	req.Header.Set("X-Tunnel-Shard-Token", shardToken)

	var tunnelServiceRequestID types.TunnelServiceRequestID
	ctx = tunnelctx.ContextWithRequestID(ctx, requestID.String())
	logger := tclog.LoggerWithContextIdentifiers(ctx, c.logger)

	attempts := c.responseRetryAttempts
	if attempts <= 0 {
		attempts = defaultResponseRetryAttempts
	}
	isNotification := response.Type() == types.ResponseTypeJSONRPCNotification
	responseBackoff := c.responseBackoff()
	for attempt := 1; attempt <= attempts; attempt++ {
		attemptReq, err := cloneRequestWithBody(req)
		if err != nil {
			return tunnelServiceRequestID, fmt.Errorf("controlplane responder: replay request body: %w", err)
		}
		var requestWritten atomic.Bool
		if isNotification {
			attemptReq = attemptReq.WithContext(httptrace.WithClientTrace(attemptReq.Context(), &httptrace.ClientTrace{
				WroteRequest: func(httptrace.WroteRequestInfo) {
					requestWritten.Store(true)
				},
			}))
		}

		resp, err := c.client.Do(attemptReq)
		if err != nil {
			// A notification remains non-final after tunnel-service accepts it. Once
			// its request was written, a transport failure has ambiguous commit state;
			// replaying it can enqueue the same notification twice.
			if attempt == attempts || (isNotification && requestWritten.Load()) {
				return tunnelServiceRequestID, fmt.Errorf("controlplane responder: post response: %w", err)
			}
			if !c.waitForRetry(ctx, responseBackoff.Duration()) {
				return tunnelServiceRequestID, retryWaitError(ctx)
			}
			continue
		}

		if id, ok := types.NewTunnelServiceRequestIDFromHeader(resp.Header); ok {
			tunnelServiceRequestID = id
			ctx = tunnelctx.ContextWithTunnelServiceRequestID(ctx, tunnelServiceRequestID)
			logger = tclog.LoggerWithContextIdentifiers(ctx, c.logger)
		}

		switch resp.StatusCode {
		case http.StatusOK:
			_ = resp.Body.Close()
			logger.DebugContext(ctx, "posted response to control-plane")
			return tunnelServiceRequestID, nil
		case http.StatusNotFound:
			_ = resp.Body.Close()
			logger.WarnContext(ctx, "response already fulfilled or unknown request")
			return tunnelServiceRequestID, nil
		default:
			statusErr := newAPIStatusError("controlplane responder: unexpected status", resp, c.nowTime())
			_ = resp.Body.Close()
			if !isRetryableResponseStatus(resp.StatusCode) || attempt == attempts ||
				(isNotification && resp.StatusCode != http.StatusTooManyRequests) {
				return tunnelServiceRequestID, statusErr
			}
			retryAfter, hasRetryAfter := statusErr.RetryAfter()
			delay := retryDelay(responseBackoff.Duration(), retryAfter, hasRetryAfter)
			if !c.waitForRetry(ctx, delay) {
				return tunnelServiceRequestID, retryWaitError(ctx)
			}
		}
	}

	return tunnelServiceRequestID, errors.New("controlplane responder: exhausted response retries")
}

func sanitizeMCPResponseHeaders(headers http.Header) http.Header {
	if len(headers) == 0 {
		return nil
	}

	keys := make([]string, 0, len(headers))
	for name := range headers {
		keys = append(keys, name)
	}
	sort.Strings(keys)

	connectionOptions := make(map[string]struct{})
	for _, name := range keys {
		if !strings.EqualFold(name, "Connection") {
			continue
		}
		for _, value := range headers[name] {
			for _, option := range strings.Split(value, ",") {
				option = strings.ToLower(strings.Trim(option, " \t"))
				if option != "" {
					connectionOptions[option] = struct{}{}
				}
			}
		}
	}

	sanitized := make(http.Header)
	for _, name := range keys {
		normalizedName := strings.ToLower(name)
		canonicalName, allowed := canonicalResponseHeaderName(normalizedName)
		if !allowed {
			continue
		}
		if _, nominated := connectionOptions[normalizedName]; nominated {
			continue
		}
		for _, value := range headers[name] {
			if value != "" {
				sanitized[canonicalName] = append(sanitized[canonicalName], value)
			}
		}
	}
	if len(sanitized) == 0 {
		return nil
	}
	return sanitized
}

func canonicalResponseHeaderName(normalizedName string) (string, bool) {
	switch normalizedName {
	case "access-control-expose-headers":
		return "Access-Control-Expose-Headers", true
	case "content-type":
		return "Content-Type", true
	case "last-event-id":
		return "Last-Event-Id", true
	case "mcp-protocol-version":
		return "Mcp-Protocol-Version", true
	case "mcp-session-id":
		return "Mcp-Session-Id", true
	case "www-authenticate":
		return "Www-Authenticate", true
	default:
		return "", false
	}
}

// Poll requests up to limit commands from the control plane.
func (c *TunnelServiceClient) Poll(ctx context.Context, limit int) ([]controlplane.PolledCommand, types.TunnelServiceRequestID, error) {
	if limit <= 0 {
		return nil, "", nil
	}

	// Keep any learned proxy-specific deadline scoped to polls. The shared HTTP
	// client also serves metadata and response calls, which retain their
	// configured timeout.
	pollTimeout := c.effectivePollTimeout()
	pollCtx, cancel := context.WithTimeout(ctx, c.pollDeadlineFor(pollTimeout))
	defer cancel()

	req, err := http.NewRequestWithContext(pollCtx, http.MethodGet, c.pollEndpoint.String(), nil)
	if err != nil {
		return nil, "", err
	}

	query := req.URL.Query()
	query.Set("limit", strconv.Itoa(limit))
	query.Set("timeout_ms", strconv.FormatInt(pollTimeoutMilliseconds(pollTimeout), 10))
	if c.pollChannelsConfigured {
		for _, channel := range c.pollChannels {
			query.Add("channel", channel.String())
		}
	}
	req.URL.RawQuery = query.Encode()
	receipt := newResponseReceiptRecorder(c.nowTime)
	var pollWrittenAt atomic.Pointer[time.Time]
	req = req.WithContext(httptrace.WithClientTrace(
		contextWithResponseReceiptRecorder(req.Context(), receipt),
		&httptrace.ClientTrace{
			WroteRequest: func(info httptrace.WroteRequestInfo) {
				if info.Err != nil {
					return
				}
				writtenAt := time.Now()
				pollWrittenAt.Store(&writtenAt)
			},
		},
	))

	resp, err := c.client.Do(req)
	if err != nil {
		if writtenAt := pollWrittenAt.Load(); writtenAt != nil {
			_, receivedHeaders := receipt.value()
			c.maybeLearnProxyPollTimeout(
				ctx,
				pollCtx,
				pollTimeout,
				c.elapsedSince(*writtenAt),
				receivedHeaders,
				err,
			)
		}
		return nil, "", err
	}
	receivedAt, recorded := receipt.value()
	if !recorded {
		receivedAt = c.nowTime()
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	var tunnelServiceRequestID types.TunnelServiceRequestID
	if id, ok := types.NewTunnelServiceRequestIDFromHeader(resp.Header); ok {
		tunnelServiceRequestID = id
		ctx = tunnelctx.ContextWithTunnelServiceRequestID(ctx, tunnelServiceRequestID)
	}

	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil, tunnelServiceRequestID, nil
	case http.StatusOK:
		cmds, err := c.decodeCommands(ctx, resp.Body, limit, receivedAt)
		return cmds, tunnelServiceRequestID, err
	default:
		return nil, tunnelServiceRequestID, newAPIStatusError("controlplane client: unexpected status", resp, c.nowTime())
	}
}

func newControlPlaneBackoff() *backoff.Backoff {
	return &backoff.Backoff{
		Min:    defaultBackoffMin,
		Max:    defaultBackoffMax,
		Factor: 2,
		Jitter: true,
	}
}

func (c *TunnelServiceClient) responseBackoff() *backoff.Backoff {
	if c.newResponseBackoff != nil {
		return c.newResponseBackoff()
	}
	return newControlPlaneBackoff()
}

func (c *TunnelServiceClient) managedCloudflareBackoff() *backoff.Backoff {
	if c.newManagedCloudflareBackoff != nil {
		return c.newManagedCloudflareBackoff()
	}
	return newControlPlaneBackoff()
}

func (c *TunnelServiceClient) waitForRetry(ctx context.Context, delay time.Duration) bool {
	if c.retrySleep != nil {
		return c.retrySleep(ctx, delay)
	}
	return sleepWithContext(ctx, delay)
}

func cloneRequestWithBody(req *http.Request) (*http.Request, error) {
	attemptReq := req.Clone(req.Context())
	if req.GetBody == nil {
		return attemptReq, nil
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	attemptReq.Body = body
	return attemptReq, nil
}

func isRetryableResponseStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func isRetryableManagedCloudflareStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests ||
		(statusCode >= http.StatusInternalServerError && statusCode <= 599)
}

func retryWaitError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("controlplane responder: retry wait: %w", err)
	}
	return errors.New("controlplane responder: retry wait interrupted")
}

func managedCloudflareRetryWaitError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("controlplane client: managed Cloudflare runtime retry wait: %w", err)
	}
	return errors.New("controlplane client: managed Cloudflare runtime retry wait interrupted")
}

func (c *TunnelServiceClient) nowTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func pollTimeoutMilliseconds(timeout time.Duration) int64 {
	timeoutMS := (runtimeconfig.ControlPlaneConfig{PollTimeout: timeout}).PollTimeoutOrDefault().Milliseconds()
	if timeoutMS > 0 {
		return timeoutMS
	}
	return 1
}

func (c *TunnelServiceClient) decodeCommands(ctx context.Context, r io.Reader, limit int, polledAt time.Time) ([]controlplane.PolledCommand, error) {
	limited := limit
	if limited <= 0 {
		limited = 1
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("controlplane client: read poll response: %w", err)
	}

	var envelope wiretypes.PolledCommandEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("controlplane client: decode poll response: %w", err)
	}
	rawCommands := envelope.Commands

	// If there are no commands, return early.
	if len(rawCommands) == 0 {
		return nil, nil
	}

	// The poll limit is a request hint to tunnel-service. If the server returns more
	// commands than requested, we MUST NOT drop them here: tunnel-service may not
	// re-deliver and dropping would lose requests. Log for observability and process
	// everything we received.
	total := len(rawCommands)
	if total > limited {
		logger := tclog.LoggerWithContextIdentifiers(ctx, c.logger)
		logger.ErrorContext(ctx, "control-plane returned more commands than requested limit",
			slog.Int("limit", limited),
			slog.Int("total", total),
		)
	}

	logger := tclog.LoggerWithContextIdentifiers(ctx, c.logger)
	out := make([]controlplane.PolledCommand, 0, len(rawCommands))
	for _, raw := range rawCommands {
		// Peek discriminator for forward-compat.
		var probe struct {
			CommandType wiretypes.CommandType `json:"command_type"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			logger.WarnContext(ctx, "control-plane command dropped: invalid payload", slog.String("error", err.Error()))
			continue
		}

		if probe.CommandType == "" {
			logger.WarnContext(ctx, "control-plane command dropped: missing command_type")
			continue
		}

		switch probe.CommandType {
		case wiretypes.CommandTypeJSONRPC:
			var rpc wiretypes.RawJSONRPCPolledCommand
			if err := json.Unmarshal(raw, &rpc); err != nil {
				logger.WarnContext(ctx, "control-plane command dropped: invalid jsonrpc payload", slog.String("error", err.Error()))
				continue
			}
			cmd, err := convertRawCommand(rpc, polledAt)
			if err != nil {
				logger.WarnContext(ctx, "control-plane command dropped: invalid jsonrpc payload", slog.String(tclog.FieldRequestID, rpc.RequestID), slog.String("error", err.Error()))
				continue
			}
			out = append(out, cmd)
		case wiretypes.CommandTypeOAuthDiscovery:
			var od wiretypes.RawOauthDiscoveryPolledCommand
			if err := json.Unmarshal(raw, &od); err != nil {
				logger.WarnContext(ctx, "control-plane command dropped: invalid oauth_discovery payload", slog.String("error", err.Error()))
				continue
			}
			cmd, err := convertRawOauthDiscoveryCommand(od, polledAt)
			if err != nil {
				logger.WarnContext(ctx, "control-plane command dropped: invalid oauth_discovery payload", slog.String(tclog.FieldRequestID, od.RequestID), slog.String("error", err.Error()))
				continue
			}
			out = append(out, cmd)
		case wiretypes.CommandTypeSessionTermination:
			var termination wiretypes.RawSessionTerminationPolledCommand
			if err := json.Unmarshal(raw, &termination); err != nil {
				logger.WarnContext(ctx, "control-plane command dropped: invalid session_termination payload", slog.String("error", err.Error()))
				continue
			}
			cmd, err := convertRawSessionTerminationCommand(termination, polledAt)
			if err != nil {
				logger.WarnContext(ctx, "control-plane command dropped: invalid session_termination payload", slog.String(tclog.FieldRequestID, termination.RequestID), slog.String("error", err.Error()))
				continue
			}
			out = append(out, cmd)
		default:
			// Unknown command type – drop with warning for forward compatibility.
			logger.WarnContext(ctx, "control-plane command dropped: unknown command_type", slog.String("command_type", string(probe.CommandType)))
			continue
		}
	}

	return out, nil
}

// parsing and conversion helpers and command types were moved into
// command_parser.go and command_types.go to keep this file focused on HTTP logic.
