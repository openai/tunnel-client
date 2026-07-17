package cloudflared

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/config"
)

func TestBundledManifestPinsProxyBuiltModule(t *testing.T) {
	t.Parallel()

	manifest := BundledManifest()
	require.Equal(t, "2026.7.2", BundledVersion())
	require.Equal(t, "https://github.com/cloudflare/cloudflared/releases/tag/2026.7.2", manifest.ReleaseURL)
	require.Equal(t, "8679787525edc8575b2948a7c4a50b6292c6d426", manifest.ReleaseCommit)
	require.Equal(t, "github.com/cloudflare/cloudflared", manifest.ModulePath)
	require.Equal(t, "github.com/cloudflare/cloudflared/cmd/cloudflared", manifest.PackagePath)
	require.Equal(t, "v0.0.0-20260715110107-8679787525ed", manifest.ModuleVersion)
	require.Equal(t, "h1:ETvjMMv3sjWuilIWK/0upuYaZI2IX+xK3alCiDmXB+g=", manifest.ModuleSum)
	require.Equal(t, "h1:4bn354lJpAv1wqGJhWWHQNjGzo/WFCkQ6uaLTUYmeqI=", manifest.GoModSum)
	require.Equal(t, "2026-07-15T13:30:00Z", manifest.BuildTime)
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

	state := NewState(&config.CloudflaredConfig{Token: "secret-token"})
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

	var logs bytes.Buffer
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

func newTestSupervisor(t *testing.T, output io.Writer, token string) (*Supervisor, *State) {
	t.Helper()
	cfg := &config.CloudflaredConfig{
		Token:        token,
		Path:         os.Args[0],
		ReadyTimeout: 3 * time.Second,
	}
	state := NewState(cfg)
	logger := slog.New(slog.NewTextHandler(output, nil))
	supervisor, err := NewSupervisor(supervisorParams{
		Config: cfg,
		State:  state,
		Logger: logger,
	})
	require.NoError(t, err)
	supervisor.newCommand = func(_ string, args ...string) *exec.Cmd {
		helperArgs := append([]string{"-test.run=TestCloudflaredHelperProcess", "--"}, args...)
		return exec.Command(os.Args[0], helperArgs...)
	}
	return supervisor, state
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
