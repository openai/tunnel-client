package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/controlplane"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

func TestBundledManifestPinsProxyBuiltModule(t *testing.T) {
	t.Parallel()

	manifest := BundledManifest()
	require.Equal(t, "2026.8.2", BundledVersion())
	require.Equal(t, "https://github.com/cloudflare/cloudflared/releases/tag/2026.8.2", manifest.ReleaseURL)
	require.Equal(t, "733bfb939963e150dcf5c4faddb1603f744fbc98", manifest.ReleaseCommit)
	require.Equal(t, "github.com/cloudflare/cloudflared", manifest.ModulePath)
	require.Equal(t, "github.com/cloudflare/cloudflared/cmd/cloudflared", manifest.PackagePath)
	require.Equal(t, "v0.0.0-20260814112252-733bfb939963", manifest.ModuleVersion)
	require.Equal(t, "h1:bDpUfzzz9W6u6C9deKDAjpLwyiCV/DdOoJaEzG6I3RQ=", manifest.ModuleSum)
	require.Equal(t, "h1:v97UyAHiewwyRGcpkuzO3d1Jbv4I8OBPuEjOAK5mZ08=", manifest.GoModSum)
	require.Equal(t, "2026-08-14T12:23:25Z", manifest.BuildTime)
	require.Equal(t, "tunnel-client maintainers", manifest.SecurityPatchOwner)
	require.Equal(t, []string{
		"linux/amd64",
		"linux/arm64",
		"darwin/amd64",
		"darwin/arm64",
		"windows/amd64",
		"windows/arm64",
	}, manifest.Platforms)
}

func TestStateKeepsTokenOutOfReadiness(t *testing.T) {
	t.Parallel()

	state := NewState(&runtimeconfig.CloudflaredSettings{Token: "secret-token"})
	ready, reason := state.Readiness()
	require.False(t, ready)
	require.Equal(t, "cloudflared startup pending", reason)
	require.NotContains(t, reason, "secret-token")
	state.setReady()
	ready, reason = state.Readiness()
	require.True(t, ready)
	require.Empty(t, reason)
}

func TestSupervisorLaunchesStopsAndRedactsOutput(t *testing.T) {
	t.Setenv("GO_WANT_CLOUDFLARED_HELPER", "1")
	t.Setenv("CLOUDFLARED_HELPER_MODE", "ready")
	t.Setenv("CLOUDFLARED_HELPER_ECHO_TOKEN", "1")

	var logs lockedBuffer
	supervisor, state := newTestSupervisor(t, &logs, "secret-cloudflared-token")
	startCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, supervisor.Start(startCtx))

	ready, reason := state.Readiness()
	require.True(t, ready, reason)
	require.Eventually(t, func() bool {
		return strings.Contains(logs.String(), "[REDACTED]")
	}, 2*time.Second, 10*time.Millisecond)
	require.NotContains(t, logs.String(), "secret-cloudflared-token")

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	require.NoError(t, supervisor.Stop(stopCtx))
	ready, reason = state.Readiness()
	require.False(t, ready)
	require.Equal(t, "cloudflared stopped", reason)
}

func TestSupervisorFetchesManagedRuntimeTokenAndRedactsOutput(t *testing.T) {
	t.Setenv("GO_WANT_CLOUDFLARED_HELPER", "1")
	t.Setenv("CLOUDFLARED_HELPER_MODE", "ready")
	t.Setenv("CLOUDFLARED_HELPER_ECHO_TOKEN", "1")

	const runtimeToken = "managed-runtime-secret-token"
	fetcher := &managedRuntimeFetcherStub{
		runtime: &controlplane.ManagedCloudflareTunnelRuntime{
			CloudflareTunnel: controlplane.ManagedCloudflareTunnelMetadata{
				TunnelID:  "provider-tunnel-id",
				Name:      "managed-provider-name",
				AccountID: "provider-account-id",
			},
			RuntimeToken: runtimeToken,
		},
	}
	cfg := &runtimeconfig.CloudflaredSettings{
		Managed:      true,
		Path:         os.Args[0],
		ReadyTimeout: 3 * time.Second,
	}
	redactedOutput := make(chan struct{}, 1)
	var logs lockedBuffer
	supervisor, state := newTestSupervisorWithConfig(t, &signalWriter{
		writer: &logs,
		marker: "[REDACTED]",
		seen:   redactedOutput,
	}, cfg, fetcher)

	startCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, supervisor.Start(startCtx))
	require.Equal(t, 1, fetcher.calls)
	require.Empty(t, cfg.Token, "fetched runtime token must not be persisted in config")

	ready, reason := state.Readiness()
	require.True(t, ready, reason)
	select {
	case <-redactedOutput:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for redacted cloudflared output")
	}
	require.Contains(t, logs.String(), "[REDACTED]")
	require.NotContains(t, logs.String(), runtimeToken)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	require.NoError(t, supervisor.Stop(stopCtx))
}

func TestSupervisorPrefersConfiguredTokenOverManagedFetch(t *testing.T) {
	t.Setenv("GO_WANT_CLOUDFLARED_HELPER", "1")
	t.Setenv("CLOUDFLARED_HELPER_MODE", "ready")

	fetcher := &managedRuntimeFetcherStub{
		err: errors.New("managed fetch must not run"),
	}
	cfg := &runtimeconfig.CloudflaredSettings{
		Token:        "configured-cloudflared-token",
		Managed:      true,
		Path:         os.Args[0],
		ReadyTimeout: 3 * time.Second,
	}
	supervisor, _ := newTestSupervisorWithConfig(t, io.Discard, cfg, fetcher)

	startCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, supervisor.Start(startCtx))
	require.Zero(t, fetcher.calls)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	require.NoError(t, supervisor.Stop(stopCtx))
}

func TestSupervisorManagedFetchFailureIsTokenSafe(t *testing.T) {
	t.Parallel()

	const runtimeToken = "managed-runtime-secret-token"
	fetcher := &managedRuntimeFetcherStub{
		err: errors.New("fetch failed with " + runtimeToken),
	}
	cfg := &runtimeconfig.CloudflaredSettings{
		Managed:      true,
		Path:         os.Args[0],
		ReadyTimeout: 3 * time.Second,
	}
	supervisor, state := newTestSupervisorWithConfig(t, io.Discard, cfg, fetcher)

	err := supervisor.Start(context.Background())
	require.Error(t, err)
	require.NotContains(t, err.Error(), runtimeToken)
	require.Equal(t, 1, fetcher.calls)
	ready, reason := state.Readiness()
	require.False(t, ready)
	require.NotContains(t, reason, runtimeToken)
}

func TestSupervisorReturnsStartupFailureWhenChildExits(t *testing.T) {
	t.Setenv("GO_WANT_CLOUDFLARED_HELPER", "1")
	t.Setenv("CLOUDFLARED_HELPER_MODE", "exit-before-ready")

	supervisor, state := newTestSupervisor(t, io.Discard, "secret-cloudflared-token")
	startCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := supervisor.Start(startCtx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "process exited before readiness")
	require.NotContains(t, err.Error(), "secret-cloudflared-token")
	ready, reason := state.Readiness()
	require.False(t, ready)
	require.NotContains(t, reason, "secret-cloudflared-token")
}

func TestSupervisorSurfacesUnexpectedExitAfterReady(t *testing.T) {
	t.Setenv("GO_WANT_CLOUDFLARED_HELPER", "1")
	t.Setenv("CLOUDFLARED_HELPER_MODE", "exit-file")
	exitFile := filepath.Join(t.TempDir(), "exit")
	t.Setenv("CLOUDFLARED_HELPER_EXIT_FILE", exitFile)

	supervisor, state := newTestSupervisor(t, io.Discard, "secret-cloudflared-token")
	startCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, supervisor.Start(startCtx))
	require.NoError(t, os.WriteFile(exitFile, []byte("exit"), 0o600))

	select {
	case err := <-supervisor.Failures():
		require.Error(t, err)
		require.Contains(t, err.Error(), "cloudflared process exited")
		require.NotContains(t, err.Error(), "secret-cloudflared-token")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for unexpected cloudflared exit")
	}
	ready, reason := state.Readiness()
	require.False(t, ready)
	require.Contains(t, reason, "cloudflared process exited")
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	require.NoError(t, supervisor.Stop(stopCtx))
}

func TestSupervisorMonitorKeepsExitFailureAfterInFlightReadyProbe(t *testing.T) {
	t.Parallel()

	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	var signalProbe sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		signalProbe.Do(func() { close(probeStarted) })
		<-releaseProbe
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseProbe) })
	}
	t.Cleanup(release)

	supervisor, state := newTestSupervisor(t, io.Discard, "secret-cloudflared-token")
	exited := make(chan struct{})
	supervisor.mu.Lock()
	supervisor.metricsAddr = strings.TrimPrefix(server.URL, "http://")
	supervisor.exited = exited
	supervisor.mu.Unlock()
	state.setReady()

	monitorCtx, monitorCancel := context.WithCancel(context.Background())
	t.Cleanup(monitorCancel)
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		supervisor.monitorReadiness(monitorCtx)
	}()

	select {
	case <-probeStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for in-flight readiness probe")
	}

	// Match waitForExit's publication order: record the terminal exit under
	// the supervisor lock, publish failed readiness, then release exit waiters.
	supervisor.mu.Lock()
	supervisor.exitedDone = true
	supervisor.waitErr = errors.New("exit status 23")
	supervisor.mu.Unlock()
	state.setNotReady("cloudflared process exited: exit status 23")
	close(exited)

	release()
	select {
	case <-monitorDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for readiness monitor to stop")
	}

	ready, reason := state.Readiness()
	require.False(t, ready)
	require.Equal(t, "cloudflared process exited: exit status 23", reason)
}

func TestSupervisorForcesManagementDiagnosticsOffInChild(t *testing.T) {
	t.Setenv("GO_WANT_CLOUDFLARED_HELPER", "1")
	t.Setenv("CLOUDFLARED_HELPER_MODE", "ready")
	t.Setenv("CLOUDFLARED_HELPER_REQUIRE_MANAGEMENT_DIAGNOSTICS_DISABLED", "1")
	t.Setenv("TUNNEL_MANAGEMENT_DIAGNOSTICS", "true")
	t.Setenv("tunnel_management_diagnostics", "1")
	t.Setenv("Tunnel_Management_Diagnostics", "false")

	supervisor, state := newTestSupervisor(t, io.Discard, "secret-cloudflared-token")
	startCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, supervisor.Start(startCtx))

	ready, reason := state.Readiness()
	require.True(t, ready, reason)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	require.NoError(t, supervisor.Stop(stopCtx))
}

func TestCloudflaredEnvironmentReplacesInheritedToken(t *testing.T) {
	t.Parallel()

	env := cloudflaredEnvironment([]string{
		"PATH=/bin",
		"TUNNEL_TOKEN=old-secret",
		"CLOUDFLARED_TOKEN_REF=new-secret",
	}, "new-secret")
	require.Contains(t, env, "PATH=/bin")
	require.Contains(t, env, "TUNNEL_TOKEN=new-secret")
	require.NotContains(t, env, "TUNNEL_TOKEN=old-secret")
	require.NotContains(t, env, "CLOUDFLARED_TOKEN_REF=new-secret")
}

func TestCloudflaredEnvironmentDisablesManagementDiagnostics(t *testing.T) {
	t.Parallel()

	env := cloudflaredEnvironment([]string{
		"PATH=/bin",
		"TUNNEL_MANAGEMENT_DIAGNOSTICS=true",
		"tunnel_management_diagnostics=1",
		"Tunnel_Management_Diagnostics=false",
	}, "new-secret")

	var diagnostics []string
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, "TUNNEL_MANAGEMENT_DIAGNOSTICS") {
			diagnostics = append(diagnostics, entry)
		}
	}

	require.Equal(t, []string{"TUNNEL_MANAGEMENT_DIAGNOSTICS=false"}, diagnostics)
}

func newTestSupervisor(t *testing.T, output io.Writer, token string) (*Supervisor, *State) {
	t.Helper()
	cfg := &runtimeconfig.CloudflaredSettings{
		Token:        token,
		Path:         os.Args[0],
		ReadyTimeout: 3 * time.Second,
	}
	return newTestSupervisorWithConfig(t, output, cfg, nil)
}

func newTestSupervisorWithConfig(t *testing.T, output io.Writer, cfg *runtimeconfig.CloudflaredSettings, fetcher controlplane.ManagedCloudflareTunnelFetcher) (*Supervisor, *State) {
	t.Helper()
	state := NewState(cfg)
	logger := slog.New(slog.NewTextHandler(output, nil))
	supervisor, err := NewSupervisor(SupervisorParams{
		Config:                cfg,
		State:                 state,
		Logger:                logger,
		ManagedRuntimeFetcher: fetcher,
	})
	require.NoError(t, err)
	supervisor.newCommand = func(_ string, args ...string) *exec.Cmd {
		helperArgs := append([]string{"-test.run=TestCloudflaredHelperProcess", "--"}, args...)
		return exec.Command(os.Args[0], helperArgs...)
	}
	return supervisor, state
}

type managedRuntimeFetcherStub struct {
	runtime *controlplane.ManagedCloudflareTunnelRuntime
	err     error
	calls   int
}

type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

type signalWriter struct {
	writer io.Writer
	marker string
	seen   chan<- struct{}
}

func (w *signalWriter) Write(payload []byte) (int, error) {
	written, err := w.writer.Write(payload)
	if err != nil {
		return written, err
	}
	if strings.Contains(string(payload[:written]), w.marker) {
		select {
		case w.seen <- struct{}{}:
		default:
		}
	}
	return written, nil
}

func (b *lockedBuffer) Write(payload []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(payload)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func (f *managedRuntimeFetcherStub) FetchManagedCloudflareTunnel(context.Context) (*controlplane.ManagedCloudflareTunnelRuntime, error) {
	f.calls++
	return f.runtime, f.err
}

func TestCloudflaredHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CLOUDFLARED_HELPER") != "1" {
		return
	}

	metricsAddr := helperArgValue(os.Args, "--metrics")
	if metricsAddr == "" {
		fmt.Fprintln(os.Stderr, "missing --metrics")
		os.Exit(2)
	}
	if os.Getenv("CLOUDFLARED_HELPER_REQUIRE_MANAGEMENT_DIAGNOSTICS_DISABLED") == "1" {
		var diagnostics []string
		for _, entry := range os.Environ() {
			key, _, ok := strings.Cut(entry, "=")
			if ok && strings.EqualFold(key, "TUNNEL_MANAGEMENT_DIAGNOSTICS") {
				diagnostics = append(diagnostics, entry)
			}
		}
		if len(diagnostics) != 1 || diagnostics[0] != "TUNNEL_MANAGEMENT_DIAGNOSTICS=false" {
			_, _ = fmt.Fprintf(os.Stderr, "unexpected management diagnostics environment: %v\n", diagnostics)
			os.Exit(2)
		}
	}
	mode := os.Getenv("CLOUDFLARED_HELPER_MODE")
	if mode == "exit-before-ready" {
		os.Exit(23)
	}

	listener, err := net.Listen("tcp", metricsAddr)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(2)
	}
	mux := http.NewServeMux()
	mux.HandleFunc(readinessPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()

	if os.Getenv("CLOUDFLARED_HELPER_ECHO_TOKEN") == "1" {
		_, _ = fmt.Fprintln(os.Stdout, os.Getenv("TUNNEL_TOKEN"))
	}
	if mode == "exit-file" {
		exitFile := os.Getenv("CLOUDFLARED_HELPER_EXIT_FILE")
		for {
			if _, err := os.Stat(exitFile); err == nil {
				_ = server.Close()
				os.Exit(23)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	<-signals
	_ = server.Close()
	os.Exit(0)
}

func helperArgValue(args []string, key string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}
