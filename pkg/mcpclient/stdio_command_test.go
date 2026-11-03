package mcpclient

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
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

type startedStdioChild struct {
	generation uint64
	pid        int
}

func expectStartedStdioChild(t *testing.T, started <-chan startedStdioChild) startedStdioChild {
	t.Helper()
	select {
	case child := <-started:
		return child
	case <-time.After(5 * time.Second):
		t.Fatal("expected stdio child to start")
		return startedStdioChild{}
	}
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
	t.Parallel()

	lifecycle := &stubLifecycle{}
	shutdowner := &stubShutdowner{}
	transport := newStdioCommandTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), lifecycle, shutdowner)

	_, err := transport.Transport(&config.MCPConfig{})
	if err == nil {
		t.Fatal("expected error for missing command args")
	}
}

func TestStdioCommandTransportRejectsInvalidFailurePolicy(t *testing.T) {
	t.Parallel()
	lifecycle := &stubLifecycle{}
	transport := newStdioCommandTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), lifecycle, &stubShutdowner{})
	_, err := transport.Transport(&config.MCPConfig{
		CommandArgs:        helperCommandArgs("wait"),
		StdioFailurePolicy: "unknown",
	})
	require.ErrorContains(t, err, "invalid stdio failure policy")
	require.Empty(t, lifecycle.hooks)
}

func TestStdioCommandTransportStartStop(t *testing.T) {
	t.Parallel()

	lifecycle := &stubLifecycle{}
	shutdowner := &stubShutdowner{ch: make(chan struct{}, 1)}
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
	require.Empty(t, shutdowner.ch, "intentional stop must not request shutdown")
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
		Command:            strings.Join(commandArgs, " "),
		CommandArgs:        commandArgs,
		StdioFailurePolicy: runtimeconfig.StdioFailurePolicyExit,
	}
	_, err := transport.Transport(cfg)
	require.NoError(t, err)
	require.Len(t, lifecycle.hooks, 1)

	hook := lifecycle.hooks[0]
	stop := startStdioCommand(t, hook)

	select {
	case <-shutdowner.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("expected shutdown request after command exit")
	}
	require.Equal(t, "closed", observationDetails(observation).ChildState)

	require.NoError(t, stop())
}

func TestStdioCommandTransportRestartsExitedChild(t *testing.T) {
	t.Parallel()

	lifecycle := &stubLifecycle{}
	shutdowner := &stubShutdowner{ch: make(chan struct{}, 1)}
	transport := newStdioCommandTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), lifecycle, shutdowner)
	transport.observation = NewProtocolObservation(&config.MCPConfig{TransportKind: config.MCPTransportStdio}, nil)
	started := make(chan startedStdioChild, 2)
	transport.onChildStarted = func(generation uint64, pid int) {
		started <- startedStdioChild{generation: generation, pid: pid}
	}
	type loss struct {
		generation uint64
		err        error
	}
	lost := make(chan loss, 2)
	transport.SetOnChildLost(func() {
		_, err := transport.Connect(context.Background())
		lost <- loss{generation: transport.Generation(), err: err}
	})
	commandArgs := helperCommandArgs("exit_once:" + filepath.Join(t.TempDir(), "first-run"))
	base, err := transport.Transport(&config.MCPConfig{CommandArgs: commandArgs})
	require.NoError(t, err)
	require.Same(t, transport, base)
	stop := startStdioCommand(t, lifecycle.hooks[0])
	first := expectStartedStdioChild(t, started)
	require.Equal(t, uint64(1), first.generation)

	select {
	case got := <-lost:
		require.Equal(t, uint64(2), got.generation)
		require.ErrorIs(t, got.err, errStdioChildRestarting)
	case <-time.After(5 * time.Second):
		t.Fatal("expected initial child loss")
	}
	second := expectStartedStdioChild(t, started)
	require.Equal(t, uint64(3), second.generation)
	require.NotEqual(t, first.pid, second.pid)
	require.Equal(t, second.pid, transport.StdioRuntimeInfo().PID)
	require.Equal(t, "running", observationDetails(transport.observation).ChildState)
	// Read the helper handshake before Connect starts the SDK's stdout reader.
	ready := make([]byte, len("ready\n"))
	readyErr := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(transport.transport.Reader, ready)
		readyErr <- err
	}()
	select {
	case err := <-readyErr:
		require.NoError(t, err)
		require.Equal(t, "ready\n", string(ready))
	case <-time.After(5 * time.Second):
		t.Fatal("expected replacement helper signal handler to be ready")
	}
	connection, err := transport.Connect(context.Background())
	require.NoError(t, err)
	versioned, ok := connection.(interface{ physicalGeneration() (uint64, bool) })
	require.True(t, ok)
	physicalGeneration, known := versioned.physicalGeneration()
	require.True(t, known)
	require.Equal(t, uint64(3), physicalGeneration)
	select {
	case <-shutdowner.ch:
		t.Fatal("restart policy requested parent shutdown")
	default:
	}

	if runtime.GOOS == "windows" {
		// Windows cannot forward SIGTERM to the replacement helper. The
		// restart assertions above still run on that platform.
		require.NoError(t, stop())
		return
	}
	transport.forwardSignal(nil, syscall.SIGTERM)
	select {
	case <-shutdowner.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("expected graceful parent shutdown after forwarding signal to replacement")
	}
	stoppedGeneration := transport.Generation()
	require.NoError(t, stop())
	require.Equal(t, stoppedGeneration, transport.Generation())
}

func TestStdioCommandTransportRestartsAfterPipeLoss(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		mode string
	}{
		{name: "stdout_eof", mode: "stdout_eof"},
		{name: "stdin_write_failed", mode: "stdin_closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if runtime.GOOS == "windows" && tc.mode == "stdin_closed" {
				t.Skip("SIGTERM cannot terminate the closed-stdin helper on Windows")
			}
			lifecycle := &stubLifecycle{}
			shutdowner := &stubShutdowner{ch: make(chan struct{}, 1)}
			transport := newStdioCommandTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), lifecycle, shutdowner)
			transport.observation = NewProtocolObservation(&config.MCPConfig{TransportKind: config.MCPTransportStdio}, nil)
			started := make(chan startedStdioChild, 2)
			transport.onChildStarted = func(generation uint64, pid int) {
				started <- startedStdioChild{generation: generation, pid: pid}
			}
			lost := make(chan struct{}, 2)
			transport.SetOnChildLost(func() { lost <- struct{}{} })
			_, err := transport.Transport(&config.MCPConfig{CommandArgs: helperCommandArgs(tc.mode)})
			require.NoError(t, err)
			stop := startStdioCommand(t, lifecycle.hooks[0])
			first := expectStartedStdioChild(t, started)

			switch tc.mode {
			case "stdout_eof":
				_, err = transport.transport.Reader.Read(make([]byte, 1))
				require.ErrorIs(t, err, io.EOF)
			case "stdin_closed":
				ready := make([]byte, len("ready\n"))
				_, err = io.ReadFull(transport.transport.Reader, ready)
				require.NoError(t, err)
				_, err = transport.transport.Writer.Write([]byte("request"))
				require.Error(t, err)
			}

			select {
			case <-lost:
			case <-time.After(5 * time.Second):
				t.Fatal("expected child-loss callback")
			}
			second := expectStartedStdioChild(t, started)
			require.Equal(t, uint64(3), second.generation)
			require.NotEqual(t, first.pid, second.pid)
			require.Equal(t, second.pid, transport.StdioRuntimeInfo().PID)
			require.Empty(t, lost, "concurrent loss paths must be coalesced")
			select {
			case <-shutdowner.ch:
				t.Fatal("restart policy requested parent shutdown")
			default:
			}
			require.NoError(t, stop())
		})
	}
}

func TestStdioCommandTransportStopCancelsRestart(t *testing.T) {
	t.Parallel()
	lifecycle := &stubLifecycle{}
	transport := newStdioCommandTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), lifecycle, &stubShutdowner{ch: make(chan struct{}, 1)})
	started := make(chan startedStdioChild, 2)
	transport.onChildStarted = func(generation uint64, pid int) {
		started <- startedStdioChild{generation: generation, pid: pid}
	}
	lost := make(chan struct{}, 1)
	transport.SetOnChildLost(func() { lost <- struct{}{} })
	_, err := transport.Transport(&config.MCPConfig{CommandArgs: helperCommandArgs("exit")})
	require.NoError(t, err)
	stop := startStdioCommand(t, lifecycle.hooks[0])
	expectStartedStdioChild(t, started)
	select {
	case <-lost:
	case <-time.After(5 * time.Second):
		t.Fatal("expected child loss")
	}
	require.NoError(t, stop())
	require.Equal(t, uint64(2), transport.Generation())
	require.Zero(t, transport.StdioRuntimeInfo().PID)
	require.Empty(t, started, "stop must prevent a new child")
}

func TestStdioCommandTransportParentSignalDuringRestart(t *testing.T) {
	t.Parallel()
	var transport *stdioCommandTransport
	lost := make(chan struct{}, 1)
	app := fx.New(
		fx.NopLogger,
		fx.Invoke(func(lifecycle fx.Lifecycle, shutdowner fx.Shutdowner) error {
			transport = newStdioCommandTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), lifecycle, shutdowner)
			transport.SetOnChildLost(func() { lost <- struct{}{} })
			_, err := transport.Transport(&config.MCPConfig{CommandArgs: helperCommandArgs("exit")})
			return err
		}),
	)
	require.NoError(t, app.Err())
	require.NoError(t, app.Start(context.Background()))
	select {
	case <-lost:
	case <-time.After(5 * time.Second):
		t.Fatal("expected child loss before parent signal")
	}
	transport.forwardSignal(nil, syscall.SIGTERM)
	select {
	case shutdown := <-app.Wait():
		require.Equal(t, 0, shutdown.ExitCode)
	case <-time.After(5 * time.Second):
		t.Fatal("expected graceful shutdown during recovery")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, app.Stop(ctx))
	require.Equal(t, uint64(2), transport.Generation())
	require.Zero(t, transport.StdioRuntimeInfo().PID)
}

func TestStdioCommandTransportCrashLoopBackoff(t *testing.T) {
	t.Parallel()
	lifecycle := &stubLifecycle{}
	transport := newStdioCommandTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), lifecycle, &stubShutdowner{ch: make(chan struct{}, 1)})
	lost := make(chan struct{}, 16)
	transport.SetOnChildLost(func() { lost <- struct{}{} })
	_, err := transport.Transport(&config.MCPConfig{CommandArgs: helperCommandArgs("exit")})
	require.NoError(t, err)
	stop := startStdioCommand(t, lifecycle.hooks[0])
	for range 2 {
		select {
		case <-lost:
		case <-time.After(5 * time.Second):
			t.Fatal("expected two child losses")
		}
	}
	transport.mu.Lock()
	delay := transport.restartDelay
	transport.mu.Unlock()
	require.GreaterOrEqual(t, delay, 2*stdioRestartInitialDelay)
	require.NoError(t, stop())
}

func TestStdioCommandTransportShutdownExitCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		mode     string
		exitCode int
	}{
		{name: "child_exit", mode: "exit", exitCode: 1},
		{name: "stdout_eof", mode: "stdout_eof", exitCode: 1},
		{name: "stdin_closed", mode: "stdin_closed", exitCode: 1},
		{name: "forwarded_parent_signal", mode: "wait", exitCode: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if runtime.GOOS == "windows" && (tc.name == "stdin_closed" || tc.name == "forwarded_parent_signal") {
				t.Skip("SIGTERM cannot terminate the closed-stdin helper on Windows")
			}

			var transport *stdioCommandTransport
			commandArgs := helperCommandArgs(tc.mode)
			app := fx.New(
				fx.NopLogger,
				fx.Invoke(func(lifecycle fx.Lifecycle, shutdowner fx.Shutdowner) error {
					transport = newStdioCommandTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), lifecycle, shutdowner)
					transport.observation = NewProtocolObservation(&config.MCPConfig{TransportKind: config.MCPTransportStdio}, nil)
					_, err := transport.Transport(&config.MCPConfig{
						Command:            strings.Join(commandArgs, " "),
						CommandArgs:        commandArgs,
						StdioFailurePolicy: runtimeconfig.StdioFailurePolicyExit,
					})
					return err
				}),
			)
			require.NoError(t, app.Err())
			require.NoError(t, app.Start(context.Background()))
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				require.NoError(t, app.Stop(ctx))
			})

			switch tc.name {
			case "stdout_eof":
				_, err := transport.transport.Reader.Read(make([]byte, 1))
				require.ErrorIs(t, err, io.EOF)
			case "stdin_closed":
				ready := make([]byte, len("ready\n"))
				_, err := io.ReadFull(transport.transport.Reader, ready)
				require.NoError(t, err)
				require.Equal(t, "ready\n", string(ready))
				_, err = transport.transport.Writer.Write([]byte("request"))
				require.Error(t, err)
			case "forwarded_parent_signal":
				transport.forwardSignal(transport.cmd, syscall.SIGTERM)
				// Ensure the helper exits even if it has not processed SIGTERM yet.
				_ = transport.stdin.Close()
			}

			select {
			case shutdown := <-app.Wait():
				require.Equal(t, tc.exitCode, shutdown.ExitCode)
			case <-time.After(5 * time.Second):
				t.Fatal("expected shutdown request after unexpected stdio closure")
			}
		})
	}
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
	t.Parallel()

	if len(os.Args) != 4 || os.Args[2] != "--" {
		return
	}
	switch os.Args[3] {
	case "exit":
		os.Exit(0)
	case "stdout_eof":
		_ = os.Stdout.Close()
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "stdin_closed":
		// Arm shutdown before publishing readiness: the parent can immediately
		// detect the broken pipe and terminate this helper.
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		_ = os.Stdin.Close()
		_, _ = io.WriteString(os.Stdout, "ready\n")
		<-signals
		os.Exit(0)
	default:
		if marker, ok := strings.CutPrefix(os.Args[3], "exit_once:"); ok {
			if file, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); err == nil {
				_ = file.Close()
				os.Exit(0)
			}
			// Bazel test binaries install a SIGTERM handler that consumes the
			// signal. Register our own before announcing readiness so the test
			// does not depend on signaling the child before that handler starts.
			signals := make(chan os.Signal, 1)
			signal.Notify(signals, syscall.SIGTERM)
			go func() {
				<-signals
				os.Exit(0)
			}()
			_, _ = io.WriteString(os.Stdout, "ready\n")
		}
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
