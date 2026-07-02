package log

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"go.openai.org/api/tunnel-client/pkg/clientinstance"
	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/types"
)

type recordSink struct {
	records []slog.Record
}

func (s *recordSink) Handle(_ context.Context, record slog.Record) {
	s.records = append(s.records, record.Clone())
}

func TestNewLoggerBindsTunnelIDToSinkRecords(t *testing.T) {
	t.Parallel()

	sink := &recordSink{}
	levelControl, err := newLevelController(&config.LoggingConfig{
		Format: config.LogFormatStructText,
		Level:  slog.LevelInfo,
	})
	require.NoError(t, err)

	logger, err := newLogger(loggerParams{
		Config: &config.LoggingConfig{
			Format: config.LogFormatStructText,
			Level:  slog.LevelInfo,
		},
		ControlPlane: &config.ControlPlaneConfig{
			TunnelID: types.TunnelID("tunnel_0123456789abcdef0123456789abcdef"),
		},
		LevelControl:  levelControl,
		DefaultWriter: io.Discard,
		Sink:          sink,
	})
	require.NoError(t, err)

	logger.Info("support diagnostics ready")
	require.Len(t, sink.records, 1)

	attrs := map[string]any{}
	sink.records[0].Attrs(func(attr slog.Attr) bool {
		attrs[attr.Key] = attr.Value.Any()
		return true
	})
	require.Equal(t, "tunnel_0123456789abcdef0123456789abcdef", attrs[FieldTunnelID])
	require.Equal(t, clientinstance.ID(), attrs[FieldClientInstanceID])
}
