package log_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"go.openai.org/api/tunnel-client/pkg/clientinstance"
	"go.openai.org/api/tunnel-client/pkg/config"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
	"go.openai.org/api/tunnel-client/pkg/tunnelctx"
	"go.openai.org/api/tunnel-client/pkg/types"
)

func TestLoggingContextHelpers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ctx = tunnelctx.ContextWithRequestID(ctx, "req-456")
	ctx = tunnelctx.ContextWithSessionID(ctx, "session-123")
	ctx = tunnelctx.ContextWithControlPlaneCommandRequestID(ctx, types.ControlPlaneRequestID("control-plane-req-789"))
	ctx = tunnelctx.ContextWithTunnelServiceRequestID(ctx, types.TunnelServiceRequestID("tunnel-service-req-456"))
	id, err := jsonrpc.MakeID(float64(12))
	if err != nil {
		t.Fatalf("make id: %v", err)
	}
	ctx = tunnelctx.ContextWithRPCRequestID(ctx, id)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger = tclog.LoggerWithContextIdentifiers(ctx, logger)
	logger.InfoContext(ctx, "test message")

	if !strings.Contains(buf.String(), "session_id=session-123") {
		t.Fatalf("expected session attribute in logs, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "request_id=req-456") {
		t.Fatalf("expected request attribute in logs, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "cmd_request_id=control-plane-req-789") {
		t.Fatalf("expected control plane command request attribute in logs, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "tunnel_request_id=tunnel-service-req-456") {
		t.Fatalf("expected tunnel service request attribute in logs, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "rpc_request_id=12") {
		t.Fatalf("expected rpc request attribute in logs, got: %s", buf.String())
	}

	t.Run("request only", func(t *testing.T) {
		ctx := tunnelctx.ContextWithRequestID(context.Background(), "only-req")
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))
		logger = tclog.LoggerWithContextIdentifiers(ctx, logger)
		logger.InfoContext(ctx, "request only")

		if !strings.Contains(buf.String(), "request_id=only-req") {
			t.Fatalf("expected request attribute in logs, got: %s", buf.String())
		}
		if strings.Contains(buf.String(), "session_id") {
			t.Fatalf("did not expect session attribute in logs, got: %s", buf.String())
		}
		if strings.Contains(buf.String(), "rpc_request_id") {
			t.Fatalf("did not expect rpc request attribute in logs, got: %s", buf.String())
		}
	})

	t.Run("string rpc id", func(t *testing.T) {
		strID, err := jsonrpc.MakeID("rpc-abc")
		if err != nil {
			t.Fatalf("make id: %v", err)
		}
		ctx := tunnelctx.ContextWithRPCRequestID(context.Background(), strID)
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))
		logger = tclog.LoggerWithContextIdentifiers(ctx, logger)
		logger.InfoContext(ctx, "string rpc id")

		if !strings.Contains(buf.String(), "rpc_request_id=rpc-abc") {
			t.Fatalf("expected rpc request attribute in logs, got: %s", buf.String())
		}
	})
}

func TestNewLoggerRejectsFileWithUnsetFormat(t *testing.T) {
	t.Parallel()

	cfg := &config.LoggingConfig{
		Format: config.LogFormatUnset,
		File:   "/tmp/should-not-be-opened",
	}

	_, _, err := tclog.NewLogger(cfg, io.Discard)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestNewLoggerEmitsRawHTTPWarning(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cfg := &config.LoggingConfig{
		Format:        config.LogFormatStructText,
		Level:         slog.LevelInfo,
		HTTPRawUnsafe: true,
	}

	_, closer, err := tclog.NewLogger(cfg, &buf)
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	tclog.CloseIfNeeded(closer)

	if !strings.Contains(buf.String(), "Raw HTTP logging enabled") {
		t.Fatalf("expected raw HTTP warning in logs, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "client_instance_id="+clientinstance.ID()) {
		t.Fatalf("expected client instance ID in logs, got: %s", buf.String())
	}
}

func TestNewLoggerWritesToFileAndCloses(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test.log")

	cfg := &config.LoggingConfig{
		Format: config.LogFormatJSON,
		Level:  slog.LevelInfo,
		File:   logPath,
	}

	logger, closer, err := tclog.NewLogger(cfg, nil)
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	logger.Info("hello", slog.String("k", "v"))
	tclog.CloseIfNeeded(closer)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("expected log file to contain data")
	}
}

func TestNewLoggerRejectsUnsupportedFormat(t *testing.T) {
	t.Parallel()

	cfg := &config.LoggingConfig{
		Format: config.LogFormat(99),
		Level:  slog.LevelInfo,
	}

	_, _, err := tclog.NewLogger(cfg, io.Discard)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestNewLoggerRejectsNilConfig(t *testing.T) {
	t.Parallel()

	_, _, err := tclog.NewLogger(nil, io.Discard)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCloseIfNeededHandlesErrors(t *testing.T) {
	t.Parallel()

	tclog.CloseIfNeeded(nil)

	tclog.CloseIfNeeded(&errorCloser{err: errors.New("close failed")})
}

func TestLevelControllerUpdatesLoggerOutput(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cfg := &config.LoggingConfig{
		Format: config.LogFormatStructText,
		Level:  slog.LevelInfo,
	}

	controller, err := tclog.NewLevelController(cfg)
	if err != nil {
		t.Fatalf("NewLevelController returned error: %v", err)
	}

	logger, closer, err := tclog.NewLoggerWithLevelController(cfg, &buf, controller)
	if err != nil {
		t.Fatalf("NewLoggerWithLevelController returned error: %v", err)
	}
	defer tclog.CloseIfNeeded(closer)

	logger.Debug("before-switch")
	if strings.Contains(buf.String(), "before-switch") {
		t.Fatalf("did not expect debug line before level switch, got: %s", buf.String())
	}

	controller.Set(slog.LevelDebug)
	logger.Debug("after-switch")

	if !strings.Contains(buf.String(), "after-switch") {
		t.Fatalf("expected debug line after level switch, got: %s", buf.String())
	}
}

func TestLevelControllerUpdatesDefaultLoggerOutput(t *testing.T) {
	originalDefault := slog.Default()
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(originalDefault)

	cfg := &config.LoggingConfig{
		Format: config.LogFormatUnset,
		Level:  slog.LevelInfo,
	}

	controller, err := tclog.NewLevelController(cfg)
	if err != nil {
		t.Fatalf("NewLevelController returned error: %v", err)
	}

	logger, closer, err := tclog.NewLoggerWithLevelController(cfg, io.Discard, controller)
	if err != nil {
		t.Fatalf("NewLoggerWithLevelController returned error: %v", err)
	}
	defer tclog.CloseIfNeeded(closer)

	logger.Debug("before-default-switch")
	if strings.Contains(buf.String(), "before-default-switch") {
		t.Fatalf("did not expect debug line before default level switch, got: %s", buf.String())
	}

	controller.Set(slog.LevelDebug)
	logger.Debug("after-default-switch")

	if !strings.Contains(buf.String(), "after-default-switch") {
		t.Fatalf("expected debug line after default level switch, got: %s", buf.String())
	}
}

func TestLevelControllerPreservesBaseDefaultHandlerFiltering(t *testing.T) {
	originalDefault := slog.Default()
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(originalDefault)

	cfg := &config.LoggingConfig{
		Format: config.LogFormatUnset,
		Level:  slog.LevelDebug,
	}

	controller, err := tclog.NewLevelController(cfg)
	if err != nil {
		t.Fatalf("NewLevelController returned error: %v", err)
	}

	logger, closer, err := tclog.NewLoggerWithLevelController(cfg, io.Discard, controller)
	if err != nil {
		t.Fatalf("NewLoggerWithLevelController returned error: %v", err)
	}
	defer tclog.CloseIfNeeded(closer)

	logger.Warn("warn-should-still-be-filtered")
	if strings.Contains(buf.String(), "warn-should-still-be-filtered") {
		t.Fatalf("did not expect warn line to bypass base handler filtering, got: %s", buf.String())
	}

	logger.Error("error-should-pass")
	if !strings.Contains(buf.String(), "error-should-pass") {
		t.Fatalf("expected error line to pass base handler filtering, got: %s", buf.String())
	}
}

type errorCloser struct {
	err error
}

func (c *errorCloser) Close() error { return c.err }
