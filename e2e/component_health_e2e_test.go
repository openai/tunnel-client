package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/healthurl"
	"github.com/openai/tunnel-client/pkg/oauth"
	harnesspkg "github.com/openai/tunnel-client/testsupport/e2e"
	"github.com/openai/tunnel-client/testsupport/mockmcpserver"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

// TestHealthDetailsControlPlaneAndQueue holds real work and response uploads
// at deterministic barriers while querying the running application's routes.
func TestHealthDetailsControlPlaneAndQueue(t *testing.T) {
	t.Parallel()

	for _, unix := range []bool{false, true} {
		t.Run(fmt.Sprintf("unix_%t", unix), func(t *testing.T) {
			if unix && runtime.GOOS == "windows" {
				t.Skip("Unix health socket is unavailable on Windows")
			}
			t.Parallel()

			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			firstGate, moreGate := make(chan struct{}), make(chan struct{})
			workEntered, workRelease, retryRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var workOnce, retryOnce sync.Once
			defer workOnce.Do(func() { close(workRelease) })
			defer retryOnce.Do(func() { close(retryRelease) })
			var failed atomic.Bool
			var proxy *httputil.ReverseProxy
			front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/response") {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						http.Error(w, "read failed", 500)
						return
					}
					_ = r.Body.Close()
					r.Body = io.NopCloser(strings.NewReader(string(body)))
					var payload struct {
						RequestID string `json:"request_id"`
					}
					if err := json.Unmarshal(body, &payload); err != nil {
						http.Error(w, "invalid JSON", 400)
						return
					}
					if payload.RequestID == "health-work-0" {
						if failed.CompareAndSwap(false, true) {
							http.Error(w, "synthetic upload failure", http.StatusServiceUnavailable)
							return
						}
						select {
						case <-retryRelease:
						case <-r.Context().Done():
							return
						}
					}
				}
				proxy.ServeHTTP(w, r)
			}))
			defer front.Close()
			commands := make([]mocktunnelservice.CommandResponse, 3)
			for i := range commands {
				id := fmt.Sprintf("health-work-%d", i)
				gate := moreGate
				if i == 0 {
					gate = firstGate
				}
				commands[i] = mocktunnelservice.CommandResponse{
					Command:      mocktunnelservice.NewCommand(id, json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"method":"tools/call","params":{"name":"echo","arguments":{}}}`, id)), nil),
					DeliverAfter: gate, NoResponseExpected: true,
				}
			}
			urlFile := filepath.Join(t.TempDir(), "health.url")
			h := harnesspkg.NewHarness(t,
				harnesspkg.WithScenarioTimeout(15*time.Second),
				harnesspkg.WithPreserveClientURLs(),
				harnesspkg.WithClientConfig(func(cfg *config.Config) {
					cfg.ControlPlane.BaseURL = mustParseURL(t, front.URL)
					cfg.ControlPlane.MaxInFlightRequests = 1
					cfg.MCP.MaxConcurrentRequests = 1
					cfg.Health.URLFile = urlFile
					cfg.Logging.Level = slog.LevelDebug
					if unix {
						cfg.Health.ListenAddr = ""
						cfg.Health.UnixSocket = healthSocketPath(t)
					}
				}),
				harnesspkg.WithMCPOptions(mockmcpserver.WithOAuthDiscoveryResources(), mockmcpserver.WithCalls(
					mockmcpserver.Call{Tool: "echo", DynamicResult: func(json.RawMessage) (json.RawMessage, error) {
						close(workEntered)
						select {
						case <-workRelease:
							return json.RawMessage(`{"ok":true}`), nil
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					}},
					mockmcpserver.Call{Tool: "echo", Result: json.RawMessage(`{"ok":true}`)},
					mockmcpserver.Call{Tool: "echo", Result: json.RawMessage(`{"ok":true}`)},
				)),
				harnesspkg.WithControlPlaneOptions(mocktunnelservice.WithSessionHeaderPropagation(), mocktunnelservice.WithInitializationPhaseCommands(), mocktunnelservice.WithCommandResponses(commands...)),
				harnesspkg.WithBeforeClientStart(func(h *harnesspkg.Harness) { proxy = httputil.NewSingleHostReverseProxy(h.ControlPlane.BaseURL()) }),
				harnesspkg.WithAfterClientStart(func(h *harnesspkg.Harness) {
					waitHealthStartup(t, h)
					client, base := healthTestClient(t, urlFile)
					waitHealthObservation(t, ctx, h.DispatcherHealth, func(s healthstate.ComponentSnapshot) bool {
						return s.Details.(healthstate.DispatcherDetails).Completed >= 2
					})
					// Both initialization commands completed; the next successful
					// poll while delivery gates are closed must be an empty poll.
					baseline := h.PollHealth.Snapshot(time.Now()).Details.(healthstate.PollingDetails).LastSuccess
					waitHealthObservation(t, ctx, h.PollHealth, func(s healthstate.ComponentSnapshot) bool {
						success := s.Details.(healthstate.PollingDetails).LastSuccess
						return success != nil && (baseline == nil || success.After(*baseline))
					})
					poll := readHealthJSON(t, client, base+"/health/control-plane")
					require.Equal(t, "ok", poll["status"], "an empty successful poll is connectivity evidence")
					close(firstGate)
					select {
					case <-workEntered:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					waitHealthObservation(t, ctx, h.QueueHealth, func(s healthstate.ComponentSnapshot) bool { return s.Details.(healthstate.QueueDetails).Depth == 0 })
					active := healthObject(t, readHealthJSON(t, client, base+"/health/dispatcher")["details"])
					require.Equal(t, float64(1), active["active"])
					require.Equal(t, float64(1), active["pool_limit"])
					require.Equal(t, float64(0), healthObject(t, readHealthJSON(t, client, base+"/health/queue")["details"])["depth"])
					close(moreGate)
					waitHealthObservation(t, ctx, h.QueueHealth, func(s healthstate.ComponentSnapshot) bool {
						d := s.Details.(healthstate.QueueDetails)
						return d.Enqueued >= 5 && d.Depth == 1
					})
					waitHealthObservation(t, ctx, h.PollHealth, func(s healthstate.ComponentSnapshot) bool { return s.State == "backpressured" })
					for range 3 {
						queue := readHealthJSON(t, client, base+"/health/queue")
						require.Equal(t, "backpressured", queue["state"])
						require.Equal(t, float64(1), healthObject(t, queue["details"])["depth"])
						assertLegacyHealthProbes(t, client, base)
					}
					workOnce.Do(func() { close(workRelease) })
					waitHealthObservation(t, ctx, h.DeliveryHealth, func(s healthstate.ComponentSnapshot) bool { return s.Status == healthstate.StatusDegraded })
					delivery := readHealthJSON(t, client, base+"/health/response-delivery")
					require.Equal(t, "degraded", delivery["status"])
					require.Equal(t, float64(503), healthObject(t, delivery["details"])["http_status"])
					require.Equal(t, "ok", readHealthJSON(t, client, base+"/health/control-plane")["status"], "poll and upload outcomes are independent")
					assertLegacyHealthProbes(t, client, base)
					retryOnce.Do(func() { close(retryRelease) })
					waitHealthObservation(t, ctx, h.DeliveryHealth, func(s healthstate.ComponentSnapshot) bool {
						d := s.Details.(healthstate.DeliveryDetails)
						return d.Accepted >= 5 && d.InProgress == 0
					})
					waitHealthObservation(t, ctx, h.DispatcherHealth, func(s healthstate.ComponentSnapshot) bool {
						d := s.Details.(healthstate.DispatcherDetails)
						return d.Active == 0 && d.Completed >= 5
					})
					delivery = readHealthJSON(t, client, base+"/health/response-delivery")
					require.Equal(t, "ok", delivery["status"])
					details := healthObject(t, delivery["details"])
					require.GreaterOrEqual(t, details["retries"].(float64), float64(1))
					require.Equal(t, float64(0), details["terminal_failures"])
					require.Equal(t, float64(0), healthObject(t, readHealthJSON(t, client, base+"/health/queue")["details"])["depth"])
				}),
			)
			h.ExecuteScenarious(t)
		})
	}
}

type changingHealthComponent interface {
	healthstate.Component
	Changes() <-chan struct{}
}

func waitHealthObservation(t *testing.T, ctx context.Context, component changingHealthComponent, predicate func(healthstate.ComponentSnapshot) bool) {
	t.Helper()
	for {
		changed := component.Changes()
		snapshot := component.Snapshot(time.Now())
		if predicate(snapshot) {
			return
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatalf("waiting for %s observation: %v; last snapshot: %+v", component.Name(), ctx.Err(), snapshot)
		}
	}
}

// TestHealthDetailsStdioSameChild reproduces discovery through the running
// application's real poll/dispatch/stdio/response path. Health reads cannot
// manufacture the proof: command delivery stays gated until the first snapshot.
func TestHealthDetailsStdioSameChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stdio shell fixture requires bash")
	}
	t.Parallel()

	for _, unix := range []bool{false, true} {
		for _, initializedNotification := range []bool{false, true} {
			t.Run(fmt.Sprintf("unix_%t/initialized_notification_%t", unix, initializedNotification), func(t *testing.T) {
				t.Parallel()

				dir := t.TempDir()
				launches := filepath.Join(dir, "launches")
				messages := filepath.Join(dir, "messages")
				urlFile := filepath.Join(dir, "health.url")
				script := filepath.Join(dir, "mcp.sh")
				require.NoError(t, os.WriteFile(script, []byte(healthStdioFixture), 0o700))
				gate := make(chan struct{})
				initialize := healthDiscoveryCommand("initialize", `{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"health-e2e","version":"1"}}`)
				initialize.DeliverAfter = gate
				tools := healthDiscoveryCommand("tools/list", `{}`)
				h := harnesspkg.NewHarness(t,
					harnesspkg.WithMCPCommand([]string{"bash", script, launches, messages}),
					harnesspkg.WithScenarioTimeout(10*time.Second),
					harnesspkg.WithControlPlaneOptions(mocktunnelservice.WithCommandResponses(initialize, tools)),
					harnesspkg.WithClientConfig(func(cfg *config.Config) {
						cfg.Health.URLFile = urlFile
						cfg.MCP.StdioSendInitializedNotification = initializedNotification
						if unix {
							cfg.Health.ListenAddr = ""
							cfg.Health.UnixSocket = healthSocketPath(t)
						}
					}),
					harnesspkg.WithAfterClientStart(func(h *harnesspkg.Harness) {
						waitHealthStartup(t, h)
						client, base := healthTestClient(t, urlFile)
						assertLegacyHealthProbes(t, client, base)
						compact := readHealthJSON(t, client, base+"/health")
						require.Equal(t, true, compact["ready"])
						require.NotContains(t, compact, "components")
						before := readHealthJSON(t, client, base+"/health/mcp")
						require.Equal(t, "not_observed", before["state"])
						require.Equal(t, "unknown", before["status"])
						require.Empty(t, healthReadOptionalFile(t, messages), "health must not send MCP messages")
						close(gate)
					}),
					harnesspkg.WithBeforeClientStop(func(h *harnesspkg.Harness) {
						client, base := healthTestClient(t, urlFile)
						beforeMessages := healthReadOptionalFile(t, messages)
						beforeLaunches := healthReadOptionalFile(t, launches)
						component := readHealthJSON(t, client, base+"/health/mcp")
						require.Equal(t, "discovered", component["state"])
						require.Equal(t, "ok", component["status"])
						details := healthObject(t, component["details"])
						require.Equal(t, "stdio", details["transport"])
						require.Equal(t, "main", details["channel"])
						require.Equal(t, "running", details["child_state"])
						require.NotEmpty(t, details["child_generation"])
						require.Equal(t, float64(1), details["initialize_epoch"])
						init := healthObject(t, details["initialize"])
						require.Equal(t, true, init["ok"])
						require.Equal(t, true, init["identity_complete"])
						require.Equal(t, "receipt-fixture", init["server_name"])
						require.Equal(t, "1.2.3", init["server_version"])
						require.Equal(t, "2025-11-25", init["protocol_version"])
						require.Equal(t, []any{"tools"}, init["capability_names"])
						catalog := healthObject(t, details["tools_list"])
						require.Equal(t, true, catalog["ok"])
						require.Equal(t, true, catalog["complete"])
						require.Equal(t, false, catalog["partial"])
						require.Equal(t, false, catalog["limited"])
						require.Equal(t, []any{"echo"}, catalog["tool_names"])
						require.Equal(t, float64(1), catalog["retained_count"])
						aggregate := readHealthJSON(t, client, base+"/health?details=true")
						mcp := healthObject(t, healthObject(t, aggregate["components"])["mcp"])
						require.Equal(t, details, mcp["details"])
						require.NotContains(t, readHealthJSON(t, client, base+"/health?details=false"), "components")
						assertLegacyHealthProbes(t, client, base)
						require.Equal(t, beforeMessages, healthReadOptionalFile(t, messages), "health requests must add no protocol traffic")
						require.Equal(t, beforeLaunches, healthReadOptionalFile(t, launches), "health requests must not launch another child")
						pids := strings.Fields(beforeLaunches)
						require.Len(t, pids, 1)
						want := pids[0] + ":initialize\n"
						if initializedNotification {
							want += pids[0] + ":notifications/initialized\n"
						}
						want += pids[0] + ":tools/list\n"
						require.Equal(t, want, beforeMessages)
						require.Len(t, h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched), 2)
					}),
				)
				h.ExecuteScenarious(t)
			})
		}
	}
}

func healthDiscoveryCommand(method, params string) mocktunnelservice.CommandResponse {
	return mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(method, json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"method":%q,"params":%s}`, method, method, params)), nil),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{RequestID: method, Assert: func(tb testing.TB, response mocktunnelservice.ReceivedResponse) {
			require.Equal(tb, http.StatusOK, response.ResponseCode)
			var payload map[string]any
			require.NoError(tb, json.Unmarshal(response.JSONResponse, &payload))
			require.Equal(tb, method, payload["id"], "private downstream aliases must not escape")
			require.NotContains(tb, payload, "error")
			require.Contains(tb, payload, "result")
		}}},
	}
}

func healthSocketPath(t *testing.T) string {
	t.Helper()
	// Keep Unix socket paths short even when macOS supplies a long TMPDIR.
	dir, err := os.MkdirTemp("/tmp", "health-e2e-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	return filepath.Join(dir, "health.sock")
}

func waitHealthStartup(t *testing.T, h *harnesspkg.Harness) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, h.WaitForMCPProbe(ctx))
	result, probe, _, err, done := h.OAuthState.Wait(5 * time.Second)
	require.True(t, done)
	if !oauth.IsOptionalDiscoveryFailure(result, probe, err) {
		require.NoError(t, err)
	}
}

func healthTestClient(t *testing.T, urlFile string) (*http.Client, string) {
	t.Helper()
	data, err := os.ReadFile(urlFile)
	require.NoError(t, err)
	target, err := healthurl.Parse(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	client, err := target.HTTPClient(2 * time.Second)
	require.NoError(t, err)
	t.Cleanup(client.CloseIdleConnections)
	return client, strings.TrimSuffix(target.RequestURL("/"), "/")
}

func readHealthJSON(t *testing.T, client *http.Client, url string) map[string]any {
	t.Helper()
	response, err := client.Get(url)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, "%s: %s", url, body)
	require.LessOrEqual(t, len(body), 64*1024)
	require.Equal(t, "application/json", response.Header.Get("Content-Type"))
	require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	require.Equal(t, float64(1), payload["schema_version"])
	return payload
}

func healthObject(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	require.True(t, ok, "expected object, got %T", value)
	return object
}

func assertLegacyHealthProbes(t *testing.T, client *http.Client, base string) {
	t.Helper()
	for path, want := range map[string]string{"/healthz": "live", "/readyz": "ready"} {
		response, err := client.Get(base + path)
		require.NoError(t, err)
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode)
		require.Equal(t, want, string(body))
		require.Equal(t, "text/plain; charset=utf-8", response.Header.Get("Content-Type"))
	}
}

func healthReadOptionalFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(data)
}

const healthStdioFixture = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$$" >> "$1"
while IFS= read -r line; do
  if [[ $line =~ \"method\":\"([^\"]+)\" ]]; then
    method="${BASH_REMATCH[1]}"
    printf '%s:%s\n' "$$" "$method" >> "$2"
  else
    continue
  fi
  if [[ $line =~ \"id\":\"([^\"]+)\" ]]; then
    id="${BASH_REMATCH[1]}"
  else
    continue
  fi
  case "$method" in
    initialize) printf '{"jsonrpc":"2.0","id":"%s","result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"receipt-fixture","version":"1.2.3"}}}\n' "$id" ;;
    tools/list) printf '{"jsonrpc":"2.0","id":"%s","result":{"tools":[{"name":"echo","description":"must-not-appear-in-health","inputSchema":{"type":"object"}}]}}\n' "$id" ;;
  esac
done
`
