package process

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"github.com/openai/tunnel-client/pkg/config"
	tclog "github.com/openai/tunnel-client/pkg/log"
)

type captureState struct {
	mu        sync.Mutex
	lastAttrs map[string]slog.Value
}

type captureHandler struct {
	state *captureState
	attrs []slog.Attr
}

func newCaptureHandler() *captureHandler {
	return &captureHandler{state: &captureState{}}
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

func (h *captureHandler) Handle(_ context.Context, record slog.Record) error {
	attrs := make(map[string]slog.Value, len(h.attrs))
	for _, attr := range h.attrs {
		attrs[attr.Key] = attr.Value
	}
	record.Attrs(func(attr slog.Attr) bool {
		attrs[attr.Key] = attr.Value
		return true
	})

	h.state.mu.Lock()
	h.state.lastAttrs = attrs
	h.state.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	combined := append([]slog.Attr{}, h.attrs...)
	combined = append(combined, attrs...)
	return &captureHandler{state: h.state, attrs: combined}
}

func (h *captureHandler) WithGroup(_ string) slog.Handler {
	return h
}

func (h *captureHandler) LastAttrs() map[string]slog.Value {
	h.state.mu.Lock()
	defer h.state.mu.Unlock()
	if h.state.lastAttrs == nil {
		return nil
	}
	attrs := make(map[string]slog.Value, len(h.state.lastAttrs))
	for key, value := range h.state.lastAttrs {
		attrs[key] = value
	}
	return attrs
}

func TestRegisterPIDFileSuccess(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "tunnel-client.pid")

	handler := newCaptureHandler()
	logger := slog.New(handler)
	app := fxtest.New(
		t,
		fx.Supply(&config.ProcessConfig{PIDFile: pidPath}),
		fx.Supply(logger),
		fx.Invoke(registerPIDFile),
	)

	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("start app: %v", err)
	}

	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse pid file: %v", err)
	}
	if pid != os.Getpid() {
		t.Fatalf("pid file mismatch: got %d want %d", pid, os.Getpid())
	}

	attrs := handler.LastAttrs()
	component, ok := attrs[tclog.FieldComponent]
	if !ok {
		t.Fatalf("missing log component field")
	}
	if component.String() != tclog.ComponentProcess {
		t.Fatalf("unexpected component field: got %q want %q", component.String(), tclog.ComponentProcess)
	}

	if err := app.Stop(ctx); err != nil {
		t.Fatalf("stop app: %v", err)
	}

	if _, err := os.Stat(pidPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected pid file to be removed, stat err: %v", err)
	}
}

func TestRegisterPIDFileWriteFailure(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := tempDir

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := fxtest.New(
		t,
		fx.Supply(&config.ProcessConfig{PIDFile: pidPath}),
		fx.Supply(logger),
		fx.Invoke(registerPIDFile),
	)

	ctx := context.Background()
	err := app.Start(ctx)
	if err == nil {
		t.Fatal("expected start error for unwritable pid file")
	}
}

func TestRegisterPIDFileRemoveFailure(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "tunnel-client.pid")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := fxtest.New(
		t,
		fx.Supply(&config.ProcessConfig{PIDFile: pidPath}),
		fx.Supply(logger),
		fx.Invoke(registerPIDFile),
	)

	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("start app: %v", err)
	}

	if err := os.Remove(pidPath); err != nil {
		t.Fatalf("remove pid file before replacement: %v", err)
	}
	if err := os.Mkdir(pidPath, 0o700); err != nil {
		t.Fatalf("replace pid file with directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pidPath, "child"), []byte("data"), 0o600); err != nil {
		t.Fatalf("populate pid directory: %v", err)
	}

	err := app.Stop(ctx)
	if err == nil {
		t.Fatal("expected stop error for pid file removal")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected non-ErrNotExist removal error: %v", err)
	}
}
