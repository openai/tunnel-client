package mcpclient

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
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
	t.Parallel()

	lifecycle := &stubLifecycle{}
	shutdowner := &stubShutdowner{}
	transport := newStdioCommandTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), lifecycle, shutdowner)
	observation := NewProtocolObservation(&config.MCPConfig{TransportKind: config.MCPTransportStdio}, nil)
	transport.observation = observation

	commandArgs := helperCommandArgs("wait")
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

	stop := startStdioCommand(t, hook)
	require.Equal(t, "running", observationDetails(observation).ChildState)
	require.Len(t, observationDetails(observation).ChildGeneration, 32)
	require.False(t, observationDetails(observation).Initialize.OK)

	transport.mu.Lock()
	started := transport.started
	proc := transport.cmd
	transport.mu.Unlock()
	if !started || proc == nil || proc.Process == nil {
		t.Fatal("expected stdio command process to be started")
	}

	require.NoError(t, stop())
	require.Equal(t, "closed", observationDetails(observation).ChildState)
}

func TestStdioCommandTransportRequestsShutdownAfterExit(t *testing.T) {
	t.Parallel()

	lifecycle := &stubLifecycle{}
	shutdowner := &stubShutdowner{ch: make(chan struct{}, 1)}
	transport := newStdioCommandTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), lifecycle, shutdowner)
	observation := NewProtocolObservation(&config.MCPConfig{TransportKind: config.MCPTransportStdio}, nil)
	transport.observation = observation

	commandArgs := helperCommandArgs("exit")
	cfg := &config.MCPConfig{
		Command:     strings.Join(commandArgs, " "),
		CommandArgs: commandArgs,
	}
	_, err := transport.Transport(cfg)
	require.NoError(t, err)
	require.Len(t, lifecycle.hooks, 1)

	hook := lifecycle.hooks[0]
	stop := startStdioCommand(t, hook)

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
	require.Equal(t, "closed", observationDetails(observation).ChildState)

	require.NoError(t, stop())
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
	t.Parallel()

	lifecycle := &stubLifecycle{}
	shutdowner := &stubShutdowner{}
	transport := newStdioCommandTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), lifecycle, shutdowner)

	commandArgs := helperCommandArgs("wait")
	cfg := &config.MCPConfig{
		Command:     strings.Join(commandArgs, " "),
		CommandArgs: commandArgs,
	}
	_, err := transport.Transport(cfg)
	require.NoError(t, err)
	require.Len(t, lifecycle.hooks, 1)

	hook := lifecycle.hooks[0]
	stop := startStdioCommand(t, hook)

	info := transport.StdioRuntimeInfo()
	require.Equal(t, cfg.Command, info.Command)
	require.Greater(t, info.PID, 0)

	require.NoError(t, stop())
}

func startStdioCommand(t *testing.T, hook fx.Hook) func() error {
	t.Helper()
	require.NoError(t, hook.OnStart(context.Background()))
	stop := sync.OnceValue(func() error {
		// Windows helpers exit through stdin EOF; race-instrumented children
		// also need time for the race detector's exit delay.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return hook.OnStop(ctx)
	})
	t.Cleanup(func() { require.NoError(t, stop()) })
	return stop
}

func helperCommandArgs(mode string) []string {
	return []string{os.Args[0], "-test.run=^TestHelperProcess$", "--", mode}
}

func TestHelperProcess(t *testing.T) {
	if len(os.Args) != 4 || os.Args[2] != "--" {
		return
	}
	switch os.Args[3] {
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
