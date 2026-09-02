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

func TestOAuthDiscoveryPreservesFailFastForNonTimeoutFailure(t *testing.T) {
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

func TestOAuthDiscoveryPreservesFailFastForMixedFailureCycle(t *testing.T) {
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

	serverURL, err := url.Parse("https://mcp.example.test/mcp")
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	state := NewDiscoveryState()
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
	return state
}
