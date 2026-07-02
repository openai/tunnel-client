package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/fx"

	"go.openai.org/api/tunnel-client/pkg/config"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
)

type stdioCommandTransport struct {
	logger     *slog.Logger
	lifecycle  fx.Lifecycle
	shutdowner fx.Shutdowner

	mu        sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	transport *mcp.IOTransport
	waitDone  chan error
	signals   chan os.Signal
	started   bool
	stopping  bool

	commandLabel string
	shutdownOnce sync.Once
}

// StdioRuntimeInfo describes the active stdio MCP process details.
type StdioRuntimeInfo struct {
	PID     int    `json:"pid,omitempty"`
	Command string `json:"command,omitempty"`
}

// StdioRuntimeInfoProvider exposes runtime details for stdio transport.
type StdioRuntimeInfoProvider interface {
	StdioRuntimeInfo() StdioRuntimeInfo
}

func newStdioCommandTransport(logger *slog.Logger, lifecycle fx.Lifecycle, shutdowner fx.Shutdowner) *stdioCommandTransport {
	if logger != nil {
		logger = logger.With(
			slog.String(tclog.FieldComponent, tclog.ComponentMcpClient),
			slog.String("transport", "stdio"),
		)
	}
	return &stdioCommandTransport{
		logger:     logger,
		lifecycle:  lifecycle,
		shutdowner: shutdowner,
	}
}

func (t *stdioCommandTransport) Transport(cfg *config.MCPConfig) (*mcp.IOTransport, error) {
	if cfg == nil {
		return nil, errors.New("mcpclient: mcp config is required for stdio transport")
	}
	if len(cfg.CommandArgs) == 0 {
		return nil, errors.New("mcpclient: mcp.command is required for stdio transport")
	}
	if t.lifecycle == nil {
		return nil, errors.New("mcpclient: lifecycle is required for stdio transport")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.transport != nil {
		return t.transport, nil
	}

	cmd := exec.Command(cfg.CommandArgs[0], cfg.CommandArgs[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcpclient: stdio command stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("mcpclient: stdio command stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	t.cmd = cmd
	t.stdin = stdin
	t.stdout = stdout
	t.waitDone = make(chan error, 1)
	t.commandLabel = strings.TrimSpace(cfg.Command)
	if t.commandLabel == "" {
		t.commandLabel = strings.Join(cfg.CommandArgs, " ")
	}

	reader := &stdioEOFReader{
		reader: stdout,
		onEOF: func() {
			t.requestShutdown("stdio MCP command stdout closed", io.EOF)
		},
	}
	writer := &stdioErrWriter{
		writer: stdin,
		onError: func(err error) {
			t.requestShutdown("stdio MCP command stdin write failed", err)
		},
	}
	t.transport = &mcp.IOTransport{
		Reader: reader,
		Writer: writer,
	}

	t.lifecycle.Append(fx.Hook{
		OnStart: t.start,
		OnStop:  t.stop,
	})

	return t.transport, nil
}

func (t *stdioCommandTransport) start(ctx context.Context) error {
	t.mu.Lock()
	cmd := t.cmd
	if cmd == nil || t.started {
		t.mu.Unlock()
		return nil
	}
	t.started = true
	t.mu.Unlock()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mcpclient: start stdio command: %w", err)
	}

	t.logInfo(ctx, "stdio MCP command started", slog.String("command", t.commandLabel))
	t.startSignalForwarding()

	go t.waitForExit()
	return nil
}

func (t *stdioCommandTransport) stop(ctx context.Context) error {
	t.stopSignalForwarding()

	t.mu.Lock()
	cmd := t.cmd
	waitDone := t.waitDone
	stdin := t.stdin
	started := t.started
	if started {
		t.stopping = true
	}
	t.mu.Unlock()

	if cmd == nil || !started {
		return nil
	}

	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}

	if waitDone == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-waitDone:
		return nil
	}
}

func (t *stdioCommandTransport) waitForExit() {
	t.mu.Lock()
	cmd := t.cmd
	waitDone := t.waitDone
	t.mu.Unlock()
	if cmd == nil || waitDone == nil {
		return
	}

	err := cmd.Wait()
	stopping := t.isStopping()
	if err != nil {
		t.logWarn(context.Background(), "stdio MCP command exited", slog.String("command", t.commandLabel), slog.String("error", err.Error()))
	} else {
		t.logInfo(context.Background(), "stdio MCP command exited", slog.String("command", t.commandLabel))
	}
	if !stopping {
		t.requestShutdown("stdio MCP command exited", err)
	}

	select {
	case waitDone <- err:
	default:
	}
}

func (t *stdioCommandTransport) isStopping() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopping
}

func (t *stdioCommandTransport) requestShutdown(reason string, cause error) {
	if t == nil || t.isStopping() {
		return
	}
	t.shutdownOnce.Do(func() {
		attrs := []any{slog.String("reason", reason), slog.String("command", t.commandLabel)}
		if cause != nil {
			attrs = append(attrs, slog.String("error", cause.Error()))
		}
		if t.shutdowner == nil {
			t.logWarn(context.Background(), "stdio MCP command failed; shutdowner unavailable", attrs...)
			return
		}
		t.logWarn(context.Background(), "stdio MCP command failed; requesting tunnel-client shutdown", attrs...)
		if err := t.shutdowner.Shutdown(); err != nil {
			t.logWarn(context.Background(), "stdio MCP shutdown request failed", slog.String("error", err.Error()))
		}
	})
}

func (t *stdioCommandTransport) startSignalForwarding() {
	t.mu.Lock()
	cmd := t.cmd
	if cmd == nil || cmd.Process == nil || t.signals != nil {
		t.mu.Unlock()
		return
	}
	signals := make(chan os.Signal, 2)
	t.signals = signals
	t.mu.Unlock()

	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		for sig := range signals {
			if cmd.Process == nil {
				continue
			}
			_ = cmd.Process.Signal(sig)
		}
	}()
}

func (t *stdioCommandTransport) stopSignalForwarding() {
	t.mu.Lock()
	signals := t.signals
	t.signals = nil
	t.mu.Unlock()
	if signals == nil {
		return
	}
	signal.Stop(signals)
	close(signals)
}

func (t *stdioCommandTransport) StdioRuntimeInfo() StdioRuntimeInfo {
	t.mu.Lock()
	defer t.mu.Unlock()

	info := StdioRuntimeInfo{
		Command: t.commandLabel,
	}
	if info.Command == "" && t.cmd != nil {
		info.Command = strings.Join(t.cmd.Args, " ")
	}
	if t.cmd != nil && t.cmd.Process != nil {
		info.PID = t.cmd.Process.Pid
	}
	return info
}

func (t *stdioCommandTransport) logInfo(ctx context.Context, msg string, attrs ...any) {
	if t.logger == nil {
		return
	}
	t.logger.InfoContext(ctx, msg, attrs...)
}

func (t *stdioCommandTransport) logWarn(ctx context.Context, msg string, attrs ...any) {
	if t.logger == nil {
		return
	}
	t.logger.WarnContext(ctx, msg, attrs...)
}

type stdioEOFReader struct {
	reader io.Reader
	onEOF  func()
}

func (r *stdioEOFReader) Read(p []byte) (int, error) {
	if r.reader == nil {
		return 0, io.EOF
	}
	n, err := r.reader.Read(p)
	if err != nil && errors.Is(err, io.EOF) && r.onEOF != nil {
		r.onEOF()
	}
	return n, err
}

func (r *stdioEOFReader) Close() error {
	if r.reader == nil {
		return nil
	}
	if closer, ok := r.reader.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

type stdioErrWriter struct {
	writer  io.Writer
	onError func(error)
}

func (w *stdioErrWriter) Write(p []byte) (int, error) {
	if w.writer == nil {
		return 0, io.ErrClosedPipe
	}
	n, err := w.writer.Write(p)
	if err != nil && w.onError != nil {
		w.onError(err)
	}
	return n, err
}

func (w *stdioErrWriter) Close() error {
	if w.writer == nil {
		return nil
	}
	if closer, ok := w.writer.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
