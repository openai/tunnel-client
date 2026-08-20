package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/controlplane"
	tclog "github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

const (
	readinessPath          = "/ready"
	readinessPollInterval  = 250 * time.Millisecond
	readinessProbeTimeout  = 500 * time.Millisecond
	readinessMonitorPeriod = time.Second
	shutdownGracePeriod    = 5 * time.Second
)

// Module wires the optional pinned cloudflared companion into the tunnel-client
// lifecycle.
var Module = fx.Module(
	"cloudflared",
	fx.Provide(NewState, NewSupervisor),
	fx.Invoke(registerLifecycle),
)

// Supervisor owns one bundled cloudflared process for the lifetime of the
// tunnel-client runtime.
type Supervisor struct {
	cfg     *runtimeconfig.CloudflaredSettings
	state   *State
	logger  *slog.Logger
	fetcher controlplane.ManagedCloudflareTunnelFetcher

	mu            sync.Mutex
	cmd           *exec.Cmd
	exited        chan struct{}
	exitedDone    bool
	waitErr       error
	stopping      bool
	metricsAddr   string
	monitorCancel context.CancelFunc
	failures      chan error
	newCommand    func(string, ...string) *exec.Cmd
}

// SupervisorParams contains the dependencies needed to construct the shared
// cloudflared supervisor. It is exported so the full-client compatibility
// package can delegate to this canonical implementation without copying it.
type SupervisorParams struct {
	fx.In

	Config                *runtimeconfig.CloudflaredSettings
	State                 *State
	Logger                *slog.Logger
	ManagedRuntimeFetcher controlplane.ManagedCloudflareTunnelFetcher `optional:"true"`
}

// NewSupervisor constructs the optional child-process supervisor.
func NewSupervisor(p SupervisorParams) (*Supervisor, error) {
	if p.Config == nil {
		return nil, errors.New("cloudflared: config is required")
	}
	if p.State == nil {
		return nil, errors.New("cloudflared: state is required")
	}
	if p.Logger == nil {
		return nil, errors.New("cloudflared: logger is required")
	}
	return &Supervisor{
		cfg:        p.Config,
		state:      p.State,
		logger:     p.Logger.With(slog.String(tclog.FieldComponent, tclog.ComponentCloudflared)),
		fetcher:    p.ManagedRuntimeFetcher,
		failures:   make(chan error, 1),
		newCommand: exec.Command,
	}, nil
}

// Failures receives one token-safe error when cloudflared exits unexpectedly.
// It never receives during a normal tunnel-client shutdown.
func (s *Supervisor) Failures() <-chan error {
	if s == nil {
		return nil
	}
	return s.failures
}

func registerLifecycle(lifecycle fx.Lifecycle, supervisor *Supervisor) error {
	if lifecycle == nil {
		return errors.New("cloudflared: lifecycle is required")
	}
	if supervisor == nil {
		return errors.New("cloudflared: supervisor is required")
	}
	lifecycle.Append(fx.Hook{
		OnStart: supervisor.Start,
		OnStop:  supervisor.Stop,
	})
	return nil
}

// Start launches the pinned companion and waits until its loopback /ready
// endpoint reports an active Cloudflare connection.
func (s *Supervisor) Start(ctx context.Context) error {
	if s == nil || s.cfg == nil || !s.cfg.Enabled() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	token, err := s.runtimeToken(ctx)
	if err != nil {
		s.state.setNotReady("cloudflared managed runtime fetch failed")
		return err
	}

	path, err := resolveExecutablePath(s.cfg.Path)
	if err != nil {
		s.state.setNotReady(err.Error())
		return err
	}
	metricsAddr, err := reserveLoopbackAddr()
	if err != nil {
		s.state.setNotReady("cloudflared readiness endpoint unavailable")
		return fmt.Errorf("cloudflared: reserve readiness address: %w", err)
	}

	cmd := s.newCommand(path, "tunnel", "--no-autoupdate", "--metrics", metricsAddr, "run")
	configureChildProcess(cmd)
	cmd.Env = cloudflaredEnvironment(os.Environ(), token)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("cloudflared: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdout.Close()
		return fmt.Errorf("cloudflared: stderr pipe: %w", err)
	}

	s.mu.Lock()
	if s.cmd != nil {
		s.mu.Unlock()
		_ = stdout.Close()
		_ = stderr.Close()
		return errors.New("cloudflared: supervisor already started")
	}
	s.cmd = cmd
	s.exited = make(chan struct{})
	s.exitedDone = false
	s.metricsAddr = metricsAddr
	s.mu.Unlock()

	if err := cmd.Start(); err != nil {
		s.mu.Lock()
		s.cmd = nil
		s.mu.Unlock()
		_ = stdout.Close()
		_ = stderr.Close()
		s.state.setNotReady("cloudflared failed to start")
		return fmt.Errorf("cloudflared: start bundled executable: %w", err)
	}

	s.logger.InfoContext(ctx,
		"bundled cloudflared started",
		slog.Int("pid", cmd.Process.Pid),
		slog.String("version", BundledVersion()),
		slog.String("metrics_addr", metricsAddr),
	)
	go s.consumeOutput(stdout, "stdout", token)
	go s.consumeOutput(stderr, "stderr", token)
	go s.waitForExit()

	readyCtx, cancel := context.WithTimeout(ctx, s.cfg.ReadyTimeout)
	defer cancel()
	if err := s.waitUntilReady(readyCtx); err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
		defer stopCancel()
		_ = s.Stop(stopCtx)
		return err
	}

	if !s.markReadyIfRunning() {
		return fmt.Errorf("cloudflared: process exited before readiness: %s", s.waitErrorString())
	}
	s.logger.InfoContext(ctx, "bundled cloudflared ready", slog.String("version", BundledVersion()))
	monitorCtx, monitorCancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.monitorCancel = monitorCancel
	s.mu.Unlock()
	go s.monitorReadiness(monitorCtx)
	return nil
}

func (s *Supervisor) runtimeToken(ctx context.Context) (string, error) {
	if s == nil || s.cfg == nil {
		return "", errors.New("cloudflared: config is required")
	}
	if strings.TrimSpace(s.cfg.Token) != "" {
		return s.cfg.Token, nil
	}
	if !s.cfg.Managed {
		return "", nil
	}
	if s.fetcher == nil {
		return "", errors.New("cloudflared: managed runtime fetcher is unavailable")
	}

	runtime, err := s.fetcher.FetchManagedCloudflareTunnel(ctx)
	if err != nil {
		// Fetcher errors may come from arbitrary transports. Keep the startup
		// error token-safe instead of formatting or chaining it.
		return "", errors.New("cloudflared: fetch managed runtime credentials failed")
	}
	if runtime == nil || strings.TrimSpace(runtime.RuntimeToken) == "" {
		return "", errors.New("cloudflared: managed runtime token is unavailable")
	}
	return runtime.RuntimeToken, nil
}

// Stop gracefully terminates the child, then kills it if it does not exit
// within the bounded shutdown grace period.
func (s *Supervisor) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	s.stopping = true
	cmd := s.cmd
	exited := s.exited
	monitorCancel := s.monitorCancel
	s.monitorCancel = nil
	s.mu.Unlock()
	if monitorCancel != nil {
		monitorCancel()
	}
	if cmd == nil || cmd.Process == nil || exited == nil {
		return nil
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		_ = cmd.Process.Kill()
	}

	grace := time.NewTimer(shutdownGracePeriod)
	defer grace.Stop()
	select {
	case <-exited:
		return nil
	case <-grace.C:
		_ = cmd.Process.Kill()
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return ctx.Err()
	}

	select {
	case <-exited:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Supervisor) waitUntilReady(ctx context.Context) error {
	ticker := time.NewTicker(readinessPollInterval)
	defer ticker.Stop()
	var lastErr error
	for {
		ready, err := s.probeReady(ctx)
		if ready {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-s.exitedCh():
			return fmt.Errorf("cloudflared: process exited before readiness: %s", s.waitErrorString())
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("cloudflared: readiness did not succeed before timeout: %w", lastErr)
			}
			return fmt.Errorf("cloudflared: readiness did not succeed before timeout: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (s *Supervisor) monitorReadiness(ctx context.Context) {
	ticker := time.NewTicker(readinessMonitorPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.exitedCh():
			return
		case <-ticker.C:
			probeCtx, cancel := context.WithTimeout(ctx, readinessProbeTimeout)
			ready, err := s.probeReady(probeCtx)
			cancel()
			if ready {
				s.state.setReady()
				continue
			}
			if err != nil {
				s.state.setNotReady("cloudflared readiness probe failed")
			} else {
				s.state.setNotReady("cloudflared is not ready")
			}
		}
	}
}

func (s *Supervisor) probeReady(ctx context.Context) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	addr := s.metricsAddr
	s.mu.Unlock()
	if addr == "" {
		return false, errors.New("cloudflared readiness address is unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+readinessPath, nil)
	if err != nil {
		return false, fmt.Errorf("build cloudflared readiness request: %w", err)
	}
	client := &http.Client{Timeout: readinessProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK, nil
}

func (s *Supervisor) waitForExit() {
	s.mu.Lock()
	cmd := s.cmd
	exited := s.exited
	s.mu.Unlock()
	if cmd == nil || exited == nil {
		return
	}
	err := cmd.Wait()
	s.mu.Lock()
	s.waitErr = err
	s.exitedDone = true
	stopping := s.stopping
	s.mu.Unlock()
	close(exited)
	if stopping {
		s.state.setNotReady("cloudflared stopped")
		s.logger.Info("bundled cloudflared stopped")
		return
	}

	message := "cloudflared process exited"
	if err != nil {
		message += ": " + err.Error()
	}
	s.state.setNotReady(message)
	s.logger.Error("bundled cloudflared exited unexpectedly", slog.String("error", errorString(err)))
	select {
	case s.failures <- errors.New(message):
	default:
	}
}

func (s *Supervisor) consumeOutput(reader io.Reader, stream, token string) {
	if reader == nil {
		return
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		message := redactToken(scanner.Text(), token)
		s.logger.Info("cloudflared output", slog.String("stream", stream), slog.String("message", message))
	}
	if err := scanner.Err(); err != nil {
		s.logger.Warn("read cloudflared output", slog.String("stream", stream), slog.String("error", err.Error()))
	}
}

func (s *Supervisor) exitedCh() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exited == nil {
		never := make(chan struct{})
		return never
	}
	return s.exited
}

func (s *Supervisor) waitErrorString() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return errorString(s.waitErr)
}

func (s *Supervisor) markReadyIfRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exitedDone {
		return false
	}
	s.state.setReady()
	return true
}

func resolveExecutablePath(override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		return validateExecutablePath(override)
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cloudflared: locate tunnel-client executable: %w", err)
	}
	candidate := filepath.Join(filepath.Dir(executable), executableName())
	path, err := validateExecutablePath(candidate)
	if err != nil {
		return "", fmt.Errorf("cloudflared: bundled executable not found beside tunnel-client at %s; use a supported distribution or set --cloudflared.path: %w", candidate, err)
	}
	return path, nil
}

func validateExecutablePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("cloudflared executable path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%s is not executable", path)
	}
	return path, nil
}

func executableName() string {
	if runtime.GOOS == "windows" {
		return "cloudflared.exe"
	}
	return "cloudflared"
}

func reserveLoopbackAddr() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", err
	}
	return addr, nil
}

func cloudflaredEnvironment(environ []string, token string) []string {
	// Keep the diagnostics toggle fail-closed even when the parent environment
	// attempts to enable remote management diagnostics.
	out := make([]string, 0, len(environ)+2)
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (strings.EqualFold(key, "TUNNEL_TOKEN") ||
			strings.EqualFold(key, "TUNNEL_MANAGEMENT_DIAGNOSTICS") ||
			(token != "" && value == token)) {
			continue
		}
		out = append(out, entry)
	}
	return append(out,
		"TUNNEL_TOKEN="+token,
		"TUNNEL_MANAGEMENT_DIAGNOSTICS=false",
	)
}

func redactToken(message, token string) string {
	if token == "" {
		return message
	}
	return strings.ReplaceAll(message, token, "[REDACTED]")
}

func errorString(err error) string {
	if err == nil {
		return "clean exit"
	}
	return err.Error()
}
