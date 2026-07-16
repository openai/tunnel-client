package dispatcherinternal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane"
	"github.com/openai/tunnel-client/pkg/harpoon/hostbus"
	tclog "github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/oauth"
	"github.com/openai/tunnel-client/pkg/tunnelctx"
	"github.com/openai/tunnel-client/pkg/types"
)

const (
	defaultAcceptHeaderValue = "application/json, text/event-stream"
)

var requiredProcessorChannels = []types.Channel{
	types.DefaultChannel,
	types.ChannelHarpoon,
}

var errResponseDeadlineExceeded = errors.New("tunnel response deadline exceeded")

// Processor forwards polled control plane commands to the downstream MCP server.
type Processor interface {
	Process(ctx context.Context, cmd controlplane.PolledCommand) error
}

// ChannelBinding describes routing behavior for a specific tunnel-service
// channel. SupportsMCP gates normal JSON-RPC forwarding; SupportsOAuth is kept
// on the main channel only because OAuth discovery is derived from the primary
// MCP server URL rather than arbitrary auxiliary channels.
type ChannelBinding struct {
	Transport                  mcpclient.ForwardingTransport
	Priority                   int
	Routable                   func() bool
	SupportsMCP                bool
	SupportsOAuth              bool
	SupportsSessionTermination bool
}

type processorParams struct {
	fx.In

	Logger          *slog.Logger
	ChannelBindings map[types.Channel]ChannelBinding `optional:"true"`
	TunnelResponder controlplane.Responder
	MCPConfig       *config.MCPConfig
	OAuthHTTPClient *http.Client                `name:"mcp_client"`
	HostBus         hostbus.HostRegistrationBus `optional:"true"`
	ControlPlaneCfg *config.ControlPlaneConfig
	MeterProvider   *sdkmetric.MeterProvider
}

type mcpProcessor struct {
	logger            *slog.Logger
	channels          map[types.Channel]channelConfig
	tunnelResponder   controlplane.Responder
	connectionMaxTTL  time.Duration
	metrics           *processorMetrics
	tunnelID          types.TunnelID
	oauthHTTPClient   *http.Client
	hostBus           hostbus.HostRegistrationBus
	mcpServerURL      *url.URL
	mcpUnixSocketPath string
	withDeadlineCause func(context.Context, time.Time, error) (context.Context, context.CancelFunc)
}

type channelFeatures struct {
	supportsMCP                bool
	supportsOAuth              bool
	supportsSessionTermination bool
}

type channelConfig struct {
	transport mcpclient.ForwardingTransport
	features  channelFeatures
	priority  int
	routable  func() bool
}

func (c channelConfig) isRoutable() bool {
	if c.routable == nil {
		return true
	}
	return c.routable()
}

func missingRequiredChannels(channels map[types.Channel]channelConfig) []types.Channel {
	missing := make([]types.Channel, 0, len(requiredProcessorChannels))
	for _, required := range requiredProcessorChannels {
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

// NewProcessor constructs a Processor from channel bindings.
func NewProcessor(p processorParams) (Processor, error) {
	if p.Logger == nil {
		return nil, fmt.Errorf("dispatcher processor: nil logger")
	}
	if p.TunnelResponder == nil {
		return nil, fmt.Errorf("dispatcher processor: nil responder")
	}
	if p.MCPConfig == nil {
		return nil, fmt.Errorf("dispatcher processor: nil MCP config")
	}
	if p.MCPConfig.ConnectionMaxTTL <= 0 {
		return nil, fmt.Errorf("dispatcher processor: non-positive MCP connection TTL")
	}
	if p.ControlPlaneCfg == nil {
		return nil, fmt.Errorf("dispatcher processor: nil control-plane config")
	}
	if p.MeterProvider == nil {
		return nil, fmt.Errorf("dispatcher processor: nil meter provider")
	}
	if p.OAuthHTTPClient == nil {
		return nil, fmt.Errorf("dispatcher processor: nil oauth http client")
	}

	baseLogger := p.Logger.With(tclog.FieldComponent, tclog.ComponentDispatcher)

	meter := p.MeterProvider.Meter("dispatcher")
	processorMetrics, err := newProcessorMetrics(meter)
	if err != nil {
		return nil, fmt.Errorf("dispatcher processor: %w", err)
	}

	transportKind := p.MCPConfig.TransportKind
	if transportKind == "" {
		transportKind = config.MCPTransportHTTPStreamable
	}
	if transportKind == config.MCPTransportHTTPStreamable && p.MCPConfig.ServerURL == nil {
		return nil, fmt.Errorf("dispatcher processor: missing MCP server URL")
	}

	channels := make(map[types.Channel]channelConfig, len(p.ChannelBindings))
	for rawChannelName, binding := range p.ChannelBindings {
		channelName := rawChannelName.Canonical()
		if channelName == "" {
			return nil, fmt.Errorf("dispatcher processor: channel name %q is invalid after normalization", rawChannelName)
		}
		if _, exists := channels[channelName]; exists {
			return nil, fmt.Errorf("dispatcher processor: duplicate channel %q", channelName)
		}
		if binding.SupportsMCP && binding.Transport == nil {
			return nil, fmt.Errorf("dispatcher processor: nil transport for channel %q with supportsMCP=true", channelName)
		}
		if channelName != types.DefaultChannel && binding.SupportsOAuth {
			return nil, fmt.Errorf("dispatcher processor: non-main channel %q must not set supportsOAuth=true", channelName)
		}

		channels[channelName] = channelConfig{
			transport: binding.Transport,
			features: channelFeatures{
				supportsMCP:                binding.SupportsMCP,
				supportsOAuth:              binding.SupportsOAuth,
				supportsSessionTermination: binding.SupportsSessionTermination,
			},
			priority: binding.Priority,
			routable: binding.Routable,
		}
	}

	missing := missingRequiredChannels(channels)
	if len(missing) > 0 {
		return nil, fmt.Errorf(
			"dispatcher processor: missing required channels %v (required channels: %v)",
			channelNames(missing),
			channelNames(requiredProcessorChannels),
		)
	}
	for _, channelName := range requiredProcessorChannels {
		cfg := channels[channelName]
		if !cfg.features.supportsMCP {
			return nil, fmt.Errorf(
				"dispatcher processor: required channel %q must set supportsMCP=true (required channels: %v)",
				channelName,
				channelNames(requiredProcessorChannels),
			)
		}
	}

	type channelRegistration struct {
		Name          string `json:"name"`
		Priority      int    `json:"priority"`
		RoutableNow   bool   `json:"routable_now"`
		SupportsMCP   bool   `json:"supports_mcp"`
		SupportsOAuth bool   `json:"supports_oauth"`
	}

	registered := make([]channelRegistration, 0, len(channels))
	for channelName, cfg := range channels {
		registered = append(registered, channelRegistration{
			Name:          channelName.String(),
			Priority:      cfg.priority,
			RoutableNow:   cfg.isRoutable(),
			SupportsMCP:   cfg.features.supportsMCP,
			SupportsOAuth: cfg.features.supportsOAuth,
		})
	}
	sort.SliceStable(registered, func(i, j int) bool {
		if registered[i].Priority != registered[j].Priority {
			return registered[i].Priority < registered[j].Priority
		}
		return registered[i].Name < registered[j].Name
	})
	baseLogger.Info("dispatcher channels registered", slog.Any("channels", registered))

	return &mcpProcessor{
		logger:            baseLogger,
		channels:          channels,
		tunnelResponder:   p.TunnelResponder,
		connectionMaxTTL:  p.MCPConfig.ConnectionMaxTTL,
		metrics:           processorMetrics,
		tunnelID:          p.ControlPlaneCfg.TunnelID,
		oauthHTTPClient:   p.OAuthHTTPClient,
		hostBus:           p.HostBus,
		mcpServerURL:      p.MCPConfig.ServerURL,
		mcpUnixSocketPath: p.MCPConfig.UnixSocketPath,
		withDeadlineCause: context.WithDeadlineCause,
	}, nil
}

// Process delivers one polled control-plane command to the channel-specific
// downstream service. Unsupported or temporarily unroutable channels are posted
// back as connector-visible errors; they never fall back to main because that
// could send a product request to the wrong private MCP server.
func (p *mcpProcessor) Process(ctx context.Context, cmd controlplane.PolledCommand) (processErr error) {
	if cmd == nil {
		return fmt.Errorf("dispatcher processor: nil command")
	}

	requestID := cmd.RequestID()
	ctx = tunnelctx.ContextWithRequestID(ctx, requestID.String())
	if controlPlaneRequestID, ok := types.NewControlPlaneRequestIDFromHeader(cmd.Headers()); ok {
		ctx = tunnelctx.ContextWithControlPlaneCommandRequestID(ctx, controlPlaneRequestID)
	}
	shardToken := cmd.ShardToken()
	if shardToken == "" {
		return fmt.Errorf("dispatcher processor: missing shard token for request %s", requestID)
	}
	ctx = tunnelctx.ContextWithShardToken(ctx, shardToken)
	if sessionID, ok := cmd.SessionID(); ok {
		ctx = tunnelctx.ContextWithSessionID(ctx, sessionID)
	}
	logger := tclog.LoggerWithContextIdentifiers(ctx, p.logger)

	var cancel context.CancelFunc
	if provider, ok := cmd.(controlplane.ResponseDeadlineProvider); ok {
		if deadline, ok := provider.ResponseDeadline(); ok {
			ctx, cancel = p.withDeadlineCause(ctx, deadline, errResponseDeadlineExceeded)
			ctx = mcpclient.ContextWithResponseDeadlineEnforcement(ctx)
		}
	}
	if cancel == nil {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	if errors.Is(context.Cause(ctx), errResponseDeadlineExceeded) {
		logger.InfoContext(ctx, "dropping command whose response deadline has passed")
		return nil
	}
	defer func() {
		if !errors.Is(context.Cause(ctx), errResponseDeadlineExceeded) {
			return
		}
		if !errors.Is(processErr, context.Canceled) && !errors.Is(processErr, context.DeadlineExceeded) {
			return
		}
		logger.InfoContext(ctx, "command response deadline reached; dropping without posting a response")
		processErr = nil
	}()

	rawChannel := cmd.Channel()
	channel := rawChannel.Canonical()
	if rawChannel == "" {
		channel = types.DefaultChannel
	}
	ctx = tunnelctx.ContextWithChannel(ctx, channel)

	channelCfg, ok := p.channels[channel]
	if !ok || !channelCfg.isRoutable() {
		return p.rejectUnsupportedChannel(ctx, logger, cmd, channel)
	}
	if !channelCfg.features.supportsMCP {
		return p.rejectUnsupportedChannel(ctx, logger, cmd, channel)
	}

	switch typedCmd := cmd.(type) {
	case controlplane.JsonRpcCommand:
		return p.processJsonRpcCommand(ctx, logger, typedCmd, channelCfg, channel)
	case controlplane.OauthDiscoveryCommand:
		if channel != types.DefaultChannel || !channelCfg.features.supportsOAuth {
			return p.rejectUnsupportedChannel(ctx, logger, cmd, channel)
		}
		return p.processOauthDiscoveryCommand(ctx, logger, typedCmd, channel)
	case controlplane.SessionTerminationCommand:
		return p.processSessionTerminationCommand(ctx, logger, typedCmd, channelCfg, channel)
	default:
		logger.ErrorContext(ctx, "polled command was not a JSON-RPC command")
		return fmt.Errorf("unexpected command type %T", cmd)
	}
}

func (p *mcpProcessor) rejectUnsupportedChannel(ctx context.Context, logger *slog.Logger, cmd controlplane.PolledCommand, channel types.Channel) error {
	statusCode := http.StatusBadRequest
	err := fmt.Errorf("unsupported channel %q", channel)
	logger.ErrorContext(ctx, "dispatcher received unsupported channel", slog.String("channel", channel.String()))

	attrs := []attribute.KeyValue{
		attribute.String("tunnel_id", p.tunnelID.String()),
		attribute.String("channel", channel.String()),
	}
	switch cmd.(type) {
	case controlplane.JsonRpcCommand:
		attrs = append(attrs, attribute.String("command_type", "jsonrpc"))
	case controlplane.OauthDiscoveryCommand:
		attrs = append(attrs, attribute.String("command_type", "oauth_discovery"))
	case controlplane.SessionTerminationCommand:
		attrs = append(attrs, attribute.String("command_type", "session_termination"))
	default:
		attrs = append(attrs, attribute.String("command_type", "unknown"))
	}
	p.metrics.recordUnsupportedChannel(ctx, attrs)

	var response *types.TunnelResponse
	switch typedCmd := cmd.(type) {
	case controlplane.JsonRpcCommand:
		req, ok := typedCmd.Message().(*jsonrpc.Request)
		if ok {
			encoded, encodeErr := buildJSONRPCErrorResponse(req, statusCode, err)
			if encodeErr != nil {
				return fmt.Errorf("build channel error response: %w", encodeErr)
			}
			response = types.NewTunnelResponse(channel, encoded, statusCode, http.Header{})
		}
	case controlplane.OauthDiscoveryCommand:
		payload, encodeErr := json.Marshal(map[string]any{
			"error": map[string]any{
				"message": fmt.Sprintf("unsupported channel %q", channel),
				"type":    "invalid_request_error",
				"code":    "unsupported_channel",
			},
		})
		if encodeErr != nil {
			return fmt.Errorf("encode channel error response: %w", encodeErr)
		}
		response = types.NewOAuthDiscoveryResponse(channel, payload, statusCode, http.Header{})
	case controlplane.SessionTerminationCommand:
		response = types.NewSessionTerminationResponse(channel, statusCode, http.Header{})
	}

	if response == nil {
		return fmt.Errorf("unsupported channel %q for command type %T", channel, cmd)
	}
	if _, postErr := p.tunnelResponder.PostResponse(ctx, cmd.RequestID(), response); postErr != nil {
		logger.ErrorContext(ctx, "failed to post channel error response to control plane", slog.String("error", postErr.Error()))
		return postErr
	}

	return err
}

func (p *mcpProcessor) processJsonRpcCommand(ctx context.Context, logger *slog.Logger, cmd controlplane.JsonRpcCommand, channelCfg channelConfig, channel types.Channel) error {
	requestID := cmd.RequestID()
	req, ok := cmd.Message().(*jsonrpc.Request)
	if !ok {
		logger.ErrorContext(ctx, "polled command payload was not a JSON-RPC request", slog.String("type", fmt.Sprintf("%T", cmd.Message())))
		return fmt.Errorf("unexpected command type %T", cmd.Message())
	}

	isNotification := !req.ID.IsValid()
	if !isNotification {
		ctx = tunnelctx.ContextWithRPCRequestID(ctx, req.ID)
		logger = tclog.LoggerWithContextIdentifiers(ctx, p.logger)
	}

	requestKindAttrs := requestKindAttributes(req)
	requestKindAttrs = append(requestKindAttrs, attribute.String("channel", channel.String()))
	latencyRecorded := &latencyFlags{}

	// Establish MCP connection only for JSON-RPC commands.
	conn, err := channelCfg.transport.Connect(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		failure := classifyTunnelFailure(0, err)
		logger.WarnContext(ctx, "failed to connect to MCP transport", tunnelFailureLogAttrs(failure, classifyTransportErrorKind(0, err))...)
		if isNotification {
			return fmt.Errorf("connect failed: %s", failure.Source)
		}

		status := normalizeTransportStatusCode(0, err)
		encodedError, encodeErr := buildTunnelFailureJSONRPCErrorResponse(req, status, failure)
		if encodeErr != nil {
			logger.ErrorContext(ctx, "failed to encode MCP error response", slog.String("error", encodeErr.Error()))
			return fmt.Errorf("encode error response: %w", encodeErr)
		}

		respHeader := http.Header{}
		respHeader.Set("Content-Type", "application/json")

		tunnelResponse := types.NewTunnelResponse(channel, encodedError, status, respHeader)
		tsRequestID, postErr := p.tunnelResponder.PostResponse(ctx, requestID, tunnelResponse)
		if postErr != nil {
			attrs := []any{slog.String("error", postErr.Error())}
			if tsRequestID != "" {
				attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tsRequestID.String()))
			}
			logger.ErrorContext(ctx, "failed to post error response to control plane", attrs...)
			return postErr
		}

		p.metrics.recordCommandLatencies(ctx, p.tunnelID, status, requestKindAttrs, cmd.EnqueuedAt(), cmd.PolledAt(), latencyRecorded)
		attrs := []any{
			slog.Int("status_code", status),
			slog.String("channel", channel.String()),
			slog.String("rpc_method", req.Method),
		}
		attrs = append(attrs, tunnelFailureLogAttrs(failure, classifyTransportErrorKind(0, err))...)
		if tsRequestID != "" {
			attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tsRequestID.String()))
		}
		logger.WarnContext(ctx, "dispatcher failed to connect to MCP transport; posted error response to control plane", attrs...)
		return nil
	}
	if err := ctx.Err(); err != nil {
		if closeErr := conn.Close(); closeErr != nil {
			logger.WarnContext(ctx, "failed to close MCP connection after context cancellation", slog.String("error", closeErr.Error()))
		}
		return err
	}

	headers := ensureDefaultAcceptHeader(cmd.Headers())
	writeResult, err := conn.Write(ctx, headers, req)
	if ctx.Err() != nil {
		if closeErr := conn.Close(); closeErr != nil {
			logger.WarnContext(ctx, "failed to close MCP connection after context cancellation", slog.String("error", closeErr.Error()))
		}
		return ctx.Err()
	}
	statusCode := normalizeTransportStatusCode(writeResult.StatusCode, err)
	respHeader := writeResult.ResponseHeaders
	if preserved := writeResult.PreservedError; preserved != nil {
		encodedError := preserved.Payload()
		respHeader = jsonRPCResponseHeaders(ctx, logger, respHeader)
		tunnelResponse := types.NewTunnelResponse(channel, encodedError, statusCode, respHeader)
		tsRequestID, postErr := p.tunnelResponder.PostResponse(ctx, requestID, tunnelResponse)
		if postErr != nil {
			attrs := []any{slog.String("error", postErr.Error())}
			if tsRequestID != "" {
				attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tsRequestID.String()))
			}
			logger.ErrorContext(ctx, "failed to post preserved MCP error response to control plane", attrs...)
			return postErr
		}

		p.metrics.recordCommandLatencies(ctx, p.tunnelID, statusCode, requestKindAttrs, cmd.EnqueuedAt(), cmd.PolledAt(), latencyRecorded)
		attrs := []any{
			slog.Int("status_code", statusCode),
			slog.String("channel", channel.String()),
			slog.String("rpc_method", req.Method),
			slog.Int64("rpc_error_code", preserved.Code()),
			jsonRPCIDAttr("rpc_response_id", req.ID),
		}
		if tsRequestID != "" {
			attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tsRequestID.String()))
		}
		logger.DebugContext(ctx, "dispatcher delivered preserved MCP error response to control plane", attrs...)
		return nil
	}
	if err != nil || statusCode >= http.StatusBadRequest {
		status := statusCode
		failure := classifyTunnelFailure(writeResult.StatusCode, err)
		encodedError, encodeErr := buildTunnelFailureJSONRPCErrorResponse(req, status, failure)
		if encodeErr != nil {
			logger.ErrorContext(ctx, "failed to encode MCP error response", slog.String("error", encodeErr.Error()))
			return fmt.Errorf("encode error response: %w", encodeErr)
		}

		if respHeader == nil {
			respHeader = http.Header{}
		}
		if respHeader.Get("Content-Type") == "" {
			respHeader = respHeader.Clone()
			respHeader.Set("Content-Type", "application/json")
		}

		tunnelResponse := types.NewTunnelResponse(channel, encodedError, status, respHeader)
		tsRequestID, postErr := p.tunnelResponder.PostResponse(ctx, requestID, tunnelResponse)
		if postErr != nil {
			attrs := []any{slog.String("error", postErr.Error())}
			if tsRequestID != "" {
				attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tsRequestID.String()))
			}
			logger.ErrorContext(ctx, "failed to post error response to control plane", attrs...)
			return postErr
		}

		p.metrics.recordCommandLatencies(ctx, p.tunnelID, status, requestKindAttrs, cmd.EnqueuedAt(), cmd.PolledAt(), latencyRecorded)
		attrs := []any{
			slog.Int("status_code", status),
			slog.String("channel", channel.String()),
			slog.String("rpc_method", req.Method),
		}
		attrs = append(attrs, tunnelFailureLogAttrs(failure, classifyTransportErrorKind(writeResult.StatusCode, err))...)
		if respHeader != nil {
			attrs = append(attrs, slog.String("response_content_type", respHeader.Get("Content-Type")))
		}
		if tsRequestID != "" {
			attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tsRequestID.String()))
		}
		logger.WarnContext(
			ctx,
			"dispatcher received MCP upstream error; posted error response to control plane",
			attrs...,
		)
		return nil
	}

	if _, ok := tunnelctx.SessionIDFromContext(ctx); !ok {
		if headerSession := mcpclient.SessionIDFromHeaders(respHeader); headerSession != nil {
			ctx = tunnelctx.ContextWithSessionID(ctx, *headerSession)
			logger = tclog.LoggerWithContextIdentifiers(ctx, p.logger)
		}
	}

	if isNotification {
		logger.DebugContext(ctx, "dispatcher forwarded notification to MCP server; acknowledging without waiting for response. conn.Write returned w/o error")

		notificationAck := types.NewNotificationAck(channel, statusCode, respHeader)
		if tsRequestID, err := p.tunnelResponder.PostResponse(ctx, requestID, notificationAck); err != nil {
			attrs := []any{slog.String("error", err.Error())}
			if tsRequestID != "" {
				attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tsRequestID.String()))
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				logger.WarnContext(ctx, "context canceled while acknowledging notification", attrs...)
			} else {
				logger.ErrorContext(ctx, "failed to acknowledge notification with control plane", attrs...)
			}
			return err
		}

		p.metrics.recordCommandLatencies(ctx, p.tunnelID, statusCode, requestKindAttrs, cmd.EnqueuedAt(), cmd.PolledAt(), latencyRecorded)
		logger.InfoContext(ctx, "dispatcher acknowledged notification with control plane")
		return nil
	}

	responseDelivered := p.forwardResponses(ctx, conn, logger, cmd, statusCode, respHeader, requestKindAttrs, latencyRecorded, channel)
	if !responseDelivered && errors.Is(context.Cause(ctx), errResponseDeadlineExceeded) {
		return ctx.Err()
	}
	logger.InfoContext(ctx, "dispatcher forwarded command to MCP server")

	return nil
}

func (p *mcpProcessor) processSessionTerminationCommand(ctx context.Context, logger *slog.Logger, cmd controlplane.SessionTerminationCommand, channelCfg channelConfig, channel types.Channel) error {
	requestKindAttrs := []attribute.KeyValue{
		attribute.String("request_kind", "session_termination"),
		attribute.String("channel", channel.String()),
	}
	latencyRecorded := &latencyFlags{}

	if !channelCfg.features.supportsSessionTermination {
		tunnelResponse := types.NewSessionTerminationResponse(channel, http.StatusMethodNotAllowed, http.Header{})
		tsRequestID, postErr := p.tunnelResponder.PostResponse(ctx, cmd.RequestID(), tunnelResponse)
		if postErr != nil {
			attrs := []any{slog.String("error", postErr.Error())}
			if tsRequestID != "" {
				attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tsRequestID.String()))
			}
			logger.ErrorContext(ctx, "failed to post unsupported session termination response to control plane", attrs...)
			return postErr
		}

		p.metrics.recordCommandLatencies(ctx, p.tunnelID, http.StatusMethodNotAllowed, requestKindAttrs, cmd.EnqueuedAt(), cmd.PolledAt(), latencyRecorded)
		attrs := []any{
			slog.Int("status_code", http.StatusMethodNotAllowed),
			slog.String("channel", channel.String()),
		}
		if tsRequestID != "" {
			attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tsRequestID.String()))
		}
		logger.WarnContext(ctx, "dispatcher rejected MCP session termination for unsupported transport", attrs...)
		return nil
	}

	terminator, ok := channelCfg.transport.(mcpclient.SessionTerminatingTransport)
	if !ok {
		return fmt.Errorf("dispatcher processor: MCP transport does not support session termination")
	}

	statusCode, respHeader, err := terminator.TerminateSession(ctx, cloneHeaders(cmd.Headers()))
	statusCode = normalizeTransportStatusCode(statusCode, err)
	if respHeader == nil {
		respHeader = http.Header{}
	}
	tunnelResponse := types.NewSessionTerminationResponse(channel, statusCode, respHeader)
	tsRequestID, postErr := p.tunnelResponder.PostResponse(ctx, cmd.RequestID(), tunnelResponse)
	if postErr != nil {
		attrs := []any{slog.String("error", postErr.Error())}
		if tsRequestID != "" {
			attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tsRequestID.String()))
		}
		logger.ErrorContext(ctx, "failed to post session termination response to control plane", attrs...)
		return postErr
	}

	p.metrics.recordCommandLatencies(ctx, p.tunnelID, statusCode, requestKindAttrs, cmd.EnqueuedAt(), cmd.PolledAt(), latencyRecorded)
	attrs := []any{
		slog.Int("status_code", statusCode),
		slog.String("channel", channel.String()),
	}
	if err != nil {
		attrs = append(attrs, slog.String("error", err.Error()))
	}
	if tsRequestID != "" {
		attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tsRequestID.String()))
	}
	if statusCode >= http.StatusBadRequest || err != nil {
		logger.WarnContext(ctx, "dispatcher received MCP session termination upstream error; posted response to control plane", attrs...)
		return nil
	}
	logger.InfoContext(ctx, "dispatcher terminated MCP session and posted response to control plane", attrs...)
	return nil
}

func (p *mcpProcessor) processOauthDiscoveryCommand(ctx context.Context, logger *slog.Logger, cmd controlplane.OauthDiscoveryCommand, channel types.Channel) error {
	if p.mcpServerURL == nil {
		return fmt.Errorf("dispatcher processor: missing MCP server URL")
	}

	candidates, _, err := oauth.BuildOAuthDiscoveryCandidates(ctx, p.oauthHTTPClient, p.mcpServerURL, logger)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return fmt.Errorf("dispatcher processor: missing OAuth metadata URLs")
	}

	resp, sourceURL, _, err := oauth.FetchOAuthMetadata(ctx, p.oauthHTTPClient, candidates, logger)
	if err != nil {
		logger.ErrorContext(ctx, "failed to fetch OAuth discovery ProtectedResourceMetaData", slog.String("error", err.Error()))
		return err
	}

	if p.hostBus != nil {
		bundle, _, bundleErr := oauth.BuildURLBundleFromPRMDWithAuthServerMetadata(
			ctx,
			p.oauthHTTPClient,
			resp.Payload(),
			time.Now(),
			sourceURL,
			oauth.URLBundleOptions{
				UnixSocketPath: p.mcpUnixSocketPath,
				UnixSocketURL:  p.mcpServerURL,
			},
			logger,
		)
		if bundleErr != nil {
			logger.WarnContext(ctx, "failed to build OAuth discovery host bundle", slog.String("error", bundleErr.Error()))
		} else if publishErr := p.hostBus.Publish(ctx, bundle); publishErr != nil {
			logger.WarnContext(ctx, "failed to publish OAuth discovery host bundle", slog.String("error", publishErr.Error()))
		} else {
			logger.InfoContext(ctx, "published OAuth discovery host bundle",
				slog.Int("url_count", len(bundle.URLs)))
		}
	}

	tsRequestID, postErr := p.tunnelResponder.PostResponse(ctx, cmd.RequestID(), resp)
	if postErr != nil {
		attrs := []any{slog.String("error", postErr.Error())}
		if tsRequestID != "" {
			attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tsRequestID.String()))
		}
		if errors.Is(postErr, context.DeadlineExceeded) || errors.Is(postErr, context.Canceled) {
			logger.WarnContext(ctx, "context canceled while posting OAuth discovery response", attrs...)
		} else {
			logger.ErrorContext(ctx, "failed to post OAuth discovery response to control plane", attrs...)
		}
		return postErr
	}

	latencyRecorded := &latencyFlags{}
	metricAttrs := []attribute.KeyValue{
		attribute.String("request_kind", "oauth_discovery"),
		attribute.String("channel", channel.String()),
	}
	p.metrics.recordCommandLatencies(ctx, p.tunnelID, resp.ResponseCode(), metricAttrs, cmd.EnqueuedAt(), cmd.PolledAt(), latencyRecorded)

	logger.InfoContext(ctx, "dispatcher delivered OAuth discovery response to control plane",
		slog.Int("status_code", resp.ResponseCode()))
	return nil
}

// forwardResponses streams MCP notifications and the final JSON-RPC response
// back to the control plane while respecting the configured TTL window. The
// MCP lifecycle is complete after a response with the same id as the original
// request is read; delivering that response to the control plane may still fail
// or expire. Intermediate JSON-RPC notifications remain stream events. If the
// downstream connection ends first, the dispatcher posts a terminal error
// response so product callers do not wait forever.
func (p *mcpProcessor) forwardResponses(ctx context.Context, conn mcpclient.ForwardingConnection, logger *slog.Logger, cmd controlplane.JsonRpcCommand, responseCode int, responseHeaders http.Header, metricAttrs []attribute.KeyValue, latencyRecorded *latencyFlags, channel types.Channel) (responseDelivered bool) {
	ttlCtx := ctx
	cancel := func() {}
	if p.connectionMaxTTL > 0 {
		ttlCtx, cancel = context.WithTimeout(ctx, p.connectionMaxTTL)
	}
	defer cancel()

	terminalResponseDelivered := false
	mcpResponseReceived := false
	defer func() {
		if terminalResponseDelivered || (mcpResponseReceived && errors.Is(context.Cause(ctx), errResponseDeadlineExceeded)) {
			return
		}
		if err := conn.Close(); err != nil {
			logger.WarnContext(ctx, "failed to close MCP connection after early response forwarding exit", tunnelFailureLogAttrs(classifyTunnelFailure(0, err), classifyTransportErrorKind(0, err))...)
		}
	}()

	req := cmd.Message().(*jsonrpc.Request)
	postTerminalErrorResponse := func(cause error) {
		if cause == nil {
			return
		}

		statusCode := http.StatusBadGateway
		failure := classifyTunnelFailure(0, cause)
		encodedError, err := buildTunnelFailureJSONRPCErrorResponse(req, statusCode, failure)
		if err != nil {
			logger.ErrorContext(ctx, "failed to encode terminal error response for control plane", slog.String("error", err.Error()))
			return
		}

		tunnelResponse := types.NewTunnelResponse(channel, encodedError, statusCode, jsonRPCResponseHeaders(ctx, logger, responseHeaders))
		tsRequestID, err := p.tunnelResponder.PostResponse(ttlCtx, cmd.RequestID(), tunnelResponse)
		if err != nil {
			attrs := []any{slog.String("error", err.Error())}
			if tsRequestID != "" {
				attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tsRequestID.String()))
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				if errors.Is(ttlCtx.Err(), context.DeadlineExceeded) {
					logger.InfoContext(ctx, "MCP connection TTL reached while delivering terminal error response", attrs...)
				} else {
					logger.DebugContext(ctx, "MCP connection context canceled while delivering terminal error response", attrs...)
				}
			} else {
				logger.ErrorContext(ctx, "failed to post terminal error response to control plane", attrs...)
			}
			return
		}

		p.metrics.recordCommandLatencies(ctx, p.tunnelID, statusCode, metricAttrs, cmd.EnqueuedAt(), cmd.PolledAt(), latencyRecorded)
		responseDelivered = true

		attrs := []any{
			slog.Int("status_code", statusCode),
		}
		attrs = append(attrs, tunnelFailureLogAttrs(failure, classifyTransportErrorKind(0, cause))...)
		if tsRequestID != "" {
			attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tsRequestID.String()))
		}
		logger.WarnContext(ctx, "dispatcher posted terminal downstream error response to control plane", attrs...)
	}

	for {
		msg, readErr := conn.Read(ttlCtx)
		if readErr != nil {
			switch {
			case errors.Is(readErr, mcp.ErrConnectionClosed) || errors.Is(readErr, io.EOF):
				logger.DebugContext(ctx, "MCP connection closed while reading response", tunnelFailureLogAttrs(classifyTunnelFailure(0, readErr), classifyTransportErrorKind(0, readErr))...)
				postTerminalErrorResponse(readErr)
			case errors.Is(readErr, context.DeadlineExceeded), errors.Is(readErr, context.Canceled):
				if errors.Is(ttlCtx.Err(), context.DeadlineExceeded) {
					logger.InfoContext(ctx, "MCP connection TTL reached; stopping response forwarding")
				} else {
					logger.DebugContext(ctx, "MCP connection context canceled while reading response")
				}
			default:
				logger.ErrorContext(ctx, "failed to read response from MCP server", tunnelFailureLogAttrs(classifyTunnelFailure(0, readErr), classifyTransportErrorKind(0, readErr))...)
				postTerminalErrorResponse(readErr)
			}
			return
		}
		if msg == nil {
			// Defensive: a nil message without an error would otherwise spin forever.
			err := newProtocolFailureError(errors.New("received nil message from MCP server without error"))
			logger.ErrorContext(ctx, "received nil message from MCP server without error")
			postTerminalErrorResponse(err)
			return
		}

		if notifyMsg, ok := asNotification(msg); ok {
			if err := p.forwardNotification(ttlCtx, logger, cmd, responseCode, responseHeaders, notifyMsg, channel); err != nil {
				return
			}
			continue
		}

		response, ok := msg.(*jsonrpc.Response)
		if !ok {
			err := newProtocolFailureError(fmt.Errorf("received non-response message from MCP server: %T", msg))
			logger.ErrorContext(
				ctx,
				"received non-response message from MCP server",
				append(attrsToArgs(messageSummaryAttrs(msg)), slog.String("type", fmt.Sprintf("%T", msg)))...,
			)
			postTerminalErrorResponse(err)
			return
		}

		logger.DebugContext(ctx, "dispatcher received response from MCP server", attrsToArgs(jsonRPCResponseCorrelationAttrs(req, response))...)

		encodedResponse, err := jsonrpc.EncodeMessage(response)
		if err != nil || len(encodedResponse) == 0 {
			if err == nil {
				err = errors.New("encoded response from MCP server was empty")
			}
			logger.ErrorContext(ctx, "failed to encode response from MCP server")
			postTerminalErrorResponse(newProtocolFailureError(err))
			return
		}

		// per https://modelcontextprotocol.io/specification/2025-06-18/basic ,
		// Responses MUST include the same ID as the request they correspond to.
		// Notifications MUST NOT include an ID.
		// streamableClientConn.processStream has similar heuristics comparing req/resp IDs and breaking out
		if !response.ID.IsValid() {
			err := newProtocolFailureError(errors.New("received response without valid ID from MCP server"))
			logger.ErrorContext(ctx, "received response without valid ID from MCP server")
			postTerminalErrorResponse(err)
			return
		}
		if response.ID != req.ID {
			err := newProtocolFailureError(errors.New("received response with mismatched ID from MCP server"))
			logger.ErrorContext(ctx, "received response with mismatched ID from MCP server", attrsToArgs(jsonRPCResponseCorrelationAttrs(req, response))...)
			postTerminalErrorResponse(err)
			return
		}
		// The matching terminal MCP response completes the downstream lifecycle.
		// Do not close the connection if response delivery later expires: serialized
		// shared transports may already be serving the next command.
		mcpResponseReceived = true
		finalResponse := true

		// Ensure final JSON-RPC responses present as application/json to the control plane,
		// even if the upstream server labeled them differently, unless the upstream
		// response is already an SSE stream.
		responseHeaders = jsonRPCResponseHeaders(ctx, logger, responseHeaders)

		tunnelResponse := types.NewTunnelResponse(channel, encodedResponse, responseCode, responseHeaders)

		tsRequestID, err := p.tunnelResponder.PostResponse(ttlCtx, cmd.RequestID(), tunnelResponse)
		if err != nil {
			attrs := []any{slog.String("error", err.Error())}
			if tsRequestID != "" {
				attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tsRequestID.String()))
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				if errors.Is(ttlCtx.Err(), context.DeadlineExceeded) {
					logger.InfoContext(ctx, "MCP connection TTL reached while delivering response", attrs...)
				} else {
					logger.DebugContext(ctx, "MCP connection context canceled while delivering response", attrs...)
				}
			} else {
				logger.ErrorContext(ctx, "failed to post response to control plane", attrs...)
			}
			return
		}

		p.metrics.recordCommandLatencies(ctx, p.tunnelID, responseCode, metricAttrs, cmd.EnqueuedAt(), cmd.PolledAt(), latencyRecorded)
		attrs := []any{
			slog.Bool("finalResponse", finalResponse),
			slog.Int("status_code", responseCode),
			slog.String("channel", channel.String()),
		}
		attrs = append(attrs, attrsToArgs(jsonRPCResponseCorrelationAttrs(req, response))...)
		if tsRequestID != "" {
			attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tsRequestID.String()))
		}
		logger.DebugContext(ctx, "dispatcher delivered response to control plane", attrs...)
		responseDelivered = true
		terminalResponseDelivered = true
		return
	}
}

func jsonRPCResponseHeaders(ctx context.Context, logger *slog.Logger, responseHeaders http.Header) http.Header {
	contentType := ""
	if responseHeaders != nil {
		contentType = responseHeaders.Get("Content-Type")
	}

	headers := http.Header{}
	if responseHeaders != nil {
		headers = responseHeaders.Clone()
	}

	if contentType == "" || !isSSEContentType(contentType) {
		originalKind := "non_sse"
		if contentType == "" {
			originalKind = "missing"
		}
		logger.DebugContext(ctx, "overriding Content-Type header", slog.String("original_kind", originalKind), slog.String("new", "application/json"))
		headers.Set("Content-Type", "application/json")
	}

	return headers
}

func (p *mcpProcessor) forwardNotification(ctx context.Context, logger *slog.Logger, cmd controlplane.JsonRpcCommand, responseCode int, responseHeaders http.Header, notifyMsg *jsonrpc.Request, channel types.Channel) error {
	logger.DebugContext(
		ctx,
		"dispatcher received notification from MCP server; forwarding to control plane",
		attrsToArgs(messageSummaryAttrs(notifyMsg))...,
	)

	encodedNotification, err := jsonrpc.EncodeMessage(notifyMsg)
	if err != nil || len(encodedNotification) == 0 {
		if err == nil {
			err = errors.New("encoded notification from MCP server was empty")
		}
		logger.ErrorContext(ctx, "failed to encode notification from MCP server")
		return newProtocolFailureError(err)
	}

	notificationHeaders := responseHeaders
	if notificationHeaders == nil {
		notificationHeaders = http.Header{}
	} else {
		notificationHeaders = notificationHeaders.Clone()
	}
	if notificationHeaders.Get("Content-Type") == "" {
		notificationHeaders.Set("Content-Type", "text/event-stream")
	}

	tunnelNotification := types.NewJSONRPCNotification(channel, encodedNotification, responseCode, notificationHeaders)
	if tsRequestID, err := p.tunnelResponder.PostResponse(ctx, cmd.RequestID(), tunnelNotification); err != nil {
		attrs := []any{slog.String("error", err.Error())}
		if tsRequestID != "" {
			attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tsRequestID.String()))
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			logger.WarnContext(ctx, "context canceled while forwarding notification to control plane", attrs...)
		} else {
			logger.ErrorContext(ctx, "failed to forward notification to control plane", attrs...)
		}
		return err
	}

	return nil
}

// asNotification returns the request when the message is a JSON-RPC notification (request without an ID).
func asNotification(msg jsonrpc.Message) (*jsonrpc.Request, bool) {
	req, ok := msg.(*jsonrpc.Request)
	if !ok || req == nil {
		return nil, false
	}
	if req.IsCall() {
		return nil, false
	}
	return req, true
}

func messageSummaryAttrs(msg jsonrpc.Message) []slog.Attr {
	switch m := msg.(type) {
	case *jsonrpc.Request:
		attrs := []slog.Attr{
			slog.String("message_kind", "request"),
			slog.String("method", m.Method),
			slog.Bool("is_call", m.ID.IsValid()),
		}
		if m.ID.IsValid() {
			attrs = append(attrs, slog.String("id", fmt.Sprint(m.ID.Raw())))
		}
		return attrs
	case *jsonrpc.Response:
		attrs := []slog.Attr{
			slog.String("message_kind", "response"),
			slog.Bool("has_error", m.Error != nil),
		}
		if m.ID.IsValid() {
			attrs = append(attrs, slog.String("id", fmt.Sprint(m.ID.Raw())))
		}
		return attrs
	default:
		return []slog.Attr{
			slog.String("message_kind", fmt.Sprintf("unknown:%T", msg)),
		}
	}
}

func jsonRPCResponseCorrelationAttrs(req *jsonrpc.Request, response *jsonrpc.Response) []slog.Attr {
	if response == nil {
		return nil
	}
	attrs := messageSummaryAttrs(response)
	if req != nil {
		attrs = append(attrs, slog.String("rpc_method", req.Method))
	}
	if response.ID.IsValid() {
		attrs = append(attrs, jsonRPCIDAttr("rpc_response_id", response.ID))
	}
	if response.Error != nil {
		var rpcErr *jsonrpc.Error
		if errors.As(response.Error, &rpcErr) {
			attrs = append(attrs, slog.Int64("rpc_error_code", rpcErr.Code))
		}
	}
	return attrs
}

func jsonRPCIDAttr(name string, id jsonrpc.ID) slog.Attr {
	switch v := id.Raw().(type) {
	case string:
		return slog.String(name, v)
	case int64:
		return slog.Int64(name, v)
	default:
		return slog.String(name, fmt.Sprint(v))
	}
}

func attrsToArgs(attrs []slog.Attr) []any {
	args := make([]any, len(attrs))
	for i, attr := range attrs {
		args[i] = attr
	}
	return args
}

func isSSEContentType(value string) bool {
	if value == "" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "text/event-stream")
}

func buildJSONRPCErrorResponse(req *jsonrpc.Request, statusCode int, cause error) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("nil request provided to build error response")
	}
	if statusCode == 0 {
		statusCode = http.StatusInternalServerError
	}
	message := http.StatusText(statusCode)
	if message == "" {
		message = "mcp transport error"
	}
	if cause != nil {
		message = fmt.Sprintf("%s: %v", message, cause)
	}
	resp := &jsonrpc.Response{
		ID: req.ID,
		Error: &jsonrpc.Error{
			Code:    jsonrpc.CodeInternalError,
			Message: message,
		},
	}
	return jsonrpc.EncodeMessage(resp)
}

func ensureDefaultAcceptHeader(headers http.Header) http.Header {
	clone := cloneHeaders(headers)
	if clone.Get("Accept") == "" {
		clone.Set("Accept", defaultAcceptHeaderValue)
	}
	return clone
}

func cloneHeaders(headers http.Header) http.Header {
	if headers == nil {
		return http.Header{}
	}
	return headers.Clone()
}

func normalizeTransportStatusCode(statusCode int, err error) int {
	if statusCode != 0 {
		return statusCode
	}
	if err != nil {
		return http.StatusBadGateway
	}
	return http.StatusOK
}

func requestKindAttributes(req *jsonrpc.Request) []attribute.KeyValue {
	if req == nil {
		return nil
	}
	kind := "call"
	if !req.IsCall() {
		kind = "notification"
	}

	attrs := []attribute.KeyValue{
		attribute.String("request_kind", kind),
	}
	if req.Method != "" {
		attrs = append(attrs, attribute.String("request_method", req.Method))
	}
	return attrs
}
