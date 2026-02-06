package proxy

import (
	"net/url"
	"testing"

	"go.openai.org/api/tunnel-client/pkg/config"
)

func TestResolveRouteDirect(t *testing.T) {
	target := mustParseURL(t, "https://example.com")
	route := ResolveRoute(RouteKindControlPlane, "control-plane", target, nil, config.ProxySourceNone, lookupEnvMap(nil))
	if route.RouteMode != RouteModeDirect {
		t.Fatalf("expected direct route, got %s", route.RouteMode)
	}
	if route.ProxySource != config.ProxySourceNone {
		t.Fatalf("expected proxy source none, got %s", route.ProxySource)
	}
}

func TestResolveRouteEnvProxy(t *testing.T) {
	lookup := lookupEnvMap(map[string]string{
		"HTTPS_PROXY": "http://proxy.example:8080",
	})
	target := mustParseURL(t, "https://example.com")
	route := ResolveRoute(RouteKindControlPlane, "control-plane", target, nil, config.ProxySourceNone, lookup)
	if route.RouteMode != RouteModeProxy {
		t.Fatalf("expected proxy route, got %s", route.RouteMode)
	}
	if route.ProxySource != config.ProxySource("env:HTTPS_PROXY") {
		t.Fatalf("unexpected proxy source: %s", route.ProxySource)
	}
	if route.ProxyID == "" {
		t.Fatalf("expected proxy id to be set")
	}
	if route.ProxyURLRedact == "" {
		t.Fatalf("expected proxy URL to be set")
	}
}

func TestResolveRouteNoProxyMatch(t *testing.T) {
	lookup := lookupEnvMap(map[string]string{
		"HTTPS_PROXY": "http://proxy.example:8080",
		"NO_PROXY":    "example.com",
	})
	target := mustParseURL(t, "https://example.com")
	route := ResolveRoute(RouteKindControlPlane, "control-plane", target, nil, config.ProxySourceNone, lookup)
	if route.RouteMode != RouteModeDirect {
		t.Fatalf("expected direct route, got %s", route.RouteMode)
	}
	if route.ProxySource != config.ProxySourceNone {
		t.Fatalf("expected proxy source none, got %s", route.ProxySource)
	}
}

func lookupEnvMap(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		if values == nil {
			return "", false
		}
		val, ok := values[key]
		return val, ok
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return parsed
}
