package internal

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.openai.org/api/tunnel-client/pkg/version"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestControlPlaneRoundTripperAddsDefaultHeaders(t *testing.T) {
	t.Parallel()

	const (
		apiKey    = "test-api-key"
		userAgent = "test-user-agent"
	)

	var seen http.Header
	rt := newControlPlaneRoundTripper(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}), apiKey, userAgent, map[string]string{"extra-header": "true"}, newDiscardLogger())

	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if !assert.NoError(t, err, "build request") {
		return
	}

	_, err = rt.RoundTrip(req)
	if !assert.NoError(t, err, "round trip failed") {
		return
	}

	assert.Equal(t, "Bearer "+apiKey, seen.Get("Authorization"), "expected Authorization header to be set")
	assert.Equal(t, "application/json", seen.Get("Accept"), "expected Accept header to be set")
	assert.Equal(t, userAgent, seen.Get("User-Agent"), "expected User-Agent header to be set")
	assert.Equal(t, version.ClientName, seen.Get(headerTunnelClientName), "expected tunnel client name header to be set")
	assert.Equal(t, version.Version, seen.Get(headerTunnelClientVersion), "expected tunnel client version header to be set")
	assert.Equal(t, "true", seen.Get("extra-header"), "expected extra header to be forwarded")
}

func TestControlPlaneRoundTripperWarnsOnOverride(t *testing.T) {
	t.Parallel()

	handler := &warnCaptureHandler{}
	logger := slog.New(handler)

	rt := newControlPlaneRoundTripper(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}), "api-key", "ua", map[string]string{"Accept": "application/problem+json"}, logger)

	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if !assert.NoError(t, err, "build request") {
		return
	}

	_, err = rt.RoundTrip(req)
	assert.NoError(t, err, "round trip failed")
	assert.True(t, handler.seenOverride, "expected override warning")
	assert.Equal(t, "Accept", handler.header, "expected warning for Accept header")
	assert.Equal(t, "application/problem+json", req.Header.Get("Accept"), "expected override to apply")
}

func TestControlPlaneRoundTripperNoWarningWhenValueMatches(t *testing.T) {
	t.Parallel()

	handler := &warnCaptureHandler{}
	logger := slog.New(handler)

	rt := newControlPlaneRoundTripper(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}), "api-key", "ua", map[string]string{"Accept": "application/json"}, logger)

	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if !assert.NoError(t, err, "build request") {
		return
	}

	_, err = rt.RoundTrip(req)
	assert.NoError(t, err, "round trip failed")
	assert.False(t, handler.seenOverride, "did not expect override warning for identical value")
	assert.Equal(t, "application/json", req.Header.Get("Accept"))
}

func TestNewControlPlaneRoundTripperPanicsOnNilLogger(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(t,
		"control-plane round tripper: logger is required",
		func() {
			_ = newControlPlaneRoundTripper(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
			}), "api-key", "ua", nil, nil)
		},
	)
}
