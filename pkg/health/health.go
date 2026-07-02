package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/healthurl"
	"github.com/openai/tunnel-client/pkg/httpguard"
	tclog "github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/metrics"
	"github.com/openai/tunnel-client/pkg/oauth"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"
)

// HealthMuxModule provides the health check endpoints and metrics exporter.
var HealthMuxModule = fx.Module(
	"health",
	fx.Provide(
		fx.Annotate(
			func() *http.ServeMux { return http.NewServeMux() },
			fx.ResultTags(`name:"admin_mux"`),
		),
		fx.Annotate(
			newHealthService,
			fx.As(new(Service)),
		),
	),
)

// Service exposes the tunnel client's health server functionality.
type Service interface {
	// Addr returns the bound listener address. It waits up to the provided
	// timeout for the listener to bind; timeout <= 0 performs a non-blocking
	// check. Returns an error if the address is still unavailable.
	Addr(timeout time.Duration) (string, error)
}

type healthService struct {
	server     *http.Server
	urlFile    string
	socketPath string
	boundCh    chan struct{}
	boundMu    sync.Once
}

func (s *healthService) Addr(timeout time.Duration) (string, error) {
	if s == nil {
		return "", errors.New("health: service is nil")
	}
	if s.boundCh == nil {
		if s.server.Addr == "" {
			return "", errors.New("health: address unavailable")
		}
		return s.server.Addr, nil
	}
	if timeout <= 0 {
		select {
		case <-s.boundCh:
			if s.server.Addr == "" {
				return "", errors.New("health: address unavailable")
			}
			return s.server.Addr, nil
		default:
			return "", errors.New("health: address unavailable")
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.boundCh:
		if s.server.Addr == "" {
			return "", errors.New("health: address unavailable")
		}
		return s.server.Addr, nil
	case <-timer.C:
		return "", fmt.Errorf("health: address unavailable after %s", timeout)
	}
}

func (s *healthService) markBound() {
	if s == nil || s.boundCh == nil {
		return
	}
	s.boundMu.Do(func() {
		close(s.boundCh)
	})
}

type healthParams struct {
	fx.In
	Lifecycle      fx.Lifecycle
	MetricExporter metrics.MetricsExporter
	HealthConfig   *config.HealthConfig
	Logger         *slog.Logger
	MeterProvider  *sdkmetric.MeterProvider
	AdminMux       *http.ServeMux `name:"admin_mux"`
	OAuthState     *oauth.DiscoveryState
	MCPProbeState  *mcpclient.ProbeState `optional:"true"`
}

func newHealthService(p healthParams) (*healthService, error) {
	logger := p.Logger.With(tclog.FieldComponent, tclog.ComponentHealth)

	if p.AdminMux == nil {
		return nil, fmt.Errorf("health: admin mux is required")
	}

	p.AdminMux.HandleFunc("/healthz", okHandler("live"))
	p.AdminMux.HandleFunc("/readyz", readinessHandler(p.OAuthState, p.MCPProbeState))
	p.AdminMux.Handle("/metrics", p.MetricExporter)

	meter := p.MeterProvider.Meter("health")
	livenessGauge, err := meter.Int64ObservableGauge(
		"liveness",
		metric.WithDescription("Reports 1 when the tunnel client process is live"),
	)
	if err != nil {
		return nil, fmt.Errorf("create liveness gauge: %w", err)
	}
	readinessGauge, err := meter.Int64ObservableGauge(
		"readiness",
		metric.WithDescription("Reports 1 when the tunnel client reports readiness"),
	)
	if err != nil {
		return nil, fmt.Errorf("create readiness gauge: %w", err)
	}

	srv := &http.Server{
		Handler:           p.AdminMux,
		ReadHeaderTimeout: 5 * time.Second,
		Addr:              p.HealthConfig.ListenAddr,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			return httpguard.WithConnectionNetwork(ctx, conn.LocalAddr().Network())
		},
	}
	service := &healthService{server: srv, boundCh: make(chan struct{})}

	p.Lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			_, err := meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
				observer.ObserveInt64(livenessGauge, 1)
				if isReady(p.OAuthState, p.MCPProbeState) {
					observer.ObserveInt64(readinessGauge, 1)
				} else {
					observer.ObserveInt64(readinessGauge, 0)
				}
				return nil
			}, livenessGauge, readinessGauge)

			if err != nil {
				return err
			}

			ln, err := listenHealth(p.HealthConfig)
			if err != nil {
				return err
			}
			srv.Addr = ln.Addr().String()
			if p.HealthConfig.UnixSocket != "" {
				service.socketPath = p.HealthConfig.UnixSocket
			}
			service.markBound()
			if p.HealthConfig.URLFile != "" {
				healthURL, err := buildHealthURL(p.HealthConfig, ln.Addr())
				if err != nil {
					_ = ln.Close()
					_ = service.removeSocket()
					return fmt.Errorf("determine health URL: %w", err)
				}
				if err := os.MkdirAll(filepath.Dir(p.HealthConfig.URLFile), 0o755); err != nil {
					_ = ln.Close()
					_ = service.removeSocket()
					return fmt.Errorf("create health URL file dir %s: %w", filepath.Dir(p.HealthConfig.URLFile), err)
				}
				if err := writePrivateURLFile(p.HealthConfig.URLFile, []byte(healthURL)); err != nil {
					_ = ln.Close()
					_ = service.removeSocket()
					return fmt.Errorf("write health URL file %s: %w", p.HealthConfig.URLFile, err)
				}
				service.urlFile = p.HealthConfig.URLFile
				logger.InfoContext(ctx,
					"🩺 HEALTH URL: "+healthURL+" (written to "+p.HealthConfig.URLFile+")",
					slog.String("url", healthURL),
					slog.String("path", p.HealthConfig.URLFile),
				)
			}
			logger.InfoContext(ctx, "health server listening", slog.String("addr", srv.Addr))
			go func() {
				err := srv.Serve(ln)
				if err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.ErrorContext(ctx, "health server error", slog.String("error", err.Error()))
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			return errors.Join(srv.Shutdown(ctx), service.removeURLFile(), service.removeSocket())
		},
	})
	return service, nil
}

func writePrivateURLFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (s *healthService) removeURLFile() error {
	if s.urlFile == "" {
		return nil
	}

	if err := os.Remove(s.urlFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove health URL file %s: %w", s.urlFile, err)
	}

	return nil
}

func (s *healthService) removeSocket() error {
	if s.socketPath == "" {
		return nil
	}

	if err := os.Remove(s.socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove health unix socket %s: %w", s.socketPath, err)
	}

	return nil
}

func listenHealth(cfg *config.HealthConfig) (net.Listener, error) {
	if cfg != nil && cfg.UnixSocket != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.UnixSocket), 0o755); err != nil {
			return nil, fmt.Errorf("create health unix socket dir %s: %w", filepath.Dir(cfg.UnixSocket), err)
		}
		if err := removeStaleUnixSocket(cfg.UnixSocket); err != nil {
			return nil, err
		}
		return net.Listen("unix", cfg.UnixSocket)
	}
	if cfg == nil {
		return nil, fmt.Errorf("health: config is required")
	}
	return net.Listen("tcp", cfg.ListenAddr)
}

func removeStaleUnixSocket(socketPath string) error {
	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat health unix socket %s: %w", socketPath, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("health unix socket path %s exists and is not a unix socket", socketPath)
	}

	conn, err := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("health unix socket path %s is already accepting connections", socketPath)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("probe health unix socket %s: %w", socketPath, err)
	}

	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale health unix socket %s: %w", socketPath, err)
	}
	return nil
}

func buildHealthURL(cfg *config.HealthConfig, addr net.Addr) (string, error) {
	if cfg != nil && cfg.UnixSocket != "" {
		return healthurl.BuildUnixBaseURL(cfg.UnixSocket), nil
	}

	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		return "", fmt.Errorf("health listener address is %T, expected *net.TCPAddr", addr)
	}

	listenAddr := ""
	if cfg != nil {
		listenAddr = cfg.ListenAddr
	}
	host := preferredHealthHost(listenAddr, tcpAddr)

	return "http://" + net.JoinHostPort(host, strconv.Itoa(tcpAddr.Port)), nil
}

func preferredHealthHost(listenAddr string, addr *net.TCPAddr) string {
	host, _, err := net.SplitHostPort(listenAddr)
	if err == nil && host != "" && !isUnspecifiedHost(host) {
		return host
	}

	if addr != nil && addr.IP != nil && !addr.IP.IsUnspecified() {
		return addr.IP.String()
	}

	return "localhost"
}

func isUnspecifiedHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

func isReady(oauthState *oauth.DiscoveryState, probeState *mcpclient.ProbeState) bool {
	status, _ := readinessStatus(oauthState, probeState)
	return status == http.StatusOK
}

func okHandler(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(status))
	}
}

func readinessHandler(oauthState *oauth.DiscoveryState, probeState *mcpclient.ProbeState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		statusCode, body := readinessStatus(oauthState, probeState)
		w.WriteHeader(statusCode)
		_, _ = w.Write([]byte(body))
	}
}

func readinessStatus(oauthState *oauth.DiscoveryState, probeState *mcpclient.ProbeState) (int, string) {
	if oauthState != nil && !oauthState.IsDone() {
		return http.StatusServiceUnavailable, "oauth discovery pending"
	}
	if oauthState != nil {
		if result, probe, _, err, ok := oauthState.Wait(10 * time.Millisecond); ok && err != nil {
			if !oauth.IsOptionalDiscoveryFailure(result, probe, err) {
				return http.StatusServiceUnavailable, "oauth discovery failed: " + sanitizeReadinessError(err)
			}
		}
	}
	if probeState != nil && !probeState.IsDone() {
		return http.StatusServiceUnavailable, "mcp startup probe pending"
	}
	if probeState != nil && probeState.IsDone() {
		if _, err, ok := probeState.Wait(10 * time.Millisecond); ok && err != nil {
			if mcpclient.IsAuthRequiredProbeError(err) {
				return http.StatusOK, "ready (mcp initialize requires auth: " + sanitizeReadinessError(err) + ")"
			}
			if mcpclient.IsTimeoutProbeError(err) {
				return http.StatusOK, "ready (mcp startup probe timed out: " + sanitizeReadinessError(err) + ")"
			}
			return http.StatusServiceUnavailable, "mcp probe failed: " + sanitizeReadinessError(err)
		}
	}
	return http.StatusOK, "ready"
}

func sanitizeReadinessError(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}
