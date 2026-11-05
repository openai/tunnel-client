package oauth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jpillora/backoff"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon/hostbus"
)

func TestOAuthDiscoveryRetriesAfterTransientTimeout(t *testing.T) {
	t.Parallel()

	var recovered atomic.Bool
	enteredRetry := make(chan time.Duration, 1)
	allowRetry := make(chan struct{})
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !recovered.Load() {
			return nil, context.DeadlineExceeded
		}

		statusCode := http.StatusNotFound
		body := ""
		if req.URL.Path == "/.well-known/oauth-protected-resource" {
			statusCode = http.StatusOK
			body = `{"resource":"https://mcp.example.test/mcp"}`
		}
		return &http.Response{
			StatusCode: statusCode,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}
	state := startOAuthDiscoveryTestApp(
		t,
		httpClient,
		deterministicOAuthDiscoveryRetryPolicy(enteredRetry, allowRetry),
	)

	var retryDelay time.Duration
	select {
	case retryDelay = <-enteredRetry:
	case <-time.After(time.Second):
		t.Fatal("OAuth discovery did not enter retry after its timeout cycle")
	}
	if retryDelay <= 0 {
		t.Fatalf("retry delay = %s, want a positive delay", retryDelay)
	}
	if state.IsDone() {
		t.Fatal("transient timeout finalized discovery instead of leaving it pending for retry")
	}

	recovered.Store(true)
	close(allowRetry)

	result, _, _, err, ok := state.Wait(time.Second)
	if !ok {
		t.Fatal("OAuth discovery did not finish after the transport recovered")
	}
	if err != nil {
		t.Fatalf("OAuth discovery remained failed after the transport recovered: %v", err)
	}
	if result == nil || result.URL != "https://mcp.example.test/.well-known/oauth-protected-resource" {
		t.Fatalf("unexpected discovery result after recovery: %#v", result)
	}
}

func TestOAuthDiscoveryRetriesAfterTransientMetadataBodyTimeout(t *testing.T) {
	t.Parallel()

	var recovered atomic.Bool
	enteredRetry := make(chan time.Duration, 1)
	allowRetry := make(chan struct{})
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/mcp" {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}
		if !recovered.Load() {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(&errorReader{err: context.DeadlineExceeded}),
				Request:    req,
			}, nil
		}

		statusCode := http.StatusNotFound
		body := ""
		if req.URL.Path == "/.well-known/oauth-protected-resource" {
			statusCode = http.StatusOK
			body = `{"resource":"https://mcp.example.test/mcp"}`
		}
		return &http.Response{
			StatusCode: statusCode,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}
	state := startOAuthDiscoveryTestApp(
		t,
		httpClient,
		deterministicOAuthDiscoveryRetryPolicy(enteredRetry, allowRetry),
	)

	select {
	case <-enteredRetry:
	case <-time.After(time.Second):
		t.Fatal("OAuth discovery did not retry after its metadata body timed out")
	}
	if state.IsDone() {
		t.Fatal("metadata body timeout finalized discovery instead of leaving it pending for retry")
	}

	recovered.Store(true)
	close(allowRetry)

	result, _, _, err, ok := state.Wait(time.Second)
	if !ok {
		t.Fatal("OAuth discovery did not finish after metadata body reads recovered")
	}
	if err != nil {
		t.Fatalf("OAuth discovery remained failed after metadata body reads recovered: %v", err)
	}
	if result == nil || result.URL != "https://mcp.example.test/.well-known/oauth-protected-resource" {
		t.Fatalf("unexpected discovery result after metadata body recovery: %#v", result)
	}
}

func TestOAuthDiscoveryRecoversAfterMissingPathAndRootTimeout(t *testing.T) {
	t.Parallel()

	for _, bodyTimeout := range []bool{false, true} {
		name := "request timeout"
		if bodyTimeout {
			name = "body timeout"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var recovered atomic.Bool
			enteredRetry := make(chan time.Duration, 1)
			allowRetry := make(chan struct{})
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				status := http.StatusNotFound
				body := io.NopCloser(strings.NewReader("not found"))
				if req.URL.Path == "/.well-known/oauth-protected-resource" {
					status = http.StatusOK
					body = io.NopCloser(strings.NewReader(`{"resource":"https://mcp.example.test/mcp"}`))
					if !recovered.Load() {
						if !bodyTimeout {
							return nil, context.DeadlineExceeded
						}
						body = io.NopCloser(&errorReader{err: context.DeadlineExceeded})
					}
				}
				return &http.Response{
					StatusCode: status,
					Header:     make(http.Header),
					Body:       body,
					Request:    req,
				}, nil
			})}
			state, catalog, bus := startOAuthDiscoveryCatalogTestApp(t, client, deterministicOAuthDiscoveryRetryPolicy(enteredRetry, allowRetry))
			select {
			case <-enteredRetry:
			case <-state.done:
				_, _, _, err, _ := state.Wait(0)
				t.Fatalf("404 followed by timeout permanently settled discovery: %v", err)
			case <-time.After(time.Second):
				t.Fatal("404 followed by timeout did not enter recovery retry")
			}
			if state.IsDone() {
				t.Fatal("discovery must remain pending while the root metadata endpoint is unavailable")
			}
			if done, _, err := catalog.Snapshot(); done {
				t.Fatalf("startup catalog settled before endpoint recovery: %v", err)
			}
			bus.mu.Lock()
			published := len(bus.bundles)
			bus.mu.Unlock()
			if published != 0 {
				t.Fatal("failed discovery published a partial startup catalog")
			}

			recovered.Store(true)
			close(allowRetry)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := catalog.Wait(ctx); err != nil {
				t.Fatalf("startup catalog did not recover without restarting: %v", err)
			}
			result, _, _, err, done := state.Wait(time.Second)
			if !done || err != nil {
				t.Fatalf("discovery did not recover: done=%v, err=%v", done, err)
			}
			if result == nil || result.URL != "https://mcp.example.test/.well-known/oauth-protected-resource" {
				t.Fatalf("unexpected recovered discovery result: %#v", result)
			}
			bus.mu.Lock()
			defer bus.mu.Unlock()
			if len(bus.bundles) != 1 || len(bus.bundles[0].URLs) == 0 {
				t.Fatalf("recovered startup bundles = %#v, want one non-empty bundle", bus.bundles)
			}
		})
	}
}

func TestOAuthDiscoveryRecoversFromInvalidStartupMetadata(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "service unavailable", status: http.StatusServiceUnavailable, body: "<html>Service Unavailable</html>"},
		{name: "empty gateway error", status: http.StatusBadGateway},
		{name: "rate limited", status: http.StatusTooManyRequests, body: "try again later"},
		{name: "invalid successful response", status: http.StatusOK, body: "<html>starting</html>"},
		{name: "empty successful response", status: http.StatusOK},
		{name: "metadata with service unavailable status", status: http.StatusServiceUnavailable, body: `{"resource":"https://mcp.example.test/mcp"}`},
		{name: "metadata with rate limited status", status: http.StatusTooManyRequests, body: `{"resource":"https://mcp.example.test/mcp"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var recovered atomic.Bool
			enteredRetry := make(chan time.Duration, 1)
			allowRetry := make(chan struct{})
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				status, body := http.StatusNotFound, ""
				if req.URL.Path == "/.well-known/oauth-protected-resource" {
					status, body = tc.status, tc.body
					if recovered.Load() {
						status, body = http.StatusOK, `{"resource":"https://mcp.example.test/mcp"}`
					}
				}
				return &http.Response{
					StatusCode: status,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(body)),
					Request:    req,
				}, nil
			})}
			state, catalog, bus := startOAuthDiscoveryCatalogTestApp(t, client, deterministicOAuthDiscoveryRetryPolicy(enteredRetry, allowRetry))
			select {
			case <-enteredRetry:
			case <-time.After(time.Second):
				t.Fatal("invalid startup metadata did not enter recovery retry")
			}
			if state.IsDone() {
				t.Fatal("invalid startup metadata permanently settled discovery")
			}
			if done, _, err := catalog.Snapshot(); done {
				t.Fatalf("invalid startup metadata settled startup catalog: %v", err)
			}
			bus.mu.Lock()
			published := len(bus.bundles)
			bus.mu.Unlock()
			if published != 0 {
				t.Fatal("invalid startup metadata published a partial catalog")
			}

			recovered.Store(true)
			close(allowRetry)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := catalog.Wait(ctx); err != nil {
				t.Fatalf("startup catalog did not recover without restarting: %v", err)
			}
			if _, _, _, err, done := state.Wait(time.Second); !done || err != nil {
				t.Fatalf("discovery did not recover: done=%v, err=%v", done, err)
			}
			bus.mu.Lock()
			defer bus.mu.Unlock()
			if len(bus.bundles) != 1 || len(bus.bundles[0].URLs) == 0 {
				t.Fatalf("recovered startup bundles = %#v, want one non-empty bundle", bus.bundles)
			}
		})
	}
}

func TestOAuthDiscoveryPreservesFailFastForNonTimeoutFailure(t *testing.T) {
	t.Parallel()

	transportErr := errors.New("dial failed")
	state := startOAuthDiscoveryTestApp(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})})

	_, _, _, err, ok := state.Wait(time.Second)
	if !ok {
		t.Fatal("non-timeout OAuth discovery failure did not finish promptly")
	}
	if !errors.Is(err, transportErr) {
		t.Fatalf("OAuth discovery error = %v, want wrapped transport error", err)
	}
}

func TestOAuthDiscoveryRetriesTransientAuthServerMetadataFailure(t *testing.T) {
	t.Parallel()
	for _, status := range []int{0, http.StatusOK, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			var recovered atomic.Bool
			enteredRetry := make(chan time.Duration, 1)
			allowRetry := make(chan struct{})
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				responseStatus, body := http.StatusNotFound, ""
				switch req.URL.Path {
				case "/.well-known/oauth-protected-resource":
					responseStatus = http.StatusOK
					body = `{"resource":"https://mcp.example.test/mcp","authorization_servers":["https://mcp.example.test"]}`
				case "/.well-known/oauth-authorization-server", "/.well-known/openid-configuration":
					if status == 0 && !recovered.Load() {
						return nil, context.DeadlineExceeded
					}
					responseStatus, body = status, "<html>Service Unavailable</html>"
					if recovered.Load() {
						responseStatus = http.StatusOK
						body = `{"issuer":"https://mcp.example.test","token_endpoint":"https://mcp.example.test/token"}`
					}
				}
				return &http.Response{
					StatusCode: responseStatus,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(body)),
					Request:    req,
				}, nil
			})}
			state, catalog, bus := startOAuthDiscoveryCatalogTestApp(t, client, deterministicOAuthDiscoveryRetryPolicy(enteredRetry, allowRetry))
			select {
			case <-enteredRetry:
			case <-time.After(time.Second):
				t.Fatal("transient authorization-server metadata failure did not enter recovery retry")
			}
			if state.IsDone() {
				t.Fatal("transient authorization-server metadata failure settled discovery")
			}
			if done, _, err := catalog.Snapshot(); done {
				t.Fatalf("partial authorization-server metadata settled startup catalog: %v", err)
			}
			bus.mu.Lock()
			published := len(bus.bundles)
			bus.mu.Unlock()
			if published != 0 {
				t.Fatal("partial authorization-server metadata published a catalog")
			}
			recovered.Store(true)
			close(allowRetry)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := catalog.Wait(ctx); err != nil {
				t.Fatalf("authorization-server metadata did not recover: %v", err)
			}
			bus.mu.Lock()
			defer bus.mu.Unlock()
			if len(bus.bundles) != 1 {
				t.Fatalf("published %d bundles, want one", len(bus.bundles))
			}
			for _, record := range bus.bundles[0].URLs {
				if record.URL.String() == "https://mcp.example.test/token" {
					return
				}
			}
			t.Fatal("recovered catalog is missing token endpoint")
		})
	}
}

func TestOAuthDiscoveryPreservesPermanentAndOptionalResponses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		optional bool
	}{
		{name: "forbidden", status: http.StatusForbidden, body: "forbidden"},
		{name: "oversized metadata", status: http.StatusOK, body: strings.Repeat("a", protectedResourceMetadataBodyLimitBytes+1)},
		{name: "optional metadata absent", status: http.StatusNotFound, optional: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			enteredRetry := make(chan time.Duration, 1)
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tc.status,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(tc.body)),
					Request:    req,
				}, nil
			})}
			state, catalog, _ := startOAuthDiscoveryCatalogTestApp(t, client, deterministicOAuthDiscoveryRetryPolicy(enteredRetry, nil))
			if _, _, _, err, done := state.Wait(time.Second); !done || err == nil {
				t.Fatalf("expected completed discovery failure, got done=%v, err=%v", done, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := catalog.Wait(ctx)
			if (err == nil) != tc.optional {
				t.Fatalf("catalog error = %v, optional=%v", err, tc.optional)
			}
			select {
			case <-enteredRetry:
				t.Fatal("permanent or optional metadata response entered retry")
			default:
			}
		})
	}
}

func TestOAuthDiscoveryDoesNotRetryResponseAfterPermanentRequestFailure(t *testing.T) {
	t.Parallel()
	enteredRetry := make(chan time.Duration, 1)
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/.well-known/oauth-protected-resource/mcp" {
			return nil, errors.New("permanent request failure")
		}
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("temporarily unavailable")),
			Request:    req,
		}, nil
	})}
	state := startOAuthDiscoveryTestApp(t, client, deterministicOAuthDiscoveryRetryPolicy(enteredRetry, nil))
	if _, _, _, err, done := state.Wait(time.Second); !done || err == nil {
		t.Fatalf("permanent request failure did not settle: done=%v, err=%v", done, err)
	}
	select {
	case <-enteredRetry:
		t.Fatal("retryable response masked a permanent request failure")
	default:
	}
}

func TestOAuthDiscoveryPreservesBestEffortAuthServerMetadata(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		issuer string
		status int
	}{
		{name: "absent", issuer: "https://mcp.example.test", status: http.StatusNotFound},
		{name: "forbidden", issuer: "https://mcp.example.test", status: http.StatusForbidden},
		{name: "untrusted origin", issuer: "https://not-trusted.example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			enteredRetry := make(chan time.Duration, 1)
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "mcp.example.test" {
					t.Error("discovery sent a request outside the trusted MCP origin")
					return nil, errors.New("unexpected origin")
				}
				status, body := tc.status, "unavailable"
				switch req.URL.Path {
				case "/.well-known/oauth-protected-resource":
					status = http.StatusOK
					body = `{"resource":"https://mcp.example.test/mcp","authorization_servers":["` + tc.issuer + `"]}`
				case "/mcp", "/.well-known/oauth-protected-resource/mcp":
					status = http.StatusNotFound
				}
				return &http.Response{
					StatusCode: status,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(body)),
					Request:    req,
				}, nil
			})}
			state, catalog, _ := startOAuthDiscoveryCatalogTestApp(t, client, deterministicOAuthDiscoveryRetryPolicy(enteredRetry, nil))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := catalog.Wait(ctx); err != nil {
				t.Fatalf("best-effort metadata enrichment blocked catalog: %v", err)
			}
			if _, _, _, err, done := state.Wait(time.Second); !done || err != nil {
				t.Fatalf("best-effort metadata enrichment blocked discovery: done=%v, err=%v", done, err)
			}
			select {
			case <-enteredRetry:
				t.Fatal("best-effort metadata enrichment entered retry")
			default:
			}
		})
	}
}

func TestOAuthDiscoveryPreservesFailFastForMixedFailureCycle(t *testing.T) {
	t.Parallel()

	permanentErr := errors.New("permanent transport failure")
	enteredRetry := make(chan time.Duration, 1)
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/mcp":
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		case "/.well-known/oauth-protected-resource/mcp":
			return nil, permanentErr
		case "/.well-known/oauth-protected-resource":
			return nil, context.DeadlineExceeded
		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, permanentErr
		}
	})}
	state := startOAuthDiscoveryTestApp(t, httpClient, oauthDiscoveryRetryPolicy{
		newBackoff: deterministicOAuthDiscoveryBackoff,
		wait: func(ctx context.Context, delay time.Duration) bool {
			select {
			case enteredRetry <- delay:
			case <-ctx.Done():
			}
			return false
		},
	})

	_, _, _, err, ok := state.Wait(time.Second)
	if !ok {
		select {
		case <-enteredRetry:
			t.Fatal("mixed timeout/non-timeout discovery cycle retried instead of failing fast")
		default:
		}
		t.Fatal("mixed failure cycle did not finish promptly")
	}
	if err == nil {
		t.Fatal("mixed failure cycle completed without an error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("discovery error = %v, want the original final timeout", err)
	}
	select {
	case <-enteredRetry:
		t.Fatal("mixed timeout/non-timeout discovery cycle retried instead of failing fast")
	default:
	}
}

func TestWaitForOAuthDiscoveryRetryStopsWhenContextIsCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if waitForOAuthDiscoveryRetry(ctx, time.Hour) {
		t.Fatal("retry wait completed after its context was canceled")
	}
}

func deterministicOAuthDiscoveryRetryPolicy(
	enteredRetry chan<- time.Duration,
	allowRetry <-chan struct{},
) oauthDiscoveryRetryPolicy {
	return oauthDiscoveryRetryPolicy{
		newBackoff: deterministicOAuthDiscoveryBackoff,
		wait: func(ctx context.Context, delay time.Duration) bool {
			select {
			case enteredRetry <- delay:
			case <-ctx.Done():
				return false
			}
			select {
			case <-allowRetry:
				return true
			case <-ctx.Done():
				return false
			}
		},
	}
}

func deterministicOAuthDiscoveryBackoff() *backoff.Backoff {
	return &backoff.Backoff{
		Min:    time.Second,
		Max:    time.Second,
		Factor: 1,
		Jitter: false,
	}
}

func startOAuthDiscoveryTestApp(
	t *testing.T,
	httpClient *http.Client,
	retryPolicies ...oauthDiscoveryRetryPolicy,
) *DiscoveryState {
	t.Helper()
	state, _, _ := startOAuthDiscoveryCatalogTestApp(t, httpClient, retryPolicies...)
	return state
}

func startOAuthDiscoveryCatalogTestApp(
	t *testing.T,
	httpClient *http.Client,
	retryPolicies ...oauthDiscoveryRetryPolicy,
) (*DiscoveryState, *hostbus.StartupCatalogState, *recordingBus) {
	t.Helper()

	serverURL, err := url.Parse("https://mcp.example.test/mcp")
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	state := NewDiscoveryState()
	catalog := hostbus.NewStartupCatalogState()
	bus := &recordingBus{notify: make(chan struct{})}
	invoke := any(startOAuthDiscovery)
	if len(retryPolicies) > 0 {
		retryPolicy := retryPolicies[0]
		invoke = func(p discoveryParams) error {
			return startOAuthDiscoveryWithRetryPolicy(p, retryPolicy)
		}
	}
	app := fx.New(
		fx.NopLogger,
		fx.Provide(
			func() *runtimeconfig.MCPConfig {
				return &runtimeconfig.MCPConfig{
					ServerURL:     serverURL,
					TransportKind: runtimeconfig.MCPTransportHTTPStreamable,
				}
			},
			fx.Annotate(
				func() *http.Client { return httpClient },
				fx.ResultTags(`name:"mcp_client"`),
			),
			func() hostbus.HostRegistrationBus { return bus },
			func() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) },
			func() *DiscoveryState { return state },
			func() *hostbus.StartupCatalogState { return catalog },
		),
		fx.Invoke(invoke),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := app.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = app.Stop(context.Background())
	})
	return state, catalog, bus
}
