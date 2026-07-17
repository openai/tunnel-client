package log

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/openai/tunnel-client/pkg/clientinstance"
	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/tunnelctx"
)

const (
	// FieldComponent is the structured logging key used to describe the emitting sub-component.
	FieldComponent = "component"

	// FieldRequestID is the structured logging key for the MCP request identifier. This id assigned by control plane.
	FieldRequestID = "request_id"

	// FieldRpcRequestID is a Request identifier, which is defined by the spec to be a string, integer, or null.
	// https://www.jsonrpc.org/specification#request_object
	FieldRpcRequestID = "rpc_request_id"

	// FieldSessionID is the structured logging key that records the MCP session identifier.
	FieldSessionID = "session_id"

	// FieldControlPlaneCommandRequestID captures the upstream X-Request-Id issued by
	// plugin-service/connectors while they communicate with tunnel-service.
	FieldControlPlaneCommandRequestID = "cmd_request_id"

	// FieldTunnelServiceRequestID captures the X-Request-Id returned when the tunnel-client
	// talks directly to tunnel-service (e.g. poll, post response).
	FieldTunnelServiceRequestID = "tunnel_request_id"

	// FieldTunnelID is the stable structured logging key for the configured tunnel identifier.
	FieldTunnelID = "tunnel_id"

	// FieldClientInstanceID is the process-scoped correlation key shared with tunnel-service.
	FieldClientInstanceID = "client_instance_id"

	ComponentHealth       = "health"
	ComponentDispatcher   = "dispatcher"
	ComponentControlPlane = "controlplane"
	ComponentMcpClient    = "mcpclient"
	ComponentProcess      = "process"
	ComponentCloudflared  = "cloudflared"
	ComponentHarpoon      = "harpoon"
)

// NewLogger constructs a slog.Logger configured according to the provided config.
// It returns the logger along with an optional closer that must be closed by the caller.
func NewLogger(cfg *config.LoggingConfig, defaultWriter io.Writer) (*slog.Logger, io.Closer, error) {
	controller, err := NewLevelController(cfg)
	if err != nil {
		return nil, nil, err
	}
	return NewLoggerWithLevelController(cfg, defaultWriter, controller)
}

// NewLoggerWithLevelController constructs a slog.Logger backed by the provided level controller.
func NewLoggerWithLevelController(cfg *config.LoggingConfig, defaultWriter io.Writer, controller *LevelController) (*slog.Logger, io.Closer, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("logging config is nil")
	}
	if controller == nil {
		return nil, nil, fmt.Errorf("log level controller is nil")
	}

	var logger *slog.Logger
	writer := defaultWriter
	var closer io.Closer

	if cfg.Format == config.LogFormatUnset {
		logger = slog.New(newDefaultHandler(slog.Default().Handler(), controller))
		if cfg.File != "" {
			return nil, nil, fmt.Errorf("invalid logging configuration: file is only supported for json or struct-text")
		}
	} else {

		if cfg.File != "" {
			f, err := os.OpenFile(cfg.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return nil, nil, fmt.Errorf("open log file: %w", err)
			}
			writer = f
			closer = f
		}

		if writer == nil {
			writer = os.Stdout
		}

		handlerOpts := buildHandlerOptions(controller.LevelVar())
		handler, err := buildHandler(cfg.Format, writer, handlerOpts)
		if err != nil {
			CloseIfNeeded(closer)
			return nil, nil, err
		}

		logger = slog.New(handler)
	}

	logger = logger.With(slog.String(FieldClientInstanceID, clientinstance.ID()))

	if cfg.HTTPRawUnsafe {
		logger.Warn("\u26a0\ufe0f  WARNING: Raw HTTP logging enabled \u2014 sensitive data may be exposed")
	}

	return logger, closer, nil
}

// CloseIfNeeded closes the provided closer if it is non-nil, ignoring already-closed errors.
func CloseIfNeeded(closer io.Closer) {
	if closer == nil {
		return
	}
	if err := closer.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		slog.Error("close log file", slog.String("error", err.Error()))
	}
}

func buildHandlerOptions(level slog.Leveler) *slog.HandlerOptions {
	return &slog.HandlerOptions{Level: level}
}

func newDefaultHandler(base slog.Handler, controller *LevelController) slog.Handler {
	if base == nil {
		base = slog.Default().Handler()
	}
	levelVar := controller.LevelVar()
	if levelVar == nil {
		return base
	}
	return &levelFilterHandler{
		base:                base,
		level:               levelVar,
		preserveBaseEnabled: shouldPreserveBaseEnabled(base, controller.Level()),
	}
}

type levelFilterHandler struct {
	base                slog.Handler
	level               *slog.LevelVar
	preserveBaseEnabled bool
}

func (h *levelFilterHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if h == nil {
		return true
	}
	if h.preserveBaseEnabled && h.base != nil && !h.base.Enabled(ctx, level) {
		return false
	}
	if h.level == nil {
		return true
	}
	return level >= h.level.Level()
}

func (h *levelFilterHandler) Handle(ctx context.Context, record slog.Record) error {
	return h.base.Handle(ctx, record)
}

func (h *levelFilterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelFilterHandler{
		base:                h.base.WithAttrs(attrs),
		level:               h.level,
		preserveBaseEnabled: h.preserveBaseEnabled,
	}
}

func (h *levelFilterHandler) WithGroup(name string) slog.Handler {
	return &levelFilterHandler{
		base:                h.base.WithGroup(name),
		level:               h.level,
		preserveBaseEnabled: h.preserveBaseEnabled,
	}
}

func shouldPreserveBaseEnabled(base slog.Handler, startupLevel slog.Level) bool {
	if base == nil {
		return false
	}
	if !isBuiltinDefaultStyleHandler(base) {
		return true
	}
	return !base.Enabled(context.Background(), startupLevel)
}

func isBuiltinDefaultStyleHandler(base slog.Handler) bool {
	switch base.(type) {
	case *slog.TextHandler, *slog.JSONHandler:
		return true
	default:
		return fmt.Sprintf("%T", base) == "*slog.defaultHandler"
	}
}

func buildHandler(format config.LogFormat, writer io.Writer, opts *slog.HandlerOptions) (slog.Handler, error) {
	switch format {
	case config.LogFormatJSON:
		return slog.NewJSONHandler(writer, opts), nil
	case config.LogFormatStructText:
		return slog.NewTextHandler(writer, opts), nil
	default:
		return nil, fmt.Errorf("unsupported log format %q", format.String())
	}
}

// LoggerWithContextIdentifiers returns a logger annotated with any identifiers stored on ctx.
func LoggerWithContextIdentifiers(ctx context.Context, logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return nil
	}
	if requestID, ok := tunnelctx.RequestIDFromContext(ctx); ok {
		logger = logger.With(slog.String(FieldRequestID, requestID))
	}
	if controlPlaneRequestID, ok := tunnelctx.ControlPlaneCommandRequestIDFromContext(ctx); ok {
		logger = logger.With(slog.String(FieldControlPlaneCommandRequestID, controlPlaneRequestID.String()))
	}
	if tunnelServiceRequestID, ok := tunnelctx.TunnelServiceRequestIDFromContext(ctx); ok {
		logger = logger.With(slog.String(FieldTunnelServiceRequestID, tunnelServiceRequestID.String()))
	}
	if rpcRequestID, ok := tunnelctx.RPCRequestIDFromContext(ctx); ok {
		logger = logger.With(rpcRequestIDAttr(rpcRequestID))
	}
	if sessionID, ok := tunnelctx.SessionIDFromContext(ctx); ok {
		logger = logger.With(slog.String(FieldSessionID, sessionID))
	}
	return logger
}

func rpcRequestIDAttr(id jsonrpc.ID) slog.Attr {
	switch v := id.Raw().(type) {
	case string:
		return slog.String(FieldRpcRequestID, v)
	case int64:
		return slog.Int64(FieldRpcRequestID, v)
	default:
		return slog.String(FieldRpcRequestID, fmt.Sprint(v))
	}
}
