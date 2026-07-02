package internal

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/openai/tunnel-client/pkg/clientinstance"
	"github.com/openai/tunnel-client/pkg/version"
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
	}), apiKey, userAgent, "", map[string]string{"extra-header": "true"}, newDiscardLogger())

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
	assert.Equal(t, clientinstance.ID(), seen.Get(clientinstance.HeaderName), "expected tunnel client instance ID header to be set")
	assert.Equal(t, "true", seen.Get("extra-header"), "expected extra header to be forwarded")
}

func TestControlPlaneRoundTripperAddsOrganizationHeader(t *testing.T) {
	t.Parallel()

	const organizationID = "org-WBHny2fx55kAfLt1W8tnt5Aw"

	var seen http.Header
	rt := newControlPlaneRoundTripper(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}), "api-key", "ua", organizationID, map[string]string{"openai-organization": "org-extra"}, newDiscardLogger())

	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if !assert.NoError(t, err, "build request") {
		return
	}

	_, err = rt.RoundTrip(req)
	assert.NoError(t, err, "round trip failed")
	assert.Equal(t, organizationID, seen.Get(headerOpenAIOrganization), "expected configured organization header to override extra header")
}

func TestControlPlaneRoundTripperWarnsOnOverride(t *testing.T) {
	t.Parallel()

	handler := &warnCaptureHandler{}
	logger := slog.New(handler)

	rt := newControlPlaneRoundTripper(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}), "api-key", "ua", "", map[string]string{"X-Debug": "new"}, logger)

	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if !assert.NoError(t, err, "build request") {
		return
	}
	req.Header.Set("X-Debug", "original")

	_, err = rt.RoundTrip(req)
	assert.NoError(t, err, "round trip failed")
	assert.True(t, handler.seenOverride, "expected override warning")
	assert.Equal(t, "X-Debug", handler.header, "expected warning for X-Debug header")
	assert.Equal(t, "new", req.Header.Get("X-Debug"), "expected override to apply")
}

func TestControlPlaneRoundTripperPreservesProtectedHeaders(t *testing.T) {
	t.Parallel()

	handler := &warnCaptureHandler{}
	logger := slog.New(handler)

	rt := newControlPlaneRoundTripper(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}), "api-key", "ua", "", map[string]string{
		"authorization":           "Bearer attacker",
		"User-Agent":              "custom-agent",
		headerTunnelClientVersion: "dev",
		clientinstance.HeaderName: "configured-id",
	}, logger)

	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if !assert.NoError(t, err, "build request") {
		return
	}

	_, err = rt.RoundTrip(req)
	assert.NoError(t, err, "round trip failed")
	assert.True(t, handler.seenOverride, "expected protected-header warning")
	assert.Equal(t, "Bearer api-key", req.Header.Get("Authorization"), "expected Authorization to be preserved")
	assert.Equal(t, "ua", req.Header.Get("User-Agent"), "expected User-Agent to be preserved")
	assert.Equal(t, version.Version, req.Header.Get(headerTunnelClientVersion), "expected client version to be preserved")
	assert.Equal(t, clientinstance.ID(), req.Header.Get(clientinstance.HeaderName), "expected client instance ID to be preserved")
}

func TestControlPlaneRoundTripperNoWarningWhenValueMatches(t *testing.T) {
	t.Parallel()

	handler := &warnCaptureHandler{}
	logger := slog.New(handler)

	rt := newControlPlaneRoundTripper(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}), "api-key", "ua", "", map[string]string{"X-Same": "same"}, logger)

	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if !assert.NoError(t, err, "build request") {
		return
	}
	req.Header.Set("X-Same", "same")

	_, err = rt.RoundTrip(req)
	assert.NoError(t, err, "round trip failed")
	assert.False(t, handler.seenOverride, "did not expect override warning for identical value")
	assert.Equal(t, "same", req.Header.Get("X-Same"))
}

func TestNewControlPlaneRoundTripperPanicsOnNilLogger(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(t,
		"control-plane round tripper: logger is required",
		func() {
			_ = newControlPlaneRoundTripper(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
			}), "api-key", "ua", "", nil, nil)
		},
	)
}
