package proxyhealth

import (
	"net/url"
	"testing"
	"time"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/proxy"
)

func TestRecordResultHistoryRetention(t *testing.T) {
	checker, route := newTestChecker(t)
	for i := 0; i < maxHistoryEntries+2; i++ {
		record := CheckRecord{Timestamp: time.Now().Add(time.Duration(i) * time.Second)}
		checker.recordResult(route, record, i%2 == 0)
	}
	summaries := checker.HealthSummaries()
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if len(summaries[0].History) != maxHistoryEntries {
		t.Fatalf("expected %d history entries, got %d", maxHistoryEntries, len(summaries[0].History))
	}
}

func TestRecordResultStateTransitions(t *testing.T) {
	checker, route := newTestChecker(t)
	checker.recordResult(route, CheckRecord{Timestamp: time.Now()}, false)
	state := checker.HealthSummaries()[0].HealthState
	if state != string(HealthStateUnhealthy) {
		t.Fatalf("expected unhealthy, got %s", state)
	}
	checker.recordResult(route, CheckRecord{Timestamp: time.Now()}, true)
	state = checker.HealthSummaries()[0].HealthState
	if state != string(HealthStateHealthy) {
		t.Fatalf("expected healthy, got %s", state)
	}
}

func newTestChecker(t *testing.T) (*Checker, proxy.Route) {
	t.Helper()
	proxyURL := mustParseURL(t, "http://proxy.example:8080")
	targetURL := mustParseURL(t, "https://example.com")
	route := proxy.ResolveRoute(proxy.RouteKindControlPlane, "control-plane", targetURL, proxyURL, config.ProxySource("flag"), lookupEnvMap(nil))
	checker := &Checker{
		routes:      []proxy.Route{route},
		routeStatus: map[string]*routeStatus{routeKey(route): {route: route, healthState: HealthStateUnhealthy}},
	}
	return checker, route
}

func lookupEnvMap(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		if values == nil {
			return "", false
		}
		val, ok := values[key]
		return val, ok
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return parsed
}
