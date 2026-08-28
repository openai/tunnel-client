package e2e_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	harnesspkg "github.com/openai/tunnel-client/testsupport/e2e"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

// These helpers are intentionally scoped to oauth_harpoon_e2e_test in BUILD.bazel.
// The core e2e target does not compile the Harpoon test files that call them.
func waitForActiveHarpoonPollers(
	t *testing.T,
	h *harnesspkg.Harness,
	clients ...*harnesspkg.TunnelClient,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, client := range clients {
		if err := client.WaitForPolls(ctx, 1); err != nil {
			t.Fatalf("wait for %s Harpoon poller: %v", client.Name(), err)
		}
		clientName := client.Name()
		if err := h.ControlPlane.WaitForHTTPRequests(ctx, 1, func(req mocktunnelservice.IncomingHTTPRequest) bool {
			return req.Method == http.MethodGet &&
				strings.HasSuffix(req.Path, "/poll") &&
				req.Headers.Get(harnesspkg.TestClientInstanceHeader) == clientName
		}); err != nil {
			t.Fatalf("wait for %s Harpoon poll request: %v", clientName, err)
		}
	}
}

func assertHarpoonResponseAttribution(
	t *testing.T,
	requests []mocktunnelservice.IncomingHTTPRequest,
	want map[string]int,
) {
	t.Helper()
	got := make(map[string]int, len(want))
	for _, request := range requests {
		if request.Method != http.MethodPost || !strings.HasSuffix(request.Path, "/response") {
			continue
		}
		got[request.Headers.Get(harnesspkg.TestClientInstanceHeader)]++
	}
	for clientName, wantCount := range want {
		if got[clientName] != wantCount {
			t.Fatalf("Harpoon response POSTs from %s = %d, want %d (all response POSTs = %v)", clientName, got[clientName], wantCount, got)
		}
		delete(got, clientName)
	}
	if len(got) != 0 {
		t.Fatalf("unexpected Harpoon response POST attribution: %v", got)
	}
}
