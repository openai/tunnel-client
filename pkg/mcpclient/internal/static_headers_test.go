package internal

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/openai/tunnel-client/pkg/headerscope"
)

func TestStaticHeadersRoundTripperScopesAndOverridesHeaders(t *testing.T) {
	t.Parallel()

	serverURL := mustParseURL(t, "https://mcp.example.test/mcp")
	var seen []http.Header
	rt := NewStaticHeadersRoundTripper(
		roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			seen = append(seen, req.Header.Clone())
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}),
		serverURL,
		map[string]string{
			"X-Internal-Auth":  "runtime-static",
			"X-Discovery-Auth": "runtime-generic",
		},
		map[string]string{
			"X-Discovery-Auth": "discovery-static",
		},
	)

	runtimeReq := mustRequest(t, context.Background(), "https://mcp.example.test/mcp")
	runtimeReq.Header.Set("X-Internal-Auth", "existing")
	_, err := rt.RoundTrip(runtimeReq)
	if err != nil {
		t.Fatalf("runtime RoundTrip returned error: %v", err)
	}

	discoveryCtx := headerscope.WithMCPDiscovery(context.Background())
	discoveryReq := mustRequest(t, discoveryCtx, "https://mcp.example.test/.well-known/oauth-protected-resource/mcp")
	_, err = rt.RoundTrip(discoveryReq)
	if err != nil {
		t.Fatalf("discovery RoundTrip returned error: %v", err)
	}

	unrelatedReq := mustRequest(t, discoveryCtx, "https://auth.example.test/.well-known/oauth-authorization-server")
	_, err = rt.RoundTrip(unrelatedReq)
	if err != nil {
		t.Fatalf("unrelated RoundTrip returned error: %v", err)
	}

	if len(seen) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(seen))
	}
	if got := seen[0].Get("X-Internal-Auth"); got != "runtime-static" {
		t.Fatalf("runtime X-Internal-Auth = %q, want runtime-static", got)
	}
	if got := seen[0].Get("X-Discovery-Auth"); got != "runtime-generic" {
		t.Fatalf("runtime X-Discovery-Auth = %q, want runtime-generic", got)
	}
	if got := seen[1].Get("X-Internal-Auth"); got != "runtime-static" {
		t.Fatalf("discovery X-Internal-Auth = %q, want runtime-static", got)
	}
	if got := seen[1].Get("X-Discovery-Auth"); got != "discovery-static" {
		t.Fatalf("discovery X-Discovery-Auth = %q, want discovery-static", got)
	}
	if diff := cmp.Diff(http.Header{}, seen[2]); diff != "" {
		t.Fatalf("unrelated host received headers (-want +got):\n%s", diff)
	}
}

func TestStaticHeadersRoundTripperLetsForwardedHeadersWin(t *testing.T) {
	t.Parallel()

	serverURL := mustParseURL(t, "https://mcp.example.test/mcp")
	base := NewForwardingRoundTripper(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("X-Internal-Auth"); got != "forwarded" {
			t.Fatalf("X-Internal-Auth = %q, want forwarded", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	}))
	rt := NewStaticHeadersRoundTripper(base, serverURL, map[string]string{"X-Internal-Auth": "static"}, nil)

	ctx, _, err := ContextWithHeaders(context.Background(), http.Header{"X-Internal-Auth": {"forwarded"}})
	if err != nil {
		t.Fatalf("ContextWithHeaders returned error: %v", err)
	}
	_, err = rt.RoundTrip(mustRequest(t, ctx, "https://mcp.example.test/mcp"))
	if err != nil {
		t.Fatalf("RoundTrip returned error: %v", err)
	}
}

func TestStaticHeadersRoundTripperHandlesNilRequestHeaders(t *testing.T) {
	t.Parallel()

	serverURL := mustParseURL(t, "https://mcp.example.test/mcp")
	rt := NewStaticHeadersRoundTripper(
		roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("X-Internal-Auth"); got != "static" {
				t.Fatalf("X-Internal-Auth = %q, want static", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}),
		serverURL,
		map[string]string{"X-Internal-Auth": "static"},
		nil,
	)

	req := mustRequest(t, context.Background(), "https://mcp.example.test/mcp")
	req.Header = nil
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip returned error: %v", err)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	return u
}

func mustRequest(t *testing.T, ctx context.Context, rawURL string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("build request %q: %v", rawURL, err)
	}
	return req
}
