package oauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOAuthDiscoveryHeaderRequiresTrustedOrigin(t *testing.T) {
	t.Parallel()
	for _, trusted := range []bool{false, true} {
		t.Run(fmt.Sprintf("trusted=%v", trusted), func(t *testing.T) {
			t.Parallel()
			var hits atomic.Int32
			metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"resource":"https://resource.example/mcp"}`))
			}))
			t.Cleanup(metadata.Close)
			mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/mcp" {
					w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/metadata"`, metadata.URL))
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"resource":"https://fallback.example/mcp"}`))
			}))
			t.Cleanup(mcp.Close)
			var origins []*url.URL
			if trusted {
				origins = []*url.URL{mustParseURL(t, metadata.URL)}
			}
			candidates, _, err := BuildOAuthDiscoveryCandidates(t.Context(), mcp.Client(), mustParseURL(t, mcp.URL+"/mcp"), testLogger(), origins...)
			require.NoError(t, err)
			response, selected, attempts, err := FetchOAuthMetadata(t.Context(), mcp.Client(), candidates, testLogger())
			require.NoError(t, err)
			if trusted {
				require.EqualValues(t, 1, hits.Load())
				require.Equal(t, metadata.URL+"/metadata", selected.String())
			} else {
				require.Zero(t, hits.Load(), "untrusted metadata must be rejected before any request")
				require.Contains(t, attempts[0].Error, "origin is not trusted")
				require.Contains(t, string(response.Payload()), "fallback.example")
			}
		})
	}
}

func TestOAuthDiscoveryIssuerRequiresTrustedOrigin(t *testing.T) {
	t.Parallel()
	for _, trusted := range []bool{false, true} {
		t.Run(fmt.Sprintf("trusted=%v", trusted), func(t *testing.T) {
			t.Parallel()
			var hits atomic.Int32
			issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"issuer":"http://%s","token_endpoint":"http://%s/token"}`, r.Host, r.Host)
			}))
			t.Cleanup(issuer.Close)
			options := URLBundleOptions{TrustedMCPURL: mustParseURL(t, "http://mcp.internal:8080/mcp")}
			if trusted {
				options.TrustedOAuthOrigins = []*url.URL{mustParseURL(t, issuer.URL)}
			}
			payload := []byte(fmt.Sprintf(`{"resource":"http://mcp.internal:8080/mcp","authorization_servers":[%q]}`, issuer.URL))
			_, result, err := BuildURLBundleFromPRMDWithAuthServerMetadata(t.Context(), issuer.Client(), payload, time.Now(), options.TrustedMCPURL, options, nil)
			require.NoError(t, err)
			require.NotNil(t, result)
			if trusted {
				require.EqualValues(t, 1, hits.Load())
				require.NotEmpty(t, result.SelectedURL)
			} else {
				require.Zero(t, hits.Load(), "untrusted issuer must not receive a metadata request")
				require.Empty(t, result.SelectedURL)
				require.Contains(t, result.Attempts[0].Error, "origin is not trusted")
			}
		})
	}
}

func TestOAuthDiscoveryRejectsUntrustedRepresentationsBeforeTransport(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ origin, destination string }{
		{"https://mcp.example", "http://169.254.169.254/latest/meta-data"},
		{"https://mcp.example", "http://10.0.0.1/admin"},
		{"https://mcp.example", "http://[::ffff:127.0.0.1]/admin"},
		{"http://127.0.0.1:3000", "http://127.0.0.1:3001/metadata"},
		{"https://mcp.example", "https://mcp.example./metadata"},
		{"https://[fe80::1%25ethA]", "https://[fe80::1%25etha]/metadata"},
		{"https://mcp.example", "http://mcp.example/metadata"},
		{"https://mcp.example", "https://mcp.example.evil/metadata"},
		{"https://mcp.example", "https://mcp.example@evil.example/metadata"},
		{"https://mcp.example", "https://user:secret@mcp.example/metadata"},
		{"https://mcp.example", "https://mcp.example/metadata#fragment"},
		{"https://mcp.example", "gopher://mcp.example/metadata"},
		{"https://mcp.example", "https://mcp.example:0/metadata"},
		{"https://mcp.example", "https://mcp.example:65536/metadata"},
	} {
		t.Run(tc.destination, func(t *testing.T) {
			t.Parallel()
			client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("untrusted URL reached the underlying transport")
				return nil, nil
			})}
			candidate := DiscoveryCandidate{URL: mustParseURL(t, tc.destination), Source: DiscoverySourceWWWAuthenticate}
			_, _, _, err := FetchOAuthMetadata(t.Context(), client, []DiscoveryCandidate{candidate}, nil, mustParseURL(t, tc.origin))
			require.Error(t, err)
		})
	}
}

func TestOAuthDiscoveryDoesNotInferTrustFromCandidateOrIssuer(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("unconfigured URL reached the underlying transport")
		return nil, nil
	})}
	target := mustParseURL(t, "http://127.0.0.1/metadata")
	for _, source := range []DiscoverySource{DiscoverySourceWWWAuthenticate, DiscoverySourceWellKnownRoot, DiscoverySourceWellKnownPath} {
		_, _, _, err := FetchOAuthMetadata(t.Context(), client, []DiscoveryCandidate{{URL: target, Source: source}}, nil)
		require.ErrorContains(t, err, "origin is not trusted")
	}
	_, err := FetchAuthServerMetadata(t.Context(), client, target.String())
	require.ErrorContains(t, err, "origin is not trusted")
	candidates := buildWellKnownCandidates(mustParseURL(t, "https://mcp.example/mcp"))
	candidates[0].URL.Host = "127.0.0.1"
	candidates = candidates[:1]
	_, _, _, err = FetchOAuthMetadata(t.Context(), client, candidates, nil)
	require.ErrorContains(t, err, "origin is not trusted")
}

func TestOAuthDiscoveryValidatesAfterRedirectCallback(t *testing.T) {
	t.Parallel()
	var calls, callbacks int
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			require.Equal(t, "mcp.example", req.URL.Host)
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"/next"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
		}),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			callbacks++
			req.URL.Host = "169.254.169.254"
			return nil
		},
	}
	_, _, _, err := FetchOAuthMetadata(context.Background(), client, buildWellKnownCandidates(mustParseURL(t, "http://mcp.example")), nil)
	require.ErrorContains(t, err, "origin is not trusted")
	require.Equal(t, 1, calls)
	require.Equal(t, 1, callbacks)
}

func TestOAuthDiscoveryTrustedOriginCanonicalForms(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ origin, destination string }{
		{"https://MCP.EXAMPLE", "https://mcp.example:443/metadata"},
		{"http://127.0.0.1:080", "http://127.0.0.1/metadata"},
		{"https://[::1]", "https://[0:0:0:0:0:0:0:1]:443/metadata"},
		{"http://internal:8080/", "http://internal:8080/custom?audience=mcp"},
	} {
		t.Run(tc.destination, func(t *testing.T) {
			t.Parallel()
			var calls int
			client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"resource":"https://resource.example/mcp"}`)), Request: req}, nil
			})}
			_, _, _, err := FetchOAuthMetadata(t.Context(), client, []DiscoveryCandidate{{URL: mustParseURL(t, tc.destination)}}, nil, mustParseURL(t, tc.origin))
			require.NoError(t, err)
			require.Equal(t, 1, calls)
		})
	}
}
