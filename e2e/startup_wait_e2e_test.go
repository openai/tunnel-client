package e2e_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/openai/tunnel-client/pkg/config"
	harnesspkg "github.com/openai/tunnel-client/testsupport/e2e"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

const retryingStartupProbeLog = "retrying MCP startup probe"

// TestStartupWaitDelaysPollingUntilMCPListenerBinds covers the sidecar startup
// race where tunnel-client starts before its pod-local MCP proxy has bound.
func TestStartupWaitDelaysPollingUntilMCPListenerBinds(t *testing.T) {
	delayedAddr := reserveLoopbackAddr(t)
	delayedURL := mustParseURL(t, "http://"+delayedAddr)
	retryLog := newSubstringSignalWriter(retryingStartupProbeLog)

	h := runSimpleToolScenarioWithHarnessOptions(
		t,
		[]harnesspkg.HarnessOption{
			harnesspkg.WithPreserveClientURLs(),
			harnesspkg.WithLogWriter(retryLog),
			harnesspkg.WithClientConfig(func(cfg *config.Config) {
				cfg.Logging.Level = slog.LevelDebug
				cfg.MCP.ServerURL = delayedURL
				cfg.MCP.StartupWaitTimeout = 10 * time.Second
			}),
			harnesspkg.WithAfterClientStart(func(h *harnesspkg.Harness) {
				waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				select {
				case <-retryLog.Seen():
				case <-waitCtx.Done():
					t.Fatalf("did not observe %q before listener bind: %v", retryingStartupProbeLog, waitCtx.Err())
				}

				if h.MCPProbeState == nil {
					t.Fatal("MCP probe state was not populated")
				}
				if h.MCPProbeState.IsDone() {
					t.Fatal("MCP probe completed before delayed listener bound")
				}

				client := h.PrimaryClient()
				if client == nil {
					t.Fatal("primary tunnel-client was not started")
				}
				if got := client.PollCount(); got != 0 {
					t.Fatalf("poll count before delayed listener bind = %d, want 0", got)
				}
				if got := len(h.ControlPlane.DeliveredCommands()); got != 0 {
					t.Fatalf("delivered commands before delayed listener bind = %d, want 0", got)
				}
				if got := len(h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchAll)); got != 0 {
					t.Fatalf("responses before delayed listener bind = %d, want 0", got)
				}

				startDelayedMCPProxy(t, delayedAddr, h.MCP.BaseURL())
			}),
		},
		nil,
	)

	for _, response := range h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchAll) {
		if response.ResponseCode == http.StatusBadGateway {
			t.Fatalf("received early terminal 502 for request %q", response.RequestID)
		}
	}
}

func reserveLoopbackAddr(t testing.TB) string {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve delayed MCP listener address: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release delayed MCP listener address: %v", err)
	}
	return addr
}

func startDelayedMCPProxy(t testing.TB, addr string, target *url.URL) {
	t.Helper()
	if target == nil {
		t.Fatal("mock MCP server URL is nil")
	}

	listener, err := net.Listen("tcp4", addr)
	if err != nil {
		t.Fatalf("bind delayed MCP listener %q: %v", addr, err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	server := &http.Server{Handler: proxy}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	t.Cleanup(func() {
		if err := server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("close delayed MCP proxy: %v", err)
		}
		select {
		case err := <-serveErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("serve delayed MCP proxy: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("delayed MCP proxy did not stop")
		}
	})
}

type substringSignalWriter struct {
	needle []byte
	seen   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	buf    []byte
}

func newSubstringSignalWriter(needle string) *substringSignalWriter {
	return &substringSignalWriter{
		needle: []byte(needle),
		seen:   make(chan struct{}),
	}
}

func (w *substringSignalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf = append(w.buf, p...)
	if bytes.Contains(w.buf, w.needle) {
		w.once.Do(func() { close(w.seen) })
	}
	if keep := len(w.needle) - 1; keep > 0 && len(w.buf) > keep {
		w.buf = append(w.buf[:0], w.buf[len(w.buf)-keep:]...)
	}
	return len(p), nil
}

func (w *substringSignalWriter) Seen() <-chan struct{} {
	return w.seen
}
