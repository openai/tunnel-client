package adminui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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

func TestAdminUIRoutesPreserveRootAndIsolateMount(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		basePath string
	}{
		{name: "default"},
		{name: "mounted", basePath: "/tools/tunnel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mux := newRouteTestMux(t, config.AdminUIConfig{BasePath: tc.basePath})
			for _, route := range []struct {
				path       string
				wantStatus int
			}{
				{path: tc.basePath + "/", wantStatus: http.StatusOK},
				{path: tc.basePath + "/ui", wantStatus: http.StatusOK},
				{path: tc.basePath + "/ui/", wantStatus: http.StatusOK},
				{path: tc.basePath + "/assets/", wantStatus: http.StatusOK},
				{path: tc.basePath + "/assets/app.js", wantStatus: http.StatusOK},
				{path: tc.basePath + "/assets/styles.css", wantStatus: http.StatusOK},
				{path: tc.basePath + "/api/status", wantStatus: http.StatusOK},
				{path: tc.basePath + "/api/logs?limit=1", wantStatus: http.StatusOK},
				{path: tc.basePath + "/favicon.ico", wantStatus: http.StatusNoContent},
				{path: tc.basePath + "/unknown", wantStatus: http.StatusNotFound},
			} {
				resp := serveRoute(mux, http.MethodGet, route.path, "127.0.0.1:1234", nil)
				if resp.Code != route.wantStatus {
					t.Errorf("GET %s status = %d, want %d", route.path, resp.Code, route.wantStatus)
				}
			}
			prefixes := []string{""}
			if tc.basePath != "" {
				prefixes = append(prefixes, tc.basePath)
			}
			for _, route := range []string{"/healthz", "/readyz", "/metrics"} {
				for _, prefix := range prefixes {
					resp := serveRoute(mux, http.MethodGet, prefix+route+"?detail=1", "127.0.0.1:1234", nil)
					if resp.Code != http.StatusOK || resp.Body.String() != "root:"+route+"?detail=1" {
						t.Errorf("GET %s%s did not retain root handler and query: %d %q", prefix, route, resp.Code, resp.Body.String())
					}
				}
			}
			for _, route := range []string{"/health", "/health/component", "/harpoon/mcp"} {
				resp := serveRoute(mux, http.MethodGet, route, "127.0.0.1:1234", nil)
				if resp.Code != http.StatusOK || resp.Body.String() != "root:"+route+"?" {
					t.Errorf("GET %s root handler changed: %d %q", route, resp.Code, resp.Body.String())
				}
				if tc.basePath != "" {
					resp = serveRoute(mux, http.MethodGet, tc.basePath+route, "127.0.0.1:1234", nil)
					if resp.Code != http.StatusNotFound {
						t.Errorf("GET %s%s status = %d, want no alias", tc.basePath, route, resp.Code)
					}
				}
			}
			if tc.basePath != "" {
				for _, route := range []string{"/", "/ui", "/assets/app.js", "/api/status", tc.basePath + "ish/ui"} {
					resp := serveRoute(mux, http.MethodGet, route, "127.0.0.1:1234", nil)
					if resp.Code != http.StatusNotFound {
						t.Errorf("GET %s status = %d, want no UI route outside mount", route, resp.Code)
					}
				}
			}
		})
	}
}

func TestAdminUIIndexRootsAndRelativeRedirects(t *testing.T) {
	t.Parallel()

	for _, basePath := range []string{"", "/tools/tunnel"} {
		t.Run("base="+basePath, func(t *testing.T) {
			t.Parallel()
			mux := newRouteTestMux(t, config.AdminUIConfig{BasePath: basePath})
			for route, root := range map[string]string{"/": "./", "/ui": "./", "/ui/": "../", "/assets/": "../"} {
				resp := serveRoute(mux, http.MethodGet, basePath+route, "127.0.0.1:1234", nil)
				if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `name="admin-ui-root" content="`+root+`"`) {
					t.Errorf("GET %s%s did not render root %q: %d %q", basePath, route, root, resp.Code, resp.Body.String())
				}
				if strings.Contains(resp.Body.String(), "{{.AppRoot}}") {
					t.Errorf("GET %s%s exposed the index template", basePath, route)
				}
				if resp.Header().Get("Cache-Control") != "no-store, max-age=0" || resp.Header().Get("Content-Type") != "text/html; charset=utf-8" {
					t.Errorf("GET %s%s changed index headers: %v", basePath, route, resp.Header())
				}
			}
			redirects := []struct {
				path     string
				status   int
				location string
			}{
				{path: basePath + "/assets?view=ui", status: http.StatusTemporaryRedirect, location: "assets/?view=ui"},
				{path: basePath + "/assets?", status: http.StatusTemporaryRedirect, location: "assets/?"},
				{path: basePath + "/assets/index.html?view=ui", status: http.StatusMovedPermanently, location: "./?view=ui"},
			}
			if basePath != "" {
				redirects = append(redirects, struct {
					path     string
					status   int
					location string
				}{path: basePath + "?view=ui", status: http.StatusTemporaryRedirect, location: "tunnel/?view=ui"})
			}
			for _, redirect := range redirects {
				resp := serveRoute(mux, http.MethodGet, redirect.path, "127.0.0.1:1234", nil)
				if resp.Code != redirect.status || resp.Header().Get("Location") != redirect.location {
					t.Errorf("GET %s redirect = %d %q, want %d %q", redirect.path, resp.Code, resp.Header().Get("Location"), redirect.status, redirect.location)
				}
			}
		})
	}
}

func TestAdminUIPublicOriginPreservesNetworkGate(t *testing.T) {
	t.Parallel()

	for _, basePath := range []string{"", "/tools/tunnel"} {
		for _, allowRemote := range []bool{false, true} {
			t.Run(fmt.Sprintf("base=%s/remote=%t", basePath, allowRemote), func(t *testing.T) {
				t.Parallel()
				mux := newRouteTestMux(t, config.AdminUIConfig{
					BasePath:     basePath,
					AllowRemote:  allowRemote,
					PublicOrigin: &url.URL{Scheme: "https", Host: "admin.example"},
				})
				for _, tc := range []struct {
					name       string
					remoteAddr string
					origin     string
					fetchSite  string
					wantStatus int
				}{
					{name: "local public origin", remoteAddr: "127.0.0.1:1234", origin: "https://admin.example", wantStatus: http.StatusServiceUnavailable},
					{name: "local legacy origin", remoteAddr: "127.0.0.1:1234", origin: "http://127.0.0.1:9090", wantStatus: http.StatusServiceUnavailable},
					{name: "local wrong origin", remoteAddr: "127.0.0.1:1234", origin: "https://attacker.example", wantStatus: http.StatusForbidden},
					{name: "local cross site", remoteAddr: "127.0.0.1:1234", origin: "https://admin.example", fetchSite: "cross-site", wantStatus: http.StatusForbidden},
					{name: "local headerless", remoteAddr: "127.0.0.1:1234", wantStatus: http.StatusServiceUnavailable},
				} {
					headers := http.Header{"Origin": {tc.origin}, "Sec-Fetch-Site": {tc.fetchSite}}
					resp := serveRoute(mux, http.MethodPost, basePath+"/api/codex/thread/start", tc.remoteAddr, headers)
					if resp.Code != tc.wantStatus {
						t.Errorf("%s status = %d, want %d", tc.name, resp.Code, tc.wantStatus)
					}
				}
				headers := http.Header{
					"Origin":          {"https://admin.example"},
					"Forwarded":       {"for=127.0.0.1;proto=https;host=admin.example"},
					"X-Forwarded-For": {"127.0.0.1"},
				}
				wantUI, wantWrite := http.StatusForbidden, http.StatusForbidden
				if allowRemote {
					wantUI, wantWrite = http.StatusOK, http.StatusServiceUnavailable
				}
				resp := serveRoute(mux, http.MethodGet, basePath+"/ui", "192.0.2.1:1234", headers)
				if resp.Code != wantUI {
					t.Errorf("remote UI status = %d, want %d", resp.Code, wantUI)
				}
				resp = serveRoute(mux, http.MethodPost, basePath+"/api/codex/thread/start", "192.0.2.1:1234", headers)
				if resp.Code != wantWrite {
					t.Errorf("remote write status = %d, want %d", resp.Code, wantWrite)
				}
				resp = serveRoute(mux, http.MethodGet, "/healthz", "192.0.2.1:1234", headers)
				if resp.Code != http.StatusOK {
					t.Errorf("root health status = %d, want existing operational access", resp.Code)
				}
				if basePath != "" {
					resp = serveRoute(mux, http.MethodGet, basePath+"/healthz", "192.0.2.1:1234", headers)
					if resp.Code != wantUI {
						t.Errorf("mounted health status = %d, want UI access policy %d", resp.Code, wantUI)
					}
				}
			})
		}
	}
}

func TestAdminUIMountPreservesStreaming(t *testing.T) {
	t.Parallel()

	for _, basePath := range []string{"", "/tools/tunnel"} {
		t.Run("base="+basePath, func(t *testing.T) {
			t.Parallel()
			adminMux, uiMux := http.NewServeMux(), http.NewServeMux()
			release := make(chan struct{})
			uiMux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
				flusher, ok := w.(http.Flusher)
				if !ok {
					t.Error("mounted response writer does not support streaming")
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: first\n\n")
				flusher.Flush()
				select {
				case <-release:
				case <-r.Context().Done():
				}
			})
			mountUI(adminMux, uiMux, basePath, false, "")
			server := httptest.NewServer(adminMux)
			t.Cleanup(server.Close)
			t.Cleanup(func() { close(release) })
			client := server.Client()
			client.Timeout = 2 * time.Second
			resp, err := client.Get(server.URL + basePath + "/stream")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			line, err := bufio.NewReader(resp.Body).ReadString('\n')
			if err != nil || line != "data: first\n" {
				t.Fatalf("streamed line = %q, error = %v", line, err)
			}
		})
	}
}

func TestAdminUILogStreamRouteUsesEventStream(t *testing.T) {
	t.Parallel()

	for _, basePath := range []string{"", "/tools/tunnel"} {
		t.Run("base="+basePath, func(t *testing.T) {
			t.Parallel()
			mux := newRouteTestMux(t, config.AdminUIConfig{BasePath: basePath})
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9090"+basePath+"/api/logs/stream", nil).WithContext(ctx)
			req.RemoteAddr = "127.0.0.1:1234"
			resp := httptest.NewRecorder()
			mux.ServeHTTP(resp, req)
			if resp.Header().Get("Content-Type") != "text/event-stream" {
				t.Fatalf("stream route returned content type %q", resp.Header().Get("Content-Type"))
			}
		})
	}
}

func TestRegisterRoutesRejectsInvalidManualBasePath(t *testing.T) {
	t.Parallel()

	for _, basePath := range []string{"/metrics", "/health/nested", "/tools/{rest...}", "//tools"} {
		t.Run(basePath, func(t *testing.T) {
			t.Parallel()
			err := registerRoutes(routeParams{
				AdminMux:      http.NewServeMux(),
				Lifecycle:     fxtest.NewLifecycle(t),
				Buffer:        NewLogBufferWithCapacity(1),
				AdminUIConfig: &config.AdminUIConfig{BasePath: basePath},
			})
			if err == nil {
				t.Fatal("invalid manually constructed base path was accepted")
			}
		})
	}
}

func TestRegisterRoutesRejectsInvalidManualPublicOrigin(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"https://admin.example/tools/tunnel", "ftp://admin.example:443"} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			origin, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			err = registerRoutes(routeParams{
				AdminMux:      http.NewServeMux(),
				Lifecycle:     fxtest.NewLifecycle(t),
				Buffer:        NewLogBufferWithCapacity(1),
				AdminUIConfig: &config.AdminUIConfig{PublicOrigin: origin},
			})
			if err == nil {
				t.Fatal("invalid manually constructed public origin was accepted")
			}
		})
	}
}

func newRouteTestMux(t *testing.T, cfg config.AdminUIConfig) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	for _, route := range []string{"/healthz", "/readyz", "/metrics", "/health", "/health/", "/harpoon/mcp"} {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, "root:%s?%s", r.URL.Path, r.URL.RawQuery)
		})
	}
	if err := registerRoutes(routeParams{
		AdminMux:      mux,
		Lifecycle:     fxtest.NewLifecycle(t),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Buffer:        NewLogBufferWithCapacity(1),
		AdminUIConfig: &cfg,
	}); err != nil {
		t.Fatalf("register routes: %v", err)
	}
	return mux
}

func serveRoute(mux *http.ServeMux, method, route, remoteAddr string, headers http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1:9090"+route, strings.NewReader(`{}`))
	req.RemoteAddr = remoteAddr
	if headers != nil {
		req.Header = headers.Clone()
	}
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	return resp
}
