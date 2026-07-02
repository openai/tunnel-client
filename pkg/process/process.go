package process

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/config"
	tclog "github.com/openai/tunnel-client/pkg/log"
)

// Module wires process-level utilities like PID file management.
var Module = fx.Module("process", fx.Invoke(registerPIDFile))

type pidFileParams struct {
	fx.In

	Lifecycle fx.Lifecycle
	Config    *config.ProcessConfig
	Logger    *slog.Logger
}

func registerPIDFile(p pidFileParams) error {
	if p.Config == nil || p.Config.PIDFile == "" {
		return nil
	}

	logger := p.Logger.With(tclog.FieldComponent, tclog.ComponentProcess)
	pid := os.Getpid()
	pidContents := strconv.Itoa(pid) + "\n"

	p.Lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := os.WriteFile(p.Config.PIDFile, []byte(pidContents), 0o644); err != nil {
				return fmt.Errorf("write pid file %s: %w", p.Config.PIDFile, err)
			}
			logger.InfoContext(ctx, "pid file written", slog.Int("pid", pid), slog.String("path", p.Config.PIDFile))
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if err := os.Remove(p.Config.PIDFile); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove pid file %s: %w", p.Config.PIDFile, err)
			}
			return nil
		},
	})

	return nil
}
