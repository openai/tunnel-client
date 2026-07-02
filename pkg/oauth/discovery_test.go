package oauth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.openai.org/api/tunnel-client/pkg/headerscope"
)

func TestFetchOAuthMetadataFallsBackToRoot(t *testing.T) {
	t.Parallel()

	var expectedResource string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/base":
			http.NotFound(w, r)
		case "/.well-known/oauth-protected-resource":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"resource":"%s"}`, expectedResource)
		default:
			http.Error(w, "unexpected path", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	baseURL, err := url.Parse(server.URL + "/base")
	require.NoError(t, err)
	expectedResource = baseURL.String()

	client := server.Client()
	client.Timeout = 2 * time.Second

	resp, sourceURL, _, fetchErr := FetchOAuthMetadata(
		context.Background(),
		client,
		buildWellKnownCandidates(baseURL),
		slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})),
	)
	require.NoError(t, fetchErr)
	require.NotNil(t, sourceURL)
	require.Equal(t, http.StatusOK, resp.ResponseCode())
	require.Equal(t, "application/json", resp.Headers().Get("Content-Type"))
	require.JSONEq(t, fmt.Sprintf(`{"resource":"%s"}`, expectedResource), string(resp.Payload()))
}

func TestFetchOAuthMetadataNoURLs(t *testing.T) {
	t.Parallel()

	_, _, _, err := FetchOAuthMetadata(context.Background(), &http.Client{Timeout: time.Second}, nil, nil)
	require.Error(t, err)
}

func TestFetchOAuthMetadataRetriesOn5xxThenSucceeds(t *testing.T) {
	t.Parallel()

	var baseCalls, rootCalls int
	var expectedResource string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/base":
			baseCalls++
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"temporary"}`, http.StatusInternalServerError)
		case "/.well-known/oauth-protected-resource":
			rootCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"resource":"%s"}`, expectedResource)
		default:
			http.Error(w, "unexpected path", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	baseURL, err := url.Parse(server.URL + "/base")
	require.NoError(t, err)
	expectedResource = baseURL.String()

	client := server.Client()
	client.Timeout = 2 * time.Second

	resp, _, _, fetchErr := FetchOAuthMetadata(
		context.Background(),
		client,
		buildWellKnownCandidates(baseURL),
		nil,
	)
	require.NoError(t, fetchErr)
	require.Equal(t, http.StatusOK, resp.ResponseCode())
	require.EqualValues(t, 1, baseCalls)
	require.EqualValues(t, 1, rootCalls)
}

func TestFetchOAuthMetadataAllFailuresReturnError(t *testing.T) {
	t.Parallel()

	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(errorServer.Close)

	client := &http.Client{Timeout: time.Second}

	resourceURL, err := url.Parse(errorServer.URL + "/base")
	require.NoError(t, err)
	_, _, _, fetchErr := FetchOAuthMetadata(
		context.Background(),
		client,
		buildWellKnownCandidates(resourceURL),
		nil,
	)
	require.Error(t, fetchErr)
}

func TestFetchOAuthMetadataEmptyBodyIsError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	client := server.Client()
	client.Timeout = time.Second

	resourceURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	_, _, _, fetchErr := FetchOAuthMetadata(
		context.Background(),
		client,
		buildWellKnownCandidates(resourceURL),
		nil,
	)
	require.Error(t, fetchErr)
	require.Contains(t, fetchErr.Error(), "empty body")
}

func TestFetchOAuthMetadataRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat("a", protectedResourceMetadataBodyLimitBytes+1)))
	}))
	t.Cleanup(server.Close)

	candidateURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	resp, _, attempts, fetchErr := FetchOAuthMetadata(
		context.Background(),
		server.Client(),
		[]DiscoveryCandidate{{URL: candidateURL, Source: DiscoverySourceWWWAuthenticate}},
		nil,
	)
	require.Error(t, fetchErr)
	require.Nil(t, resp)
	require.Contains(t, fetchErr.Error(), "exceeds")
	require.Len(t, attempts, 1)
	require.Contains(t, attempts[0].Error, "exceeds")
}

func TestFetchOAuthMetadataFallsBackOn404OversizedBody(t *testing.T) {
	t.Parallel()

	var calls int
	var expectedResource string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(strings.Repeat("a", protectedResourceMetadataBodyLimitBytes+1)))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"resource":"%s"}`, expectedResource)
	}))
	t.Cleanup(server.Close)

	baseURL, err := url.Parse(server.URL + "/base")
	require.NoError(t, err)
	expectedResource = baseURL.String()

	resp, _, attempts, fetchErr := FetchOAuthMetadata(
		context.Background(),
		server.Client(),
		buildWellKnownCandidates(baseURL),
		nil,
	)
	require.NoError(t, fetchErr)
	require.Equal(t, http.StatusOK, resp.ResponseCode())
	require.GreaterOrEqual(t, calls, 2)
	require.Contains(t, attempts[0].Error, "exceeds")
}

func TestFetchOAuthMetadataFallsBackOn5xxEmptyBody(t *testing.T) {
	t.Parallel()

	var calls int
	var expectedResource string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			// 5xx with empty body should be retried if there are more candidates.
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"resource":"%s"}`, expectedResource)
		}
	}))
	t.Cleanup(server.Close)

	baseURL, err := url.Parse(server.URL + "/base")
	require.NoError(t, err)
	expectedResource = baseURL.String()
	client := server.Client()
	client.Timeout = 2 * time.Second

	resp, _, _, fetchErr := FetchOAuthMetadata(
		context.Background(),
		client,
		buildWellKnownCandidates(baseURL),
		nil,
	)
	require.NoError(t, fetchErr)
	require.Equal(t, http.StatusOK, resp.ResponseCode())
	require.GreaterOrEqual(t, calls, 2)
}

func TestFetchOAuthMetadataReadErrorIsReturned(t *testing.T) {
	t.Parallel()

	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, http.MethodGet, req.Method)
			require.Equal(t, "application/json", req.Header.Get("Accept"))
			require.NotEmpty(t, req.Header.Get("User-Agent"))

			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(&errorReader{err: errors.New("read failed")}),
				Request:    req,
			}, nil
		}),
		Timeout: time.Second,
	}

	resourceURL, err := url.Parse("https://example.com")
	require.NoError(t, err)
	_, _, _, fetchErr := FetchOAuthMetadata(
		context.Background(),
		client,
		buildWellKnownCandidates(resourceURL),
		nil,
	)
	require.Error(t, fetchErr)
	require.Contains(t, fetchErr.Error(), "read failed")
}

func TestFetchOAuthMetadataRetriesOnNetworkError(t *testing.T) {
	t.Parallel()

	u1, err := url.Parse("https://example.com/.well-known/oauth-protected-resource/base")
	require.NoError(t, err)

	var calls int
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			require.Equal(t, "application/json", req.Header.Get("Accept"))
			require.NotEmpty(t, req.Header.Get("User-Agent"))

			if req.URL.Path == u1.Path {
				return nil, errors.New("dial failed")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"resource":"https://example.com"}`)),
				Request:    req,
			}, nil
		}),
		Timeout: time.Second,
	}

	resourceURL, err := url.Parse("https://example.com/base")
	require.NoError(t, err)
	resp, _, _, fetchErr := FetchOAuthMetadata(
		context.Background(),
		client,
		buildWellKnownCandidates(resourceURL),
		nil,
	)
	require.NoError(t, fetchErr)
	require.Equal(t, http.StatusOK, resp.ResponseCode())
	require.GreaterOrEqual(t, calls, 2)
}

func TestFetchOAuthMetadataRetriesOnNetworkErrorWithLogger(t *testing.T) {
	t.Parallel()

	u1, err := url.Parse("https://example.com/.well-known/oauth-protected-resource/base")
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))

	var calls int
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.URL.Path == u1.Path {
				return nil, errors.New("dial failed")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"resource":"https://example.com"}`)),
				Request:    req,
			}, nil
		}),
		Timeout: time.Second,
	}

	resourceURL, err := url.Parse("https://example.com/base")
	require.NoError(t, err)
	resp, _, _, fetchErr := FetchOAuthMetadata(
		context.Background(),
		client,
		buildWellKnownCandidates(resourceURL),
		logger,
	)
	require.NoError(t, fetchErr)
	require.Equal(t, http.StatusOK, resp.ResponseCode())
	require.GreaterOrEqual(t, calls, 2)
}

func TestFetchOAuthMetadataRetriesOn5xxReadErrorThenSucceeds(t *testing.T) {
	t.Parallel()

	u1, err := url.Parse("https://example.com/.well-known/oauth-protected-resource/base")
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))

	var calls int
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.URL.Path == u1.Path {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Header:     make(http.Header),
					Body:       io.NopCloser(&errorReader{err: errors.New("read failed")}),
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"resource":"https://example.com"}`)),
				Request:    req,
			}, nil
		}),
		Timeout: time.Second,
	}

	resourceURL, err := url.Parse("https://example.com/base")
	require.NoError(t, err)
	resp, _, _, fetchErr := FetchOAuthMetadata(
		context.Background(),
		client,
		buildWellKnownCandidates(resourceURL),
		logger,
	)
	require.NoError(t, fetchErr)
	require.Equal(t, http.StatusOK, resp.ResponseCode())
	require.GreaterOrEqual(t, calls, 2)
}

func TestFetchOAuthMetadataAttemptsCapture(t *testing.T) {
	t.Parallel()

	var expectedResource string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/base":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"resource":"%s"}`, expectedResource)
		case "/.well-known/oauth-protected-resource":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"resource":"%s"}`, expectedResource)
		default:
			http.Error(w, "unexpected path", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	baseURL, err := url.Parse(server.URL + "/base")
	require.NoError(t, err)
	expectedResource = baseURL.String()

	client := server.Client()
	client.Timeout = 2 * time.Second

	resp, _, attempts, fetchErr := FetchOAuthMetadata(
		context.Background(),
		client,
		buildWellKnownCandidates(baseURL),
		nil,
	)
	require.NoError(t, fetchErr)
	require.Equal(t, http.StatusOK, resp.ResponseCode())
	require.Len(t, attempts, 2)
	require.True(t, attempts[0].Tried)
	require.True(t, attempts[0].Selected)
	require.Equal(t, DiscoverySourceWellKnownPath, attempts[0].Source)
	require.False(t, attempts[1].Tried)
	require.False(t, attempts[1].Selected)
	require.Equal(t, DiscoverySourceWellKnownRoot, attempts[1].Source)
}

func TestFetchOAuthMetadataRetriesTimeoutWithIncreasingRequestTimeout(t *testing.T) {
	t.Parallel()

	targetURL, err := url.Parse("https://example.com/.well-known/oauth-protected-resource")
	require.NoError(t, err)

	var calls int
	var deadlines []time.Time
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if dl, ok := req.Context().Deadline(); ok {
				deadlines = append(deadlines, dl)
			}
			return nil, context.DeadlineExceeded
		}),
		Timeout: 30 * time.Second,
	}

	_, _, _, fetchErr := FetchOAuthMetadata(
		context.Background(),
		client,
		[]DiscoveryCandidate{{URL: targetURL, Source: DiscoverySourceWellKnownRoot}},
		nil,
	)
	require.Error(t, fetchErr)
	require.Equal(t, 1+oauthMetadataRequestRetryCount, calls)
	require.Len(t, deadlines, 1+oauthMetadataRequestRetryCount)
	retryDeadlines := deadlines[1:]
	require.Len(t, retryDeadlines, oauthMetadataRequestRetryCount)
	require.True(t, retryDeadlines[1].After(retryDeadlines[0]), "second retry deadline should be later than first")
	require.True(t, retryDeadlines[2].After(retryDeadlines[1]), "third retry deadline should be later than second")
}

func TestFetchOAuthMetadataTimeoutRetriesPreserveDiscoveryContext(t *testing.T) {
	t.Parallel()

	targetURL, err := url.Parse("https://mcp.example.test/.well-known/oauth-protected-resource")
	require.NoError(t, err)

	var discoveryContexts []bool
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			discoveryContexts = append(discoveryContexts, headerscope.IsMCPDiscovery(req.Context()))
			return nil, context.DeadlineExceeded
		}),
		Timeout: 30 * time.Second,
	}

	_, _, _, fetchErr := FetchOAuthMetadata(
		context.Background(),
		client,
		[]DiscoveryCandidate{{URL: targetURL, Source: DiscoverySourceWellKnownRoot}},
		nil,
	)
	require.Error(t, fetchErr)
	require.Len(t, discoveryContexts, 1+oauthMetadataRequestRetryCount)
	for _, isDiscovery := range discoveryContexts {
		require.True(t, isDiscovery)
	}
}

func TestValidateProtectedResourceMetadataUsesAuthorizationServerIndexZero(t *testing.T) {
	t.Parallel()

	err := validateProtectedResourceMetadata([]byte(`{
		"resource":"https://resource.internal/mcp",
		"authorization_servers":["https://auth-0.internal",":://invalid"]
	}`))
	require.NoError(t, err)
}

func TestValidateProtectedResourceMetadataRejectsInvalidAuthorizationServerIndexZero(t *testing.T) {
	t.Parallel()

	err := validateProtectedResourceMetadata([]byte(`{
		"resource":"https://resource.internal/mcp",
		"authorization_servers":[":://invalid","https://auth-1.internal"]
	}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "authorization server[0]")
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type errorReader struct {
	err error
}

func (r *errorReader) Read([]byte) (int, error) { return 0, r.err }
