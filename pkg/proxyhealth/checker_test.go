package proxyhealth

import (
	"bufio"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/proxy"
	"go.openai.org/api/tunnel-client/pkg/tlsconfig"
)

func TestRecordResultHistoryRetention(t *testing.T) {
	checker, route := newTestChecker(t)
	for i := 0; i < maxHistoryEntries+2; i++ {
		record := CheckRecord{Timestamp: time.Now().Add(time.Duration(i) * time.Second)}
		checker.recordResult(route, record, i%2 == 0)
	}
	summaries := checker.HealthSummaries()
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if len(summaries[0].History) != maxHistoryEntries {
		t.Fatalf("expected %d history entries, got %d", maxHistoryEntries, len(summaries[0].History))
	}
}

func TestRecordResultStateTransitions(t *testing.T) {
	checker, route := newTestChecker(t)
	checker.recordResult(route, CheckRecord{Timestamp: time.Now()}, false)
	state := checker.HealthSummaries()[0].HealthState
	if state != string(HealthStateUnhealthy) {
		t.Fatalf("expected unhealthy, got %s", state)
	}
	checker.recordResult(route, CheckRecord{Timestamp: time.Now()}, true)
	state = checker.HealthSummaries()[0].HealthState
	if state != string(HealthStateHealthy) {
		t.Fatalf("expected healthy, got %s", state)
	}
}

func TestHealthSummariesReturnsDeterministicRouteOrder(t *testing.T) {
	proxyURL := mustParseURL(t, "http://proxy.example:8080")
	targetURL := mustParseURL(t, "https://example.com")
	controlPlaneRoute := proxy.ResolveRoute(proxy.RouteKindControlPlane, "control-plane", targetURL, proxyURL, config.ProxySource("flag"), lookupEnvMap(nil))
	mcpRoute := proxy.ResolveRoute(proxy.RouteKindMCPChannel, "alpha", targetURL, proxyURL, config.ProxySource("flag"), lookupEnvMap(nil))
	checker := &Checker{
		routes: []proxy.Route{mcpRoute, controlPlaneRoute},
		routeStatus: map[string]*routeStatus{
			routeKey(mcpRoute):          {route: mcpRoute, healthState: HealthStateHealthy},
			routeKey(controlPlaneRoute): {route: controlPlaneRoute, healthState: HealthStateUnhealthy},
		},
	}

	for i := 0; i < 100; i++ {
		summaries := checker.HealthSummaries()
		if len(summaries) != 2 {
			t.Fatalf("expected 2 summaries, got %d", len(summaries))
		}
		if summaries[0].Route.Kind != string(proxy.RouteKindControlPlane) || summaries[1].Route.Kind != string(proxy.RouteKindMCPChannel) {
			t.Fatalf("HealthSummaries returned nondeterministic order: %#v", summaries)
		}
	}
}

func TestConnectThroughProxyIncludesProxyAuthorization(t *testing.T) {
	t.Parallel()

	clientConn, proxyConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = proxyConn.Close()
	})

	requestLines := serveConnectRequest(proxyConn, "HTTP/1.1 200 Connection established\r\n\r\n")

	proxyURL := mustParseURL(t, "http://alice:wonderland@proxy.example:8080")
	duration, category, err := connectThroughProxyWithTLSConfig(clientConn, proxyURL, "api.example.com:443", time.Second, nil)
	if err != nil {
		t.Fatalf("connectThroughProxy returned error: %v", err)
	}
	if category != "2xx" {
		t.Fatalf("status category = %q, want %q", category, "2xx")
	}
	if duration <= 0 {
		t.Fatalf("duration = %v, want > 0", duration)
	}

	rawRequest := waitForRequest(t, requestLines)
	if !strings.Contains(rawRequest, "CONNECT api.example.com:443 HTTP/1.1\r\n") {
		t.Fatalf("missing CONNECT request line: %q", rawRequest)
	}
	encodedCreds := base64.StdEncoding.EncodeToString([]byte("alice:wonderland"))
	wantHeader := "Proxy-Authorization: Basic " + encodedCreds + "\r\n"
	if !strings.Contains(rawRequest, wantHeader) {
		t.Fatalf("missing proxy authorization header: got %q want to contain %q", rawRequest, wantHeader)
	}
}

func TestConnectThroughHTTPSProxyEncryptsProxyAuthorization(t *testing.T) {
	t.Parallel()

	requests := make(chan *http.Request, 1)
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(proxy.Close)

	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial HTTPS proxy: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	proxyURL := mustParseURL(t, "https://alice:wonderland@"+proxy.Listener.Addr().String())
	transport, ok := proxy.Client().Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil {
		t.Fatalf("TLS test transport missing client TLS config: %T", proxy.Client().Transport)
	}
	bundle := &tlsconfig.Bundle{RootCAs: transport.TLSClientConfig.RootCAs}
	tlsConfig := proxyTLSConfig(bundle)
	if tlsConfig == nil || tlsConfig.RootCAs != bundle.RootCAs {
		t.Fatal("proxyTLSConfig did not preserve the configured CA bundle")
	}

	duration, category, err := connectThroughProxyWithTLSConfig(
		conn,
		proxyURL,
		"api.example.com:443",
		time.Second,
		tlsConfig,
	)
	if err != nil {
		t.Fatalf("connectThroughProxyWithTLSConfig returned error: %v", err)
	}
	if category != "2xx" {
		t.Fatalf("status category = %q, want %q", category, "2xx")
	}
	if duration <= 0 {
		t.Fatalf("duration = %v, want > 0", duration)
	}

	select {
	case req := <-requests:
		if req.Method != http.MethodConnect {
			t.Fatalf("method = %q, want %q", req.Method, http.MethodConnect)
		}
		encodedCreds := base64.StdEncoding.EncodeToString([]byte("alice:wonderland"))
		if got, want := req.Header.Get("Proxy-Authorization"), "Basic "+encodedCreds; got != want {
			t.Fatalf("Proxy-Authorization = %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for HTTPS CONNECT request")
	}
}

func TestConnectThroughProxyReturnsStatusCategoryForErrors(t *testing.T) {
	t.Parallel()

	clientConn, proxyConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = proxyConn.Close()
	})

	serveConnectRequest(proxyConn, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n")

	_, category, err := connectThroughProxyWithTLSConfig(clientConn, nil, "api.example.com:443", time.Second, nil)
	if err == nil {
		t.Fatal("expected error for 4xx CONNECT response")
	}
	if category != "4xx" {
		t.Fatalf("status category = %q, want %q", category, "4xx")
	}
}

func TestConnectThroughProxyOmitsProxyAuthorizationWithoutCredentials(t *testing.T) {
	t.Parallel()

	clientConn, proxyConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = proxyConn.Close()
	})

	requestLines := serveConnectRequest(proxyConn, "HTTP/1.1 200 Connection established\r\n\r\n")

	proxyURL := mustParseURL(t, "http://proxy.example:8080")
	_, category, err := connectThroughProxyWithTLSConfig(clientConn, proxyURL, "api.example.com:443", time.Second, nil)
	if err != nil {
		t.Fatalf("connectThroughProxy returned error: %v", err)
	}
	if category != "2xx" {
		t.Fatalf("status category = %q, want %q", category, "2xx")
	}

	rawRequest := waitForRequest(t, requestLines)
	if strings.Contains(rawRequest, "Proxy-Authorization:") {
		t.Fatalf("unexpected proxy authorization header in request: %q", rawRequest)
	}
}

func serveConnectRequest(proxyConn net.Conn, response string) <-chan string {
	requestLines := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(proxyConn)
		var builder strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			builder.WriteString(line)
			if line == "\r\n" {
				break
			}
		}
		requestLines <- builder.String()
		_, _ = proxyConn.Write([]byte(response))
	}()
	return requestLines
}

func waitForRequest(t *testing.T, requestLines <-chan string) string {
	t.Helper()

	select {
	case rawRequest := <-requestLines:
		return rawRequest
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for CONNECT request")
		return ""
	}
}

func newTestChecker(t *testing.T) (*Checker, proxy.Route) {
	t.Helper()
	proxyURL := mustParseURL(t, "http://proxy.example:8080")
	targetURL := mustParseURL(t, "https://example.com")
	route := proxy.ResolveRoute(proxy.RouteKindControlPlane, "control-plane", targetURL, proxyURL, config.ProxySource("flag"), lookupEnvMap(nil))
	checker := &Checker{
		routes:      []proxy.Route{route},
		routeStatus: map[string]*routeStatus{routeKey(route): {route: route, healthState: HealthStateUnhealthy}},
	}
	return checker, route
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
