package proxyhealth

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/harpoon"
	"github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/proxy"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
)

const (
	defaultDialTimeout    = 5 * time.Second
	defaultConnectTimeout = 5 * time.Second
	maxHistoryEntries     = 10
)

// HealthState tracks proxy health state.
type HealthState string

const (
	HealthStateDirect    HealthState = "direct"
	HealthStateHealthy   HealthState = "healthy"
	HealthStateUnhealthy HealthState = "unhealthy"
)

// CheckRecord captures a single proxy check result.
type CheckRecord struct {
	Timestamp          time.Time `json:"timestamp"`
	Success            bool      `json:"success"`
	TCPDurationMS      int64     `json:"tcp_duration_ms,omitempty"`
	ConnectDurationMS  int64     `json:"connect_duration_ms,omitempty"`
	ErrorPhase         string    `json:"error_phase,omitempty"`
	ErrorReason        string    `json:"error_reason,omitempty"`
	HTTPStatusCategory string    `json:"http_status_category,omitempty"`
}

// RouteHealthSummary reports health and recent checks for a route.
type RouteHealthSummary struct {
	Route       proxy.RouteSummary `json:"route"`
	HealthState string             `json:"health_state"`
	LastCheck   *time.Time         `json:"last_check,omitempty"`
	LastSuccess *time.Time         `json:"last_success,omitempty"`
	History     []CheckRecord      `json:"history,omitempty"`
}

// Snapshotter exposes proxy health snapshots.
type Snapshotter interface {
	RouteSummaries() []proxy.RouteSummary
	HealthSummaries() []RouteHealthSummary
	IdentityMap() []proxy.IdentityRecord
}

// Checker runs proxy health checks.
type Checker struct {
	logger        *slog.Logger
	interval      time.Duration
	routes        []proxy.Route
	identityMap   []proxy.IdentityRecord
	metrics       *proxyMetrics
	statusMu      sync.RWMutex
	routeStatus   map[string]*routeStatus
	started       bool
	startStopMu   sync.Mutex
	meterProvider *sdkmetric.MeterProvider
	tlsBundle     *tlsconfig.Bundle
	cancel        context.CancelFunc
}

type routeStatus struct {
	route       proxy.Route
	healthState HealthState
	lastCheck   time.Time
	lastSuccess time.Time
	history     []CheckRecord
}

type checkerParams struct {
	fx.In

	Lifecycle     fx.Lifecycle
	Logger        *slog.Logger
	Config        *config.ProxyHealthConfig
	ControlPlane  *config.ControlPlaneConfig
	MCPConfig     *config.MCPConfig
	HarpoonConfig *config.HarpoonConfig
	HarpoonReg    *harpoon.Registry `optional:"true"`
	MeterProvider *sdkmetric.MeterProvider
	TLSBundle     *tlsconfig.Bundle
}

// Module wires the proxy health checker.
var Module = fx.Module(
	"proxyhealth",
	fx.Provide(newChecker),
	fx.Provide(func(checker *Checker) Snapshotter { return checker }),
	fx.Invoke(startChecker),
)

func newChecker(p checkerParams) (*Checker, error) {
	logger := p.Logger
	if logger == nil {
		return nil, errors.New("proxyhealth checker requires a non-nil logger")
	}
	logger = logger.With(log.FieldComponent, "proxyhealth")
	interval := defaultProxyCheckInterval(p.Config)
	checker := &Checker{
		logger:        logger,
		interval:      interval,
		routes:        buildRoutes(p.ControlPlane, p.MCPConfig, p.HarpoonConfig, p.HarpoonReg, os.LookupEnv),
		meterProvider: p.MeterProvider,
		tlsBundle:     p.TLSBundle,
	}
	checker.identityMap = proxy.BuildIdentityMap(checker.routes)
	checker.routeStatus = make(map[string]*routeStatus)
	for _, route := range checker.routes {
		checker.routeStatus[routeKey(route)] = &routeStatus{
			route:       route,
			healthState: initialHealthState(route),
		}
	}
	metrics, err := newProxyMetrics(p.MeterProvider, checker)
	if err != nil {
		return nil, err
	}
	checker.metrics = metrics
	checker.logIdentityMap()
	return checker, nil
}

func startChecker(lifecycle fx.Lifecycle, checker *Checker) {
	if lifecycle == nil || checker == nil {
		return
	}
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			checker.Start()
			return nil
		},
		OnStop: func(context.Context) error {
			checker.Stop()
			return nil
		},
	})
}

func (c *Checker) Start() {
	c.startStopMu.Lock()
	defer c.startStopMu.Unlock()
	if c.started {
		return
	}
	c.started = true
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	go c.run(ctx)
}

func (c *Checker) Stop() {
	c.startStopMu.Lock()
	defer c.startStopMu.Unlock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.started = false
}

func (c *Checker) run(ctx context.Context) {
	c.runOnce(ctx)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.runOnce(ctx)
		}
	}
}

func (c *Checker) runOnce(ctx context.Context) {
	for _, route := range c.routes {
		if route.RouteMode != proxy.RouteModeProxy {
			c.recordDirect(route)
			continue
		}
		record, success := c.checkProxyRoute(ctx, route)
		c.recordResult(route, record, success)
	}
}

func (c *Checker) recordDirect(route proxy.Route) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	status := c.routeStatus[routeKey(route)]
	if status == nil {
		return
	}
	status.healthState = HealthStateDirect
}

func (c *Checker) checkProxyRoute(ctx context.Context, route proxy.Route) (CheckRecord, bool) {
	record := CheckRecord{Timestamp: time.Now()}
	if route.ProxyHostPort == "" {
		record.ErrorPhase = "tcp"
		record.ErrorReason = "missing_proxy_host"
		return record, false
	}
	if route.TargetHostPort == "" {
		record.ErrorPhase = "connect"
		record.ErrorReason = "missing_target_host"
		return record, false
	}

	conn, tcpDuration, err := dialTCP(ctx, route.ProxyHostPort, defaultDialTimeout)
	record.TCPDurationMS = tcpDuration.Milliseconds()
	if err != nil {
		record.ErrorPhase = "tcp"
		record.ErrorReason = classifyDialError(err)
		c.recordFailure(route, "tcp", record.ErrorReason)
		return record, false
	}
	defer func() {
		_ = conn.Close()
	}()

	connectDuration, statusCategory, err := connectThroughProxyWithTLSConfig(
		conn,
		route.ProxyURL,
		route.TargetHostPort,
		defaultConnectTimeout,
		proxyTLSConfig(c.tlsBundle),
	)
	record.ConnectDurationMS = connectDuration.Milliseconds()
	record.HTTPStatusCategory = statusCategory
	if err != nil {
		record.ErrorPhase = "connect"
		record.ErrorReason = classifyConnectError(err)
		if statusCategory != "" {
			record.ErrorReason = "bad_status"
		}
		c.recordFailure(route, "connect", record.ErrorReason)
		return record, false
	}

	record.Success = true
	return record, true
}

func dialTCP(ctx context.Context, hostPort string, timeout time.Duration) (net.Conn, time.Duration, error) {
	dialer := &net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", hostPort)
	return conn, time.Since(start), err
}

func proxyTLSConfig(bundle *tlsconfig.Bundle) *tls.Config {
	if bundle == nil || bundle.RootCAs == nil {
		return nil
	}
	return &tls.Config{RootCAs: bundle.RootCAs}
}

func connectThroughProxyWithTLSConfig(conn net.Conn, proxyURL *url.URL, targetHostPort string, timeout time.Duration, tlsConfig *tls.Config) (time.Duration, string, error) {
	if conn == nil {
		return 0, "", errors.New("missing connection")
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	start := time.Now()
	proxyConn := conn

	if proxyURL != nil && strings.EqualFold(proxyURL.Scheme, "https") {
		config := &tls.Config{}
		if tlsConfig != nil {
			config = tlsConfig.Clone()
		}
		if config.ServerName == "" {
			config.ServerName = proxyURL.Hostname()
		}
		tlsConn := tls.Client(conn, config)
		if err := tlsConn.Handshake(); err != nil {
			return time.Since(start), "", fmt.Errorf("tls handshake with proxy: %w", err)
		}
		proxyConn = tlsConn
	}

	proxyAuth := ""
	if proxyURL != nil && proxyURL.User != nil {
		user := proxyURL.User.Username()
		pass, _ := proxyURL.User.Password()
		payload := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
		proxyAuth = "Proxy-Authorization: Basic " + payload + "\r\n"
	}

	request := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n", targetHostPort, targetHostPort, proxyAuth)
	if _, err := proxyConn.Write([]byte(request)); err != nil {
		return time.Since(start), "", err
	}

	reader := bufio.NewReader(proxyConn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return time.Since(start), "", err
	}
	parts := strings.SplitN(strings.TrimSpace(statusLine), " ", 3)
	if len(parts) < 2 {
		return time.Since(start), "", fmt.Errorf("invalid proxy response: %s", statusLine)
	}
	statusCode := parts[1]
	category := ""
	if len(statusCode) >= 1 {
		category = string(statusCode[0]) + "xx"
	}
	if !strings.HasPrefix(statusCode, "2") {
		return time.Since(start), category, fmt.Errorf("proxy connect failed: %s", statusLine)
	}
	return time.Since(start), category, nil
}

func classifyDialError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	return "dial_error"
}

func classifyConnectError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	return "connect_error"
}

func (c *Checker) recordResult(route proxy.Route, record CheckRecord, success bool) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	status := c.routeStatus[routeKey(route)]
	if status == nil {
		return
	}
	status.lastCheck = record.Timestamp
	if success {
		status.lastSuccess = record.Timestamp
		status.healthState = HealthStateHealthy
	} else {
		status.healthState = HealthStateUnhealthy
	}
	status.history = append(status.history, record)
	if len(status.history) > maxHistoryEntries {
		status.history = status.history[len(status.history)-maxHistoryEntries:]
	}
	if c.metrics != nil {
		c.metrics.recordCheck(route, record)
	}
}

func (c *Checker) recordFailure(route proxy.Route, phase, reason string) {
	if c.metrics != nil {
		c.metrics.recordFailure(route, phase, reason)
	}
}

func (c *Checker) RouteSummaries() []proxy.RouteSummary {
	out := make([]proxy.RouteSummary, 0, len(c.routes))
	for _, route := range c.routes {
		out = append(out, proxy.Summary(route))
	}
	return out
}

func (c *Checker) HealthSummaries() []RouteHealthSummary {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	out := make([]RouteHealthSummary, 0, len(c.routeStatus))
	for _, status := range c.routeStatus {
		summary := RouteHealthSummary{
			Route:       proxy.Summary(status.route),
			HealthState: string(status.healthState),
			History:     append([]CheckRecord(nil), status.history...),
		}
		if !status.lastCheck.IsZero() {
			last := status.lastCheck
			summary.LastCheck = &last
		}
		if !status.lastSuccess.IsZero() {
			last := status.lastSuccess
			summary.LastSuccess = &last
		}
		out = append(out, summary)
	}
	sort.Slice(out, func(i, j int) bool {
		return routeSummaryKey(out[i].Route) < routeSummaryKey(out[j].Route)
	})
	return out
}

func routeSummaryKey(summary proxy.RouteSummary) string {
	return summary.Kind + "\x00" + summary.Name + "\x00" + summary.RouteMode + "\x00" + summary.ProxyURL + "\x00" + summary.Target
}

func (c *Checker) IdentityMap() []proxy.IdentityRecord {
	return append([]proxy.IdentityRecord(nil), c.identityMap...)
}

func (c *Checker) logIdentityMap() {
	if c.logger == nil {
		return
	}
	if len(c.identityMap) == 0 {
		return
	}
	c.logger.Info("proxy identity map", slog.Any("records", c.identityMap))
}

func buildRoutes(controlPlane *config.ControlPlaneConfig, mcp *config.MCPConfig, harpoonCfg *config.HarpoonConfig, harpoonReg *harpoon.Registry, lookupEnv func(string) (string, bool)) []proxy.Route {
	routes := make([]proxy.Route, 0)
	if controlPlane != nil {
		name := "control-plane"
		routes = append(routes, proxy.ResolveRoute(proxy.RouteKindControlPlane, name, controlPlane.BaseURL, controlPlane.HTTPProxy, controlPlane.HTTPProxySource, lookupEnv))
	}
	if mcp != nil {
		for _, binding := range mcp.ChannelBindings {
			channel := binding.Channel.Canonical().String()
			var target *url.URL
			if binding.TransportKind == "" || binding.TransportKind == config.MCPTransportHTTPStreamable {
				target = binding.ServerURL
			}
			routes = append(routes, proxy.ResolveRoute(proxy.RouteKindMCPChannel, channel, target, binding.HTTPProxy, binding.HTTPProxySource, lookupEnv))
		}
	}
	if harpoonCfg != nil {
		targets := collectHarpoonTargets(harpoonCfg, harpoonReg)
		for _, target := range targets {
			name := target.Label
			if name == "" && target.BaseURL != nil {
				name = target.BaseURL.Hostname()
			}
			routes = append(routes, proxy.ResolveRoute(proxy.RouteKindHarpoon, name, target.BaseURL, harpoonCfg.HTTPProxy, harpoonCfg.HTTPProxySource, lookupEnv))
		}
	}
	return routes
}

func collectHarpoonTargets(cfg *config.HarpoonConfig, reg *harpoon.Registry) []harpoon.Target {
	if reg != nil {
		return reg.Targets()
	}
	if cfg == nil {
		return nil
	}
	return convertConfigTargets(cfg.Targets)
}

func routeKey(route proxy.Route) string {
	return fmt.Sprintf("%s:%s", route.Kind, route.Name)
}

func initialHealthState(route proxy.Route) HealthState {
	if route.RouteMode != proxy.RouteModeProxy {
		return HealthStateDirect
	}
	return HealthStateUnhealthy
}

func convertConfigTargets(targets []config.HarpoonTarget) []harpoon.Target {
	out := make([]harpoon.Target, 0, len(targets))
	for _, target := range targets {
		out = append(out, harpoon.Target{
			Label:       target.Label,
			Description: target.Description,
			Source:      "config",
			BaseURL:     target.BaseURL,
		})
	}
	return out
}

func defaultProxyCheckInterval(cfg *config.ProxyHealthConfig) time.Duration {
	if cfg == nil || cfg.CheckInterval <= 0 {
		return 60 * time.Second
	}
	return cfg.CheckInterval
}
