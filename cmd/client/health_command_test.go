package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.openai.org/api/tunnel-client/pkg/healthurl"
)

func TestHealthCommandWithURLFileAndPIDFile(t *testing.T) {
	t.Parallel()

	server := healthTestServer(t, http.StatusOK, "live", http.StatusOK, "ready")
	t.Cleanup(server.Close)

	tempDir := t.TempDir()
	urlFile := filepath.Join(tempDir, "health.url")
	pidFile := filepath.Join(tempDir, "health.pid")
	require.NoError(t, os.WriteFile(urlFile, []byte(server.URL+"/healthz\n"), 0o600))
	require.NoError(t, os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600))

	stdout, stderr, err := executeCommand(t, map[string]string{}, "health", "--url-file", urlFile, "--pid-file", pidFile)

	require.NoError(t, err, stderr)
	require.Empty(t, stderr)
	require.Contains(t, stdout, "Locator: url_file="+urlFile)
	require.Contains(t, stdout, "Healthz: PASS")
	require.Contains(t, stdout, "Readyz: PASS")
	require.Contains(t, stdout, "Process: PASS")
	require.Contains(t, stdout, "Result: OK")
}

func TestHealthCommandPortSugar(t *testing.T) {
	t.Parallel()

	server := healthTestServer(t, http.StatusOK, "live", http.StatusOK, "ready")
	t.Cleanup(server.Close)

	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	_, portText, err := net.SplitHostPort(u.Host)
	require.NoError(t, err)

	stdout, stderr, err := executeCommand(t, map[string]string{}, "health", "--port", portText, "--json")

	require.NoError(t, err, stderr)
	require.Empty(t, stderr)

	var report healthReport
	require.NoError(t, json.Unmarshal([]byte(stdout), &report))
	require.Equal(t, "ok", report.Result)
	require.Equal(t, server.URL, report.BaseURL)
	require.True(t, report.Healthz.OK)
	require.True(t, report.Readyz.OK)
}

func TestHealthCommandReturnsExitCode2WhenNotReady(t *testing.T) {
	t.Parallel()

	server := healthTestServer(t, http.StatusOK, "live", http.StatusServiceUnavailable, "oauth discovery pending")
	t.Cleanup(server.Close)

	stdout, stderr, err := executeCommand(t, map[string]string{}, "health", "--url", server.URL)

	require.Error(t, err)
	require.Equal(t, 2, exitCode(err))
	require.Empty(t, stderr)
	require.Contains(t, stdout, "Healthz: PASS")
	require.Contains(t, stdout, "Readyz: FAIL")
	require.Contains(t, stdout, "oauth discovery pending")
	require.Contains(t, stdout, "Result: FAIL")
}

func TestHealthCommandRejectsMissingLocator(t *testing.T) {
	t.Parallel()

	_, stderr, err := executeCommand(t, map[string]string{}, "health")

	require.Error(t, err)
	require.Empty(t, stderr)
	require.Contains(t, err.Error(), "choose exactly one of --url, --url-file, or --port")
}

func TestHealthCommandPIDFileCrossCheckFailsCleanly(t *testing.T) {
	t.Parallel()

	server := healthTestServer(t, http.StatusOK, "live", http.StatusOK, "ready")
	t.Cleanup(server.Close)

	stdout, stderr, err := executeCommand(t, map[string]string{}, "health", "--url", server.URL, "--pid-file", filepath.Join(t.TempDir(), "missing.pid"))

	require.Error(t, err)
	require.Equal(t, 2, exitCode(err))
	require.Empty(t, stderr)
	require.Contains(t, stdout, "Process: FAIL")
	require.Contains(t, stdout, "read ")
	require.Contains(t, stdout, "Result: FAIL")
}

func TestHealthCommandRequiresControlPlanePoll(t *testing.T) {
	t.Parallel()

	server := healthTestServerWithMetrics(
		t,
		http.StatusOK,
		"live",
		http.StatusOK,
		"ready",
		http.StatusOK,
		"commands_poll_last_successful_timestamp_seconds 1\n",
	)
	t.Cleanup(server.Close)

	stdout, stderr, err := executeCommand(
		t,
		map[string]string{},
		"health",
		"--url",
		server.URL,
		"--require-control-plane-poll",
	)

	require.NoError(t, err, stderr)
	require.Empty(t, stderr)
	require.Contains(t, stdout, "Control-plane poll: PASS")
	require.Contains(t, stdout, "last_success_unix_seconds=1")
	require.Contains(t, stdout, "Result: OK")
}

func TestHealthCommandRequiresControlPlanePollFailsBeforeFirstSuccessfulPoll(t *testing.T) {
	t.Parallel()

	server := healthTestServerWithMetrics(
		t,
		http.StatusOK,
		"live",
		http.StatusOK,
		"ready",
		http.StatusOK,
		"commands_poll_last_successful_timestamp_seconds 0\n",
	)
	t.Cleanup(server.Close)

	stdout, stderr, err := executeCommand(
		t,
		map[string]string{},
		"health",
		"--url",
		server.URL,
		"--require-control-plane-poll",
	)

	require.Error(t, err)
	require.Equal(t, 2, exitCode(err))
	require.Empty(t, stderr)
	require.Contains(t, stdout, "Control-plane poll: FAIL")
	require.Contains(t, stdout, "no successful control-plane poll observed")
	require.Contains(t, stdout, "Result: FAIL")
}

func TestHealthCommandWithUnixSocketURLFile(t *testing.T) {
	t.Parallel()

	socketPath := shortSocketPath(t, "tunnel-client-health-command-*.sock")
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = listener.Close()
	})

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/healthz":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("live"))
			case "/readyz":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("ready"))
			case "/metrics":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("commands_poll_last_successful_timestamp_seconds 1\n"))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}),
	}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, server.Shutdown(ctx))
	})

	urlFile := filepath.Join(t.TempDir(), "health.url")
	require.NoError(t, os.WriteFile(urlFile, []byte(healthurl.BuildUnixBaseURL(socketPath)+"\n"), 0o600))

	stdout, stderr, err := executeCommand(
		t,
		map[string]string{},
		"health",
		"--url-file",
		urlFile,
		"--require-control-plane-poll",
	)

	require.NoError(t, err, stderr)
	require.Empty(t, stderr)
	require.Contains(t, stdout, "Healthz: PASS")
	require.Contains(t, stdout, "Readyz: PASS")
	require.Contains(t, stdout, "Control-plane poll: PASS")
	require.Contains(t, stdout, "Result: OK")
}

func healthTestServer(t *testing.T, healthStatus int, healthBody string, readyStatus int, readyBody string) *httptest.Server {
	return healthTestServerWithMetrics(
		t,
		healthStatus,
		healthBody,
		readyStatus,
		readyBody,
		http.StatusNotFound,
		"",
	)
}

func healthTestServerWithMetrics(
	t *testing.T,
	healthStatus int,
	healthBody string,
	readyStatus int,
	readyBody string,
	metricsStatus int,
	metricsBody string,
) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(healthStatus)
			_, _ = w.Write([]byte(healthBody))
		case "/readyz":
			w.WriteHeader(readyStatus)
			_, _ = w.Write([]byte(readyBody))
		case "/metrics":
			w.WriteHeader(metricsStatus)
			_, _ = w.Write([]byte(metricsBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}
