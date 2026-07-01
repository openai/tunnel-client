package harpoon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/harpoon/internal/hostclassifier"
	"go.openai.org/api/tunnel-client/pkg/oauth"
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

func TestListTargetsGroupTagSerialization(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, true, []Target{
		{
			Label:       "oauth-prmd-auth-server-0",
			Description: "PRMD authorization server",
			Category:    "oauth",
			Source:      "oauth",
			Tags:        []string{"authorization-server", "protected-resource-metadata", "group=auth-server:0"},
			BaseURL:     mustParseURL(t, "https://auth.internal/prmd"),
		},
		{
			Label:       "oauth-auth-server-metadata-0",
			Description: "Auth server metadata URL",
			Category:    "oauth",
			Source:      "oauth",
			Tags:        []string{"auth-server-metadata", "group=auth-server:0"},
			BaseURL:     mustParseURL(t, "https://auth.internal/.well-known/oauth-authorization-server"),
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

	unfiltered := server.listTargets(listTargetsRequest{})
	unfilteredJSON, err := json.Marshal(unfiltered)
	require.NoError(t, err)
	require.JSONEq(t, `
	{
	  "targets": [
	    {
	      "label": "oauth-prmd-auth-server-0",
	      "description": "PRMD authorization server",
	      "category": "oauth",
	      "source": "oauth",
	      "tags": ["authorization-server", "group=auth-server:0", "protected-resource-metadata"],
	      "allowed_methods": ["GET", "POST", "PUT"]
	    },
	    {
	      "label": "oauth-auth-server-metadata-0",
	      "description": "Auth server metadata URL",
	      "category": "oauth",
	      "source": "oauth",
	      "tags": ["auth-server-metadata", "group=auth-server:0"],
	      "allowed_methods": ["GET", "POST", "PUT"]
	    }
	  ]
	}`,
		string(unfilteredJSON),
	)

	filtered := server.listTargets(listTargetsRequest{Tags: []string{"group=auth-server:0"}})
	filteredJSON, err := json.Marshal(filtered)
	require.NoError(t, err)
	require.JSONEq(t, `
	{
	  "targets": [
	    {
	      "label": "oauth-prmd-auth-server-0",
	      "description": "PRMD authorization server",
	      "category": "oauth",
	      "source": "oauth",
	      "tags": ["authorization-server", "group=auth-server:0", "protected-resource-metadata"],
	      "allowed_methods": ["GET", "POST", "PUT"]
	    },
	    {
	      "label": "oauth-auth-server-metadata-0",
	      "description": "Auth server metadata URL",
	      "category": "oauth",
	      "source": "oauth",
	      "tags": ["auth-server-metadata", "group=auth-server:0"],
	      "allowed_methods": ["GET", "POST", "PUT"]
	    }
	  ]
	}`,
		string(filteredJSON),
	)
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

func TestCallTargetSupportsHTTPAndUnixSocket(t *testing.T) {
	t.Parallel()

	t.Run("HTTP", func(t *testing.T) {
		t.Parallel()
		assertCallTargetTransport(t, newHarpoonHTTPServer(t), "")
	})

	t.Run("UnixSocket", func(t *testing.T) {
		t.Parallel()

		socketPath := shortSocketPath(t, "harpoon-callout-*.sock")
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			t.Skipf("unix socket unavailable: %v", err)
		}
		server := httptest.NewUnstartedServer(harpoonCalloutHandler(t))
		server.Listener = listener
		server.Start()
		t.Cleanup(server.Close)

		assertCallTargetTransport(t, "http://harpoon-http-callout-fixture", socketPath)
	})
}

func TestCallTargetUsesDiscoveredOAuthUnixSocket(t *testing.T) {
	t.Parallel()

	socketPath := shortSocketPath(t, "harpoon-oauth-*.sock")
	const (
		logicalBaseURL = "http://localhost"
		variant        = "dcr10"
		resourceURL    = logicalBaseURL + "/mcp/" + variant
		prmdURL        = logicalBaseURL + "/.well-known/oauth-protected-resource/mcp/" + variant
		authServerURL  = logicalBaseURL + "/" + variant
	)
	tokenCalls := 0
	newHarpoonUnixServer(t, socketPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "localhost", r.Host)
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server/" + variant:
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 "https://external-idp.example.com/oauth2/aus2jrb9zi4O8hseE0h8",
				"authorization_endpoint": authServerURL + "/authorize",
				"token_endpoint":         authServerURL + "/token",
				"registration_endpoint":  authServerURL + "/register",
				"revocation_endpoint":    authServerURL + "/revoke",
			}))
		case "/dcr10/token":
			tokenCalls++
			_, _ = w.Write([]byte(r.Method + " " + r.URL.Path))
		default:
			http.NotFound(w, r)
		}
	}))

	baseTransport, err := transport.ApplyUnixSocketPath(http.DefaultTransport, socketPath)
	require.NoError(t, err)
	bundle, _, err := oauth.BuildURLBundleFromPRMDWithAuthServerMetadata(
		context.Background(),
		&http.Client{Transport: baseTransport},
		[]byte(`{"resource":"`+resourceURL+`","authorization_servers":["`+authServerURL+`"]}`),
		time.Unix(42, 0).UTC(),
		mustParseURL(t, prmdURL),
		oauth.URLBundleOptions{
			UnixSocketPath: socketPath,
			UnixSocketURL:  mustParseURL(t, resourceURL),
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, true, nil)
	require.NoError(t, err)
	classifier := hostclassifier.NewHostClassifier(config.HarpoonHostClassifierConfig{
		IncludeSuffix:  []string{"localhost"},
		IncludePrivate: false,
	})
	require.NoError(t, registerHostBundle(bundle, classifier, registry, logger))
	for _, label := range []string{
		"oauth-auth-server-metadata-0",
		"oauth-registration-endpoint-0",
		"oauth-token-endpoint-0",
	} {
		target, ok := registry.Lookup(label)
		require.Truef(t, ok, "expected OAuth target %q", label)
		require.Equal(t, socketPath, target.UnixSocketPath)
	}

	server, err := NewServer(&config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
	}, registry, NewCallBuffer(), logger)
	require.NoError(t, err)
	resp, err := server.callTarget(context.Background(), callTargetRequest{
		Label:  "oauth-token-endpoint-0",
		Method: http.MethodPost,
	})
	require.NoError(t, err)
	require.Equal(t, "POST /dcr10/token", decodeBody(t, resp.BodyBase64))
	require.Equal(t, 1, tokenCalls)
}

func TestCallTargetSelectsTransportPerRedirectedTarget(t *testing.T) {
	t.Parallel()

	t.Run("HTTPToUnixSocket", func(t *testing.T) {
		t.Parallel()

		socketPath := shortSocketPath(t, "harpoon-redirect-*.sock")
		unixURL := "http://harpoon-unix-target/callout"
		newHarpoonUnixServer(t, socketPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/callout", r.URL.Path)
			_, _ = w.Write([]byte("unix"))
		}))
		httpRedirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, unixURL, http.StatusFound)
		}))
		t.Cleanup(httpRedirect.Close)

		client := newTestServer(t, &config.HarpoonConfig{
			AllowPlaintextHTTP: true,
			MaxResponseBytes:   1024,
			MaxRedirects:       5,
			Targets: []config.HarpoonTarget{
				{Label: "http", BaseURL: mustParseURL(t, httpRedirect.URL)},
				{Label: "unix", BaseURL: mustParseURL(t, unixURL), UnixSocketPath: socketPath},
			},
		})
		for range 2 {
			resp, err := client.callTarget(context.Background(), callTargetRequest{
				Label:           "http",
				Method:          http.MethodGet,
				FollowRedirects: boolPtr(true),
			})
			require.NoError(t, err)
			require.Equal(t, "unix", decodeBody(t, resp.BodyBase64))
		}
		require.Len(t, client.unixBySocket, 1)
	})

	t.Run("UnixSocketToHTTP", func(t *testing.T) {
		t.Parallel()

		httpFinal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/callout", r.URL.Path)
			_, _ = w.Write([]byte("http"))
		}))
		t.Cleanup(httpFinal.Close)
		socketPath := shortSocketPath(t, "harpoon-redirect-*.sock")
		unixURL := "http://harpoon-unix-target/callout"
		newHarpoonUnixServer(t, socketPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, httpFinal.URL+"/callout", http.StatusFound)
		}))

		client := newTestServer(t, &config.HarpoonConfig{
			AllowPlaintextHTTP: true,
			MaxResponseBytes:   1024,
			MaxRedirects:       5,
			Targets: []config.HarpoonTarget{
				{Label: "unix", BaseURL: mustParseURL(t, unixURL), UnixSocketPath: socketPath},
				{Label: "http", BaseURL: mustParseURL(t, httpFinal.URL+"/callout")},
			},
		})
		for range 2 {
			resp, err := client.callTarget(context.Background(), callTargetRequest{
				Label:           "unix",
				Method:          http.MethodGet,
				FollowRedirects: boolPtr(true),
			})
			require.NoError(t, err)
			require.Equal(t, "http", decodeBody(t, resp.BodyBase64))
		}
		require.Len(t, client.unixBySocket, 1)
	})
}

func TestCallTargetKeepsURLsWithoutRegisteredTargets(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/meta":
			_, _ = w.Write([]byte(`{
				"authorization_endpoint":"` + server.URL + `/authorize",
				"registration_endpoint":"` + server.URL + `/register",
				"token_endpoint":"` + server.URL + `/token"
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, true, []Target{
		{
			Label:   "metadata",
			BaseURL: mustParseURL(t, server.URL+"/meta"),
		},
		{
			Label:   "token",
			BaseURL: mustParseURL(t, server.URL+"/token"),
		},
		{
			Label:   "oauth-registration-endpoint-0",
			BaseURL: mustParseURL(t, server.URL+"/register"),
		},
	})
	require.NoError(t, err)

	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
	}
	client, err := NewServer(cfg, registry, NewCallBuffer(), logger)
	require.NoError(t, err)

	resp, err := client.callTarget(context.Background(), callTargetRequest{
		Label:  "metadata",
		Method: http.MethodGet,
	})
	require.NoError(t, err)

	decodedBody := decodeBody(t, resp.BodyBase64)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(decodedBody), &payload))
	require.Equal(t, server.URL+"/authorize", payload["authorization_endpoint"])
	require.Equal(t, "harpoon://oauth-registration-endpoint-0", payload["registration_endpoint"])
	require.Equal(t, "harpoon://token", payload["token_endpoint"])
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

func TestCallTargetRedirectSchemeMismatchIncludesExplicitMessage(t *testing.T) {
	metadataTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/.well-known/oauth-authorization-server", http.StatusFound)
	}))
	defer metadataTarget.Close()

	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
		Targets: []config.HarpoonTarget{
			{Label: "primary", BaseURL: mustParseURL(t, metadataTarget.URL)},
			{Label: "oauth-auth-server-metadata-0", BaseURL: mustParseURL(t, "https://example.com/.well-known/oauth-authorization-server/")},
		},
	}
	client := newTestServer(t, cfg)

	_, err := client.callTarget(context.Background(), callTargetRequest{
		Label:  "primary",
		Method: http.MethodGet,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "redirect blocked: scheme mismatch (allowlisted https, redirected to http)")
}

func TestCallTargetRedirectHostMismatchStaysGeneric(t *testing.T) {
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("blocked"))
	}))
	defer blocked.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, blocked.URL+"/escape", http.StatusFound)
	}))
	defer redirector.Close()

	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
		Targets: []config.HarpoonTarget{{
			Label:   "primary",
			BaseURL: mustParseURL(t, redirector.URL),
		}},
	}
	client := newTestServer(t, cfg)

	_, err := client.callTarget(context.Background(), callTargetRequest{
		Label:  "primary",
		Method: http.MethodGet,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "redirect blocked")
	require.NotContains(t, err.Error(), "scheme mismatch")
	require.NotContains(t, err.Error(), "host mismatch")
}

func TestCallTargetRedirectMismatchFieldsAreLogged(t *testing.T) {
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))
	metadataTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/.well-known/oauth-authorization-server", http.StatusFound)
	}))
	defer metadataTarget.Close()

	registry, err := NewRegistry(logger, true, []Target{
		{Label: "primary", BaseURL: mustParseURL(t, metadataTarget.URL)},
		{Label: "oauth-auth-server-metadata-0", BaseURL: mustParseURL(t, "https://example.com/.well-known/oauth-authorization-server/")},
	})
	require.NoError(t, err)

	server, err := NewServer(&config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
	}, registry, NewCallBuffer(), logger)
	require.NoError(t, err)

	_, err = server.callTarget(context.Background(), callTargetRequest{
		Label:  "primary",
		Method: http.MethodGet,
	})
	require.Error(t, err)

	logOutput := logBuffer.String()
	require.Contains(t, logOutput, "msg=\"harpoon request failed\"")
	require.Contains(t, logOutput, "redirect_mismatch_kind=scheme_mismatch_https_to_http")
	require.Contains(t, logOutput, "redirect_expected_url=https://example.com/.well-known/oauth-authorization-server/")
	require.Contains(t, logOutput, "redirect_expected_scheme=https")
	require.Contains(t, logOutput, "redirect_actual_scheme=http")
	require.Contains(t, logOutput, "redirect_reason=\"redirect target not in allow list\"")
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

func TestCallTargetSanitizesHeadersAndSetsStableUserAgent(t *testing.T) {
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "Application/JSON; charset=utf-8")
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
			"Accept":              "application/json",
			"Authorization":       "Bearer token",
			"Content-Type":        "application/json",
			"Cookie":              "session=secret",
			"Forwarded":           "for=10.0.0.1;proto=https",
			"True-Client-IP":      "10.0.0.2",
			"User-Agent":          "malicious-override",
			"X-API-Key":           "secret",
			"X-LiteLLM-API-Key":   "upstream-secret",
			"X-OpenAI-Skip-Auth":  "1",
			"X-Trace-Id":          "trace-123",
			"X-Forwarded-For":     "10.0.0.1",
			"Connection":          "keep-alive",
			"Proxy-Authorization": "Basic secret",
		},
	})
	require.NoError(t, err)

	require.Equal(t, "application/json", receivedHeaders.Get("Accept"))
	require.Equal(t, "Bearer token", receivedHeaders.Get("Authorization"))
	require.Equal(t, "application/json", receivedHeaders.Get("Content-Type"))
	require.Equal(t, version.UserAgent, receivedHeaders.Get("User-Agent"))
	require.Equal(t, "", receivedHeaders.Get("Cookie"))
	require.Equal(t, "", receivedHeaders.Get("Forwarded"))
	require.Equal(t, "", receivedHeaders.Get("True-Client-IP"))
	require.Equal(t, "", receivedHeaders.Get("X-Forwarded-For"))
	require.Equal(t, "", receivedHeaders.Get("X-OpenAI-Skip-Auth"))
	require.Equal(t, "", receivedHeaders.Get("Connection"))
	require.Equal(t, "", receivedHeaders.Get("Proxy-Authorization"))
	require.Equal(t, "secret", receivedHeaders.Get("X-API-Key"))
	require.Equal(t, "upstream-secret", receivedHeaders.Get("X-LiteLLM-API-Key"))
	require.Equal(t, "trace-123", receivedHeaders.Get("X-Trace-Id"))

	snapshot := client.callBuffer.Snapshot(1, "svc")
	require.Len(t, snapshot, 1)
	require.Equal(t, "application/json", snapshot[0].ResponseContentType)
}

func TestFilterOutboundHeadersReportsLowCardinalityDrops(t *testing.T) {
	t.Parallel()

	headers, dropped, classifications := filterOutboundHeaders(map[string]string{
		"Accept":              "application/json",
		"Authorization":       "Bearer token",
		"Content-Type":        "application/json",
		"Cookie":              "session=secret",
		"Forwarded":           "for=10.0.0.1;proto=https",
		"True-Client-IP":      "10.0.0.2",
		"X-Forwarded-For":     "10.0.0.1",
		"X-OpenAI-Skip-Auth":  "1",
		"X-Trace-Id":          "trace",
		"X-API-Key":           "secret",
		"Connection":          "keep-alive",
		"Host":                "internal.example",
		"Proxy-Authorization": "Basic secret",
		"Transfer-Encoding":   "chunked",
	})

	require.Equal(t, "application/json", headers.Get("Accept"))
	require.Equal(t, "Bearer token", headers.Get("Authorization"))
	require.Equal(t, "application/json", headers.Get("Content-Type"))
	require.Equal(t, "", headers.Get("Forwarded"))
	require.Equal(t, "", headers.Get("X-Forwarded-For"))
	require.Equal(t, "", headers.Get("X-OpenAI-Skip-Auth"))
	require.Equal(t, "trace", headers.Get("X-Trace-Id"))
	require.Equal(t, "secret", headers.Get("X-API-Key"))
	require.Equal(t, 9, dropped)
	require.Equal(t, []string{"custom", "not-forwardable", "sensitive-name"}, classifications)
}

func TestIsBlockedOutboundHeaderBlocksSpoofingAndRelayHeaders(t *testing.T) {
	t.Parallel()

	for _, headerName := range []string{
		"Connection",
		"Content-Length",
		"Cookie",
		"Forwarded",
		"Host",
		"Proxy-Authorization",
		"True-Client-IP",
		"Transfer-Encoding",
		"User-Agent",
		"Via",
		"X-Forwarded-For",
		"X-Forwarded-Host",
		"X-OpenAI-Authorization",
		"X-OpenAI-Skip-Auth",
		"X-Real-IP",
	} {
		require.True(t, isBlockedOutboundHeader(headerName), headerName)
	}

	for _, headerName := range []string{
		"Accept",
		"Authorization",
		"Content-Type",
		"X-API-Key",
		"X-Discovery-Auth",
		"X-LiteLLM-API-Key",
		"X-Trace-Id",
	} {
		require.False(t, isBlockedOutboundHeader(headerName), headerName)
	}
}

func TestResponseContentTypeForLogTruncatesLongValues(t *testing.T) {
	t.Parallel()

	got := responseContentTypeForLog("text/" + strings.Repeat("a", maxContentTypeLogBytes*2))

	require.LessOrEqual(t, len(got), maxContentTypeLogBytes)
	require.True(t, strings.HasPrefix(got, "text/"))
	require.True(t, utf8.ValidString(got))
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

func newHarpoonHTTPServer(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(harpoonCalloutHandler(t))
	t.Cleanup(server.Close)
	return server.URL
}

func newHarpoonUnixServer(t *testing.T, socketPath string, handler http.Handler) {
	t.Helper()
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("unix socket unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
}

func assertCallTargetTransport(t *testing.T, baseURL, unixSocketPath string) {
	t.Helper()
	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
		Targets: []config.HarpoonTarget{{
			Label:          "svc",
			BaseURL:        mustParseURL(t, baseURL+"/callout"),
			UnixSocketPath: unixSocketPath,
		}},
	}
	client := newTestServer(t, cfg)
	resp, err := client.callTarget(context.Background(), callTargetRequest{
		Label:  "svc",
		Method: http.MethodGet,
	})
	require.NoError(t, err)
	require.Equal(t, "GET /callout", decodeBody(t, resp.BodyBase64))
}

func boolPtr(value bool) *bool {
	return &value
}

func harpoonCalloutHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/callout", r.URL.Path)
		_, _ = w.Write([]byte(r.Method + " " + r.URL.Path))
	})
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
