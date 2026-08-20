package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/jpillora/backoff"
	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane"
	tclog "github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/tunnelctx"
	"github.com/openai/tunnel-client/pkg/types"
)

func TestPollerRetryHonorsRetryAfter(t *testing.T) {
	fixedNow := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		header    string
		wantDelay time.Duration
	}{
		{
			name:      "delta seconds",
			header:    "5",
			wantDelay: 5 * time.Second,
		},
		{
			name:      "HTTP date",
			header:    fixedNow.Add(5 * time.Second).Format(http.TimeFormat),
			wantDelay: 5 * time.Second,
		},
		{
			name:      "malformed falls back to backoff",
			header:    "later",
			wantDelay: defaultBackoffMin,
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			fetcher := &sequenceFetcher{
				errs: []error{
					newRetryAfterStatusError(t, testCase.header, fixedNow),
					nil,
				},
				cancel: cancel,
			}
			runner, err := NewPoller(
				&chanQueue{ch: make(chan controlplane.PolledCommand, 1)},
				fetcher,
				newDiscardLogger(),
				testMeterProvider.Meter("test"),
				time.Second,
				0,
				0,
				0,
			)
			require.NoError(t, err)

			p := runner.(*poller)
			p.backoff.Jitter = false
			var gotDelays []time.Duration
			p.retrySleep = func(_ context.Context, delay time.Duration) bool {
				gotDelays = append(gotDelays, delay)
				return true
			}

			runner.Run(ctx)

			require.Equal(t, []time.Duration{testCase.wantDelay}, gotDelays)
		})
	}
}

func TestParseRetryAfterRejectsInvalidValuesAndClampsExcessiveValues(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	for _, value := range []string{
		"-1",
		"not-a-date",
		now.Add(-time.Second).Format(http.TimeFormat),
	} {
		_, ok := parseRetryAfter(value, now)
		require.Falsef(t, ok, "Retry-After %q should be ignored", value)
	}

	delay, ok := parseRetryAfter("999999999999999999999999", now)
	require.True(t, ok)
	require.Equal(t, maxRetryAfterDelay, delay)
}

func TestPostResponseRetriesTransportFailureWithSameRequest(t *testing.T) {
	client := newResponseRetryTestClient(t)
	var attempts []capturedRequest
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts = append(attempts, captureRequest(t, req))
		if len(attempts) == 1 {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("temporary transport failure")}
		}
		return testHTTPResponse(req, http.StatusOK, nil), nil
	})

	var waits []time.Duration
	client.retrySleep = func(_ context.Context, delay time.Duration) bool {
		waits = append(waits, delay)
		return true
	}
	client.newResponseBackoff = deterministicResponseBackoff

	// Keep the caller deadline tighter than the client's transport timeout so
	// the observed request deadline is the one PostResponse must preserve.
	deadline := time.Now().Add(5 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	ctx = responseContext(ctx, "shard-transport", "control-request-transport")

	_, err := client.PostResponse(ctx, types.RequestID("request-transport"), testResponse())
	require.NoError(t, err)
	require.Len(t, attempts, 2)
	require.Len(t, waits, 1)
	require.Equal(t, defaultBackoffMin, waits[0])
	require.Equal(t, attempts[0].body, attempts[1].body)
	require.Equal(t, attempts[0].headers, attempts[1].headers)
	require.Equal(t, attempts[0].path, attempts[1].path)
	require.Equal(t, "/v1/tunnels/cli-tunnel/response", attempts[0].path)
	require.Equal(t, "shard-transport", attempts[0].headers.Get("X-Tunnel-Shard-Token"))
	require.Equal(t, "control-request-transport", attempts[0].headers.Get("X-Client-Request-Id"))
	require.True(t, attempts[0].hasDeadline)
	require.True(t, attempts[1].hasDeadline)
	require.Equal(t, attempts[0].deadline, attempts[1].deadline)
}

func TestPostResponseDoesNotRetryJSONRPCNotificationAfterAmbiguousFailure(t *testing.T) {
	tests := []struct {
		name     string
		response func(*http.Request) (*http.Response, error)
	}{
		{
			name: "transport failure",
			response: func(req *http.Request) (*http.Response, error) {
				markRequestWritten(t, req)
				return nil, &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset after write")}
			},
		},
		{
			name: "retryable service unavailable",
			response: func(req *http.Request) (*http.Response, error) {
				return testHTTPResponse(req, http.StatusServiceUnavailable, http.Header{
					"Retry-After": []string{"1"},
				}), nil
			},
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			client := newResponseRetryTestClient(t)
			attempts := 0
			client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 {
					return testCase.response(req)
				}
				return testHTTPResponse(req, http.StatusOK, nil), nil
			})
			var waits []time.Duration
			client.retrySleep = func(_ context.Context, delay time.Duration) bool {
				waits = append(waits, delay)
				return true
			}

			notification := types.NewJSONRPCNotification(
				types.DefaultChannel,
				json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/progress"}`),
				http.StatusOK,
				http.Header{},
			)
			_, err := client.PostResponse(
				responseContext(context.Background(), "shard-notification", ""),
				types.RequestID("request-notification"),
				notification,
			)

			require.Error(t, err)
			require.Equal(t, 1, attempts)
			require.Empty(t, waits)
		})
	}
}

func TestPostResponseRetriesJSONRPCNotificationBeforeCommit(t *testing.T) {
	tests := []struct {
		name     string
		response func(*http.Request) (*http.Response, error)
	}{
		{
			name: "transport failure before write",
			response: func(*http.Request) (*http.Response, error) {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
			},
		},
		{
			name: "rate limited before resolve",
			response: func(req *http.Request) (*http.Response, error) {
				markRequestWritten(t, req)
				return testHTTPResponse(req, http.StatusTooManyRequests, nil), nil
			},
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			client := newResponseRetryTestClient(t)
			attempts := 0
			client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 {
					return testCase.response(req)
				}
				return testHTTPResponse(req, http.StatusOK, nil), nil
			})
			var waits []time.Duration
			client.retrySleep = func(_ context.Context, delay time.Duration) bool {
				waits = append(waits, delay)
				return true
			}

			notification := types.NewJSONRPCNotification(
				types.DefaultChannel,
				json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/progress"}`),
				http.StatusOK,
				http.Header{},
			)
			_, err := client.PostResponse(
				responseContext(context.Background(), "shard-notification", ""),
				types.RequestID("request-notification"),
				notification,
			)

			require.NoError(t, err)
			require.Equal(t, 2, attempts)
			require.Len(t, waits, 1)
		})
	}
}

func TestPostResponseRetriesJSONRPCNotificationBeforeWriteWithRawHTTPLogging(t *testing.T) {
	client := newResponseRetryTestClient(t)
	attempts := 0
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		}
		return testHTTPResponse(req, http.StatusOK, nil), nil
	})
	client.client.Transport = tclog.NewRoundTripper(
		base,
		newDiscardLogger(),
		&config.LoggingConfig{HTTPRawUnsafe: true},
		tclog.ComponentControlPlane,
	)
	var waits []time.Duration
	client.retrySleep = func(_ context.Context, delay time.Duration) bool {
		waits = append(waits, delay)
		return true
	}

	notification := types.NewJSONRPCNotification(
		types.DefaultChannel,
		json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/progress"}`),
		http.StatusOK,
		http.Header{},
	)
	_, err := client.PostResponse(
		responseContext(context.Background(), "shard-raw-logging", ""),
		types.RequestID("request-raw-logging"),
		notification,
	)

	require.NoError(t, err)
	require.Equal(t, 2, attempts)
	require.Len(t, waits, 1)
}

func TestPostResponseRetryAfterAndMalformedFallback(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		wantDelay time.Duration
	}{
		{
			name:      "503 Retry-After",
			header:    "5",
			wantDelay: 5 * time.Second,
		},
		{
			name:      "malformed Retry-After falls back",
			header:    "later",
			wantDelay: defaultBackoffMin,
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			client := newResponseRetryTestClient(t)
			attempts := 0
			client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 {
					return testHTTPResponse(req, http.StatusServiceUnavailable, http.Header{
						"Retry-After": []string{testCase.header},
					}), nil
				}
				return testHTTPResponse(req, http.StatusOK, nil), nil
			})
			client.newResponseBackoff = deterministicResponseBackoff
			var waits []time.Duration
			client.retrySleep = func(_ context.Context, delay time.Duration) bool {
				waits = append(waits, delay)
				return true
			}

			_, err := client.PostResponse(
				responseContext(context.Background(), "shard-retry-after", ""),
				types.RequestID("request-retry-after"),
				testResponse(),
			)
			require.NoError(t, err)
			require.Equal(t, 2, attempts)
			require.Equal(t, []time.Duration{testCase.wantDelay}, waits)
		})
	}
}

func TestPostResponseStopsWhenContextCanceledDuringRetryWait(t *testing.T) {
	client := newResponseRetryTestClient(t)
	attempts := 0
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return testHTTPResponse(req, http.StatusServiceUnavailable, http.Header{
			"Retry-After": []string{"60"},
		}), nil
	})

	waitStarted := make(chan struct{})
	client.retrySleep = func(ctx context.Context, _ time.Duration) bool {
		close(waitStarted)
		<-ctx.Done()
		return false
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.PostResponse(
			responseContext(ctx, "shard-cancel", ""),
			types.RequestID("request-cancel"),
			testResponse(),
		)
		result <- err
	}()

	<-waitStarted
	cancel()
	err := <-result
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, attempts)
}

func TestPostResponseDoesNotRetryNonRetryable4xx(t *testing.T) {
	client := newResponseRetryTestClient(t)
	attempts := 0
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return testHTTPResponse(req, http.StatusBadRequest, nil), nil
	})
	client.retrySleep = func(_ context.Context, _ time.Duration) bool {
		t.Fatal("non-retryable response must not wait for retry")
		return false
	}

	_, err := client.PostResponse(
		responseContext(context.Background(), "shard-400", ""),
		types.RequestID("request-400"),
		testResponse(),
	)
	require.Error(t, err)
	require.Equal(t, 1, attempts)
}

func TestPostResponsePreservesNotFoundTerminalSuccess(t *testing.T) {
	client := newResponseRetryTestClient(t)
	attempts := 0
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return testHTTPResponse(req, http.StatusNotFound, nil), nil
	})
	client.retrySleep = func(_ context.Context, _ time.Duration) bool {
		t.Fatal("terminal 404 must not wait for retry")
		return false
	}

	_, err := client.PostResponse(
		responseContext(context.Background(), "shard-404", ""),
		types.RequestID("request-404"),
		testResponse(),
	)
	require.NoError(t, err)
	require.Equal(t, 1, attempts)
}

func TestFetchManagedCloudflareTunnelRetriesTransportFailure(t *testing.T) {
	client := newResponseRetryTestClient(t)
	attempts := 0
	client.secretClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("temporary transport failure")}
		}
		return testManagedCloudflareRuntimeResponse(req), nil
	})
	client.newManagedCloudflareBackoff = deterministicResponseBackoff
	var waits []time.Duration
	client.retrySleep = func(_ context.Context, delay time.Duration) bool {
		waits = append(waits, delay)
		return true
	}

	runtime, err := client.FetchManagedCloudflareTunnel(context.Background())
	require.NoError(t, err)
	require.NotNil(t, runtime)
	require.Equal(t, 2, attempts)
	require.Equal(t, []time.Duration{defaultBackoffMin}, waits)
}

func TestFetchManagedCloudflareTunnelRetries429And5xx(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		header    string
		wantDelay time.Duration
	}{
		{
			name:      "429 honors Retry-After",
			status:    http.StatusTooManyRequests,
			header:    "5",
			wantDelay: 5 * time.Second,
		},
		{
			name:      "500 falls back to backoff",
			status:    http.StatusInternalServerError,
			wantDelay: defaultBackoffMin,
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			client := newResponseRetryTestClient(t)
			attempts := 0
			client.secretClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 {
					headers := make(http.Header)
					if testCase.header != "" {
						headers.Set("Retry-After", testCase.header)
					}
					return testHTTPResponse(req, testCase.status, headers), nil
				}
				return testManagedCloudflareRuntimeResponse(req), nil
			})
			client.newManagedCloudflareBackoff = deterministicResponseBackoff
			var waits []time.Duration
			client.retrySleep = func(_ context.Context, delay time.Duration) bool {
				waits = append(waits, delay)
				return true
			}

			runtime, err := client.FetchManagedCloudflareTunnel(context.Background())
			require.NoError(t, err)
			require.NotNil(t, runtime)
			require.Equal(t, 2, attempts)
			require.Equal(t, []time.Duration{testCase.wantDelay}, waits)
		})
	}
}

func TestFetchManagedCloudflareTunnelStopsWhenContextCanceledDuringRetryWait(t *testing.T) {
	client := newResponseRetryTestClient(t)
	attempts := 0
	client.secretClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return testHTTPResponse(req, http.StatusServiceUnavailable, http.Header{
			"Retry-After": []string{"60"},
		}), nil
	})

	waitStarted := make(chan struct{})
	client.retrySleep = func(ctx context.Context, _ time.Duration) bool {
		close(waitStarted)
		<-ctx.Done()
		return false
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.FetchManagedCloudflareTunnel(ctx)
		result <- err
	}()

	<-waitStarted
	cancel()
	err := <-result
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, attempts)
}

func TestFetchManagedCloudflareTunnelDoesNotRetryNonRetryable4xx(t *testing.T) {
	client := newResponseRetryTestClient(t)
	attempts := 0
	client.secretClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return testHTTPResponse(req, http.StatusForbidden, nil), nil
	})
	client.retrySleep = func(_ context.Context, _ time.Duration) bool {
		t.Fatal("non-retryable response must not wait for retry")
		return false
	}

	_, err := client.FetchManagedCloudflareTunnel(context.Background())
	require.Error(t, err)
	require.Equal(t, 1, attempts)
}

func TestRetryableResponseStatuses(t *testing.T) {
	for _, statusCode := range []int{
		http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		require.Truef(t, isRetryableResponseStatus(statusCode), "status %d should retry", statusCode)
	}
	for _, statusCode := range []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusRequestEntityTooLarge,
	} {
		require.Falsef(t, isRetryableResponseStatus(statusCode), "status %d should not retry", statusCode)
	}
}

func TestRetryableManagedCloudflareStatuses(t *testing.T) {
	for _, statusCode := range []int{
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		require.Truef(t, isRetryableManagedCloudflareStatus(statusCode), "status %d should retry", statusCode)
	}
	for _, statusCode := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		require.Falsef(t, isRetryableManagedCloudflareStatus(statusCode), "status %d should not retry", statusCode)
	}
}

func markRequestWritten(t *testing.T, req *http.Request) {
	t.Helper()
	trace := httptrace.ContextClientTrace(req.Context())
	require.NotNil(t, trace)
	require.NotNil(t, trace.WroteRequest)
	trace.WroteRequest(httptrace.WroteRequestInfo{})
}

func newRetryAfterStatusError(t *testing.T, value string, now time.Time) *APIStatusError {
	t.Helper()
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Status:     "503 Service Unavailable",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
	}
	resp.Header.Set("Retry-After", value)
	return newAPIStatusError("controlplane client: unexpected status", resp, now)
}

func newResponseRetryTestClient(t *testing.T) *TunnelServiceClient {
	t.Helper()
	client, err := NewTunnelServiceClient(
		context.Background(),
		&config.ControlPlaneConfig{
			BaseURL:  mustParseURL(t, "https://control-plane.example"),
			TunnelID: types.TunnelID("cli-tunnel"),
			APIKey:   "test-api-key",
		},
		nil,
		newDiscardLogger(),
		&config.LoggingConfig{},
		testMeterProvider,
	)
	require.NoError(t, err)
	return client
}

func deterministicResponseBackoff() *backoff.Backoff {
	return &backoff.Backoff{
		Min:    defaultBackoffMin,
		Max:    defaultBackoffMax,
		Factor: 2,
		Jitter: false,
	}
}

func responseContext(ctx context.Context, shardToken string, controlPlaneRequestID string) context.Context {
	ctx = tunnelctx.ContextWithShardToken(ctx, shardToken)
	ctx = tunnelctx.ContextWithChannel(ctx, types.DefaultChannel)
	if controlPlaneRequestID != "" {
		ctx = tunnelctx.ContextWithControlPlaneCommandRequestID(
			ctx,
			types.ControlPlaneRequestID(controlPlaneRequestID),
		)
	}
	return ctx
}

func testResponse() *types.TunnelResponse {
	return types.NewNotificationAck(types.DefaultChannel, http.StatusOK, http.Header{})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type capturedRequest struct {
	body        string
	headers     http.Header
	path        string
	deadline    time.Time
	hasDeadline bool
}

func captureRequest(t *testing.T, req *http.Request) capturedRequest {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	_ = req.Body.Close()
	deadline, hasDeadline := req.Context().Deadline()
	return capturedRequest{
		body:        string(body),
		headers:     req.Header.Clone(),
		path:        req.URL.Path,
		deadline:    deadline,
		hasDeadline: hasDeadline,
	}
}

func testHTTPResponse(req *http.Request, statusCode int, headers http.Header) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	return &http.Response{
		StatusCode: statusCode,
		Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}
}

func testManagedCloudflareRuntimeResponse(req *http.Request) *http.Response {
	resp := testHTTPResponse(req, http.StatusOK, http.Header{
		"Content-Type": []string{"application/json"},
	})
	resp.Body = io.NopCloser(strings.NewReader(`{
		"cloudflare_tunnel": {
			"tunnel_id": "provider-tunnel-id",
			"name": "managed-provider-name",
			"account_id": "provider-account-id"
		},
		"runtime_token": "managed-runtime-token"
	}`))
	return resp
}
