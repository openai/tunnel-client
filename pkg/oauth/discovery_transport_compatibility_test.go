package oauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func discoveryCompatibilityFetchers() []struct {
	name  string
	fetch func(context.Context, *http.Client, *url.URL, ...*url.URL) error
} {
	return []struct {
		name  string
		fetch func(context.Context, *http.Client, *url.URL, ...*url.URL) error
	}{
		{
			name: "protected resource",
			fetch: func(ctx context.Context, client *http.Client, origin *url.URL, trusted ...*url.URL) error {
				endpoint := *origin
				endpoint.Path = defaultProtectedResourceMetadataURI
				_, _, _, err := FetchOAuthMetadata(ctx, client, []DiscoveryCandidate{{URL: &endpoint}}, nil, trusted...)
				return err
			},
		},
		{
			name: "authorization server",
			fetch: func(ctx context.Context, client *http.Client, origin *url.URL, trusted ...*url.URL) error {
				_, err := FetchAuthServerMetadata(ctx, client, origin.String(), trusted...)
				return err
			},
		},
	}
}

func TestDiscoveryTransportCompatibilityConfiguredLoopback(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="http://%s/metadata"`, r.Host))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"resource":"http://%s/mcp"}`, r.Host)
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	serverURL := mustParseURL(t, server.URL+"/mcp")
	candidates, _, err := BuildOAuthDiscoveryCandidates(t.Context(), client, serverURL, testLogger())
	require.NoError(t, err)
	response, selected, _, err := FetchOAuthMetadata(t.Context(), client, candidates, nil)
	require.NoError(t, err)
	require.Equal(t, server.URL+"/metadata", selected.String())
	require.JSONEq(t, fmt.Sprintf(`{"resource":%q}`, serverURL.String()), string(response.Payload()))
}

func TestDiscoveryTransportCompatibilityTLSCookiesAndRedirects(t *testing.T) {
	t.Parallel()
	for _, fetcher := range discoveryCompatibilityFetchers() {
		t.Run(fetcher.name, func(t *testing.T) {
			t.Parallel()
			outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintf(w, "%s/%s", r.Header.Get("X-Caller-Transport"), r.Header.Get("X-Caller-Redirect"))
			}))
			t.Cleanup(outside.Close)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/outside" {
					http.Redirect(w, r, outside.URL, http.StatusFound)
					return
				}
				initial, err := r.Cookie("initial")
				if err != nil || initial.Value != "caller" || r.Header.Get("X-Caller-Transport") != "preserved" {
					http.Error(w, "missing caller cookie or transport header", http.StatusUnauthorized)
					return
				}
				if r.URL.Path != "/metadata" {
					http.SetCookie(w, &http.Cookie{Name: "discovered", Value: "session", Path: "/", Secure: true})
					http.Redirect(w, r, "/metadata", http.StatusFound)
					return
				}
				discovered, err := r.Cookie("discovered")
				if err != nil || discovered.Value != "session" || r.Header.Get("X-Caller-Redirect") != "preserved" {
					http.Error(w, "missing redirect cookie or caller policy header", http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"resource":"https://%s/mcp","issuer":"https://%s"}`, r.Host, r.Host)
			}))
			t.Cleanup(server.Close)
			origin := mustParseURL(t, server.URL)
			client := server.Client()
			base := client.Transport
			client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				cloned := req.Clone(req.Context())
				cloned.Header.Set("X-Caller-Transport", "preserved")
				return base.RoundTrip(cloned)
			})
			jar, err := cookiejar.New(nil)
			require.NoError(t, err)
			jar.SetCookies(origin, []*http.Cookie{{Name: "initial", Value: "caller", Path: "/", Secure: true}})
			client.Jar = jar
			var redirects atomic.Int32
			client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
				redirects.Add(1)
				req.Header.Set("X-Caller-Redirect", "preserved")
				return nil
			}

			require.NoError(t, fetcher.fetch(t.Context(), client, origin, origin))
			require.EqualValues(t, 1, redirects.Load())
			require.Contains(t, jar.Cookies(origin), &http.Cookie{Name: "discovered", Value: "session", Quoted: false})

			// The original client must retain its own cross-origin redirect policy.
			resp, err := client.Get(server.URL + "/outside")
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, "preserved/preserved", string(body))
			require.EqualValues(t, 2, redirects.Load())
		})
	}
}

func TestDiscoveryTransportCompatibilityRedirectSentinel(t *testing.T) {
	t.Parallel()
	for _, fetcher := range discoveryCompatibilityFetchers() {
		t.Run(fetcher.name, func(t *testing.T) {
			t.Parallel()
			sentinel := errors.New("caller stopped the redirect")
			var redirectedRequests, callbacks atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/redirected" {
					redirectedRequests.Add(1)
				}
				http.Redirect(w, r, "/redirected", http.StatusFound)
			}))
			t.Cleanup(server.Close)
			client := server.Client()
			client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
				callbacks.Add(1)
				return sentinel
			}
			origin := mustParseURL(t, server.URL)
			err := fetcher.fetch(t.Context(), client, origin, origin)
			require.ErrorIs(t, err, sentinel)
			require.Positive(t, callbacks.Load())
			require.Zero(t, redirectedRequests.Load())
		})
	}
}

func TestDiscoveryTransportCompatibilityConfiguredProxy(t *testing.T) {
	t.Parallel()
	for _, fetcher := range discoveryCompatibilityFetchers() {
		for _, trusted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/trusted=%v", fetcher.name, trusted), func(t *testing.T) {
				t.Parallel()
				requests := make(chan string, 8)
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests <- r.URL.String()
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"resource":"http://mcp.example.invalid/mcp","issuer":"http://auth.example.invalid"}`)
				}))
				t.Cleanup(proxy.Close)
				transport := &http.Transport{Proxy: http.ProxyURL(mustParseURL(t, proxy.URL))}
				t.Cleanup(transport.CloseIdleConnections)
				client := &http.Client{Transport: transport, Timeout: time.Second}
				mcp := mustParseURL(t, "http://mcp.example.invalid")
				auth := mustParseURL(t, "http://auth.example.invalid")
				origins := []*url.URL{mcp}
				if trusted {
					origins = append(origins, auth)
				}

				err := fetcher.fetch(t.Context(), client, auth, origins...)
				if trusted {
					require.NoError(t, err)
					require.Len(t, requests, 1)
					require.True(t, strings.HasPrefix(<-requests, auth.String()+"/.well-known/"))
				} else {
					require.ErrorContains(t, err, "origin is not trusted")
					require.Empty(t, requests, "rejected origins must never reach the configured proxy")
				}
			})
		}
	}
}

func TestDiscoveryTransportCompatibilityClientTimeout(t *testing.T) {
	t.Parallel()
	origin := mustParseURL(t, "http://127.0.0.1:3001")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	parentDeadline, _ := ctx.Deadline()
	var requestDeadline time.Time
	client := &http.Client{
		Timeout: 20 * time.Millisecond,
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			requestDeadline, _ = req.Context().Deadline()
			<-req.Context().Done()
			return nil, req.Context().Err()
		}),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.String()+"/metadata", nil)
	require.NoError(t, err)
	_, err = withTrustedDiscoveryOrigins(client, []*url.URL{origin}).Do(req)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, requestDeadline.IsZero())
	require.True(t, requestDeadline.Before(parentDeadline), "the caller's shorter client timeout must reach the transport")
	require.NoError(t, ctx.Err(), "client timeout must not cancel the caller's context")
}

func TestDiscoveryTransportCompatibilityRequestCancellation(t *testing.T) {
	t.Parallel()
	for _, fetcher := range discoveryCompatibilityFetchers() {
		t.Run(fetcher.name, func(t *testing.T) {
			t.Parallel()
			origin := mustParseURL(t, "http://127.0.0.1:3001")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			started := make(chan struct{})
			var once sync.Once
			client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				once.Do(func() { close(started) })
				<-req.Context().Done()
				return nil, req.Context().Err()
			})}
			result := make(chan error, 1)
			go func() {
				result <- fetcher.fetch(ctx, client, origin, origin)
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("metadata request did not reach the caller's transport")
			}
			cancel()
			select {
			case err := <-result:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(2 * time.Second):
				t.Fatal("metadata fetch did not stop after request cancellation")
			}
		})
	}
}
