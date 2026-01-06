package dispatcherinternal

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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.openai.org/api/tunnel-client/pkg/config"
)

func TestFetchOAuthMetadataFallsBackToRoot(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/base":
			http.NotFound(w, r)
		case "/.well-known/oauth-protected-resource":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"resource":"%s"}`, r.Host)
		default:
			http.Error(w, "unexpected path", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	baseURL, err := url.Parse(server.URL + "/base")
	require.NoError(t, err)

	cfg := &config.MCPConfig{ServerURL: baseURL}
	require.NoError(t, cfg.BootstrapOAuthResourceMetadataURLs())
	urls := cfg.OAuthResourceMetadataURLs

	client := server.Client()
	client.Timeout = 2 * time.Second

	resp, fetchErr := fetchOAuthMetadata(
		context.Background(),
		client,
		urls,
		slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})),
	)
	require.NoError(t, fetchErr)
	require.Equal(t, http.StatusOK, resp.ResponseCode())
	require.Equal(t, "application/json", resp.Headers().Get("Content-Type"))
	require.JSONEq(t, fmt.Sprintf(`{"resource":"%s"}`, baseURL.Host), string(resp.Payload()))
}

func TestFetchOAuthMetadataNoURLs(t *testing.T) {
	t.Parallel()

	_, err := fetchOAuthMetadata(context.Background(), &http.Client{Timeout: time.Second}, nil, nil)
	require.Error(t, err)
}

func TestFetchOAuthMetadataRetriesOn5xxThenSucceeds(t *testing.T) {
	t.Parallel()

	var baseCalls, rootCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/base":
			baseCalls++
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"temporary"}`, http.StatusInternalServerError)
		case "/.well-known/oauth-protected-resource":
			rootCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"resource":"%s"}`, r.Host)
		default:
			http.Error(w, "unexpected path", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	baseURL, err := url.Parse(server.URL + "/base")
	require.NoError(t, err)

	cfg := &config.MCPConfig{ServerURL: baseURL}
	require.NoError(t, cfg.BootstrapOAuthResourceMetadataURLs())

	client := server.Client()
	client.Timeout = 2 * time.Second

	resp, fetchErr := fetchOAuthMetadata(context.Background(), client, cfg.OAuthResourceMetadataURLs, nil)
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

	closedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedServer.Close()

	closedURL, err := url.Parse(closedServer.URL + "/.well-known/oauth-protected-resource")
	require.NoError(t, err)
	errorURL, err := url.Parse(errorServer.URL + "/.well-known/oauth-protected-resource")
	require.NoError(t, err)

	client := &http.Client{Timeout: time.Second}

	_, fetchErr := fetchOAuthMetadata(context.Background(), client, []*url.URL{closedURL, errorURL}, nil)
	require.Error(t, fetchErr)
}

func TestFetchOAuthMetadataEmptyBodyIsError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	u, err := url.Parse(server.URL + "/.well-known/oauth-protected-resource")
	require.NoError(t, err)

	client := server.Client()
	client.Timeout = time.Second

	_, fetchErr := fetchOAuthMetadata(context.Background(), client, []*url.URL{u}, nil)
	require.Error(t, fetchErr)
	require.Contains(t, fetchErr.Error(), "empty body")
}

func TestFetchOAuthMetadataFallsBackOn5xxEmptyBody(t *testing.T) {
	t.Parallel()

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			// 5xx with empty body should be retried if there are more candidates.
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"resource":"https://example.com"}`))
		}
	}))
	t.Cleanup(server.Close)

	baseURL, err := url.Parse(server.URL + "/base")
	require.NoError(t, err)
	cfg := &config.MCPConfig{ServerURL: baseURL}
	require.NoError(t, cfg.BootstrapOAuthResourceMetadataURLs())

	client := server.Client()
	client.Timeout = 2 * time.Second

	resp, fetchErr := fetchOAuthMetadata(context.Background(), client, cfg.OAuthResourceMetadataURLs, nil)
	require.NoError(t, fetchErr)
	require.Equal(t, http.StatusOK, resp.ResponseCode())
	require.GreaterOrEqual(t, calls, 2)
}

func TestFetchOAuthMetadataReadErrorIsReturned(t *testing.T) {
	t.Parallel()

	u, err := url.Parse("https://example.com/.well-known/oauth-protected-resource")
	require.NoError(t, err)

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

	_, fetchErr := fetchOAuthMetadata(context.Background(), client, []*url.URL{u}, nil)
	require.Error(t, fetchErr)
	require.Contains(t, fetchErr.Error(), "read failed")
}

func TestFetchOAuthMetadataRetriesOnNetworkError(t *testing.T) {
	t.Parallel()

	u1, err := url.Parse("https://example.com/.well-known/oauth-protected-resource/base")
	require.NoError(t, err)
	u2, err := url.Parse("https://example.com/.well-known/oauth-protected-resource")
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

	resp, fetchErr := fetchOAuthMetadata(context.Background(), client, []*url.URL{u1, nil, u2}, nil)
	require.NoError(t, fetchErr)
	require.Equal(t, http.StatusOK, resp.ResponseCode())
	require.GreaterOrEqual(t, calls, 2)
}

func TestFetchOAuthMetadataReturnsNoResponsesWhenAllCandidatesNil(t *testing.T) {
	t.Parallel()

	client := &http.Client{Timeout: time.Second}
	_, err := fetchOAuthMetadata(context.Background(), client, []*url.URL{nil, nil}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no responses")
}

func TestFetchOAuthMetadataRetriesOnNetworkErrorWithLogger(t *testing.T) {
	t.Parallel()

	u1, err := url.Parse("https://example.com/.well-known/oauth-protected-resource/base")
	require.NoError(t, err)
	u2, err := url.Parse("https://example.com/.well-known/oauth-protected-resource")
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

	resp, fetchErr := fetchOAuthMetadata(context.Background(), client, []*url.URL{u1, u2}, logger)
	require.NoError(t, fetchErr)
	require.Equal(t, http.StatusOK, resp.ResponseCode())
	require.GreaterOrEqual(t, calls, 2)
}

func TestFetchOAuthMetadataRetriesOn5xxReadErrorThenSucceeds(t *testing.T) {
	t.Parallel()

	u1, err := url.Parse("https://example.com/.well-known/oauth-protected-resource/base")
	require.NoError(t, err)
	u2, err := url.Parse("https://example.com/.well-known/oauth-protected-resource")
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

	resp, fetchErr := fetchOAuthMetadata(context.Background(), client, []*url.URL{u1, u2}, logger)
	require.NoError(t, fetchErr)
	require.Equal(t, http.StatusOK, resp.ResponseCode())
	require.GreaterOrEqual(t, calls, 2)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type errorReader struct {
	err error
}

func (r *errorReader) Read([]byte) (int, error) { return 0, r.err }
