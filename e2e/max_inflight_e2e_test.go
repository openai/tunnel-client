package e2e_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"go.openai.org/api/tunnel-client/pkg/config"
	harnesspkg "go.openai.org/api/tunnel-client/testsupport/e2e"
	"go.openai.org/api/tunnel-client/testsupport/mocktunnelservice"
)

const maxTunnelServicePollLimit = 25

func TestHarnessCapsPollLimitWhenMaxInFlightExceedsTunnelServiceLimit(t *testing.T) {
	h := runSimpleToolScenarioWithHarnessOptions(
		t,
		[]harnesspkg.HarnessOption{
			harnesspkg.WithClientConfig(func(cfg *config.Config) {
				cfg.ControlPlane.MaxInFlightRequests = maxTunnelServicePollLimit + 5
			}),
		},
		nil,
	)

	assertPollRequestsRespectTunnelServiceLimit(t, h.ControlPlane.ReceivedHTTPRequests())
}

func assertPollRequestsRespectTunnelServiceLimit(t *testing.T, requests []mocktunnelservice.IncomingHTTPRequest) {
	t.Helper()

	var (
		pollRequests int
		sawMaxLimit  bool
	)
	for _, req := range requests {
		if req.Method != http.MethodGet || !strings.HasSuffix(req.Path, "/poll") {
			continue
		}
		pollRequests++

		values, err := url.ParseQuery(req.RawQuery)
		if err != nil {
			t.Fatalf("parse poll query %q: %v", req.RawQuery, err)
		}
		rawLimit := values.Get("limit")
		if rawLimit == "" {
			t.Fatalf("poll request missing limit query: %s %s?%s", req.Method, req.Path, req.RawQuery)
		}
		limit, err := strconv.Atoi(rawLimit)
		if err != nil {
			t.Fatalf("parse poll limit %q: %v", rawLimit, err)
		}
		if limit > maxTunnelServicePollLimit {
			t.Fatalf("poll request exceeded tunnel-service limit: got %d want <= %d", limit, maxTunnelServicePollLimit)
		}
		if limit == maxTunnelServicePollLimit {
			sawMaxLimit = true
		}
	}

	if pollRequests == 0 {
		t.Fatal("expected at least one poll request")
	}
	if !sawMaxLimit {
		t.Fatalf("expected a poll request capped at %d", maxTunnelServicePollLimit)
	}
}
