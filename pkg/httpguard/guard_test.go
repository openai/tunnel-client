package httpguard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestIsLoopbackRequest(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		remoteAddr string
		want       bool
	}{
		{name: "ipv4 loopback", remoteAddr: "127.0.0.1:1234", want: true},
		{name: "ipv6 loopback", remoteAddr: "[::1]:443", want: true},
		{name: "non loopback", remoteAddr: "10.1.2.3:443", want: false},
		{name: "invalid remote addr", remoteAddr: "malformed", want: false},
		{name: "missing port", remoteAddr: "127.0.0.1", want: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "http://example.test", nil)
			req.RemoteAddr = tc.remoteAddr
			if got := IsLoopbackRequest(req); got != tc.want {
				t.Fatalf("IsLoopbackRequest() = %v, want %v", got, tc.want)
			}
		})
	}

	if got := IsLoopbackRequest(nil); got {
		t.Fatalf("IsLoopbackRequest(nil) = true, want false")
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.test", nil)
	req = req.WithContext(WithConnectionNetwork(req.Context(), "unix"))
	if got := IsLoopbackRequest(req); !got {
		t.Fatalf("IsLoopbackRequest(unix) = false, want true")
	}
}

func TestLocalOnly(t *testing.T) {
	t.Parallel()

	t.Run("rejects non loopback with default message", func(t *testing.T) {
		t.Parallel()

		called := false
		handler := LocalOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		}), "")

		req := httptest.NewRequest(http.MethodGet, "http://example.test", nil)
		req.RemoteAddr = "192.168.1.10:5555"
		resp := httptest.NewRecorder()

		handler.ServeHTTP(resp, req)

		if called {
			t.Fatalf("next handler should not be called for remote request")
		}
		if resp.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", resp.Code, http.StatusForbidden)
		}
		if got := resp.Body.String(); got != defaultLoopbackMessage+"\n" {
			t.Fatalf("body = %q, want %q", got, defaultLoopbackMessage+"\n")
		}
	})

	t.Run("ignores forwarded loopback spoof from remote address", func(t *testing.T) {
		t.Parallel()

		called := false
		handler := LocalOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		}), "loopback only")

		req := httptest.NewRequest(http.MethodGet, "http://example.test", nil)
		req.RemoteAddr = "203.0.113.10:5555"
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		req.Header.Set("Forwarded", `for="[::1]"`)
		resp := httptest.NewRecorder()

		handler.ServeHTTP(resp, req)

		if called {
			t.Fatalf("next handler should not be called for forwarded-header spoof")
		}
		if resp.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", resp.Code, http.StatusForbidden)
		}
	})

	t.Run("allows loopback", func(t *testing.T) {
		t.Parallel()

		called := false
		handler := LocalOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		}), "custom")

		req := httptest.NewRequest(http.MethodGet, "http://example.test", nil)
		req.RemoteAddr = "127.0.0.1:9999"
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)

		if !called {
			t.Fatalf("next handler should be called for loopback request")
		}
		if resp.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", resp.Code, http.StatusNoContent)
		}
	})
}

func TestSameOriginUnsafe(t *testing.T) {
	t.Parallel()

	t.Run("allows same-origin unsafe browser request", func(t *testing.T) {
		t.Parallel()

		called := false
		handler := SameOriginUnsafe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		}), "")

		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9090/api/codex/thread/start", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Origin", "http://127.0.0.1:9090")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp := httptest.NewRecorder()

		handler.ServeHTTP(resp, req)

		if !called {
			t.Fatalf("next handler should be called for same-origin request")
		}
		if resp.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", resp.Code, http.StatusNoContent)
		}
	})

	t.Run("rejects dns rebinding origin on loopback transport", func(t *testing.T) {
		t.Parallel()

		called := false
		handler := SameOriginUnsafe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		}), "")

		req := httptest.NewRequest(http.MethodPost, "http://attacker.example:9090/api/codex/thread/start", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Origin", "http://attacker.example:9090")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp := httptest.NewRecorder()

		handler.ServeHTTP(resp, req)

		if called {
			t.Fatalf("next handler should not be called for DNS-rebound local request")
		}
		if resp.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", resp.Code, http.StatusForbidden)
		}
	})

	t.Run("rejects cross-origin unsafe browser request", func(t *testing.T) {
		t.Parallel()

		called := false
		handler := SameOriginUnsafe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		}), "")

		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9090/api/codex/thread/start", nil)
		req.Header.Set("Origin", "https://attacker.example")
		resp := httptest.NewRecorder()

		handler.ServeHTTP(resp, req)

		if called {
			t.Fatalf("next handler should not be called for cross-origin request")
		}
		if resp.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", resp.Code, http.StatusForbidden)
		}
	})

	t.Run("rejects cross-site fetch metadata without origin", func(t *testing.T) {
		t.Parallel()

		called := false
		handler := SameOriginUnsafe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		}), "")

		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9090/api/codex/thread/start", nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		resp := httptest.NewRecorder()

		handler.ServeHTTP(resp, req)

		if called {
			t.Fatalf("next handler should not be called for cross-site request")
		}
		if resp.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", resp.Code, http.StatusForbidden)
		}
	})

	t.Run("allows safe browser reads", func(t *testing.T) {
		t.Parallel()

		called := false
		handler := SameOriginUnsafe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		}), "")

		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9090/api/codex/status", nil)
		req.Header.Set("Origin", "https://attacker.example")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		resp := httptest.NewRecorder()

		handler.ServeHTTP(resp, req)

		if !called {
			t.Fatalf("next handler should be called for safe method")
		}
		if resp.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", resp.Code, http.StatusNoContent)
		}
	})

	t.Run("allows local non-browser unsafe request", func(t *testing.T) {
		t.Parallel()

		called := false
		handler := SameOriginUnsafe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		}), "")

		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9090/api/codex/thread/start", nil)
		resp := httptest.NewRecorder()

		handler.ServeHTTP(resp, req)

		if !called {
			t.Fatalf("next handler should be called for local non-browser request")
		}
		if resp.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", resp.Code, http.StatusNoContent)
		}
	})
}

func TestSameOriginUnsafeWithPublicOrigin(t *testing.T) {
	t.Parallel()

	publicOrigin, err := url.Parse("https://admin.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		method     string
		origin     string
		referer    string
		fetchSite  string
		forwarded  bool
		wantStatus int
	}{
		{name: "legacy local origin", origin: "http://127.0.0.1:9090", wantStatus: http.StatusNoContent},
		{name: "public origin", origin: "https://admin.example", wantStatus: http.StatusNoContent},
		{name: "public origin default port", origin: "https://ADMIN.example:443", wantStatus: http.StatusNoContent},
		{name: "public referer", referer: "https://admin.example/tools/tunnel/ui?tab=logs", wantStatus: http.StatusNoContent},
		{name: "headerless legacy client", wantStatus: http.StatusNoContent},
		{name: "wrong scheme", origin: "http://admin.example", wantStatus: http.StatusForbidden},
		{name: "wrong port", origin: "https://admin.example:8443", wantStatus: http.StatusForbidden},
		{name: "subdomain", origin: "https://other.admin.example", wantStatus: http.StatusForbidden},
		{name: "trailing dot is distinct", origin: "https://admin.example.", wantStatus: http.StatusForbidden},
		{name: "null origin", origin: "null", wantStatus: http.StatusForbidden},
		{name: "origin with credentials", origin: "https://user@admin.example", wantStatus: http.StatusForbidden},
		{name: "origin with path", origin: "https://admin.example/tools/tunnel", wantStatus: http.StatusForbidden},
		{name: "origin with empty query", origin: "https://admin.example?", wantStatus: http.StatusForbidden},
		{name: "origin with fragment", origin: "https://admin.example#", wantStatus: http.StatusForbidden},
		{name: "origin with empty port", origin: "https://admin.example:", wantStatus: http.StatusForbidden},
		{name: "origin takes precedence over referer", origin: "https://attacker.example", referer: "https://admin.example/tools/tunnel/ui", wantStatus: http.StatusForbidden},
		{name: "cross site public origin", origin: "https://admin.example", fetchSite: "cross-site", wantStatus: http.StatusForbidden},
		{name: "cross site without origin", fetchSite: "cross-site", wantStatus: http.StatusForbidden},
		{name: "forwarded headers do not allow origin", origin: "https://attacker.example", forwarded: true, wantStatus: http.StatusForbidden},
		{name: "forwarded headers do not change public origin", origin: "https://admin.example", forwarded: true, wantStatus: http.StatusNoContent},
		{name: "safe request remains allowed", method: http.MethodGet, origin: "https://attacker.example", fetchSite: "cross-site", wantStatus: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			method := tc.method
			if method == "" {
				method = http.MethodPost
			}
			handler := SameOriginUnsafeWithPublicOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}), "", publicOrigin)
			req := httptest.NewRequest(method, "http://127.0.0.1:9090/api/codex/thread/start", nil)
			req.RemoteAddr = "127.0.0.1:1234"
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.referer != "" {
				req.Header.Set("Referer", tc.referer)
			}
			if tc.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			}
			if tc.forwarded {
				req.Header.Set("Forwarded", "for=127.0.0.1;proto=https;host=attacker.example")
				req.Header.Set("X-Forwarded-For", "127.0.0.1")
				req.Header.Set("X-Forwarded-Host", "attacker.example")
				req.Header.Set("X-Forwarded-Proto", "https")
				req.Header.Set("X-Forwarded-Prefix", "/tools/tunnel")
				req.Header.Set("X-Ingress-Path", "/tools/tunnel")
			}
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, req)
			if resp.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %q", resp.Code, tc.wantStatus, resp.Body.String())
			}
		})
	}
}

func TestPublicOriginComparesExactHostAndEffectivePort(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		configured string
		request    string
		want       bool
	}{
		{configured: "http://admin.example", request: "http://ADMIN.example:080", want: true},
		{configured: "https://admin.example:8443", request: "https://admin.example:8443", want: true},
		{configured: "https://admin.example:8443", request: "https://admin.example", want: false},
		{configured: "https://[2001:db8::1]", request: "https://[2001:0db8:0:0:0:0:0:1]:443", want: true},
		{configured: "http://127.0.0.1", request: "http://[::ffff:127.0.0.1]", want: false},
		{configured: "http://[fe80::1]", request: "http://[fe80::1%25eth0]", want: false},
		{configured: "ftp://admin.example:443", request: "ftp://admin.example:443", want: false},
	} {
		t.Run(tc.configured+"_"+tc.request, func(t *testing.T) {
			t.Parallel()
			configured, err := url.Parse(tc.configured)
			if err != nil {
				t.Fatal(err)
			}
			if got := matchesPublicOrigin(tc.request, configured, false); got != tc.want {
				t.Fatalf("matchesPublicOrigin(%q, %q) = %t, want %t", tc.request, tc.configured, got, tc.want)
			}
		})
	}
}

func TestGuardedMuxHandleFunc(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	guarded := NewGuardedMux(mux, false, "loopback only")
	guarded.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	remoteReq := httptest.NewRequest(http.MethodGet, "http://example.test/healthz", nil)
	remoteReq.RemoteAddr = "10.0.0.5:1234"
	remoteResp := httptest.NewRecorder()
	mux.ServeHTTP(remoteResp, remoteReq)
	if remoteResp.Code != http.StatusForbidden {
		t.Fatalf("remote status = %d, want %d", remoteResp.Code, http.StatusForbidden)
	}

	loopbackReq := httptest.NewRequest(http.MethodGet, "http://example.test/healthz", nil)
	loopbackReq.RemoteAddr = "127.0.0.1:1234"
	loopbackResp := httptest.NewRecorder()
	mux.ServeHTTP(loopbackResp, loopbackReq)
	if loopbackResp.Code != http.StatusNoContent {
		t.Fatalf("loopback status = %d, want %d", loopbackResp.Code, http.StatusNoContent)
	}
}

func TestWithShutdownContextCancelsRequestContext(t *testing.T) {
	t.Parallel()

	shutdownCtx, cancelShutdown := context.WithCancel(context.Background())
	defer cancelShutdown()

	requestObservedCancel := make(chan struct{}, 1)
	handlerDone := make(chan struct{}, 1)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		requestObservedCancel <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
		handlerDone <- struct{}{}
	})

	wrapped := WithShutdownContext(next, shutdownCtx)

	req := httptest.NewRequest(http.MethodGet, "http://example.test", nil)
	req.RemoteAddr = "127.0.0.1:2222"
	resp := httptest.NewRecorder()

	go wrapped.ServeHTTP(resp, req)
	cancelShutdown()

	select {
	case <-requestObservedCancel:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("request context was not canceled by shutdown context")
	}

	select {
	case <-handlerDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("wrapped handler did not complete")
	}
}
