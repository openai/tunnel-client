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
	"net/url"
	"os"
	"strconv"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	otelmetric "go.opentelemetry.io/otel/metric"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/controlplane"
	"go.openai.org/api/tunnel-client/pkg/controlplane/apierror"
	wiretypes "go.openai.org/api/tunnel-client/pkg/controlplane/wiretypes"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
	tcmetrics "go.openai.org/api/tunnel-client/pkg/metrics"
	"go.openai.org/api/tunnel-client/pkg/tlsconfig"
	tctransport "go.openai.org/api/tunnel-client/pkg/transport"
	"go.openai.org/api/tunnel-client/pkg/tunnelctx"
	"go.openai.org/api/tunnel-client/pkg/types"
	"go.openai.org/api/tunnel-client/pkg/version"
)

const (
	pollPathFormat               = "/v1/tunnels/%s/poll"
	responsePathFormat           = "/v1/tunnels/%s/response"
	metadataPathFormat           = "/v1/tunnels/%s"
	maxControlPlaneErrorBodySize = 64 * 1024
	tunnelIntegrationSocketEnv   = "TUNNEL_INTEGRATION_TUNNEL_SERVICE_SOCKET_PATH"
)

var errMissingConfig = errors.New("controlplane client: config is required")

// TunnelServiceClient implements the Fetcher and Responder interfaces backed by
// the control-plane HTTP API.
type TunnelServiceClient struct {
	client           *http.Client
	pollEndpoint     *url.URL
	responseEndpoint *url.URL
	metadataEndpoint *url.URL
	logger           *slog.Logger
	tunnelID         types.TunnelID
	apiKey           string
	userAgent        string
	pollTimeout      time.Duration
	pollGuardrail    time.Duration
}

// NewTunnelServiceClient constructs an HTTP-backed client using the provided config.
func NewTunnelServiceClient(ctx context.Context, cfg *config.ControlPlaneConfig, tlsBundle *tlsconfig.Bundle, logger *slog.Logger, loggingCfg *config.LoggingConfig, meterProvider otelmetric.MeterProvider) (*TunnelServiceClient, error) {
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
	if meterProvider == nil {
		return nil, errors.New("controlplane client: meter provider is required")
	}

	if logger == nil {
		return nil, errors.New("controlplane client: logger is required")
	}

	tunnelIDSegment := url.PathEscape(cfg.TunnelID.String())
	pollEndpoint := config.ResolveControlPlanePath(cfg.BaseURL, cfg.URLPath, fmt.Sprintf(pollPathFormat, tunnelIDSegment))
	responseEndpoint := config.ResolveControlPlanePath(cfg.BaseURL, cfg.URLPath, fmt.Sprintf(responsePathFormat, tunnelIDSegment))
	metadataEndpoint := config.ResolveControlPlanePath(cfg.BaseURL, cfg.URLPath, fmt.Sprintf(metadataPathFormat, tunnelIDSegment))

	pollTimeout := cfg.PollTimeoutOrDefault()
	pollGuardrail := cfg.PollDeadlineGuardrailOrDefault()
	pollDeadline := cfg.PollDeadlineTimeoutOrDefault()

	transport, err := buildControlPlaneHTTPTransport(cfg, tlsBundle, logger, loggingCfg, meterProvider)
	if err != nil {
		return nil, err
	}
	tlsconfig.LogBundleUsage(logger, tlsBundle)

	client := &TunnelServiceClient{
		client: &http.Client{
			Timeout:   pollDeadline,
			Transport: transport,
		},
		pollEndpoint:     pollEndpoint,
		responseEndpoint: responseEndpoint,
		metadataEndpoint: metadataEndpoint,
		logger:           logger,
		tunnelID:         cfg.TunnelID,
		apiKey:           cfg.APIKey,
		userAgent:        version.UserAgent,
		pollTimeout:      pollTimeout,
		pollGuardrail:    pollGuardrail,
	}
	logger.InfoContext(ctx, "TunnelServiceClient created",
		slog.String("tunnel_id", client.tunnelID.String()),
		slog.String("poll_endpoint", client.pollEndpoint.String()),
		slog.String("response_endpoint", client.responseEndpoint.String()),
		slog.String("metadata_endpoint", client.metadataEndpoint.String()),
		slog.Int64("poll_timeout_ms", pollTimeoutMilliseconds(pollTimeout)),
		slog.Int64("poll_deadline_guardrail_ms", pollGuardrail.Milliseconds()),
		slog.Int64("poll_deadline_ms", pollDeadline.Milliseconds()),
	)

	return client, nil
}

// TunnelMetadata captures the minimal tunnel metadata needed for boot logging.
type TunnelMetadata struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type APIStatusError struct {
	prefix     string
	statusCode int
	status     string
	info       apierror.Info
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

func newAPIStatusError(prefix string, resp *http.Response) *APIStatusError {
	statusErr := &APIStatusError{
		prefix:     prefix,
		statusCode: resp.StatusCode,
		status:     resp.Status,
	}

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
		return nil, newAPIStatusError("controlplane client: unexpected metadata status", resp)
	}

	var metadata TunnelMetadata
	if err := json.NewDecoder(resp.Body).Decode(&metadata); err != nil {
		return nil, fmt.Errorf("controlplane client: decode tunnel metadata: %w", err)
	}

	return &metadata, nil
}

func buildControlPlaneHTTPTransport(cfg *config.ControlPlaneConfig, tlsBundle *tlsconfig.Bundle, logger *slog.Logger, loggingCfg *config.LoggingConfig, meterProvider otelmetric.MeterProvider) (http.RoundTripper, error) {
	// Order matters (outermost to innermost):
	//   1. Control-plane round tripper applies auth headers before anything else.
	//   2. Logging wraps otel instrumentation so dumps include the final headers.
	//   3. otelhttp instrumentation sits closest to the network for accurate metrics.
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
	base = otelhttp.NewTransport(
		base,
		otelhttp.WithMeterProvider(meterProvider),
		tcmetrics.WithHTTPClientMetricAttributesFn(),
	)
	base = tclog.NewRoundTripper(base, logger, loggingCfg, tclog.ComponentControlPlane)

	return newControlPlaneRoundTripper(
		base,
		cfg.APIKey,
		version.UserAgent,
		cfg.OrganizationID,
		cfg.ExtraHeaders,
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

	payload := wiretypes.TunnelResponsePayload{
		RequestID:       requestID.String(),
		ResponseHeaders: response.Headers(),
		ResponseCode:    response.ResponseCode(),
		ResponseType:    wiretypes.ResponsePayloadJSONRPC,
	}
	payload.Channel = channel.String()
	if rawResponse := response.Payload(); len(rawResponse) > 0 {
		payload.JSONResponse = rawResponse
	}
	switch response.Type() {
	case types.ResponseTypeJSONRPCNotification:
		payload.ResponseType = wiretypes.ResponsePayloadJSONRPCNotify
	case types.ResponseTypeNotificationAcknowledgment:
		payload.ResponseType = wiretypes.ResponsePayloadNotifyAck
	case types.ResponseTypeOAuthDiscovery:
		payload.ResponseType = wiretypes.ResponsePayloadOAuth
	case types.ResponseTypeSessionTermination:
		payload.ResponseType = wiretypes.ResponsePayloadSessionTermination
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

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("controlplane responder: post response: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	var tunnelServiceRequestID types.TunnelServiceRequestID
	if id, ok := types.NewTunnelServiceRequestIDFromHeader(resp.Header); ok {
		tunnelServiceRequestID = id
		ctx = tunnelctx.ContextWithTunnelServiceRequestID(ctx, tunnelServiceRequestID)
	}

	ctx = tunnelctx.ContextWithRequestID(ctx, requestID.String())
	logger := tclog.LoggerWithContextIdentifiers(ctx, c.logger)
	switch resp.StatusCode {
	case http.StatusOK:
		logger.DebugContext(ctx, "posted response to control-plane")
		return tunnelServiceRequestID, nil
	case http.StatusNotFound:
		logger.WarnContext(ctx, "response already fulfilled or unknown request")
		return tunnelServiceRequestID, nil
	default:
		return tunnelServiceRequestID, newAPIStatusError("controlplane responder: unexpected status", resp)
	}
}

// Poll requests up to limit commands from the control plane.
func (c *TunnelServiceClient) Poll(ctx context.Context, limit int) ([]controlplane.PolledCommand, types.TunnelServiceRequestID, error) {
	if limit <= 0 {
		return nil, "", nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.pollEndpoint.String(), nil)
	if err != nil {
		return nil, "", err
	}

	query := req.URL.Query()
	query.Set("limit", strconv.Itoa(limit))
	query.Set("timeout_ms", strconv.FormatInt(pollTimeoutMilliseconds(c.pollTimeout), 10))
	req.URL.RawQuery = query.Encode()

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", err
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
		cmds, err := c.decodeCommands(ctx, resp.Body, limit)
		return cmds, tunnelServiceRequestID, err
	default:
		return nil, tunnelServiceRequestID, newAPIStatusError("controlplane client: unexpected status", resp)
	}
}

func pollTimeoutMilliseconds(timeout time.Duration) int64 {
	timeoutMS := (config.ControlPlaneConfig{PollTimeout: timeout}).PollTimeoutOrDefault().Milliseconds()
	if timeoutMS > 0 {
		return timeoutMS
	}
	return 1
}

func (c *TunnelServiceClient) decodeCommands(ctx context.Context, r io.Reader, limit int) ([]controlplane.PolledCommand, error) {
	limited := limit
	if limited <= 0 {
		limited = 1
	}

	polledAt := time.Now()

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
