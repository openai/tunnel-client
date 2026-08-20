package log_test

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openai/tunnel-client/pkg/config"
	tclog "github.com/openai/tunnel-client/pkg/log"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestLoggingRoundTripperEmitsRawHTTP(t *testing.T) {
	t.Helper()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rt := tclog.NewRoundTripper(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     http.Header{"X-Test": {"value"}},
			Body:       io.NopCloser(strings.NewReader("response body")),
		}, nil
	}), logger, &config.LoggingConfig{HTTPRawUnsafe: true}, "test-component")

	req, err := http.NewRequest(http.MethodPost, "http://example.com/raw", strings.NewReader("request body"))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	logs := buf.String()
	for _, snippet := range []string{
		"raw http request",
		"raw http response",
		"request body",
		"response body",
		"component=test-component",
	} {
		if !strings.Contains(logs, snippet) {
			t.Fatalf("expected log output to contain %q, got:\n%s", snippet, logs)
		}
	}
}

func TestLoggingRoundTripperDoesNotEmitSyntheticHTTPTraceEvents(t *testing.T) {
	t.Helper()

	const requestBody = "request body must reach the real transport"
	var wroteRequests atomic.Int32
	base := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if got := wroteRequests.Load(); got != 0 {
			t.Fatalf("WroteRequest callbacks before real transport write = %d, want 0", got)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := req.Body.Close(); err != nil {
			t.Fatalf("close request body: %v", err)
		}
		if got := string(body); got != requestBody {
			t.Fatalf("request body = %q, want %q", got, requestBody)
		}

		trace := httptrace.ContextClientTrace(req.Context())
		if trace == nil || trace.WroteRequest == nil {
			t.Fatal("real transport did not receive WroteRequest trace")
		}
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       http.NoBody,
		}, nil
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rt := tclog.NewRoundTripper(base, logger, &config.LoggingConfig{HTTPRawUnsafe: true}, "")
	req, err := http.NewRequest(http.MethodPost, "http://example.com/raw", strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) {
			wroteRequests.Add(1)
		},
	}))

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	if got := wroteRequests.Load(); got != 1 {
		t.Fatalf("WroteRequest callbacks = %d, want exactly 1 from the real transport", got)
	}
}

func TestLoggingRoundTripperSkipsWhenDisabled(t *testing.T) {
	t.Helper()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rt := tclog.NewRoundTripper(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("resp")),
		}, nil
	}), logger, &config.LoggingConfig{HTTPRawUnsafe: false}, "test-component")

	req, err := http.NewRequest(http.MethodGet, "http://example.com/raw", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	if buf.Len() != 0 {
		t.Fatalf("expected no logs when raw logging disabled, got:\n%s", buf.String())
	}
}

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errReadCloser) Close() error             { return nil }

func TestLoggingRoundTripperLogsDumpErrors(t *testing.T) {
	t.Helper()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rt := tclog.NewRoundTripper(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"X-Test": {"value"}},
			Body:       errReadCloser{},
		}, nil
	}), logger, &config.LoggingConfig{HTTPRawUnsafe: true}, "")

	req, err := http.NewRequest(http.MethodPost, "http://example.com/raw", errReadCloser{})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	logs := buf.String()
	for _, snippet := range []string{
		"failed to dump raw http request",
		"failed to dump raw http response",
		"read failed",
	} {
		if !strings.Contains(logs, snippet) {
			t.Fatalf("expected log output to contain %q, got:\n%s", snippet, logs)
		}
	}
}
