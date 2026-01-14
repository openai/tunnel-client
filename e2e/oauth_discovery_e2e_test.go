package e2e_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/controlplane/wiretypes"
	harnesspkg "go.openai.org/api/tunnel-client/testsupport/e2e"
	"go.openai.org/api/tunnel-client/testsupport/mockmcpserver"
	"go.openai.org/api/tunnel-client/testsupport/mocktunnelservice"
)

func TestHarnessHandlesOAuthDiscoveryCommand(t *testing.T) {

	const requestID = "cmd-oauth"

	oauthCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewOAuthDiscoveryCommand(requestID, nil),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: requestID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadOAuth) {
					target.Fatalf("oauth discovery response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("oauth discovery response code mismatch: %d", resp.ResponseCode)
				}
				var payload map[string]any
				if err := json.Unmarshal(resp.JSONResponse, &payload); err != nil {
					target.Fatalf("decode oauth discovery payload: %v", err)
				}
				if payload["resource"] == "" {
					target.Fatalf("oauth discovery payload missing resource: %v", payload)
				}
			},
		}},
	}

	h := harnesspkg.NewHarness(
		t,
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelDebug
		}),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithCommandResponses(oauthCommand),
		),
		harnesspkg.WithMCPOptions(
			mockmcpserver.WithOAuthDiscoveryResources(),
		),
	)

	h.ExecuteScenarious(t)

	matched := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	if len(matched) != 1 {
		t.Fatalf("expected single oauth discovery response; got %d", len(matched))
	}
	if matched[0].RequestID != requestID {
		t.Fatalf("unexpected response request id: %s", matched[0].RequestID)
	}
}
