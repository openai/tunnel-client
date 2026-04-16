package health

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.openai.org/api/tunnel-client/pkg/mcpclient"
	"go.openai.org/api/tunnel-client/pkg/oauth"
)

func TestBuildHealthURLAssignsRandomPort(t *testing.T) {
	t.Parallel()

	t.Run("UnspecifiedHostDefaultsToLocalhost", func(t *testing.T) {
		t.Parallel()

		ln := listen(t, "tcp", ":0")
		defer func() {
			require.NoError(t, ln.Close())
		}()

		healthURL := mustBuildHealthURL(t, ":0", ln.Addr())
		parsed := parseURL(t, healthURL)

		require.Equal(t, "localhost", parsed.Hostname())
		require.Equal(t, portString(t, ln.Addr()), parsed.Port())
	})

	t.Run("IPv4Loopback", func(t *testing.T) {
		t.Parallel()

		ln := listen(t, "tcp4", "127.0.0.1:0")
		defer func() {
			require.NoError(t, ln.Close())
		}()

		healthURL := mustBuildHealthURL(t, "127.0.0.1:0", ln.Addr())
		parsed := parseURL(t, healthURL)

		require.Equal(t, "127.0.0.1", parsed.Hostname())
		require.Equal(t, portString(t, ln.Addr()), parsed.Port())
	})

	t.Run("IPv6Loopback", func(t *testing.T) {
		ln, err := net.Listen("tcp6", "[::1]:0")
		if err != nil {
			t.Skipf("ipv6 loopback not available: %v", err)
		}
		defer func() {
			require.NoError(t, ln.Close())
		}()

		healthURL := mustBuildHealthURL(t, "[::1]:0", ln.Addr())
		parsed := parseURL(t, healthURL)

		require.Equal(t, "::1", parsed.Hostname())
		require.Equal(t, portString(t, ln.Addr()), parsed.Port())
	})
}

func TestBuildHealthURLWithWildcardListenAddr(t *testing.T) {
	t.Parallel()

	t.Run("UsesResolvedListenerIP", func(t *testing.T) {
		t.Parallel()

		ln := listen(t, "tcp4", "127.0.0.1:0")
		defer func() {
			require.NoError(t, ln.Close())
		}()

		healthURL := mustBuildHealthURL(t, "0.0.0.0:0", ln.Addr())
		parsed := parseURL(t, healthURL)

		require.Equal(t, "127.0.0.1", parsed.Hostname())
		require.Equal(t, portString(t, ln.Addr()), parsed.Port())
	})

	t.Run("DefaultsToLocalhostWhenListenerIPUnspecified", func(t *testing.T) {
		t.Parallel()

		ln := listen(t, "tcp4", "0.0.0.0:0")
		defer func() {
			require.NoError(t, ln.Close())
		}()

		tcpAddr := ln.Addr().(*net.TCPAddr)
		healthURL := mustBuildHealthURL(t, "0.0.0.0:0", &net.TCPAddr{
			IP:   net.IPv4zero,
			Port: tcpAddr.Port,
		})
		parsed := parseURL(t, healthURL)

		require.Equal(t, "localhost", parsed.Hostname())
		require.Equal(t, portString(t, ln.Addr()), parsed.Port())
	})
}

func TestBuildHealthURLRejectsNonTCPAddr(t *testing.T) {
	t.Parallel()

	_, err := buildHealthURL(":0", fakeAddr{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected *net.TCPAddr")
}

func TestPreferredHealthHostUsesListenerIPWhenListenAddrHostUnspecified(t *testing.T) {
	t.Parallel()

	host := preferredHealthHost(":0", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	require.Equal(t, "127.0.0.1", host)
}

func TestPreferredHealthHostWithWildcardListenAddr(t *testing.T) {
	t.Parallel()

	ln := listen(t, "tcp4", "127.0.0.1:0")
	defer func() {
		require.NoError(t, ln.Close())
	}()

	host := preferredHealthHost("0.0.0.0:0", ln.Addr().(*net.TCPAddr))
	require.Equal(t, "127.0.0.1", host)
}

func TestRemoveURLFileMissingDoesNotError(t *testing.T) {
	t.Parallel()

	service := &healthService{urlFile: filepath.Join(t.TempDir(), "health-url-does-not-exist")}
	require.NoError(t, service.removeURLFile())
}

func TestIsUnspecifiedHost(t *testing.T) {
	t.Parallel()

	require.True(t, isUnspecifiedHost("0.0.0.0"))
	require.True(t, isUnspecifiedHost("::"))
	require.False(t, isUnspecifiedHost("127.0.0.1"))
	require.False(t, isUnspecifiedHost("localhost"))
}

func TestOkHandler(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	okHandler("live")(rec, req)

	res := rec.Result()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Contains(t, res.Header.Get("Content-Type"), "text/plain")
}

func TestReadinessHandler(t *testing.T) {
	t.Parallel()

	t.Run("ReadyWhenStateNil", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()

		readinessHandler(nil, nil)(rec, req)

		res := rec.Result()
		require.Equal(t, http.StatusOK, res.StatusCode)
	})

	t.Run("NotReadyWhenPending", func(t *testing.T) {
		t.Parallel()

		state := oauth.NewDiscoveryState()

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()

		readinessHandler(state, nil)(rec, req)

		res := rec.Result()
		require.Equal(t, http.StatusServiceUnavailable, res.StatusCode)
	})

	t.Run("ReadyWhenDone", func(t *testing.T) {
		t.Parallel()

		state := oauth.NewDiscoveryState()
		state.Set(nil, nil, nil, nil)

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()

		readinessHandler(state, nil)(rec, req)

		res := rec.Result()
		require.Equal(t, http.StatusOK, res.StatusCode)
	})

	t.Run("NotReadyWhenMCPStartupProbePending", func(t *testing.T) {
		t.Parallel()

		oauthState := oauth.NewDiscoveryState()
		oauthState.Set(nil, nil, nil, nil)
		probeState := mcpclient.NewProbeState()

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()

		readinessHandler(oauthState, probeState)(rec, req)

		res := rec.Result()
		require.Equal(t, http.StatusServiceUnavailable, res.StatusCode)
		require.Equal(t, "mcp startup probe pending", rec.Body.String())
	})

	t.Run("NotReadyWhenOAuthDiscoveryFails", func(t *testing.T) {
		t.Parallel()

		state := oauth.NewDiscoveryState()
		state.Set(nil, errors.New("metadata fetch failed"), nil, nil)

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()

		readinessHandler(state, nil)(rec, req)

		res := rec.Result()
		require.Equal(t, http.StatusServiceUnavailable, res.StatusCode)
		require.Contains(t, rec.Body.String(), "oauth discovery failed")
	})

	t.Run("ReadyWhenOAuthDiscoveryIsDisabledForNonHTTPTransport", func(t *testing.T) {
		t.Parallel()

		state := oauth.NewDiscoveryState()
		state.Set(nil, errors.New(`oauth discovery disabled for transport "stdio"`), nil, nil)

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()

		readinessHandler(state, nil)(rec, req)

		res := rec.Result()
		require.Equal(t, http.StatusOK, res.StatusCode)
		require.Equal(t, "ready", rec.Body.String())
	})

	t.Run("ReadyWhenOAuthDiscoveryServerURLIsNotConfigured", func(t *testing.T) {
		t.Parallel()

		state := oauth.NewDiscoveryState()
		state.Set(nil, errors.New("oauth discovery server URL is not configured"), nil, nil)

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()

		readinessHandler(state, nil)(rec, req)

		res := rec.Result()
		require.Equal(t, http.StatusOK, res.StatusCode)
		require.Equal(t, "ready", rec.Body.String())
	})

	t.Run("ReadyWhenOAuthDiscoveryIsNotAdvertised", func(t *testing.T) {
		t.Parallel()

		state := oauth.NewDiscoveryState()
		state.Set(&oauth.DiscoveryResult{
			Attempts: []oauth.DiscoveryAttempt{
				{
					URL:        "http://localhost:3001/.well-known/oauth-protected-resource/mcp",
					Source:     oauth.DiscoverySourceWellKnownPath,
					Tried:      true,
					StatusCode: http.StatusNotFound,
				},
				{
					URL:        "http://localhost:3001/.well-known/oauth-protected-resource",
					Source:     oauth.DiscoverySourceWellKnownRoot,
					Tried:      true,
					StatusCode: http.StatusNotFound,
				},
			},
		}, errors.New("oauth discovery invalid metadata from http://localhost:3001/.well-known/oauth-protected-resource: decode protected resource metadata: invalid character '<' looking for beginning of value"), &oauth.WWWAuthenticateProbeStatus{
			Attempted: true,
			Error:     "oauth discovery: WWW-Authenticate probe GET got status 200",
		}, nil)

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()

		readinessHandler(state, nil)(rec, req)

		res := rec.Result()
		require.Equal(t, http.StatusOK, res.StatusCode)
		require.Equal(t, "ready", rec.Body.String())
	})

	t.Run("ReadyWhenOAuthDiscoveryIsNotAdvertisedAndWWWAuthenticateProbeGets400", func(t *testing.T) {
		t.Parallel()

		state := oauth.NewDiscoveryState()
		state.Set(&oauth.DiscoveryResult{
			Attempts: []oauth.DiscoveryAttempt{
				{
					URL:        "http://localhost:3001/.well-known/oauth-protected-resource/mcp",
					Source:     oauth.DiscoverySourceWellKnownPath,
					Tried:      true,
					StatusCode: http.StatusNotFound,
				},
				{
					URL:        "http://localhost:3001/.well-known/oauth-protected-resource",
					Source:     oauth.DiscoverySourceWellKnownRoot,
					Tried:      true,
					StatusCode: http.StatusNotFound,
				},
			},
		}, errors.New("oauth discovery invalid metadata from http://localhost:3001/.well-known/oauth-protected-resource: decode protected resource metadata: invalid character '<' looking for beginning of value"), &oauth.WWWAuthenticateProbeStatus{
			Attempted: true,
			Error:     "oauth discovery: WWW-Authenticate probe GET got status 400",
		}, nil)

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()

		readinessHandler(state, nil)(rec, req)

		res := rec.Result()
		require.Equal(t, http.StatusOK, res.StatusCode)
		require.Equal(t, "ready", rec.Body.String())
	})

	t.Run("ReadyButExplicitWhenMCPInitializeRequiresAuth", func(t *testing.T) {
		t.Parallel()

		oauthState := oauth.NewDiscoveryState()
		oauthState.Set(nil, nil, nil, nil)

		probeState := mcpclient.NewProbeState()
		probeState.Set(errors.New(`calling "initialize": Unauthorized`))

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()

		readinessHandler(oauthState, probeState)(rec, req)

		res := rec.Result()
		require.Equal(t, http.StatusOK, res.StatusCode)
		require.Contains(t, rec.Body.String(), "requires auth")
	})

	t.Run("NotReadyWhenMCPProbeFails", func(t *testing.T) {
		t.Parallel()

		oauthState := oauth.NewDiscoveryState()
		oauthState.Set(nil, nil, nil, nil)

		probeState := mcpclient.NewProbeState()
		probeState.Set(errors.New("dial tcp 127.0.0.1:1: connection refused"))

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()

		readinessHandler(oauthState, probeState)(rec, req)

		res := rec.Result()
		require.Equal(t, http.StatusServiceUnavailable, res.StatusCode)
		require.Contains(t, rec.Body.String(), "mcp probe failed")
	})

	t.Run("ReadyWhenMCPProbeTimesOut", func(t *testing.T) {
		t.Parallel()

		oauthState := oauth.NewDiscoveryState()
		oauthState.Set(nil, nil, nil, nil)

		probeState := mcpclient.NewProbeState()
		probeState.Set(mcpclient.NewProbeTimeoutError(2*time.Second, context.DeadlineExceeded))

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()

		readinessHandler(oauthState, probeState)(rec, req)

		res := rec.Result()
		require.Equal(t, http.StatusOK, res.StatusCode)
		require.Contains(t, rec.Body.String(), "probe timed out")
	})

}

type fakeAddr struct{}

func (fakeAddr) Network() string { return "fake" }
func (fakeAddr) String() string  { return "fake" }

func mustBuildHealthURL(t *testing.T, listenAddr string, addr net.Addr) string {
	t.Helper()

	healthURL, err := buildHealthURL(listenAddr, addr)
	require.NoError(t, err)
	return healthURL
}

func listen(t *testing.T, network, address string) net.Listener {
	t.Helper()

	ln, err := net.Listen(network, address)
	require.NoErrorf(t, err, "listen %s %s", network, address)
	return ln
}

func parseURL(t *testing.T, raw string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(raw)
	require.NoErrorf(t, err, "parse URL %s", raw)
	return parsed
}

func portString(t *testing.T, addr net.Addr) string {
	t.Helper()

	tcpAddr, ok := addr.(*net.TCPAddr)
	require.Truef(t, ok, "listener addr %T is not *net.TCPAddr", addr)
	require.NotZero(t, tcpAddr.Port, "listener should assign a random port")
	return strconv.Itoa(tcpAddr.Port)
}
