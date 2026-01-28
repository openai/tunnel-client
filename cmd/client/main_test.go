package main

import (
	"io"
	"log/slog"
	"net/http"
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
	"go.openai.org/api/tunnel-client/pkg/types"
)

func TestAppBoots(t *testing.T) {
	tempDir := t.TempDir()
	healthURLPath := filepath.Join(tempDir, "health_url")
	pidPath := filepath.Join(tempDir, "pid")

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
			ListenAddr: "127.0.0.1:0",
			URLFile:    healthURLPath,
		},
		Process: config.ProcessConfig{
			PIDFile: pidPath,
		},
		MCP: config.MCPConfig{
			ServerURL:             mustParseURL(t, "http://127.0.0.1"),
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
		"/readyz":  "ready",
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

	resp, err := client.Get(baseURL + "/metrics")
	require.NoError(t, err)
	metricsBody, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	require.Equal(t, http.StatusOK, resp.StatusCode, "metrics response status")
	require.Contains(t, string(metricsBody), "liveness", "metrics should include liveness gauge")

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

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL %q: %v", raw, err)
	}
	return parsed
}
