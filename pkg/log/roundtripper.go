package log

import (
	"log/slog"
	"net/http"
	"net/http/httputil"

	"go.openai.org/api/tunnel-client/pkg/config"
)

// LoggingRoundTripper wraps a base RoundTripper and emits raw HTTP request and response dumps when enabled.
type LoggingRoundTripper struct {
	base   http.RoundTripper
	logger *slog.Logger
}

// NewRoundTripper constructs a RoundTripper that logs raw HTTP traffic when the provided logging config enables it.
// The component name, when non-empty, is attached to the emitted logs via the FieldComponent attribute.
func NewRoundTripper(base http.RoundTripper, logger *slog.Logger, cfg *config.LoggingConfig, component string) http.RoundTripper {
	if base == nil {
		panic("log.NewRoundTripper: base transport is required")
	}
	if logger == nil {
		panic("log.NewRoundTripper: logger is required")
	}
	if cfg == nil {
		panic("log.NewRoundTripper: logging config is required")
	}

	if !cfg.HTTPRawUnsafe {
		return base
	}

	if component != "" {
		logger = logger.With(slog.String(FieldComponent, component))
	}

	return &LoggingRoundTripper{
		base:   base,
		logger: logger,
	}
}

// RoundTrip logs raw request and response dumps surrounding the underlying transport call.
func (l *LoggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if l.logger == nil {
		return l.base.RoundTrip(req)
	}

	l.dumpRequest(req)

	resp, err := l.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if resp != nil {
		l.dumpResponse(req, resp)
	}
	return resp, nil
}

func (l *LoggingRoundTripper) dumpRequest(req *http.Request) {
	if dumpReq, err := httputil.DumpRequestOut(req, true); err == nil {
		l.logger.DebugContext(req.Context(), "raw http request", slog.String("dump", string(dumpReq)))
	} else {
		l.logger.WarnContext(req.Context(), "failed to dump raw http request", slog.String("error", err.Error()))
	}
}

func (l *LoggingRoundTripper) dumpResponse(req *http.Request, resp *http.Response) {
	if dumpResp, err := httputil.DumpResponse(resp, true); err == nil {
		l.logger.DebugContext(req.Context(), "raw http response", slog.String("dump", string(dumpResp)))
	} else {
		l.logger.WarnContext(req.Context(), "failed to dump raw http response", slog.String("error", err.Error()))
	}
}
