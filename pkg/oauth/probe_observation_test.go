package oauth

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newProbeTestState() *DiscoveryState {
	now := time.Date(2026, time.January, 2, 3, 4, 5, 6, time.FixedZone("test", 3600))
	return newDiscoveryStateWithClock(func() time.Time { return now })
}

func TestProbeObservationLifecycleAndCopies(t *testing.T) {
	t.Parallel()
	state := newProbeTestState()
	receipt := state.beginProbe(probeTypeWWWAuthenticate, http.MethodPost)
	require.NotNil(t, receipt)
	require.Regexp(t, `^[0-9a-f]{32}-[0-9a-f]{16}$`, receipt.ID())

	snapshot := state.healthSnapshot()
	require.Len(t, snapshot.activeProbes, 1)
	require.Empty(t, snapshot.recentProbes)
	require.Equal(t, "startup", snapshot.activeProbes[0].Origin)
	require.Equal(t, "www_authenticate", snapshot.activeProbes[0].ProbeType)
	require.Equal(t, "POST", snapshot.activeProbes[0].Method)
	require.Equal(t, "pending", snapshot.activeProbes[0].Disposition)
	require.Equal(t, time.UTC, snapshot.activeProbes[0].StartedAt.Location())
	require.Nil(t, snapshot.activeProbes[0].ResponseAt)
	require.Nil(t, snapshot.activeProbes[0].CompletedAt)

	receipt.observeResponse(http.StatusUnsupportedMediaType, http.Header{
		"x-ReQuEsT-iD":  {"req_123:abc.-"},
		"Authorization": {"secret"},
	})
	snapshot = state.healthSnapshot()
	require.Equal(t, http.StatusUnsupportedMediaType, snapshot.activeProbes[0].StatusCode)
	require.Equal(t, "req_123:abc.-", snapshot.activeProbes[0].ResponseRequestID)
	require.NotNil(t, snapshot.activeProbes[0].ResponseAt)
	require.Nil(t, snapshot.activeProbes[0].CompletedAt)
	snapshot.activeProbes[0].ProbeID = "mutated"
	*snapshot.activeProbes[0].ResponseAt = time.Time{}

	receipt.finish(probeDispositionFallback)
	snapshot = state.healthSnapshot()
	require.Empty(t, snapshot.activeProbes)
	require.Len(t, snapshot.recentProbes, 1)
	require.Equal(t, receipt.ID(), snapshot.recentProbes[0].ProbeID)
	require.Equal(t, "fallback", snapshot.recentProbes[0].Disposition)
	require.NotNil(t, snapshot.recentProbes[0].ResponseAt)
	require.False(t, snapshot.recentProbes[0].ResponseAt.IsZero())
	require.NotNil(t, snapshot.recentProbes[0].CompletedAt)
	snapshot.recentProbes[0].ResponseRequestID = "mutated"
	*snapshot.recentProbes[0].CompletedAt = time.Time{}

	receipt.observeResponse(http.StatusOK, http.Header{"X-Request-Id": {"late"}})
	receipt.finish(probeDispositionAccepted)
	snapshot = state.healthSnapshot()
	require.Equal(t, "req_123:abc.-", snapshot.recentProbes[0].ResponseRequestID)
	require.Equal(t, "fallback", snapshot.recentProbes[0].Disposition)
	require.False(t, snapshot.recentProbes[0].CompletedAt.IsZero())
	require.False(t, snapshot.limited)
}

func TestBoundedResponseRequestID(t *testing.T) {
	t.Parallel()
	maximum := strings.Repeat("a", maxResponseRequestIDSize)
	headerLimit := http.Header{"X-Request-Id": {"one"}}
	for i := range maxResponseHeaderNames - 1 {
		headerLimit[fmt.Sprintf("X-Other-%d", i)] = nil
	}
	tooManyHeaders := headerLimit.Clone()
	tooManyHeaders["X-Over-Limit"] = nil
	connectionLimit := make([]string, maxConnectionValues)
	for i := range connectionLimit {
		connectionLimit[i] = "keep-alive"
	}
	for _, tc := range []struct {
		name    string
		headers http.Header
		want    string
		limited bool
	}{
		{name: "absent"},
		{name: "valid", headers: http.Header{"x-request-id": {"Ab09._:-"}}, want: "Ab09._:-"},
		{name: "maximum", headers: http.Header{"X-Request-Id": {maximum}}, want: maximum},
		{name: "empty values", headers: http.Header{"X-Request-Id": nil}, limited: true},
		{name: "empty value", headers: http.Header{"X-Request-Id": {""}}, limited: true},
		{name: "too long", headers: http.Header{"X-Request-Id": {maximum + "a"}}, limited: true},
		{name: "duplicate values", headers: http.Header{"X-Request-Id": {"one", "two"}}, limited: true},
		{name: "duplicate case variants", headers: http.Header{"X-Request-Id": {"one"}, "x-request-id": {"two"}}, limited: true},
		{name: "space", headers: http.Header{"X-Request-Id": {"req 1"}}, limited: true},
		{name: "slash", headers: http.Header{"X-Request-Id": {"req/1"}}, limited: true},
		{name: "comma", headers: http.Header{"X-Request-Id": {"one,two"}}, limited: true},
		{name: "unicode", headers: http.Header{"X-Request-Id": {"réq"}}, limited: true},
		{name: "newline", headers: http.Header{"X-Request-Id": {"req\n1"}}, limited: true},
		{name: "connection nominated", headers: http.Header{"X-Request-Id": {"one"}, "cOnNeCtIoN": {"keep-alive, x-ReQuEsT-iD"}}, limited: true},
		{name: "other connection option", headers: http.Header{"X-Request-Id": {"one"}, "Connection": {"keep-alive"}}, want: "one"},
		{name: "header count limit", headers: headerLimit, want: "one"},
		{name: "too many headers", headers: tooManyHeaders, limited: true},
		{name: "connection value limit", headers: http.Header{"X-Request-Id": {"one"}, "Connection": connectionLimit}, want: "one"},
		{name: "too many connection values", headers: http.Header{"X-Request-Id": {"one"}, "Connection": make([]string, maxConnectionValues+1)}, limited: true},
		{name: "connection byte limit", headers: http.Header{"X-Request-Id": {"one"}, "Connection": {strings.Repeat("a", maxConnectionBytes)}}, want: "one"},
		{name: "too many connection bytes", headers: http.Header{"X-Request-Id": {"one"}, "Connection": {strings.Repeat("a", maxConnectionBytes+1)}}, limited: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, limited := boundedResponseRequestID(tc.headers)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.limited, limited)
		})
	}
}

func TestProbeObservationsOverlapAndCancellation(t *testing.T) {
	t.Parallel()
	state := newProbeTestState()
	first := state.beginProbe(probeTypeWWWAuthenticate, http.MethodPost)
	second := state.beginProbe(probeTypeWWWAuthenticate, http.MethodGet)
	first.finish(probeDispositionRedirect)
	third := state.beginProbe(probeTypeProtectedResourceMetadata, http.MethodGet)
	first.observeResponse(http.StatusOK, nil)
	first.finish(probeDispositionRetry)

	snapshot := state.healthSnapshot()
	require.Len(t, snapshot.activeProbes, 2)
	require.Equal(t, second.ID(), snapshot.activeProbes[0].ProbeID)
	require.Equal(t, third.ID(), snapshot.activeProbes[1].ProbeID)
	require.Equal(t, "redirect", snapshot.recentProbes[0].Disposition)

	state.cancelActiveProbes()
	second.finish(probeDispositionAccepted)
	third.observeResponse(http.StatusOK, nil)
	snapshot = state.healthSnapshot()
	require.Empty(t, snapshot.activeProbes)
	require.Len(t, snapshot.recentProbes, 3)
	require.Equal(t, second.ID(), snapshot.recentProbes[1].ProbeID)
	require.Equal(t, third.ID(), snapshot.recentProbes[2].ProbeID)
	require.Equal(t, "cancelled", snapshot.recentProbes[1].Disposition)
	require.Equal(t, "cancelled", snapshot.recentProbes[2].Disposition)
	require.False(t, state.IsDone())
	require.True(t, snapshot.completedAt.IsZero())

	state.Set(nil, nil, nil, nil)
	snapshot = state.healthSnapshot()
	require.True(t, state.IsDone())
	require.Equal(t, "cancelled", snapshot.recentProbes[2].Disposition)
	require.Nil(t, state.beginProbe(probeTypeWWWAuthenticate, http.MethodPost))
	require.True(t, state.healthSnapshot().limited)
}

func TestProbeObservationCapacityAndHistory(t *testing.T) {
	t.Parallel()
	state := newProbeTestState()
	receipts := make([]*probeReceipt, 0, maxActiveProbes+5)
	for range maxActiveProbes {
		receipt := state.beginProbe(probeTypeProtectedResourceMetadata, http.MethodGet)
		require.NotNil(t, receipt)
		receipts = append(receipts, receipt)
	}
	require.Nil(t, state.beginProbe(probeTypeProtectedResourceMetadata, http.MethodGet))
	require.True(t, state.healthSnapshot().limited)
	for _, receipt := range receipts {
		receipt.finish(probeDispositionAccepted)
	}
	for range 5 {
		receipt := state.beginProbe(probeTypeAuthorizationServerMetadata, http.MethodGet)
		require.NotNil(t, receipt)
		receipts = append(receipts, receipt)
		receipt.finish(probeDispositionRetry)
	}
	snapshot := state.healthSnapshot()
	require.Empty(t, snapshot.activeProbes)
	require.Len(t, snapshot.recentProbes, maxRecentProbes)
	for i, observation := range snapshot.recentProbes {
		require.Equal(t, receipts[i+5].ID(), observation.ProbeID)
	}
	require.True(t, snapshot.limited)
}

func TestProbeObservationInvalidDataIsOmitted(t *testing.T) {
	t.Parallel()
	state := newProbeTestState()
	receipt := state.beginProbe(probeTypeWWWAuthenticate, "private-method")
	require.NotNil(t, receipt)
	receipt.observeResponse(1000, http.Header{"X-Request-Id": {"private/value"}})
	receipt.observeResponse(http.StatusOK, http.Header{"X-Request-Id": {"second"}})
	for _, disposition := range []probeDisposition{
		probeDispositionPending,
		probeDispositionNotAdvertised,
		probeDispositionTerminalFailure,
		probeDispositionCancelled,
	} {
		receipt.finish(disposition)
	}
	snapshot := state.healthSnapshot()
	require.Len(t, snapshot.activeProbes, 1)
	require.Equal(t, "OTHER", snapshot.activeProbes[0].Method)
	require.Zero(t, snapshot.activeProbes[0].StatusCode)
	require.Empty(t, snapshot.activeProbes[0].ResponseRequestID)
	require.Equal(t, "pending", snapshot.activeProbes[0].Disposition)
	require.True(t, snapshot.limited)
	receipt.finish(probeDispositionAccepted)
	require.Nil(t, state.beginProbe(probeType("private-type"), http.MethodGet))
	require.True(t, state.healthSnapshot().limited)

	var nilState *DiscoveryState
	var nilReceipt *probeReceipt
	require.Nil(t, nilState.beginProbe(probeTypeWWWAuthenticate, http.MethodGet))
	require.Empty(t, nilReceipt.ID())
	nilReceipt.observeResponse(http.StatusOK, nil)
	nilReceipt.finish(probeDispositionAccepted)
	nilState.cancelActiveProbes()
}

func TestSetWithProbeUsesFinalDiscoveryClassification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		result      *DiscoveryResult
		err         error
		disposition probeDisposition
		optional    bool
	}{
		{name: "accepted", disposition: probeDispositionAccepted},
		{name: "optional absence", result: &DiscoveryResult{Attempts: []DiscoveryAttempt{{Tried: true, StatusCode: http.StatusNotFound}}}, err: errors.New("not found"), disposition: probeDispositionNotAdvertised, optional: true},
		{name: "terminal failure", err: errors.New("failed"), disposition: probeDispositionTerminalFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := newProbeTestState()
			receipt := state.beginProbe(probeTypeProtectedResourceMetadata, http.MethodGet)
			state.setWithProbe(tc.result, tc.err, nil, nil, receipt)
			snapshot := state.healthSnapshot()
			require.True(t, state.IsDone())
			require.Empty(t, snapshot.activeProbes)
			require.Len(t, snapshot.recentProbes, 1)
			require.Equal(t, string(tc.disposition), snapshot.recentProbes[0].Disposition)
			require.Equal(t, snapshot.completedAt, *snapshot.recentProbes[0].CompletedAt)
			require.Equal(t, tc.optional, snapshot.optionalFailure)
			result, _, _, err, ok := state.Wait(0)
			require.True(t, ok)
			require.Same(t, tc.result, result)
			require.Equal(t, tc.err, err)
		})
	}

	state := newProbeTestState()
	state.setWithProbe(nil, errors.New("configuration failed"), nil, nil, nil)
	require.Empty(t, state.healthSnapshot().recentProbes)
}

func TestProbeObservationConcurrentSnapshots(t *testing.T) {
	t.Parallel()
	state := newProbeTestState()
	ready := make(chan *probeReceipt, maxActiveProbes)
	release := make(chan struct{})
	var workers sync.WaitGroup
	var releaseOnce sync.Once
	releaseWorkers := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		releaseWorkers()
		workers.Wait()
	})
	for range maxActiveProbes {
		workers.Go(func() {
			receipt := state.beginProbe(probeTypeProtectedResourceMetadata, http.MethodGet)
			ready <- receipt
			<-release
			receipt.observeResponse(http.StatusOK, http.Header{"X-Request-Id": {"request"}})
			receipt.finish(probeDispositionAccepted)
		})
	}
	ids := make(map[string]struct{}, maxActiveProbes)
	for range maxActiveProbes {
		receipt := <-ready
		require.NotNil(t, receipt)
		ids[receipt.ID()] = struct{}{}
	}
	require.Len(t, ids, maxActiveProbes)
	require.Len(t, state.healthSnapshot().activeProbes, maxActiveProbes)

	readerStarted := make(chan struct{})
	stopReader := make(chan struct{})
	var reader sync.WaitGroup
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(stopReader) }) }
	t.Cleanup(func() {
		stop()
		reader.Wait()
	})
	reader.Go(func() {
		close(readerStarted)
		for {
			select {
			case <-stopReader:
				return
			default:
				snapshot := state.healthSnapshot()
				for i := range snapshot.activeProbes {
					snapshot.activeProbes[i].ProbeID = "mutated copy"
				}
			}
		}
	})
	<-readerStarted
	releaseWorkers()
	workers.Wait()
	stop()
	reader.Wait()
	snapshot := state.healthSnapshot()
	require.Empty(t, snapshot.activeProbes)
	require.Len(t, snapshot.recentProbes, maxRecentProbes)
	for _, observation := range snapshot.recentProbes {
		_, ok := ids[observation.ProbeID]
		require.True(t, ok)
	}
	require.False(t, snapshot.limited)
}
