package log

import (
	"context"
	"io"
	"log/slog"

	"go.uber.org/fx"

	"go.openai.org/api/tunnel-client/pkg/config"
)

// Module exposes the logger as an Fx module that owns the lifecycle of any
// resources it creates.
var Module = fx.Module("logger", fx.Provide(newLogger))

type loggerParams struct {
	fx.In

	Lifecycle     fx.Lifecycle
	Config        *config.LoggingConfig
	DefaultWriter io.Writer `optional:"true"`
	Sink          Sink      `optional:"true"`
}

func newLogger(p loggerParams) (*slog.Logger, error) {
	logger, closer, err := NewLogger(p.Config, p.DefaultWriter)
	if err != nil {
		return nil, err
	}

	if p.Sink != nil && logger != nil {
		logger = slog.New(newTeeHandler(logger.Handler(), p.Sink))
	}

	if closer != nil {
		closer := closer // capture for closure
		p.Lifecycle.Append(fx.Hook{
			OnStop: func(ctx context.Context) error {
				CloseIfNeeded(closer)
				return nil
			},
		})
	}

	return logger, nil
}
