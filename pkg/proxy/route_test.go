package proxy

import (
	"maps"
	"net/url"
	"testing"

	"github.com/openai/tunnel-client/pkg/config"
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
	t.Parallel()

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
	t.Parallel()

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

func TestResolveRoutePrefersExplicitProxyOverEnv(t *testing.T) {
	t.Parallel()

	lookup := lookupEnvMap(map[string]string{
		"HTTPS_PROXY": "http://env-proxy.example:8080",
	})
	target := mustParseURL(t, "https://example.com")
	explicitProxy := mustParseURL(t, "http://flag-proxy.example:8443")

	route := ResolveRoute(
		RouteKindControlPlane,
		"control-plane",
		target,
		explicitProxy,
		config.ProxySource("flag"),
		lookup,
	)

	if route.ProxySource != config.ProxySource("flag") {
		t.Fatalf("proxy source = %s, want %s", route.ProxySource, config.ProxySource("flag"))
	}
	if route.ProxyHostPort != "flag-proxy.example:8443" {
		t.Fatalf("proxy host:port = %q, want %q", route.ProxyHostPort, "flag-proxy.example:8443")
	}
}

func TestResolveRouteIgnoresInvalidEnvProxy(t *testing.T) {
	t.Parallel()

	lookup := lookupEnvMap(map[string]string{
		"HTTPS_PROXY": "://bad-proxy",
	})
	target := mustParseURL(t, "https://example.com")
	route := ResolveRoute(RouteKindControlPlane, "control-plane", target, nil, config.ProxySourceNone, lookup)

	if route.RouteMode != RouteModeDirect {
		t.Fatalf("route mode = %s, want %s", route.RouteMode, RouteModeDirect)
	}
	if route.ProxySource != config.ProxySourceNone {
		t.Fatalf("proxy source = %s, want %s", route.ProxySource, config.ProxySourceNone)
	}
}

func TestEnvProxyValueHTTPSPrecedence(t *testing.T) {
	t.Parallel()

	lookup := lookupEnvMap(map[string]string{
		"http_proxy":  "http://lower-http.example:8080",
		"HTTP_PROXY":  "http://upper-http.example:8080",
		"https_proxy": "http://lower-https.example:8080",
		"HTTPS_PROXY": "http://upper-https.example:8080",
	})

	value, source := envProxyValue("https", lookup)
	if value != "http://upper-https.example:8080" {
		t.Fatalf("value = %q, want %q", value, "http://upper-https.example:8080")
	}
	if source != "HTTPS_PROXY" {
		t.Fatalf("source = %q, want %q", source, "HTTPS_PROXY")
	}
}

func TestProxyBypassHost(t *testing.T) {
	t.Parallel()

	target := mustParseURL(t, "https://api.example.com:443")
	testCases := []struct {
		name    string
		noProxy string
		want    bool
	}{
		{name: "wildcard", noProxy: "*", want: true},
		{name: "exact host", noProxy: "api.example.com", want: true},
		{name: "suffix match", noProxy: ".example.com", want: true},
		{name: "exact host and port", noProxy: "api.example.com:443", want: true},
		{name: "wrong port", noProxy: "api.example.com:444", want: false},
		{name: "no match", noProxy: "internal.example.com", want: false},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := proxyBypassHost(tc.noProxy, target); got != tc.want {
				t.Fatalf("proxyBypassHost(%q) = %v, want %v", tc.noProxy, got, tc.want)
			}
		})
	}
}

func TestBuildIdentityMapDeduplicates(t *testing.T) {
	t.Parallel()

	routes := []Route{
		{
			ProxyID:        "id-1",
			ProxyURLRedact: "http://proxy-1.example:8080",
			ProxySource:    config.ProxySource("flag"),
		},
		{
			ProxyID:        "id-1",
			ProxyURLRedact: "http://proxy-1.example:8080",
			ProxySource:    config.ProxySourceEnvironment,
		},
		{
			ProxyID:        "id-2",
			ProxyURLRedact: "http://proxy-2.example:8080",
			ProxySource:    config.ProxySource("env:HTTPS_PROXY"),
		},
	}

	got := BuildIdentityMap(routes)
	if len(got) != 2 {
		t.Fatalf("len(identityMap) = %d, want 2", len(got))
	}

	gotByID := make(map[string]IdentityRecord, len(got))
	for _, record := range got {
		gotByID[record.ProxyID] = record
	}

	wantByID := map[string]IdentityRecord{
		"id-1": {ProxyID: "id-1", ProxyURL: "http://proxy-1.example:8080", ProxySource: config.ProxySource("flag").String()},
		"id-2": {ProxyID: "id-2", ProxyURL: "http://proxy-2.example:8080", ProxySource: config.ProxySource("env:HTTPS_PROXY").String()},
	}
	if !maps.Equal(gotByID, wantByID) {
		t.Fatalf("identity map mismatch: got=%v want=%v", gotByID, wantByID)
	}
}

func TestNormalizeProxyURL(t *testing.T) {
	t.Parallel()

	proxyURL := mustParseURL(t, "HTTP://User:pass@Proxy.Example.COM:8080/path?x=1")
	if got := NormalizeProxyURL(proxyURL); got != "http://proxy.example.com:8080" {
		t.Fatalf("NormalizeProxyURL() = %q, want %q", got, "http://proxy.example.com:8080")
	}
}

func TestHostPortForURL(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "https default port", raw: "https://example.com", want: "example.com:443"},
		{name: "http default port", raw: "http://example.com", want: "example.com:80"},
		{name: "explicit port", raw: "https://example.com:8443", want: "example.com:8443"},
	}
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := mustParseURL(t, tc.raw)
			if got := HostPortForURL(target); got != tc.want {
				t.Fatalf("HostPortForURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
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
