package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/require"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
	"go.uber.org/fx/fxtest"

	"github.com/openai/tunnel-client/pkg/app"
	"github.com/openai/tunnel-client/pkg/codexappserver"
	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/health"
	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/healthurl"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/metrics"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon/hostbus"
	"github.com/openai/tunnel-client/pkg/types"
)

func TestRunStartupDiagnostics(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		advertised bool
		optional   bool
	}{
		{name: "empty 404", status: http.StatusNotFound, optional: true},
		{name: "HTML 404", status: http.StatusNotFound, body: "<html>not found</html>", optional: true},
		{name: "JSON 404", status: http.StatusNotFound, body: `{"error":"not_found"}`, optional: true},
		// A terminal non-JSON response is independent of optional all-404 discovery.
		{name: "forbidden metadata", status: http.StatusForbidden, body: "403: Forbidden"},
		{name: "advertised 404", status: http.StatusNotFound, advertised: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			mcpServer := mcp.NewServer(&mcp.Implementation{Name: "startup fixture", Version: "1"}, nil)
			mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/.well-known/") {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
					return
				}
				if tc.advertised && r.Method == http.MethodPost && r.ContentLength == 0 {
					w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="http://`+r.Host+`/.well-known/oauth-protected-resource"`)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				mcpHandler.ServeHTTP(w, r)
			}))
			t.Cleanup(upstream.Close)
			polled := make(chan struct{})
			var pollOnce sync.Once
			controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/poll") {
					pollOnce.Do(func() { close(polled) })
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"name":"startup fixture"}`)
			}))
			t.Cleanup(controlPlane.Close)
			cfg := &config.Config{
				ControlPlane: config.ControlPlaneConfig{BaseURL: mustParseURL(t, controlPlane.URL), TunnelID: "tunnel_0123456789abcdef0123456789abcdef", APIKey: "test-key", MaxInFlightRequests: 1, PollTimeout: time.Minute},
				Health:       config.HealthConfig{ListenAddr: "127.0.0.1:0"},
				MCP:          config.MCPConfig{ServerURL: mustParseURL(t, upstream.URL+"/mcp"), TransportKind: config.MCPTransportHTTPStreamable, MaxConcurrentRequests: 1, ConnectionMaxTTL: time.Minute},
			}
			logs := &startupLogRecorder{summary: make(chan map[string]any, 1), pollingDisabled: make(chan struct{})}
			logger := slog.New(slog.NewJSONHandler(logs, nil))
			var svc health.Service
			var catalog *hostbus.StartupCatalogState
			var probe *mcpclient.ProbeState
			fxApp := app.New(cfg, isolatedAppMetrics(), isolatedAppCodex(t), fx.Replace(logger),
				fx.WithLogger(func() fxevent.Logger { return newTunnelEventLogger(logger, &cfg.ControlPlane, nil, &cfg.MCP) }),
				fx.Populate(&svc, &catalog, &probe),
			)
			require.NoError(t, fxApp.Start(ctx))
			t.Cleanup(func() {
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer stopCancel()
				require.NoError(t, fxApp.Stop(stopCtx))
			})
			var summary map[string]any
			select {
			case summary = <-logs.summary:
			case <-ctx.Done():
				t.Fatal("OAuth summary was not logged")
			}
			catalogErr := catalog.Wait(ctx)
			require.NoError(t, probe.WaitUntilDone(ctx))
			if tc.optional {
				require.NoError(t, catalogErr)
				select {
				case <-polled:
				case <-ctx.Done():
					t.Fatal("optional discovery did not start polling")
				}
			} else {
				require.Error(t, catalogErr)
				select {
				case <-logs.pollingDisabled:
				case <-ctx.Done():
					t.Fatal("failed startup catalog did not disable polling")
				}
				select {
				case <-polled:
					t.Fatal("failed startup catalog polled for commands")
				default:
				}
			}
			addr, err := svc.Addr(time.Second)
			require.NoError(t, err)
			get := func(path string) *http.Response {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
				require.NoError(t, err)
				resp, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				t.Cleanup(func() { _ = resp.Body.Close() })
				return resp
			}
			ready := get("/readyz")
			var snapshot struct {
				State   string                   `json:"state"`
				Details healthstate.OAuthDetails `json:"details"`
			}
			require.NoError(t, json.NewDecoder(get("/health/oauth").Body).Decode(&snapshot))
			require.Empty(t, snapshot.Details.ActiveProbes)
			require.NotEmpty(t, snapshot.Details.RecentProbes)
			lastProbe := snapshot.Details.RecentProbes[len(snapshot.Details.RecentProbes)-1]
			require.Equal(t, tc.status, lastProbe.StatusCode)
			if tc.optional {
				require.Equal(t, http.StatusOK, ready.StatusCode)
				require.Equal(t, "not_advertised", snapshot.State)
				require.Equal(t, "not_advertised", lastProbe.Disposition)
				require.Equal(t, "INFO", summary["level"])
				require.Equal(t, "OAuth metadata not advertised; continuing without OAuth", summary["msg"])
				require.NotContains(t, summary, "error")
			} else {
				require.Equal(t, http.StatusServiceUnavailable, ready.StatusCode)
				require.Equal(t, "failed", snapshot.State)
				require.Equal(t, "terminal_failure", lastProbe.Disposition)
				require.Equal(t, "WARN", summary["level"])
				require.Equal(t, "OAuth discovery failed", summary["msg"])
			}
			logs.mu.Lock()
			defer logs.mu.Unlock()
			require.Contains(t, logs.messages, "tunnel-client process started; readiness and control-plane polling are reported separately")
			require.NotContains(t, logs.messages, "🟢 tunnel-client started")
		})
	}
}

type startupLogRecorder struct {
	mu              sync.Mutex
	messages        []string
	summary         chan map[string]any
	pollingDisabled chan struct{}
}

func (w *startupLogRecorder) Write(data []byte) (int, error) {
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		return 0, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	message, _ := record["msg"].(string)
	if strings.Contains(message, "tunnel-client") {
		w.messages = append(w.messages, message)
	}
	switch message {
	case "OAuth discovery failed", "OAuth metadata not advertised; continuing without OAuth":
		w.summary <- record
	case "startup catalog failed; control-plane polling disabled":
		close(w.pollingDisabled)
	}
	return len(data), nil
}

func TestRootCommandContextCancelsOnSIGTERM(t *testing.T) {
	// Signals are process-wide, so this test runs before parallel tests start.
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM is not available on Windows")
	}

	ctx, stop := newRootCommandContext()
	defer stop()

	process, err := os.FindProcess(os.Getpid())
	require.NoError(t, err)
	require.NoError(t, process.Signal(syscall.SIGTERM))

	select {
	case <-ctx.Done():
		require.ErrorIs(t, ctx.Err(), context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("root command context did not cancel after SIGTERM")
	}
}

func TestAppBoots(t *testing.T) {
	t.Parallel()
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
		isolatedAppMetrics(),
		isolatedAppCodex(t),
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
	require.Equal(t, http.StatusOK, resp.StatusCode, "metrics response status: %s", metricsBody)
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

func isolatedAppMetrics() fx.Option {
	return fx.Decorate(func(lc fx.Lifecycle) (*sdkmetric.MeterProvider, metrics.MetricsExporter, error) {
		registry := prometheus.NewRegistry()
		registry.MustRegister(
			prometheus.NewGoCollector(),
			prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
		)
		exporter, err := otelprometheus.New(otelprometheus.WithRegisterer(registry))
		if err != nil {
			return nil, nil, err
		}
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
		lc.Append(fx.Hook{OnStop: provider.Shutdown})
		handler := promhttp.InstrumentMetricHandler(
			registry, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}),
		)
		return provider, handler, nil
	})
}

func isolatedAppCodex(t *testing.T) fx.Option {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	env := map[string]string{
		"TUNNEL_CLIENT_CODEX_APP_SERVER_CMD":  executable,
		"TUNNEL_CLIENT_CODEX_APP_SERVER_ARGS": "-test.run=^TestAppCodexHelperProcess$ -- tunnel-client-app-codex-helper",
		"TUNNEL_CLIENT_CODEX_APP_SERVER_CWD":  t.TempDir(),
	}
	return fx.Decorate(func(lc fx.Lifecycle, logger *slog.Logger) *codexappserver.Bridge {
		bridge := codexappserver.NewBridgeWithLookupEnv(nil, logger, func(key string) (string, bool) {
			value, ok := env[key]
			return value, ok
		})
		lc.Append(fx.Hook{OnStart: bridge.EnsureStarted, OnStop: bridge.Stop})
		return bridge
	})
}

// TestAppCodexHelperProcess handles the bridge startup protocol without reading
// user configuration or starting an installed Codex executable.
func TestAppCodexHelperProcess(t *testing.T) {
	t.Parallel()
	if os.Args[len(os.Args)-1] != "tunnel-client-app-codex-helper" {
		return
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		encoder := json.NewEncoder(os.Stdout)
		for scanner.Scan() {
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				done <- err
				return
			}
			if len(request.ID) == 0 {
				continue
			}
			var result any
			switch request.Method {
			case "initialize":
				result = map[string]any{"userAgent": "app-test-codex-fixture"}
			case "getAuthStatus":
				result = map[string]any{"requiresOpenaiAuth": false}
			case "account/read":
				result = map[string]any{"account": nil, "requiresOpenaiAuth": false}
			default:
				done <- fmt.Errorf("unexpected Codex fixture method %q", request.Method)
				return
			}
			if err := encoder.Encode(map[string]any{"id": request.ID, "result": result}); err != nil {
				done <- err
				return
			}
		}
		done <- scanner.Err()
	}()
	select {
	case <-signals:
	case err := <-done:
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	os.Exit(0)
}

func requireAppLivenessMetric(t *testing.T, client *http.Client, baseURL string) {
	t.Helper()
	resp, err := client.Get(baseURL + "/metrics")
	require.NoError(t, err)
	body, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "liveness")
}

func TestAppBindsHealthBeforeCloudflaredReadiness(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a Unix executable wrapper")
	}
	t.Parallel()

	tempDir := t.TempDir()
	healthURLPath := filepath.Join(tempDir, "health_url")
	readyGatePath := filepath.Join(tempDir, "cloudflared-ready")
	cloudflaredPath := filepath.Join(tempDir, "cloudflared")
	wrapper := "#!/bin/sh\n" +
		"export GO_WANT_DELAYED_CLOUDFLARED_HELPER='1'\n" +
		"export DELAYED_CLOUDFLARED_READY_GATE=" + shellSingleQuote(readyGatePath) + "\n" +
		"exec " + shellSingleQuote(os.Args[0]) + " -test.run=TestDelayedCloudflaredHelper -- \"$@\"\n"
	require.NoError(t, os.WriteFile(cloudflaredPath, []byte(wrapper), 0o700))

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
		Cloudflared: config.CloudflaredConfig{
			Token:        "test-cloudflared-token",
			Path:         cloudflaredPath,
			ReadyTimeout: 20 * time.Second,
		},
		MCP: config.MCPConfig{
			TransportKind:         config.MCPTransportStdio,
			Command:               "cat",
			CommandArgs:           []string{"cat"},
			ConnectionMaxTTL:      time.Minute,
			MaxConcurrentRequests: 10,
		},
	}

	fxApp := fx.New(app.Options(
		cfg,
		fx.StopTimeout(5*time.Second),
		fx.NopLogger,
		isolatedAppMetrics(),
		isolatedAppCodex(t),
	)...)
	require.Equal(t, 35*time.Second, fxApp.StartTimeout())

	startCtx, startCancel := context.WithTimeout(context.Background(), fxApp.StartTimeout())
	startErr := make(chan error, 1)
	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		startErr <- fxApp.Start(startCtx)
	}()

	stopped := false
	t.Cleanup(func() {
		if stopped {
			return
		}
		_ = os.WriteFile(readyGatePath, []byte("ready"), 0o600)
		startCancel()
		select {
		case <-startDone:
		case <-time.After(5 * time.Second):
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = fxApp.Stop(stopCtx)
	})

	var baseURL string
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(healthURLPath)
		if err != nil {
			return false
		}
		baseURL = strings.TrimSpace(string(data))
		return baseURL != ""
	}, 5*time.Second, 25*time.Millisecond)

	select {
	case err := <-startErr:
		t.Fatalf("app start returned before cloudflared readiness gate: %v", err)
	default:
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/healthz")
	require.NoError(t, err)
	healthBody, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(healthBody), "live")

	resp, err = client.Get(baseURL + "/readyz")
	require.NoError(t, err)
	readyBody, readErr := io.ReadAll(resp.Body)
	closeErr = resp.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	// Fx readiness gates are unordered. Catalog registration can also remain
	// pending while the cloudflared hook blocks the remaining startup hooks.
	require.Contains(t, []string{"cloudflared startup pending", "startup catalog pending"}, strings.TrimSpace(string(readyBody)))
	requireAppLivenessMetric(t, client, baseURL)

	require.NoError(t, os.WriteFile(readyGatePath, []byte("ready"), 0o600))
	select {
	case err := <-startErr:
		require.NoError(t, err)
	case <-startCtx.Done():
		t.Fatalf("app did not finish starting after cloudflared became ready: %v", startCtx.Err())
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	require.NoError(t, fxApp.Stop(stopCtx))
	startCancel()
	stopped = true
}

func TestAppManagedCloudflaredFetchesRuntimeAndBecomesReady(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a Unix executable wrapper")
	}
	t.Parallel()

	tempDir := t.TempDir()
	healthURLPath := filepath.Join(tempDir, "health_url")
	readyGatePath := filepath.Join(tempDir, "cloudflared-ready")
	cloudflaredPath := filepath.Join(tempDir, "cloudflared")
	const runtimeToken = "managed-runtime-secret-token"
	runtimeTokenDigest := sha256.Sum256([]byte(runtimeToken))
	wrapper := "#!/bin/sh\n" +
		"export GO_WANT_DELAYED_CLOUDFLARED_HELPER='1'\n" +
		"export DELAYED_CLOUDFLARED_READY_GATE=" + shellSingleQuote(readyGatePath) + "\n" +
		"export DELAYED_CLOUDFLARED_EXPECT_TOKEN_SHA256=" + shellSingleQuote(fmt.Sprintf("%x", runtimeTokenDigest)) + "\n" +
		"exec " + shellSingleQuote(os.Args[0]) + " -test.run=TestDelayedCloudflaredHelper -- \"$@\"\n"
	require.NoError(t, os.WriteFile(cloudflaredPath, []byte(wrapper), 0o700))

	tunnelID := types.TunnelID("tunnel_0123456789abcdef0123456789abcdef")
	runtimeRequest := make(chan struct{}, 1)
	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tunnels/" + url.PathEscape(tunnelID.String()):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"` + tunnelID.String() + `","name":"test tunnel","description":"test fixture"}`))
		case "/v1/tunnels/" + url.PathEscape(tunnelID.String()) + "/cloudflare/runtime":
			if r.Header.Get("Authorization") != "Bearer test-api-key" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			select {
			case runtimeRequest <- struct{}{}:
			default:
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"cloudflare_tunnel": map[string]any{
					"tunnel_id":  "provider-tunnel-id",
					"name":       "provider-tunnel-name",
					"account_id": "provider-account-id",
				},
				"runtime_token": runtimeToken,
			})
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
			BaseURL:               mustParseURL(t, controlPlane.URL),
			TunnelID:              tunnelID,
			APIKey:                "test-api-key",
			MaxInFlightRequests:   1,
			PollTimeout:           100 * time.Millisecond,
			PollDeadlineGuardrail: 100 * time.Millisecond,
		},
		Logging: config.LoggingConfig{
			Level: slog.LevelInfo,
		},
		Health: config.HealthConfig{
			ListenAddr: "127.0.0.1:0",
			URLFile:    healthURLPath,
		},
		Cloudflared: config.CloudflaredConfig{
			Managed:      true,
			Path:         cloudflaredPath,
			ReadyTimeout: 10 * time.Second,
		},
		MCP: config.MCPConfig{
			TransportKind:         config.MCPTransportStdio,
			Command:               "cat",
			CommandArgs:           []string{"cat"},
			ConnectionMaxTTL:      time.Minute,
			MaxConcurrentRequests: 10,
		},
	}

	fxApp := fx.New(app.Options(
		cfg,
		fx.StopTimeout(5*time.Second),
		fx.NopLogger,
		isolatedAppMetrics(),
		isolatedAppCodex(t),
	)...)
	require.Equal(t, 25*time.Second+200*time.Millisecond, fxApp.StartTimeout())

	startCtx, startCancel := context.WithTimeout(context.Background(), fxApp.StartTimeout())
	startErr := make(chan error, 1)
	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		startErr <- fxApp.Start(startCtx)
	}()

	stopped := false
	t.Cleanup(func() {
		if stopped {
			return
		}
		_ = os.WriteFile(readyGatePath, []byte("ready"), 0o600)
		startCancel()
		select {
		case <-startDone:
		case <-time.After(5 * time.Second):
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = fxApp.Stop(stopCtx)
	})

	select {
	case <-runtimeRequest:
	case <-time.After(5 * time.Second):
		t.Fatal("managed cloudflared runtime fetch did not reach control plane")
	}
	require.Empty(t, cfg.Cloudflared.Token, "managed runtime token must not persist in config")

	select {
	case err := <-startErr:
		t.Fatalf("app start returned before cloudflared readiness gate: %v", err)
	default:
	}

	require.NoError(t, os.WriteFile(readyGatePath, []byte("ready"), 0o600))
	select {
	case err := <-startErr:
		require.NoError(t, err)
	case <-startCtx.Done():
		t.Fatalf("app did not finish starting after managed cloudflared became ready: %v", startCtx.Err())
	}

	data, err := os.ReadFile(healthURLPath)
	require.NoError(t, err)
	baseURL := strings.TrimSpace(string(data))
	require.NoError(t, waitForReady(&http.Client{Timeout: 2 * time.Second}, baseURL, 5*time.Second))
	requireAppLivenessMetric(t, &http.Client{Timeout: 2 * time.Second}, baseURL)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	require.NoError(t, fxApp.Stop(stopCtx))
	startCancel()
	stopped = true
}

func TestDelayedCloudflaredHelper(t *testing.T) {
	t.Parallel()
	if os.Getenv("GO_WANT_DELAYED_CLOUDFLARED_HELPER") != "1" {
		return
	}

	metricsAddr := cloudflaredHelperArgValue(os.Args, "--metrics")
	if metricsAddr == "" {
		_, _ = fmt.Fprintln(os.Stderr, "missing --metrics")
		os.Exit(2)
	}
	if expectedTokenDigest := os.Getenv("DELAYED_CLOUDFLARED_EXPECT_TOKEN_SHA256"); expectedTokenDigest != "" {
		actualTokenDigest := sha256.Sum256([]byte(os.Getenv("TUNNEL_TOKEN")))
		if fmt.Sprintf("%x", actualTokenDigest) != expectedTokenDigest {
			_, _ = fmt.Fprintln(os.Stderr, "unexpected tunnel token")
			os.Exit(2)
		}
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)

	readyGatePath := os.Getenv("DELAYED_CLOUDFLARED_READY_GATE")
	for {
		if _, err := os.Stat(readyGatePath); err == nil {
			break
		}
		select {
		case <-signals:
			os.Exit(0)
		case <-time.After(10 * time.Millisecond):
		}
	}

	listener, err := net.Listen("tcp", metricsAddr)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(2)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()

	<-signals
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	os.Exit(0)
}

func cloudflaredHelperArgValue(args []string, key string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func TestAppFailsToStartWithBusyHealthPort(t *testing.T) {
	t.Parallel()
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
			isolatedAppMetrics(),
			isolatedAppCodex(t),
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
	t.Parallel()
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
			isolatedAppMetrics(),
			isolatedAppCodex(t),
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
		_, err = io.Copy(io.Discard, resp.Body)
		require.NoErrorf(t, err, "read %s response body", path)
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
