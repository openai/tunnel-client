package log

import (
	"context"
	"log/slog"
)

// Sink receives a copy of each log record emitted by the tunnel-client logger.
//
// Implementations MUST NOT retain slog.Record beyond the scope of Handle unless
// they first clone or fully materialize it, since slog.Record may reference
// transient state.
type Sink interface {
	Handle(ctx context.Context, record slog.Record)
}

type teeHandler struct {
	base slog.Handler
	sink Sink
	// attrs are the slog.Attrs bound to this handler via slog.Logger.With(...).
	// These attrs do not appear on slog.Record directly, but they do appear in output.
	attrs []slog.Attr
}

func newTeeHandler(base slog.Handler, sink Sink) slog.Handler {
	if base == nil {
		base = slog.Default().Handler()
	}
	if sink == nil {
		return base
	}
	return &teeHandler{base: base, sink: sink}
}

func (h *teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *teeHandler) Handle(ctx context.Context, record slog.Record) error {
	if h.sink != nil {
		// Materialize a synthetic record that includes handler-bound attrs so the sink
		// sees the same identifiers (e.g., component/request ids) as the log output.
		combined := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
		record.Attrs(func(a slog.Attr) bool {
			combined.AddAttrs(a)
			return true
		})
		if len(h.attrs) > 0 {
			combined.AddAttrs(h.attrs...)
		}
		h.sink.Handle(ctx, combined)
	}
	return h.base.Handle(ctx, record)
}

func (h *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &teeHandler{
		base:  h.base.WithAttrs(attrs),
		sink:  h.sink,
		attrs: append(append([]slog.Attr{}, h.attrs...), attrs...),
	}
}

func (h *teeHandler) WithGroup(name string) slog.Handler {
	return &teeHandler{
		base:  h.base.WithGroup(name),
		sink:  h.sink,
		attrs: h.attrs,
	}
}
