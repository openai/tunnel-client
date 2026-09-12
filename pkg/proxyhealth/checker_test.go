package proxyhealth

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/harpoon"
	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/proxy"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
)

func TestTemplateOnlyProxyHealthChecksOriginWithConnect(t *testing.T) {
	for _, useRegistry := range []bool{true, false} {
		t.Run(fmt.Sprintf("shared_registry_%t", useRegistry), func(t *testing.T) {
			const (
				targetCredential = "Bearer private-target-credential"
				privateRoute     = "/private-resources/{privateID}"
			)
			var targetRequests atomic.Int32
			targetServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				targetRequests.Add(1)
			}))
			t.Cleanup(targetServer.Close)
			type proxyRequest struct {
				method, target, host string
				headers              http.Header
				trailing             []byte
				err                  error
			}
			requests := make(chan proxyRequest, 2)
			var responseStatus atomic.Int32
			responseStatus.Store(http.StatusOK)
			proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				observed := proxyRequest{method: r.Method, target: r.RequestURI, host: r.Host, headers: r.Header.Clone()}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					observed.err = err
					requests <- observed
					return
				}
				defer func() { _ = conn.Close() }()
				status := int(responseStatus.Load())
				_, observed.err = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\n\r\n", status, http.StatusText(status))
				if observed.err == nil {
					// The checker closes immediately after CONNECT. Receiving no
					// additional bytes proves it sent neither TLS nor a GET call.
					observed.trailing, observed.err = io.ReadAll(conn)
				}
				requests <- observed
			}))
			t.Cleanup(proxyServer.Close)
			template := &runtimeconfig.HarpoonTargetTemplate{
				Version: 1, Origin: targetServer.URL, Method: http.MethodGet,
				PathTemplate: privateRoute,
				Parameters: map[string]runtimeconfig.HarpoonTemplateParameter{
					"privateID": {Type: "string", Required: true, Pattern: "[A-Za-z0-9_-]+", MaxLength: 64},
				},
				Headers: map[string]string{"Authorization": targetCredential},
			}
			cfg := &config.HarpoonConfig{
				HTTPProxy: mustParseURL(t, proxyServer.URL), HTTPProxySource: config.ProxySource("flag"),
				Targets: []config.HarpoonTarget{{Label: "private-resource", Template: template}},
			}
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			var registry *harpoon.Registry
			if useRegistry {
				var err error
				registry, err = harpoon.NewRegistry(logger, false, convertConfigTargets(cfg.Targets))
				if err != nil {
					t.Fatal(err)
				}
			}
			routes, err := buildRoutes(nil, nil, cfg, registry, logger, lookupEnvMap(nil))
			if err != nil {
				t.Fatal(err)
			}
			if len(routes) != 1 {
				t.Fatalf("template-only proxy routes = %d, want 1", len(routes))
			}
			route := routes[0]
			if route.RouteMode != proxy.RouteModeProxy || route.TargetHostPort != targetServer.Listener.Addr().String() {
				t.Fatalf("template route did not preserve proxied fixed origin: %#v", route)
			}
			if route.TargetURL == nil || route.TargetURL.Path != "" || route.TargetURL.RawQuery != "" || route.TargetURL.User != nil {
				t.Fatalf("proxy health route must contain only the compiled origin: %#v", route.TargetURL)
			}
			checker := &Checker{
				logger: logger, routes: routes,
				routeStatus: map[string]*routeStatus{routeKey(route): {route: route, healthState: initialHealthState(route)}},
			}
			checker.identityMap = proxy.BuildIdentityMap(routes)
			checker.logIdentityMap()
			health := newComponentHealth()
			attachComponentHealth(health, checker)
			if snapshot := health.Snapshot(time.Now()); snapshot.State != "pending" {
				t.Fatalf("template-only proxy health = %q, want pending", snapshot.State)
			}
			for _, status := range []int{http.StatusOK, http.StatusForbidden} {
				responseStatus.Store(int32(status))
				checker.runOnce(t.Context())
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				var request proxyRequest
				select {
				case request = <-requests:
				case <-ctx.Done():
					cancel()
					t.Fatal("proxy did not finish recording CONNECT")
				}
				cancel()
				if request.err != nil {
					t.Fatal(request.err)
				}
				if request.method != http.MethodConnect || request.target != route.TargetHostPort || request.host != route.TargetHostPort {
					t.Fatalf("unexpected proxy request: %#v", request)
				}
				if len(request.trailing) != 0 || request.headers.Get("Authorization") != "" {
					t.Fatalf("CONNECT included a template request or credential: %#v", request)
				}
				snapshot := health.Snapshot(time.Now())
				wantStatus := healthstate.StatusOK
				if status != http.StatusOK {
					wantStatus = healthstate.StatusDegraded
				}
				if snapshot.Status != wantStatus {
					t.Fatalf("proxy returned %d: component status = %s, want %s", status, snapshot.Status, wantStatus)
				}
				encoded, err := json.Marshal(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				for _, private := range []string{targetCredential, privateRoute, "privateID", "private-resource", route.TargetHostPort} {
					if strings.Contains(string(encoded), private) || strings.Contains(logs.String(), private) {
						t.Fatalf("proxy component snapshot or logs exposed %q", private)
					}
				}
			}
			if got := targetRequests.Load(); got != 0 {
				t.Fatalf("proxy health invoked template operation %d times", got)
			}
		})
	}
}

func TestTemplateProxyHealthRejectsInvalidFallbackPolicy(t *testing.T) {
	cfg := &config.HarpoonConfig{Targets: []config.HarpoonTarget{{
		Label: "resource",
		Template: &runtimeconfig.HarpoonTargetTemplate{
			Version: 1, Origin: "https://user:private-credential@example.com", Method: http.MethodGet,
			PathTemplate: "/resource/{id}",
			Parameters: map[string]runtimeconfig.HarpoonTemplateParameter{
				"id": {Type: "string", Required: true, Pattern: "[A-Za-z0-9_-]+", MaxLength: 64},
			},
		},
	}}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	routes, err := buildRoutes(nil, nil, cfg, nil, logger, lookupEnvMap(nil))
	if err == nil || len(routes) != 0 {
		t.Fatalf("invalid template policy produced proxy routes: %v, %v", routes, err)
	}
	if strings.Contains(err.Error(), "private-credential") {
		t.Fatal("invalid template policy exposed its credential")
	}
}

func TestRecordResultHistoryRetention(t *testing.T) {
	checker, route := newTestChecker(t)
	for i := range maxHistoryEntries + 2 {
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

	for range 100 {
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
