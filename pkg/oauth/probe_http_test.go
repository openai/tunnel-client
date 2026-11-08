package oauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/healthstate"
)

func startupHTTPProbeDetails(t *testing.T, state *DiscoveryState) (healthstate.ComponentSnapshot, healthstate.OAuthDetails) {
	t.Helper()
	snapshot := (&componentHealth{state: state, enabled: true}).Snapshot(time.Now())
	details, ok := snapshot.Details.(healthstate.OAuthDetails)
	require.True(t, ok)
	return snapshot, details
}

func assertStartupHTTPProbe(
	t *testing.T,
	observation healthstate.OAuthProbeObservation,
	kind probeType,
	method string,
	status int,
	disposition probeDisposition,
	responseRequestID string,
) {
	t.Helper()
	require.NotEmpty(t, observation.ProbeID)
	require.Equal(t, "startup", observation.Origin)
	require.Equal(t, string(kind), observation.ProbeType)
	require.Equal(t, method, observation.Method)
	require.Equal(t, status, observation.StatusCode)
	require.Equal(t, string(disposition), observation.Disposition)
	require.Equal(t, responseRequestID, observation.ResponseRequestID)
	require.False(t, observation.StartedAt.IsZero())
	if status == 0 {
		require.Nil(t, observation.ResponseAt)
	} else {
		require.NotNil(t, observation.ResponseAt)
	}
	if disposition == probeDispositionPending {
		require.Nil(t, observation.CompletedAt)
	} else {
		require.NotNil(t, observation.CompletedAt)
	}
}

type signalFirstReadBody struct {
	io.ReadCloser
	started chan struct{}
	once    sync.Once
}

func (b *signalFirstReadBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	return b.ReadCloser.Read(p)
}

func TestStartupProbeFallbackSequencePublishesHeadersBeforeBody(t *testing.T) {
	t.Parallel()
	const metadataPath = "/.well-known/oauth-protected-resource/mcp"
	releaseBody := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseBody) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/mcp" && r.Method == http.MethodPost:
			w.Header().Set("X-Request-Id", "post-415")
			w.WriteHeader(http.StatusUnsupportedMediaType)
		case r.URL.Path == "/mcp" && r.Method == http.MethodGet:
			w.Header().Set("X-Request-Id", "get-405")
			w.WriteHeader(http.StatusMethodNotAllowed)
		case r.URL.Path == metadataPath:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Request-Id", "metadata-200")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-releaseBody
			_, _ = fmt.Fprintf(w, `{"resource":"http://%s/mcp"}`, r.Host)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(release)

	readStarted := make(chan struct{})
	client := server.Client()
	base := client.Transport
	client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		resp, err := base.RoundTrip(req)
		if resp != nil && req.URL.Path == metadataPath {
			resp.Body = &signalFirstReadBody{ReadCloser: resp.Body, started: readStarted}
		}
		return resp, err
	})
	state := NewDiscoveryState()
	ctx := withStartupProbeSession(t.Context(), state, t.Context())
	candidates, probe, err := BuildOAuthDiscoveryCandidates(ctx, client, mustParseURL(t, server.URL+"/mcp"), testLogger())
	require.NoError(t, err)
	completed := make(chan error, 1)
	go func() {
		started := time.Now()
		resp, sourceURL, attempts, fetchErr := FetchOAuthMetadata(ctx, client, candidates, testLogger())
		result := BuildDiscoveryResult(resp, sourceURL, started, attempts)
		state.setWithProbe(result, fetchErr, probe, candidatesToStrings(candidates), takeStartupProbe(ctx))
		completed <- fetchErr
	}()

	select {
	case <-readStarted:
	case <-time.After(time.Second):
		t.Fatal("metadata body read did not start")
	}
	snapshot, details := startupHTTPProbeDetails(t, state)
	require.Equal(t, "pending", snapshot.State)
	require.False(t, details.DiscoveryComplete)
	require.Len(t, details.RecentProbes, 2)
	assertStartupHTTPProbe(t, details.RecentProbes[0], probeTypeWWWAuthenticate, http.MethodPost, http.StatusUnsupportedMediaType, probeDispositionFallback, "post-415")
	assertStartupHTTPProbe(t, details.RecentProbes[1], probeTypeWWWAuthenticate, http.MethodGet, http.StatusMethodNotAllowed, probeDispositionFallback, "get-405")
	require.Len(t, details.ActiveProbes, 1)
	assertStartupHTTPProbe(t, details.ActiveProbes[0], probeTypeProtectedResourceMetadata, http.MethodGet, http.StatusOK, probeDispositionPending, "metadata-200")

	release()
	select {
	case err := <-completed:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("metadata discovery did not finish after body release")
	}
	snapshot, details = startupHTTPProbeDetails(t, state)
	require.Equal(t, "complete", snapshot.State)
	require.True(t, details.DiscoveryComplete)
	require.Empty(t, details.ActiveProbes)
	require.Len(t, details.RecentProbes, 3)
	assertStartupHTTPProbe(t, details.RecentProbes[2], probeTypeProtectedResourceMetadata, http.MethodGet, http.StatusOK, probeDispositionAccepted, "metadata-200")
	require.NotEqual(t, details.RecentProbes[0].ProbeID, details.RecentProbes[1].ProbeID)
	require.NotEqual(t, details.RecentProbes[1].ProbeID, details.RecentProbes[2].ProbeID)
}

func TestStartupProbeValidChallengeRecordsRedirectExchangeAndSkipsGET(t *testing.T) {
	t.Parallel()
	var topLevelGETs atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mcp":
			if r.Method == http.MethodGet {
				topLevelGETs.Add(1)
			}
			w.Header().Set("X-Request-Id", "redirect-302")
			http.Redirect(w, r, "/challenge", http.StatusFound)
		case "/challenge":
			w.Header().Set("X-Request-Id", "challenge-401")
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="http://%s/metadata"`, r.Host))
			w.WriteHeader(http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	state := NewDiscoveryState()
	ctx := withStartupProbeSession(t.Context(), state, t.Context())
	candidates, probe, err := BuildOAuthDiscoveryCandidates(ctx, server.Client(), mustParseURL(t, server.URL+"/mcp"), testLogger())
	require.NoError(t, err)
	require.Equal(t, server.URL+"/metadata", probe.URL)
	require.Equal(t, server.URL+"/metadata", candidates[0].URL.String())
	require.Zero(t, topLevelGETs.Load())
	_, details := startupHTTPProbeDetails(t, state)
	require.Empty(t, details.ActiveProbes)
	require.Len(t, details.RecentProbes, 2)
	assertStartupHTTPProbe(t, details.RecentProbes[0], probeTypeWWWAuthenticate, http.MethodPost, http.StatusFound, probeDispositionRedirect, "redirect-302")
	assertStartupHTTPProbe(t, details.RecentProbes[1], probeTypeWWWAuthenticate, http.MethodGet, http.StatusUnauthorized, probeDispositionAccepted, "challenge-401")
}

func TestStartupProbeOwnerCancellationDuringChallenge(t *testing.T) {
	t.Parallel()
	owner, cancelOwner := context.WithCancel(t.Context())
	defer cancelOwner()
	fetchCtx, cancelFetch := context.WithTimeout(owner, DefaultDiscoveryTimeout)
	defer cancelFetch()
	state := NewDiscoveryState()
	ctx := withStartupProbeSession(fetchCtx, state, owner)
	started := make(chan struct{})
	var startedOnce sync.Once
	var transportCalls atomic.Int32
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		transportCalls.Add(1)
		startedOnce.Do(func() { close(started) })
		<-owner.Done()
		return nil, owner.Err()
	})}
	serverURL := mustParseURL(t, "https://mcp.example.test/mcp")
	completed := make(chan error, 1)
	go func() {
		candidates, _, err := BuildOAuthDiscoveryCandidates(ctx, client, serverURL, testLogger())
		if err == nil {
			_, _, _, err = FetchOAuthMetadata(ctx, client, candidates, testLogger())
		}
		completed <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("challenge request did not start")
	}
	cancelOwner()
	select {
	case err := <-completed:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("discovery did not return after owner cancellation")
	}
	_, details := startupHTTPProbeDetails(t, state)
	require.Empty(t, details.RecentProbes, "decisions after observable owner cancellation leave receipts for owner cleanup")
	require.Len(t, details.ActiveProbes, int(transportCalls.Load()))
	require.NotEmpty(t, details.ActiveProbes)

	// This is the startup owner's cleanup after discovery returns. Child
	// request cancellation alone never emits a cancelled disposition.
	state.cancelActiveProbes()
	snapshot, details := startupHTTPProbeDetails(t, state)
	require.Equal(t, "pending", snapshot.State)
	require.False(t, state.IsDone())
	require.Empty(t, details.ActiveProbes)
	require.Len(t, details.RecentProbes, int(transportCalls.Load()))
	for _, observation := range details.RecentProbes {
		require.Zero(t, observation.StatusCode)
		require.Nil(t, observation.ResponseAt)
		require.Equal(t, string(probeDispositionCancelled), observation.Disposition)
	}
}

func TestStartupProbeDerivedDeadlineKeepsFallbackAndRetry(t *testing.T) {
	t.Parallel()
	owner := t.Context()
	// An already-expired fetch deadline deterministically exercises both its
	// child challenge deadline and metadata retries without cancelling owner.
	fetchCtx, cancelFetch := context.WithDeadline(owner, time.Now().Add(-time.Second))
	defer cancelFetch()
	state := NewDiscoveryState()
	ctx := withStartupProbeSession(fetchCtx, state, owner)
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	})}
	candidates, _, err := BuildOAuthDiscoveryCandidates(ctx, client, mustParseURL(t, "https://mcp.example.test/mcp"), testLogger())
	require.NoError(t, err)
	_, _, _, err = FetchOAuthMetadata(ctx, client, candidates, testLogger())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, discoveryFailureTypeTimeoutOnly, classifyDiscoveryFailure(err))
	require.NoError(t, owner.Err())
	finishStartupProbe(ctx, probeDispositionRetry)
	_, details := startupHTTPProbeDetails(t, state)
	require.Empty(t, details.ActiveProbes)
	require.NotEmpty(t, details.RecentProbes)
	assertStartupHTTPProbe(t, details.RecentProbes[0], probeTypeWWWAuthenticate, http.MethodPost, 0, probeDispositionFallback, "")
	assertStartupHTTPProbe(t, details.RecentProbes[len(details.RecentProbes)-1], probeTypeProtectedResourceMetadata, http.MethodGet, 0, probeDispositionRetry, "")
	for _, observation := range details.RecentProbes {
		require.NotEqual(t, string(probeDispositionCancelled), observation.Disposition)
	}
}

func TestStartupProbeUnscopedDiscoveryIsIsolated(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mcp":
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="http://%s/metadata"`, r.Host))
			w.WriteHeader(http.StatusUnauthorized)
		case "/metadata":
			_, _ = fmt.Fprintf(w, `{"resource":"http://%s/mcp"}`, r.Host)
		case "/.well-known/oauth-authorization-server":
			_, _ = fmt.Fprintf(w, `{"issuer":"http://%s"}`, r.Host)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	state := NewDiscoveryState()
	scoped := withStartupProbeSession(t.Context(), state, t.Context())
	origin := mustParseURL(t, server.URL+"/mcp")
	_, _, err := BuildOAuthDiscoveryCandidates(scoped, client, origin, testLogger())
	require.NoError(t, err)
	_, before := startupHTTPProbeDetails(t, state)
	require.Len(t, before.RecentProbes, 1)
	assertStartupHTTPProbe(t, before.RecentProbes[0], probeTypeWWWAuthenticate, http.MethodPost, http.StatusUnauthorized, probeDispositionAccepted, "")

	unscoped := t.Context()
	require.Same(t, client, withStartupProbeHTTPClient(unscoped, client, probeTypeWWWAuthenticate))
	candidates, _, err := BuildOAuthDiscoveryCandidates(unscoped, client, origin, testLogger())
	require.NoError(t, err)
	_, _, _, err = FetchOAuthMetadata(unscoped, client, candidates, testLogger())
	require.NoError(t, err)
	_, err = FetchAuthServerMetadata(unscoped, client, server.URL, origin)
	require.NoError(t, err)
	_, after := startupHTTPProbeDetails(t, state)
	require.Equal(t, before, after)
}

func TestStartupProbeTrustRejectionHasNoResponseStatus(t *testing.T) {
	t.Parallel()
	var transportCalls atomic.Int32
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		transportCalls.Add(1)
		return nil, context.Canceled
	})}
	state := NewDiscoveryState()
	ctx := withStartupProbeSession(t.Context(), state, t.Context())
	candidates := []DiscoveryCandidate{{URL: mustParseURL(t, "https://outside.example.test/metadata")}}
	resp, sourceURL, attempts, err := FetchOAuthMetadata(ctx, client, candidates, nil, mustParseURL(t, "https://mcp.example.test"))
	require.ErrorContains(t, err, "origin is not trusted")
	state.setWithProbe(BuildDiscoveryResult(resp, sourceURL, time.Now(), attempts), err, nil, nil, takeStartupProbe(ctx))
	require.Zero(t, transportCalls.Load())
	snapshot, details := startupHTTPProbeDetails(t, state)
	require.Equal(t, "failed", snapshot.State)
	require.Empty(t, details.ActiveProbes)
	require.Len(t, details.RecentProbes, 1)
	assertStartupHTTPProbe(t, details.RecentProbes[0], probeTypeProtectedResourceMetadata, http.MethodGet, 0, probeDispositionTerminalFailure, "")
}

type legacyProbeCancellationTransport struct {
	started   chan struct{}
	cancelled chan struct{}
	once      sync.Once
}

func (t *legacyProbeCancellationTransport) RoundTrip(*http.Request) (*http.Response, error) {
	close(t.started)
	<-t.cancelled
	return nil, context.DeadlineExceeded
}

func (t *legacyProbeCancellationTransport) CancelRequest(*http.Request) {
	t.once.Do(func() { close(t.cancelled) })
}

func TestStartupProbeHTTPClientPreservesLegacyCancellation(t *testing.T) {
	t.Parallel()
	transport := &legacyProbeCancellationTransport{started: make(chan struct{}), cancelled: make(chan struct{})}
	t.Cleanup(func() { transport.CancelRequest(nil) })
	client := &http.Client{Transport: transport, Timeout: 20 * time.Millisecond}
	state := NewDiscoveryState()
	ctx := withStartupProbeSession(t.Context(), state, t.Context())
	observed := withStartupProbeHTTPClient(ctx, client, probeTypeWWWAuthenticate)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://mcp.example.test/mcp", strings.NewReader(""))
	require.NoError(t, err)
	completed := make(chan error, 1)
	go func() {
		_, err := observed.Do(req)
		completed <- err
	}()
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		t.Fatal("legacy transport did not start")
	}
	select {
	case err := <-completed:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("client timeout did not reach the legacy transport cancellation method")
	}
	finishStartupProbe(ctx, probeDispositionFallback)
	_, details := startupHTTPProbeDetails(t, state)
	require.Len(t, details.RecentProbes, 1)
	assertStartupHTTPProbe(t, details.RecentProbes[0], probeTypeWWWAuthenticate, http.MethodPost, 0, probeDispositionFallback, "")
}
