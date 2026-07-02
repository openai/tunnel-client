package adminui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/fx/fxtest"

	"github.com/openai/tunnel-client/pkg/config"
)

func TestRegisterRoutesProtectsCodexWritesFromCrossOriginPosts(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	err := registerRoutes(routeParams{
		AdminMux:      mux,
		Lifecycle:     fxtest.NewLifecycle(t),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Buffer:        NewLogBufferWithCapacity(1),
		AdminUIConfig: &config.AdminUIConfig{},
	})
	if err != nil {
		t.Fatalf("register routes: %v", err)
	}

	crossOriginReq := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1:9090/api/codex/thread/start",
		strings.NewReader(`{"cwd":"/tmp"}`),
	)
	crossOriginReq.RemoteAddr = "127.0.0.1:1234"
	crossOriginReq.Header.Set("Origin", "https://attacker.example")
	crossOriginResp := httptest.NewRecorder()

	mux.ServeHTTP(crossOriginResp, crossOriginReq)

	if crossOriginResp.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want %d", crossOriginResp.Code, http.StatusForbidden)
	}

	sameOriginReq := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1:9090/api/codex/thread/start",
		strings.NewReader(`{"cwd":"/tmp"}`),
	)
	sameOriginReq.RemoteAddr = "127.0.0.1:1234"
	sameOriginReq.Header.Set("Origin", "http://127.0.0.1:9090")
	sameOriginResp := httptest.NewRecorder()

	mux.ServeHTTP(sameOriginResp, sameOriginReq)

	if sameOriginResp.Code != http.StatusServiceUnavailable {
		t.Fatalf("same-origin status = %d, want %d", sameOriginResp.Code, http.StatusServiceUnavailable)
	}
}
