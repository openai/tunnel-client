package log

import (
	"context"
	"io"
	"log/slog"

	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/clientinstance"
	"github.com/openai/tunnel-client/pkg/config"
)

// Module exposes the logger as an Fx module that owns the lifecycle of any
// resources it creates.
var Module = fx.Module("logger", fx.Provide(newLevelController, newLogger))

type loggerParams struct {
	fx.In

	Lifecycle     fx.Lifecycle
	Config        *config.LoggingConfig
	ControlPlane  *config.ControlPlaneConfig
	LevelControl  *LevelController
	DefaultWriter io.Writer `optional:"true"`
	Sink          Sink      `optional:"true"`
}

func newLevelController(cfg *config.LoggingConfig) (*LevelController, error) {
	return NewLevelController(cfg)
}

func newLogger(p loggerParams) (*slog.Logger, error) {
	logger, closer, err := NewLoggerWithLevelController(p.Config, p.DefaultWriter, p.LevelControl)
	if err != nil {
		return nil, err
	}

	if p.Sink != nil && logger != nil {
		logger = slog.New(newTeeHandler(
			logger.Handler(),
			p.Sink,
			slog.String(FieldClientInstanceID, clientinstance.ID()),
		))
	}
	if logger != nil && p.ControlPlane != nil && p.ControlPlane.TunnelID != "" {
		logger = logger.With(slog.String(FieldTunnelID, p.ControlPlane.TunnelID.String()))
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
