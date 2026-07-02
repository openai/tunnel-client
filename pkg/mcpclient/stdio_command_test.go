package mcpclient

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/config"
)

type stubLifecycle struct {
	hooks []fx.Hook
}

func (s *stubLifecycle) Append(hook fx.Hook) {
	s.hooks = append(s.hooks, hook)
}

type stubShutdowner struct {
	ch chan struct{}
}

func (s *stubShutdowner) Shutdown(...fx.ShutdownOption) error {
	if s == nil || s.ch == nil {
		return nil
	}
	select {
	case s.ch <- struct{}{}:
	default:
	}
	return nil
}

func TestStdioCommandTransportRequiresCommand(t *testing.T) {
	lifecycle := &stubLifecycle{}
	shutdowner := &stubShutdowner{}
	transport := newStdioCommandTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), lifecycle, shutdowner)

	_, err := transport.Transport(&config.MCPConfig{})
	if err == nil {
		t.Fatal("expected error for missing command args")
	}
}

func TestStdioCommandTransportStartStop(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("TEST_HELPER_MODE", "wait")

	lifecycle := &stubLifecycle{}
	shutdowner := &stubShutdowner{}
	transport := newStdioCommandTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), lifecycle, shutdowner)

	commandArgs := helperCommandArgs()
	cfg := &config.MCPConfig{
		Command:     strings.Join(commandArgs, " "),
		CommandArgs: commandArgs,
	}
	_, err := transport.Transport(cfg)
	require.NoError(t, err)
	require.Len(t, lifecycle.hooks, 1)

	hook := lifecycle.hooks[0]
	require.NotNil(t, hook.OnStart)
	require.NotNil(t, hook.OnStop)

	require.NoError(t, hook.OnStart(context.Background()))

	transport.mu.Lock()
	started := transport.started
	proc := transport.cmd
	transport.mu.Unlock()
	if !started || proc == nil || proc.Process == nil {
		t.Fatal("expected stdio command process to be started")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, hook.OnStop(stopCtx))
}

func TestStdioCommandTransportRequestsShutdownAfterExit(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("TEST_HELPER_MODE", "exit")

	lifecycle := &stubLifecycle{}
	shutdowner := &stubShutdowner{ch: make(chan struct{}, 1)}
	transport := newStdioCommandTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), lifecycle, shutdowner)

	commandArgs := helperCommandArgs()
	cfg := &config.MCPConfig{
		Command:     strings.Join(commandArgs, " "),
		CommandArgs: commandArgs,
	}
	_, err := transport.Transport(cfg)
	require.NoError(t, err)
	require.Len(t, lifecycle.hooks, 1)

	hook := lifecycle.hooks[0]
	require.NoError(t, hook.OnStart(context.Background()))

	require.Eventually(t, func() bool {
		transport.mu.Lock()
		waitDone := transport.waitDone
		transport.mu.Unlock()
		return len(waitDone) > 0
	}, 5*time.Second, 10*time.Millisecond)

	select {
	case <-shutdowner.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("expected shutdown request after command exit")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, hook.OnStop(stopCtx))
}

func TestStdioEOFReaderCallsOnEOF(t *testing.T) {
	t.Parallel()

	called := false
	reader := &stdioEOFReader{
		reader: strings.NewReader(""),
		onEOF: func() {
			called = true
		},
	}

	_, err := reader.Read(make([]byte, 1))

	require.ErrorIs(t, err, io.EOF)
	require.True(t, called)
}

func TestStdioErrWriterCallsOnWriteError(t *testing.T) {
	t.Parallel()

	writeErr := errors.New("write failed")
	var gotErr error
	writer := &stdioErrWriter{
		writer: errWriter{err: writeErr},
		onError: func(err error) {
			gotErr = err
		},
	}

	_, err := writer.Write([]byte("request"))

	require.ErrorIs(t, err, writeErr)
	require.ErrorIs(t, gotErr, writeErr)
}

func TestStdioCommandTransportRuntimeInfo(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("TEST_HELPER_MODE", "wait")

	lifecycle := &stubLifecycle{}
	shutdowner := &stubShutdowner{}
	transport := newStdioCommandTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), lifecycle, shutdowner)

	commandArgs := helperCommandArgs()
	cfg := &config.MCPConfig{
		Command:     strings.Join(commandArgs, " "),
		CommandArgs: commandArgs,
	}
	_, err := transport.Transport(cfg)
	require.NoError(t, err)
	require.Len(t, lifecycle.hooks, 1)

	hook := lifecycle.hooks[0]
	require.NoError(t, hook.OnStart(context.Background()))

	info := transport.StdioRuntimeInfo()
	require.Equal(t, cfg.Command, info.Command)
	require.Greater(t, info.PID, 0)

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, hook.OnStop(stopCtx))
}

func helperCommandArgs() []string {
	return []string{os.Args[0], "-test.run=TestHelperProcess"}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	switch os.Getenv("TEST_HELPER_MODE") {
	case "exit":
		os.Exit(0)
	default:
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
}

type errWriter struct {
	err error
}

func (w errWriter) Write([]byte) (int, error) {
	return 0, w.err
}
