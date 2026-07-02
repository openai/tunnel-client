package oauth

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.openai.org/api/tunnel-client/pkg/headerscope"
)

func TestBuildResourceMetadataURLs(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "path-specific",
			raw:  "https://example.com/public/mcp",
			want: []string{
				"https://example.com/.well-known/oauth-protected-resource/public/mcp",
				"https://example.com/.well-known/oauth-protected-resource",
			},
		},
		{
			name: "root-only",
			raw:  "https://example.com/",
			want: []string{"https://example.com/.well-known/oauth-protected-resource"},
		},
		{
			name: "trailing-slash-path",
			raw:  "https://example.com/public/mcp/",
			want: []string{
				"https://example.com/.well-known/oauth-protected-resource/public/mcp",
				"https://example.com/.well-known/oauth-protected-resource",
			},
		},
		{
			name: "already-well-known",
			raw:  "https://example.com/.well-known/oauth-protected-resource/public/mcp",
			want: []string{
				"https://example.com/.well-known/oauth-protected-resource/public/mcp",
				"https://example.com/.well-known/oauth-protected-resource",
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			serverURL, err := url.Parse(tc.raw)
			require.NoError(t, err)

			got := metadataURLsToStrings(BuildResourceMetadataURLs(serverURL))
			require.Equal(t, tc.want, got)
		})
	}
}

func metadataURLsToStrings(urls []*url.URL) []string {
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		if u == nil {
			continue
		}
		out = append(out, u.String())
	}
	return out
}

func candidateURLsToStrings(candidates []DiscoveryCandidate) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.URL == nil {
			continue
		}
		out = append(out, candidate.URL.String())
	}
	return out
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestParseResourceMetadataFromWWWAuthenticate(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		header    string
		want      string
		expectErr bool
	}{
		{
			name:   "quoted",
			header: `Bearer realm="demo", resource_metadata="https://example.com/pr"`,
			want:   "https://example.com/pr",
		},
		{
			name:   "bare",
			header: "Bearer resource_metadata=https://example.com/pr",
			want:   "https://example.com/pr",
		},
		{
			name:   "multiple challenges picks bearer",
			header: `Basic realm="legacy", Bearer realm="demo", resource_metadata="https://example.com/pr"`,
			want:   "https://example.com/pr",
		},
		{
			name:      "ignores non bearer resource metadata",
			header:    `Basic realm="legacy", resource_metadata="https://evil.example/pr"`,
			expectErr: true,
		},
		{
			name:      "missing",
			header:    `Bearer realm="demo"`,
			expectErr: true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := parseResourceMetadataFromWWWAuthenticate(tc.header)
			if tc.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, parsed.String())
		})
	}
}

func TestBuildOAuthDiscoveryCandidatesProbeUsesDiscoveryContext(t *testing.T) {
	t.Parallel()

	probeURL := "https://mcp.example.test/.well-known/oauth-protected-resource/context"
	var methods []string
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			methods = append(methods, req.Method)
			require.True(t, headerscope.IsMCPDiscovery(req.Context()), "probe should mark requests as MCP discovery")
			require.Equal(t, "application/json", req.Header.Get("Accept"))
			require.NotEmpty(t, req.Header.Get("User-Agent"))

			statusCode := http.StatusUnauthorized
			header := make(http.Header)
			header.Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s"`, probeURL))
			if req.Method == http.MethodPost {
				statusCode = http.StatusNotFound
				header = make(http.Header)
			}
			return &http.Response{
				StatusCode: statusCode,
				Header:     header,
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}),
	}
	serverEndpoint, err := url.Parse("https://mcp.example.test/public/mcp")
	require.NoError(t, err)

	candidates, probe, err := BuildOAuthDiscoveryCandidates(
		context.Background(),
		client,
		serverEndpoint,
		testLogger(),
	)
	require.NoError(t, err)
	require.NotNil(t, probe)
	require.True(t, probe.Attempted)
	require.Equal(t, probeURL, probe.URL)
	require.Equal(t, []string{http.MethodPost, http.MethodGet}, methods)
	require.Equal(t, DiscoverySourceWWWAuthenticate, candidates[0].Source)
}

func TestBuildOAuthDiscoveryCandidatesRequiresLogger(t *testing.T) {
	t.Parallel()

	serverEndpoint, err := url.Parse("https://example.com/public/mcp")
	require.NoError(t, err)

	_, _, err = BuildOAuthDiscoveryCandidates(
		context.Background(),
		http.DefaultClient,
		serverEndpoint,
		nil,
	)
	require.Error(t, err)
}

func TestBuildOAuthDiscoveryCandidatesProbeFirst(t *testing.T) {
	t.Parallel()

	probePath := "/.well-known/oauth-protected-resource/custom"
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/public/mcp" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s"`, serverURL+probePath))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	serverURL = server.URL

	serverEndpoint, err := url.Parse(server.URL + "/public/mcp")
	require.NoError(t, err)

	candidates, probe, err := BuildOAuthDiscoveryCandidates(
		context.Background(),
		server.Client(),
		serverEndpoint,
		testLogger(),
	)
	require.NoError(t, err)

	require.NotNil(t, probe)
	require.True(t, probe.Attempted)
	require.Equal(t, server.URL+probePath, probe.URL)
	require.NotEmpty(t, candidates)
	require.Equal(t, DiscoverySourceWWWAuthenticate, candidates[0].Source)

	expected := []string{
		server.URL + probePath,
		server.URL + "/.well-known/oauth-protected-resource/public/mcp",
		server.URL + "/.well-known/oauth-protected-resource",
	}
	require.Equal(t, expected, candidateURLsToStrings(candidates))
}

func TestBuildOAuthDiscoveryCandidatesFallbackToGET(t *testing.T) {
	t.Parallel()

	probePath := "/.well-known/oauth-protected-resource/custom"
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/public/mcp" {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Method == http.MethodGet {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s"`, serverURL+probePath))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	serverURL = server.URL

	serverEndpoint, err := url.Parse(server.URL + "/public/mcp")
	require.NoError(t, err)

	candidates, probe, err := BuildOAuthDiscoveryCandidates(
		context.Background(),
		server.Client(),
		serverEndpoint,
		testLogger(),
	)
	require.NoError(t, err)

	require.NotNil(t, probe)
	require.True(t, probe.Attempted)
	require.Equal(t, server.URL+probePath, probe.URL)
	require.Empty(t, probe.Error)
	require.NotEmpty(t, candidates)
	require.Equal(t, DiscoverySourceWWWAuthenticate, candidates[0].Source)

	expected := []string{
		server.URL + probePath,
		server.URL + "/.well-known/oauth-protected-resource/public/mcp",
		server.URL + "/.well-known/oauth-protected-resource",
	}
	require.Equal(t, expected, candidateURLsToStrings(candidates))
}

func TestBuildOAuthDiscoveryCandidatesDedupesProbe(t *testing.T) {
	t.Parallel()

	probePath := "/.well-known/oauth-protected-resource/public/mcp"
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/public/mcp" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata=%s`, serverURL+probePath))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	serverURL = server.URL

	serverEndpoint, err := url.Parse(server.URL + "/public/mcp")
	require.NoError(t, err)

	candidates, probe, err := BuildOAuthDiscoveryCandidates(
		context.Background(),
		server.Client(),
		serverEndpoint,
		testLogger(),
	)
	require.NoError(t, err)

	require.NotNil(t, probe)
	require.True(t, probe.Attempted)
	require.Equal(t, server.URL+probePath, probe.URL)
	require.NotEmpty(t, candidates)
	require.Equal(t, DiscoverySourceWWWAuthenticate, candidates[0].Source)

	expected := []string{
		server.URL + probePath,
		server.URL + "/.well-known/oauth-protected-resource",
	}
	require.Equal(t, expected, candidateURLsToStrings(candidates))
}

func TestBuildOAuthDiscoveryCandidatesMissingHeaderFallsBack(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/public/mcp" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	serverEndpoint, err := url.Parse(server.URL + "/public/mcp")
	require.NoError(t, err)

	candidates, probe, err := BuildOAuthDiscoveryCandidates(
		context.Background(),
		server.Client(),
		serverEndpoint,
		testLogger(),
	)
	require.NoError(t, err)

	require.NotNil(t, probe)
	require.True(t, probe.Attempted)
	require.Empty(t, probe.URL)
	require.NotEmpty(t, probe.Error)
	require.Len(t, candidates, 2)
	require.Equal(t, DiscoverySourceWellKnownPath, candidates[0].Source)
}

func TestBuildOAuthDiscoveryCandidatesProbeErrorBothMethods(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/public/mcp" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	serverEndpoint, err := url.Parse(server.URL + "/public/mcp")
	require.NoError(t, err)

	candidates, probe, err := BuildOAuthDiscoveryCandidates(
		context.Background(),
		server.Client(),
		serverEndpoint,
		testLogger(),
	)
	require.NoError(t, err)

	require.NotNil(t, probe)
	require.True(t, probe.Attempted)
	require.Empty(t, probe.URL)
	require.NotEmpty(t, probe.Error)
	require.Contains(t, probe.Error, "GET")
	require.Len(t, candidates, 2)
	require.Equal(t, DiscoverySourceWellKnownPath, candidates[0].Source)
}

func TestBuildOAuthDiscoveryCandidatesMissingResourceMetadataFallsBack(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/public/mcp" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="demo"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	serverEndpoint, err := url.Parse(server.URL + "/public/mcp")
	require.NoError(t, err)

	candidates, probe, err := BuildOAuthDiscoveryCandidates(
		context.Background(),
		server.Client(),
		serverEndpoint,
		testLogger(),
	)
	require.NoError(t, err)

	require.NotNil(t, probe)
	require.True(t, probe.Attempted)
	require.Empty(t, probe.URL)
	require.NotEmpty(t, probe.Error)
	require.Len(t, candidates, 2)
	require.Equal(t, DiscoverySourceWellKnownPath, candidates[0].Source)
}
