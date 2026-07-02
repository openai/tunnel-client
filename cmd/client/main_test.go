package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"go.openai.org/api/tunnel-client/pkg/app"
	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/health"
	"go.openai.org/api/tunnel-client/pkg/healthurl"
	"go.openai.org/api/tunnel-client/pkg/types"
)

func TestAppBoots(t *testing.T) {
	tempDir := t.TempDir()
	healthURLPath := filepath.Join(tempDir, "health_url")
	pidPath := filepath.Join(tempDir, "pid")
	tunnelID := types.TunnelID("tunnel_0123456789abcdef0123456789abcdef")

	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tunnels/" + url.PathEscape(tunnelID.String()):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"` + tunnelID.String() + `","name":"test tunnel","description":"test fixture"}`))
		case "/v1/tunnels/" + url.PathEscape(tunnelID.String()) + "/poll":
			w.WriteHeader(http.StatusNoContent)
		case "/v1/tunnels/" + url.PathEscape(tunnelID.String()) + "/response":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(controlPlane.Close)

	cfg := &config.Config{
		ControlPlane: config.ControlPlaneConfig{
			BaseURL:             mustParseURL(t, controlPlane.URL),
			TunnelID:            tunnelID,
			APIKey:              "test-api-key",
			MaxInFlightRequests: 1,
			PollTimeout:         100 * time.Millisecond,
		},
		Logging: config.LoggingConfig{
			Level: slog.LevelInfo,
		},
		Health: config.HealthConfig{
			ListenAddr: "127.0.0.1:0",
			URLFile:    healthURLPath,
		},
		Process: config.ProcessConfig{
			PIDFile: pidPath,
		},
		MCP: config.MCPConfig{
			TransportKind:         config.MCPTransportStdio,
			Command:               "cat",
			CommandArgs:           []string{"cat"},
			ConnectionMaxTTL:      time.Minute,
			MaxConcurrentRequests: 10,
		},
	}

	var svc health.Service
	stopped := false

	opts := app.Options(
		cfg,
		fx.StartTimeout(5*time.Second),
		fx.StopTimeout(5*time.Second),
		fx.Populate(&svc),
	)

	app := fxtest.New(t, opts...)
	t.Cleanup(func() {
		if !stopped {
			app.RequireStop()
		}
	})

	app.RequireStart()

	require.NotNil(t, svc)

	addr, err := svc.Addr(2 * time.Second)
	require.NoError(t, err)
	baseURL := "http://" + addr

	client := &http.Client{Timeout: 2 * time.Second}

	endpoints := map[string]string{
		"/healthz": "live",
	}

	for path, wantBody := range endpoints {
		resp, err := client.Get(baseURL + path)
		require.NoErrorf(t, err, "GET %s", path)
		body, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		require.NoError(t, readErr)
		require.NoError(t, closeErr)
		require.Equalf(t, http.StatusOK, resp.StatusCode, "%s response status", path)
		require.Containsf(t, string(body), wantBody, "%s response body", path)
	}

	require.NoError(t, waitForReady(client, baseURL, 6*time.Second))

	resp, err := client.Get(baseURL + "/metrics")
	require.NoError(t, err)
	metricsBody, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	require.Equal(t, http.StatusOK, resp.StatusCode, "metrics response status")
	require.Contains(t, string(metricsBody), "liveness", "metrics should include liveness gauge")

	resp, err = client.Get(baseURL + "/api/logs/export?minutes=30")
	require.NoError(t, err)
	exportBody, readErr := io.ReadAll(resp.Body)
	closeErr = resp.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	require.Equal(t, http.StatusOK, resp.StatusCode, "logs export response status")
	exportFiles := readTarGzFiles(t, exportBody)
	require.Contains(t, exportFiles, "tunnel-client.logs.ndjson")
	require.Contains(t, exportFiles, "tunnel-client.metrics.prom")
	require.Contains(t, exportFiles["tunnel-client.metrics.prom"], "liveness")

	resp, err = client.Get(baseURL + "/ui")
	require.NoError(t, err)
	uiBody, uiReadErr := io.ReadAll(resp.Body)
	uiCloseErr := resp.Body.Close()
	require.NoError(t, uiReadErr)
	require.NoError(t, uiCloseErr)
	require.Equal(t, http.StatusOK, resp.StatusCode, "ui response status")
	require.Contains(t, string(uiBody), "tunnel-client", "ui should include tunnel-client title")

	healthURLContents, err := os.ReadFile(healthURLPath)
	require.NoError(t, err)
	require.Equal(t, baseURL, strings.TrimSpace(string(healthURLContents)), "health URL file contents")

	pidContents, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	require.Equal(t, strconv.Itoa(os.Getpid()), strings.TrimSpace(string(pidContents)), "pid file contents")

	app.RequireStop()
	stopped = true

	_, err = os.Stat(pidPath)
	require.ErrorIs(t, err, os.ErrNotExist, "pid file removed on shutdown")
	_, err = os.Stat(healthURLPath)
	require.ErrorIs(t, err, os.ErrNotExist, "health URL file removed on shutdown")
}

func TestAppFailsToStartWithBusyHealthPort(t *testing.T) {
	busyListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, busyListener.Close())
	}()

	cfg := &config.Config{
		ControlPlane: config.ControlPlaneConfig{
			BaseURL:             mustParseURL(t, "http://127.0.0.1"),
			TunnelID:            types.TunnelID("tunnel_0123456789abcdef0123456789abcdef"),
			APIKey:              "test-api-key",
			MaxInFlightRequests: 1,
			PollTimeout:         100 * time.Millisecond,
		},
		Logging: config.LoggingConfig{
			Level: slog.LevelInfo,
		},
		Health: config.HealthConfig{
			ListenAddr: busyListener.Addr().String(),
		},
		MCP: config.MCPConfig{
			TransportKind:         config.MCPTransportStdio,
			Command:               "cat",
			CommandArgs:           []string{"cat"},
			ConnectionMaxTTL:      time.Minute,
			MaxConcurrentRequests: 10,
		},
	}

	app := fxtest.New(t,
		app.Options(
			cfg,
			fx.StartTimeout(5*time.Second),
			fx.StopTimeout(5*time.Second),
		)...,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = app.Start(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "address already in use")
}

func TestAppServesHealthAndAdminOverUnixSocket(t *testing.T) {
	tempDir := t.TempDir()
	socketPath := shortSocketPath(t, "tunnel-client-main-health-*.sock")
	healthURLPath := filepath.Join(tempDir, "health_url")
	tunnelID := types.TunnelID("tunnel_0123456789abcdef0123456789abcdef")

	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tunnels/" + url.PathEscape(tunnelID.String()):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"` + tunnelID.String() + `","name":"test tunnel","description":"test fixture"}`))
		case "/v1/tunnels/" + url.PathEscape(tunnelID.String()) + "/poll":
			w.WriteHeader(http.StatusNoContent)
		case "/v1/tunnels/" + url.PathEscape(tunnelID.String()) + "/response":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(controlPlane.Close)

	cfg := &config.Config{
		ControlPlane: config.ControlPlaneConfig{
			BaseURL:             mustParseURL(t, controlPlane.URL),
			TunnelID:            tunnelID,
			APIKey:              "test-api-key",
			MaxInFlightRequests: 1,
			PollTimeout:         100 * time.Millisecond,
		},
		Logging: config.LoggingConfig{
			Level: slog.LevelInfo,
		},
		Health: config.HealthConfig{
			UnixSocket: socketPath,
			URLFile:    healthURLPath,
		},
		MCP: config.MCPConfig{
			TransportKind:         config.MCPTransportStdio,
			Command:               "cat",
			CommandArgs:           []string{"cat"},
			ConnectionMaxTTL:      time.Minute,
			MaxConcurrentRequests: 10,
		},
	}

	var svc health.Service
	app := fxtest.New(t,
		app.Options(
			cfg,
			fx.StartTimeout(5*time.Second),
			fx.StopTimeout(5*time.Second),
			fx.Populate(&svc),
		)...,
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)

	require.NotNil(t, svc)
	addr, err := svc.Addr(2 * time.Second)
	require.NoError(t, err)
	require.Equal(t, socketPath, addr)

	healthURLContents, err := os.ReadFile(healthURLPath)
	require.NoError(t, err)
	baseURL := strings.TrimSpace(string(healthURLContents))
	require.Equal(t, healthurl.BuildUnixBaseURL(socketPath), baseURL)

	target, err := healthurl.Parse(baseURL)
	require.NoError(t, err)
	client, err := target.HTTPClient(2 * time.Second)
	require.NoError(t, err)

	require.NoError(t, waitForReady(client, target.RequestBaseURL, 6*time.Second))

	for _, path := range []string{"/healthz", "/api/status", "/api/logs"} {
		resp, err := client.Get(target.RequestURL(path))
		require.NoErrorf(t, err, "GET %s", path)
		require.NoError(t, resp.Body.Close())
		require.Equalf(t, http.StatusOK, resp.StatusCode, "%s response status", path)
	}
}

func waitForReady(client *http.Client, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		resp, err := client.Get(baseURL + "/readyz")
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			closeErr := resp.Body.Close()
			if readErr == nil && closeErr == nil &&
				resp.StatusCode == http.StatusOK &&
				strings.Contains(string(body), "ready") {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("readyz never became ready within %s", timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL %q: %v", raw, err)
	}
	return parsed
}

func readTarGzFiles(t *testing.T, data []byte) map[string]string {
	t.Helper()

	gz, err := gzip.NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, gz.Close())
	}()

	tr := tar.NewReader(gz)
	files := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		body, err := io.ReadAll(tr)
		require.NoError(t, err)
		files[hdr.Name] = string(body)
	}
	return files
}

func shortSocketPath(t *testing.T, pattern string) string {
	t.Helper()

	socketFile, err := os.CreateTemp("/tmp", pattern)
	require.NoError(t, err)
	require.NoError(t, socketFile.Close())
	require.NoError(t, os.Remove(socketFile.Name()))
	t.Cleanup(func() {
		_ = os.Remove(socketFile.Name())
	})
	return socketFile.Name()
}
