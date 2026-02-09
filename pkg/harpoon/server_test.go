package harpoon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/transport"
	"go.openai.org/api/tunnel-client/pkg/version"
)

func TestListTargetsDoesNotExposeURLs(t *testing.T) {
	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   config.DefaultHarpoonMaxResponseBytes,
		MaxRedirects:       config.DefaultHarpoonMaxRedirects,
		Targets: []config.HarpoonTarget{{
			Label:       "auth",
			Description: "Auth service",
			BaseURL:     mustParseURL(t, "http://example.com"),
		}},
	}
	server := newTestServer(t, cfg)

	resp := server.listTargets(listTargetsRequest{})
	require.Len(t, resp.Targets, 1)
	require.Equal(t, "auth", resp.Targets[0].Label)
	require.Equal(t, "Auth service", resp.Targets[0].Description)
	require.Equal(t, "config", resp.Targets[0].Category)
	require.Equal(t, "config", resp.Targets[0].Source)
	require.NotEmpty(t, resp.Targets[0].AllowedMethods)

	payload, err := json.Marshal(resp)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "http://")
	require.NotContains(t, string(payload), "https://")
}

func TestListTargetsFilters(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, true, []Target{
		{
			Label:    "oauth-auth",
			Category: "oauth",
			Source:   "oauth",
			Tags:     []string{"auth-server-metadata", "issuer"},
			BaseURL:  mustParseURL(t, "https://auth.internal/issuer"),
		},
		{
			Label:    "oauth-prmd",
			Category: "oauth",
			Source:   "oauth",
			Tags:     []string{"protected-resource-metadata", "resource"},
			BaseURL:  mustParseURL(t, "https://resource.internal/prmd"),
		},
		{
			Label:    "config",
			Category: "config",
			Source:   "config",
			BaseURL:  mustParseURL(t, "https://config.internal"),
		},
	})
	require.NoError(t, err)

	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
	}
	server, err := NewServer(cfg, registry, NewCallBuffer(), logger)
	require.NoError(t, err)

	all := server.listTargets(listTargetsRequest{})
	require.Len(t, all.Targets, 3)

	oauthOnly := server.listTargets(listTargetsRequest{Categories: []string{"oauth"}})
	require.Len(t, oauthOnly.Targets, 2)

	sourceOnly := server.listTargets(listTargetsRequest{Sources: []string{"config"}})
	require.Len(t, sourceOnly.Targets, 1)
	require.Equal(t, "config", sourceOnly.Targets[0].Label)

	tagged := server.listTargets(listTargetsRequest{Tags: []string{"auth-server-metadata"}})
	require.Len(t, tagged.Targets, 1)
	require.Equal(t, "oauth-auth", tagged.Targets[0].Label)

	allTags := server.listTargets(listTargetsRequest{Tags: []string{"auth-server-metadata", "issuer"}})
	require.Len(t, allTags.Targets, 1)
	require.Equal(t, "oauth-auth", allTags.Targets[0].Label)

	combined := server.listTargets(listTargetsRequest{Categories: []string{"oauth"}, Tags: []string{"protected-resource-metadata"}})
	require.Len(t, combined.Targets, 1)
	require.Equal(t, "oauth-prmd", combined.Targets[0].Label)

	empty := server.listTargets(listTargetsRequest{Categories: []string{""}, Sources: []string{""}, Tags: []string{""}})
	require.Len(t, empty.Targets, 3)
}

func TestCallTargetSupportsMethods(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Method))
	}))
	defer server.Close()

	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
		Targets: []config.HarpoonTarget{{
			Label:   "svc",
			BaseURL: mustParseURL(t, server.URL),
		}},
	}
	client := newTestServer(t, cfg)

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
		resp, err := client.callTarget(context.Background(), callTargetRequest{
			Label:  "svc",
			Method: method,
		})
		require.NoError(t, err)
		body := decodeBody(t, resp.BodyBase64)
		require.Equal(t, method, body)
	}
}

func TestCallTargetUsesProxy(t *testing.T) {
	targetCalled := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalled <- struct{}{}
		http.Error(w, "unexpected direct request", http.StatusBadGateway)
	}))
	defer target.Close()

	proxyCalled := make(chan struct{}, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalled <- struct{}{}
		_, _ = w.Write([]byte("proxied"))
	}))
	defer proxy.Close()

	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
		Targets: []config.HarpoonTarget{{
			Label:   "svc",
			BaseURL: mustParseURL(t, target.URL),
		}},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, cfg.AllowPlaintextHTTP, convertTargets(cfg.Targets))
	require.NoError(t, err)
	buffer := NewCallBuffer()
	proxyTransport, err := transport.ApplyProxy(transport.CloneDefault(), mustParseURL(t, proxy.URL))
	require.NoError(t, err)
	server, err := NewServer(cfg, registry, buffer, logger, WithHTTPTransport(proxyTransport))
	require.NoError(t, err)

	resp, err := server.callTarget(context.Background(), callTargetRequest{
		Label:  "svc",
		Method: http.MethodGet,
	})
	require.NoError(t, err)
	require.Equal(t, "proxied", decodeBody(t, resp.BodyBase64))

	select {
	case <-proxyCalled:
	default:
		t.Fatalf("expected proxy to receive request")
	}
	select {
	case <-targetCalled:
		t.Fatalf("expected target not to be called directly")
	default:
	}
}

func TestCallTargetRejectsInvalidMethod(t *testing.T) {
	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
		Targets: []config.HarpoonTarget{{
			Label:   "svc",
			BaseURL: mustParseURL(t, "http://example.com"),
		}},
	}
	client := newTestServer(t, cfg)

	_, err := client.callTarget(context.Background(), callTargetRequest{
		Label:  "svc",
		Method: "DELETE",
	})
	require.Error(t, err)
}

func TestCallTargetValidatesTimeouts(t *testing.T) {
	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
		Targets: []config.HarpoonTarget{{
			Label:   "svc",
			BaseURL: mustParseURL(t, "http://example.com"),
		}},
	}
	client := newTestServer(t, cfg)

	tooShort := 50
	_, err := client.callTarget(context.Background(), callTargetRequest{
		Label:     "svc",
		Method:    http.MethodGet,
		TimeoutMS: &tooShort,
	})
	require.Error(t, err)

	tooLong := 130000
	_, err = client.callTarget(context.Background(), callTargetRequest{
		Label:     "svc",
		Method:    http.MethodGet,
		TimeoutMS: &tooLong,
	})
	require.Error(t, err)
}

func TestCallTargetEnforcesSizeLimits(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(strings.Repeat("a", 20)))
	}))
	defer server.Close()

	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   10,
		MaxRedirects:       5,
		Targets: []config.HarpoonTarget{{
			Label:   "svc",
			BaseURL: mustParseURL(t, server.URL),
		}},
	}
	client := newTestServer(t, cfg)

	_, err := client.callTarget(context.Background(), callTargetRequest{
		Label:  "svc",
		Method: http.MethodGet,
	})
	require.Error(t, err)
	require.True(t, called)

	called = false
	body := strings.Repeat("b", 20)
	_, err = client.callTarget(context.Background(), callTargetRequest{
		Label:  "svc",
		Method: http.MethodPost,
		Body:   body,
	})
	require.Error(t, err)
	require.False(t, called)
}

func TestCallTargetRedirectHandling(t *testing.T) {
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer second.Close()

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+"/ok", http.StatusFound)
	}))
	defer primary.Close()

	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
		Targets: []config.HarpoonTarget{
			{Label: "primary", BaseURL: mustParseURL(t, primary.URL)},
			{Label: "second", BaseURL: mustParseURL(t, second.URL+"/ok")},
		},
	}
	client := newTestServer(t, cfg)

	resp, err := client.callTarget(context.Background(), callTargetRequest{
		Label:  "primary",
		Method: http.MethodGet,
	})
	require.NoError(t, err)
	require.Equal(t, "ok", decodeBody(t, resp.BodyBase64))

	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("blocked"))
	}))
	defer blocked.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, blocked.URL+"/escape", http.StatusFound)
	}))
	defer redirector.Close()

	blockedCfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
		Targets: []config.HarpoonTarget{{
			Label:   "primary",
			BaseURL: mustParseURL(t, redirector.URL),
		}},
	}
	client = newTestServer(t, blockedCfg)

	_, err = client.callTarget(context.Background(), callTargetRequest{
		Label:  "primary",
		Method: http.MethodGet,
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), blocked.URL)
}

func TestCallTargetRedirectLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusFound)
	}))
	defer server.Close()

	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
		Targets:            []config.HarpoonTarget{{Label: "loop", BaseURL: mustParseURL(t, server.URL)}},
	}
	client := newTestServer(t, cfg)

	maxRedirects := 1
	_, err := client.callTarget(context.Background(), callTargetRequest{
		Label:        "loop",
		Method:       http.MethodGet,
		MaxRedirects: &maxRedirects,
	})
	require.Error(t, err)
}

func TestIntegrationRedirectTruncationWithExactTargets(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/start":
			http.Redirect(w, r, "/api/large", http.StatusFound)
		case "/api/large":
			_, _ = w.Write([]byte(strings.Repeat("x", 50)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   10,
		MaxRedirects:       5,
		Targets: []config.HarpoonTarget{
			{Label: "svc-start", BaseURL: mustParseURL(t, server.URL+"/api/start")},
			{Label: "svc-large", BaseURL: mustParseURL(t, server.URL+"/api/large")},
		},
	}
	client := newTestServer(t, cfg)

	_, err := client.callTarget(context.Background(), callTargetRequest{
		Label:  "svc-start",
		Method: http.MethodGet,
	})
	require.Error(t, err)
	require.Contains(t, paths, "/api/start")
	require.Contains(t, paths, "/api/large")
}

func TestCallTargetPayloadCaptureDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
		CapturePayloads:    false,
		Targets: []config.HarpoonTarget{{
			Label:   "svc",
			BaseURL: mustParseURL(t, server.URL),
		}},
	}
	client := newTestServer(t, cfg)

	_, err := client.callTarget(context.Background(), callTargetRequest{
		Label:  "svc",
		Method: http.MethodPost,
		Body:   `{"hello":"world"}`,
	})
	require.NoError(t, err)

	snapshot := client.callBuffer.Snapshot(1, "svc")
	require.Len(t, snapshot, 1)
	require.Empty(t, snapshot[0].RequestBody)
	require.Empty(t, snapshot[0].ResponseBody)
	require.False(t, snapshot[0].BodyIsBase64)
}

func TestCallTargetPayloadCaptureEnabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{0xff, 0x00, 0x01})
	}))
	defer server.Close()

	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
		CapturePayloads:    true,
		Targets: []config.HarpoonTarget{{
			Label:   "svc",
			BaseURL: mustParseURL(t, server.URL),
		}},
	}
	client := newTestServer(t, cfg)

	_, err := client.callTarget(context.Background(), callTargetRequest{
		Label:  "svc",
		Method: http.MethodPost,
		Body:   `{"hello":"world"}`,
	})
	require.NoError(t, err)

	snapshot := client.callBuffer.Snapshot(1, "svc")
	require.Len(t, snapshot, 1)
	require.Equal(t, `{"hello":"world"}`, snapshot[0].RequestBody)
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte{0xff, 0x00, 0x01}), snapshot[0].ResponseBody)
	require.True(t, snapshot[0].BodyIsBase64)
}

func TestCallTargetFiltersHeadersAndSetsStableUserAgent(t *testing.T) {
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
		Targets: []config.HarpoonTarget{{
			Label:   "svc",
			BaseURL: mustParseURL(t, server.URL),
		}},
	}
	client := newTestServer(t, cfg)

	_, err := client.callTarget(context.Background(), callTargetRequest{
		Label:  "svc",
		Method: http.MethodPost,
		Body:   `{"hello":"world"}`,
		Headers: map[string]string{
			"Accept":        "application/json",
			"Authorization": "Bearer token",
			"Content-Type":  "application/json",
			"User-Agent":    "malicious-override",
			"X-Trace-Id":    "trace-123",
		},
	})
	require.NoError(t, err)

	require.Equal(t, "application/json", receivedHeaders.Get("Accept"))
	require.Equal(t, "Bearer token", receivedHeaders.Get("Authorization"))
	require.Equal(t, "application/json", receivedHeaders.Get("Content-Type"))
	require.Equal(t, version.UserAgent, receivedHeaders.Get("User-Agent"))
	require.Equal(t, "", receivedHeaders.Get("X-Trace-Id"))
}

func newTestServer(t *testing.T, cfg *config.HarpoonConfig) *Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, cfg.AllowPlaintextHTTP, convertTargets(cfg.Targets))
	require.NoError(t, err)
	buffer := NewCallBuffer()
	server, err := NewServer(cfg, registry, buffer, logger)
	require.NoError(t, err)
	return server
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	return parsed
}

func decodeBody(t *testing.T, encoded string) string {
	t.Helper()
	payload, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	return string(payload)
}
