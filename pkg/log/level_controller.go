package log

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/openai/tunnel-client/pkg/config"
)

var supportedRuntimeLogLevels = []string{"debug", "info", "warn"}

// LevelController exposes the live slog level used by tunnel-client handlers.
type LevelController struct {
	levelVar *slog.LevelVar
}

// NewLevelController initializes a mutable level controller from the startup config.
func NewLevelController(cfg *config.LoggingConfig) (*LevelController, error) {
	if cfg == nil {
		return nil, fmt.Errorf("logging config is nil")
	}

	levelVar := new(slog.LevelVar)
	levelVar.Set(cfg.Level)
	return &LevelController{levelVar: levelVar}, nil
}

func (c *LevelController) LevelVar() *slog.LevelVar {
	if c == nil {
		return nil
	}
	return c.levelVar
}

func (c *LevelController) Level() slog.Level {
	if c == nil || c.levelVar == nil {
		return slog.LevelInfo
	}
	return c.levelVar.Level()
}

func (c *LevelController) LevelString() string {
	return normalizeLogLevel(c.Level())
}

func (c *LevelController) Set(level slog.Level) {
	if c == nil || c.levelVar == nil {
		return
	}
	c.levelVar.Set(level)
}

func (c *LevelController) SetString(raw string) (slog.Level, error) {
	level, err := ParseRuntimeLogLevel(raw)
	if err != nil {
		return 0, err
	}
	c.Set(level)
	return level, nil
}

func SupportedRuntimeLogLevels() []string {
	out := make([]string, len(supportedRuntimeLogLevels))
	copy(out, supportedRuntimeLogLevels)
	return out
}

func ParseRuntimeLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	default:
		return 0, fmt.Errorf("unsupported log level %q (expected one of: %s)", raw, strings.Join(supportedRuntimeLogLevels, ", "))
	}
}

func normalizeLogLevel(level slog.Level) string {
	return strings.ToLower(level.String())
}
