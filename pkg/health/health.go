package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"go.openai.org/api/tunnel-client/pkg/config"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
	"go.openai.org/api/tunnel-client/pkg/metrics"
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
	Addr() string
}

type healthService struct {
	server  *http.Server
	urlFile string
}

func (s *healthService) Addr() string {
	return s.server.Addr
}

type healthParams struct {
	fx.In
	Lifecycle      fx.Lifecycle
	MetricExporter metrics.MetricsExporter
	HealthConfig   *config.HealthConfig
	Logger         *slog.Logger
	MeterProvider  *sdkmetric.MeterProvider
	AdminMux       *http.ServeMux `name:"admin_mux"`
}

func newHealthService(p healthParams) (*healthService, error) {
	logger := p.Logger.With(tclog.FieldComponent, tclog.ComponentHealth)

	if p.AdminMux == nil {
		return nil, fmt.Errorf("health: admin mux is required")
	}

	p.AdminMux.HandleFunc("/healthz", okHandler("live"))
	p.AdminMux.HandleFunc("/readyz", okHandler("ready"))
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
	}
	service := &healthService{server: srv}

	p.Lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			_, err := meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
				observer.ObserveInt64(livenessGauge, 1)
				observer.ObserveInt64(readinessGauge, 1)
				return nil
			}, livenessGauge, readinessGauge)

			if err != nil {
				return err
			}

			ln, err := net.Listen("tcp", srv.Addr)
			if err != nil {
				return err
			}
			srv.Addr = ln.Addr().String()
			if p.HealthConfig.URLFile != "" {
				healthURL, err := buildHealthURL(p.HealthConfig.ListenAddr, ln.Addr())
				if err != nil {
					_ = ln.Close()
					return fmt.Errorf("determine health URL: %w", err)
				}
				if err := os.WriteFile(p.HealthConfig.URLFile, []byte(healthURL), 0o644); err != nil {
					_ = ln.Close()
					return fmt.Errorf("write health URL file %s: %w", p.HealthConfig.URLFile, err)
				}
				service.urlFile = p.HealthConfig.URLFile
				logger.InfoContext(ctx, "health URL written", slog.String("url", healthURL), slog.String("path", p.HealthConfig.URLFile))
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
			return errors.Join(srv.Shutdown(ctx), service.removeURLFile())
		},
	})
	return service, nil
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

func buildHealthURL(listenAddr string, addr net.Addr) (string, error) {
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		return "", fmt.Errorf("health listener address is %T, expected *net.TCPAddr", addr)
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

func okHandler(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(status))
	}
}
