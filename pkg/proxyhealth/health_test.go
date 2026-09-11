package proxyhealth

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/proxy"
)

func TestComponentHealthBoundedPrivateAndDefensive(t *testing.T) {
	t.Parallel()
	checker := &Checker{routeStatus: make(map[string]*routeStatus)}
	now := time.Now()
	for i := range 40 {
		checker.routeStatus[fmt.Sprintf("secret-%02d", i)] = &routeStatus{route: proxy.Route{Kind: proxy.RouteKindControlPlane, Name: "secret-name", ProxyURL: &url.URL{Host: "private.example"}}, healthState: HealthStateHealthy, lastCheck: now, lastSuccess: now, history: []CheckRecord{{ErrorReason: "secret-history"}}}
	}
	h := newComponentHealth()
	attachComponentHealth(h, checker)
	require.Len(t, h.keys, 16)
	require.Equal(t, "secret-00", h.keys[0])
	require.Equal(t, "secret-15", h.keys[15])
	snapshot := h.Snapshot(now)
	require.True(t, snapshot.Limited)
	d := snapshot.Details.(healthstate.ProxyDetails)
	require.Equal(t, 40, d.RouteCount)
	require.Len(t, d.Routes, 16)
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret")
	require.NotContains(t, string(encoded), "private.example")
	*d.Routes[0].LastSuccess = time.Time{}
	d.Routes[0].Label = "changed"
	require.Equal(t, "route-1", h.Snapshot(now).Details.(healthstate.ProxyDetails).Routes[0].Label)
	require.False(t, h.Snapshot(now).Details.(healthstate.ProxyDetails).Routes[0].LastSuccess.IsZero())
	checker.routeStatus["secret-00"].healthState = HealthStateUnhealthy
	attachComponentHealth(h, checker)
	require.Equal(t, healthstate.StatusDegraded, h.Snapshot(now).Status)
}

func TestComponentHealthDistinguishesTLSWithoutChangingLegacyPhase(t *testing.T) {
	t.Parallel()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("untrusted TLS certificate unexpectedly allowed CONNECT")
		w.WriteHeader(http.StatusOK)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	route := proxy.ResolveRoute(proxy.RouteKindControlPlane, "control-plane", mustParseURL(t, "https://example.com"), mustParseURL(t, server.URL), config.ProxySource("flag"), lookupEnvMap(nil))
	checker := &Checker{routeStatus: map[string]*routeStatus{routeKey(route): {route: route, healthState: HealthStateUnhealthy}}}
	h := newComponentHealth()
	attachComponentHealth(h, checker)
	record, success := checker.checkProxyRoute(t.Context(), route)
	require.False(t, success)
	require.True(t, record.tlsFailure)
	require.Equal(t, "connect", record.ErrorPhase, "legacy proxy diagnostics retain their phase")
	checker.recordResult(route, record, success)
	snapshot := h.Snapshot(time.Now())
	require.Equal(t, healthstate.StatusDegraded, snapshot.Status)
	require.Equal(t, "tls_failed", snapshot.Details.(healthstate.ProxyDetails).Routes[0].FailureCategory)
	legacy, err := json.Marshal(checker.HealthSummaries())
	require.NoError(t, err)
	require.Contains(t, string(legacy), `"error_phase":"connect"`)
	require.NotContains(t, string(legacy), "tlsFailure")
	checker.recordResult(route, CheckRecord{Timestamp: time.Now(), ErrorPhase: "connect"}, false)
	require.Equal(t, "connect_failed", h.Snapshot(time.Now()).Details.(healthstate.ProxyDetails).Routes[0].FailureCategory)
}

func TestComponentHealthIncludesFailuresOutsideRouteSample(t *testing.T) {
	t.Parallel()
	checker := &Checker{routeStatus: make(map[string]*routeStatus)}
	var last proxy.Route
	now := time.Now()
	for i := range 20 {
		route := proxy.Route{Kind: proxy.RouteKindMCPChannel, Name: fmt.Sprintf("channel-%02d", i), RouteMode: proxy.RouteModeProxy}
		checker.routeStatus[routeKey(route)] = &routeStatus{route: route, healthState: HealthStateHealthy, lastCheck: now}
		last = route
	}
	h := newComponentHealth()
	attachComponentHealth(h, checker)
	require.NotContains(t, h.keys, routeKey(last))
	require.Equal(t, healthstate.StatusOK, h.Snapshot(now).Status)
	require.Equal(t, now.UTC(), *h.Snapshot(now).ObservedAt)
	failedAt := now.Add(time.Second)
	checker.recordResult(last, CheckRecord{Timestamp: failedAt, ErrorPhase: "tcp"}, false)
	failure := h.Snapshot(failedAt)
	require.Equal(t, healthstate.StatusDegraded, failure.Status)
	require.Equal(t, failedAt.UTC(), *failure.ObservedAt, "omitted routes still advance component freshness")
	*failure.ObservedAt = time.Time{}
	require.Equal(t, failedAt.UTC(), *h.Snapshot(failedAt).ObservedAt, "the timestamp is a defensive copy")
	recoveredAt := now.Add(2 * time.Second)
	checker.recordResult(last, CheckRecord{Timestamp: recoveredAt, Success: true}, true)
	recovery := h.Snapshot(recoveredAt)
	require.Equal(t, healthstate.StatusOK, recovery.Status)
	require.Equal(t, recoveredAt.UTC(), *recovery.ObservedAt)
}

func TestComponentHealthDetailsJSONStableAcrossInsertionOrder(t *testing.T) {
	t.Parallel()
	const routeCount = 40
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	encodeDetails := func(reverse bool) []byte {
		checker := &Checker{routeStatus: make(map[string]*routeStatus)}
		for offset := range routeCount {
			i := offset
			if reverse {
				i = routeCount - 1 - offset
			}
			route := proxy.Route{Kind: proxy.RouteKindMCPChannel, Name: fmt.Sprintf("ordering-route-%02d", i), RouteMode: proxy.RouteModeProxy}
			status := &routeStatus{route: route, healthState: HealthStateHealthy, lastCheck: now.Add(time.Duration(i) * time.Second), lastSuccess: now}
			if i%2 != 0 {
				status.healthState = HealthStateUnhealthy
				status.history = []CheckRecord{{ErrorPhase: "tcp"}}
			}
			checker.routeStatus[routeKey(route)] = status
		}
		h := newComponentHealth()
		attachComponentHealth(h, checker)
		snapshot := h.Snapshot(now.Add(time.Minute))
		require.True(t, snapshot.Limited)
		details := snapshot.Details.(healthstate.ProxyDetails)
		require.Equal(t, routeCount, details.RouteCount)
		require.Len(t, details.Routes, 16)
		for i, route := range details.Routes {
			require.Equal(t, now.Add(time.Duration(i)*time.Second), *route.LastCheck, "retain the same ordered route subset")
		}
		encoded, err := json.Marshal(details)
		require.NoError(t, err)
		return encoded
	}
	forward := encodeDetails(false)
	reverse := encodeDetails(true)
	require.Equal(t, forward, reverse, "JSON bytes, including field and route order, must match after truncation")
}
