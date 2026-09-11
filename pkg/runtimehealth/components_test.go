package runtimehealth

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/httpguard"
)

type fixedHealthComponent struct {
	name  string
	value healthstate.ComponentSnapshot
	reads int
}

func (c *fixedHealthComponent) Name() string { return c.name }
func (c *fixedHealthComponent) Snapshot(time.Time) healthstate.ComponentSnapshot {
	c.reads++
	return c.value
}

func TestComponentHealthRepresentationAndReadiness(t *testing.T) {
	for _, defaultDetails := range []bool{false, true} {
		for _, ready := range []bool{false, true} {
			t.Run(fmt.Sprintf("details=%t/ready=%t", defaultDetails, ready), func(t *testing.T) {
				observed := time.Date(2026, 9, 10, 20, 55, 45, 0, time.UTC)
				component := &fixedHealthComponent{name: "mcp", value: healthstate.ComponentSnapshot{Status: healthstate.StatusUnknown, State: "not_observed", ObservedAt: &observed}}
				h, err := newComponentHealth([]healthstate.Component{component}, defaultDetails, func() bool { return ready })
				require.NoError(t, err)
				for _, tc := range []struct {
					path    string
					details bool
				}{{"/health", defaultDetails}, {"/health?details=true", true}, {"/health?details=false", false}} {
					before := component.reads
					w := httptest.NewRecorder()
					h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
					require.Equal(t, http.StatusOK, w.Code)
					require.Equal(t, "application/json", w.Header().Get("Content-Type"))
					require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
					var body map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
					require.JSONEq(t, fmt.Sprint(ready), string(body["ready"]))
					require.JSONEq(t, "true", string(body["live"]))
					require.JSONEq(t, "1", string(body["schema_version"]))
					_, gotDetails := body["components"]
					require.Equal(t, tc.details, gotDetails)
					if tc.details {
						require.JSONEq(t, `{"mcp":{"status":"unknown","state":"not_observed","observed_at":"2026-09-10T20:55:45Z","limited":false}}`, string(body["components"]))
						require.Equal(t, before+1, component.reads)
					} else {
						require.Equal(t, before, component.reads)
					}
					require.Contains(t, string(body["runtime"]), `"lifecycle":"starting"`)
					require.Contains(t, string(body["runtime"]), `"instance_id":`)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health/mcp", nil))
				var body map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				delete(body, "snapshot_at")
				encoded, err := json.Marshal(body)
				require.NoError(t, err)
				require.JSONEq(t, `{"schema_version":1,"component":"mcp","status":"unknown","state":"not_observed","observed_at":"2026-09-10T20:55:45Z","limited":false}`, string(encoded))
				h.lifecycle.Store(1)
				snapshot, err := h.snapshot(time.Now(), false)
				require.NoError(t, err)
				require.Equal(t, "running", snapshot.Runtime.Lifecycle)
				h.lifecycle.Store(2)
				snapshot, err = h.snapshot(time.Now(), false)
				require.NoError(t, err)
				require.Equal(t, "draining", snapshot.Runtime.Lifecycle)
			})
		}
	}
}

func TestComponentHealthJSONOrder(t *testing.T) {
	const timestamp = `"2026-09-10T20:55:45Z"`
	const alpha = `{"status":"disabled","state":"disabled","limited":false}`
	const middle = `{"status":"ok","state":"buffering","observed_at":` + timestamp + `,"limited":false,"details":{"depth":2,"capacity":8,"utilization":0.25,"enqueued":3,"dequeued":1,"backpressure_seconds":0}}`
	const zulu = `{"status":"unknown","state":"not_observed","limited":false}`
	const summary = `"schema_version":1,"live":true,"ready":true,"snapshot_at":` + timestamp + `,"runtime":{"instance_id":"test-instance","version":"test-version","flavor":"runtime","started_at":` + timestamp + `,"uptime_seconds":0,"lifecycle":"starting"}`
	const compact = `{` + summary + `}`
	const detailed = `{` + summary + `,"components":{"alpha":` + alpha + `,"middle":` + middle + `,"zulu":` + zulu + `}}`
	individual := `{"schema_version":1,"snapshot_at":` + timestamp + `,"component":"middle",` + middle[1:]

	// Replace only live values in the original HTTP bytes. Re-encoding the
	// response would sort its keys and conceal a wire-order regression.
	normalizeLiveValues := func(t *testing.T, body string) string {
		t.Helper()
		var live struct {
			SnapshotAt json.RawMessage `json:"snapshot_at"`
			Runtime    struct {
				InstanceID    json.RawMessage `json:"instance_id"`
				UptimeSeconds json.RawMessage `json:"uptime_seconds"`
			} `json:"runtime"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &live))
		require.NotEmpty(t, live.SnapshotAt)
		for _, field := range []struct {
			name, replacement string
			value             json.RawMessage
		}{
			{"snapshot_at", timestamp, live.SnapshotAt},
			{"instance_id", `"test-instance"`, live.Runtime.InstanceID},
			{"uptime_seconds", "0", live.Runtime.UptimeSeconds},
		} {
			if len(field.value) != 0 {
				prefix := `"` + field.name + `":`
				require.Equal(t, 1, strings.Count(body, prefix+string(field.value)))
				body = strings.Replace(body, prefix+string(field.value), prefix+field.replacement, 1)
			}
		}
		return body
	}
	for _, order := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		for _, defaultDetails := range []bool{false, true} {
			t.Run(fmt.Sprintf("order=%v/details=%t", order, defaultDetails), func(t *testing.T) {
				observed := time.Date(2026, 9, 10, 20, 55, 45, 0, time.UTC)
				components := []healthstate.Component{
					&fixedHealthComponent{name: "alpha", value: healthstate.ComponentSnapshot{Status: healthstate.StatusDisabled, State: "disabled"}},
					&fixedHealthComponent{name: "middle", value: healthstate.ComponentSnapshot{Status: healthstate.StatusOK, State: "buffering", ObservedAt: &observed, Details: healthstate.QueueDetails{Depth: 2, Capacity: 8, Utilization: 0.25, Enqueued: 3, Dequeued: 1}}},
					&fixedHealthComponent{name: "zulu", value: healthstate.ComponentSnapshot{Status: healthstate.StatusUnknown, State: "not_observed"}},
				}
				h, err := newComponentHealth([]healthstate.Component{components[order[0]], components[order[1]], components[order[2]]}, defaultDetails, func() bool { return true })
				require.NoError(t, err)
				h.startedAt = observed
				h.build.Version, h.build.Flavor = "test-version", "runtime"
				defaultBody := compact
				if defaultDetails {
					defaultBody = detailed
				}
				for _, route := range []struct{ path, expected string }{
					{"/health", defaultBody},
					{"/health?details=false", compact},
					{"/health?details=true", detailed},
					{"/health/middle", individual},
				} {
					for range 2 {
						w := httptest.NewRecorder()
						h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, route.path, nil))
						require.Equal(t, http.StatusOK, w.Code)
						require.Equal(t, route.expected, normalizeLiveValues(t, w.Body.String()), route.path)
					}
				}
			})
		}
	}
}

func TestComponentHealthRejectsInvalidRequests(t *testing.T) {
	h, err := newComponentHealth(nil, false, func() bool { return true })
	require.NoError(t, err)
	for _, path := range []string{"/health?details=", "/health?details", "/health?details=1", "/health?details=TRUE", "/health?details=true&details=false", "/health?other=true", "/health?details=true&other=false", "/health?details=%zz", "/health/mcp?details=true", "/health/mcp?", "/health?"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			require.Equal(t, http.StatusBadRequest, w.Code)
			require.JSONEq(t, `{"error":"invalid_query"}`, w.Body.String())
		})
	}
	for _, path := range []string{"/health/", "/health/missing", "/health/mcp/nested"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusNotFound, w.Code)
	}
	for _, method := range []string{http.MethodHead, http.MethodPost, http.MethodPut, http.MethodOptions} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/health", nil))
		require.Equal(t, http.StatusMethodNotAllowed, w.Code)
		require.Equal(t, "GET", w.Header().Get("Allow"))
	}
}

func TestComponentHealthLocalAccess(t *testing.T) {
	component := &fixedHealthComponent{name: "mcp", value: healthstate.ComponentSnapshot{Status: healthstate.StatusDisabled, State: "disabled"}}
	h, err := newComponentHealth([]healthstate.Component{component}, false, func() bool { return true })
	require.NoError(t, err)
	guarded := httpguard.LocalOnly(h, "local health only")
	for _, path := range []string{"/health", "/health?details=true", "/health/mcp"} {
		for _, tc := range []struct {
			address, network string
			status           int
		}{{"192.0.2.2:8000", "tcp", 403}, {"127.0.0.1:8000", "tcp", 200}, {"[::1]:8000", "tcp", 200}, {"@", "unix", 200}} {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.RemoteAddr = tc.address
			request = request.WithContext(httpguard.WithConnectionNetwork(context.Background(), tc.network))
			w := httptest.NewRecorder()
			guarded.ServeHTTP(w, request)
			require.Equal(t, tc.status, w.Code, "%s %s", path, tc.address)
		}
	}
}

func TestComponentHealthRegistrationAndMetadataBounds(t *testing.T) {
	for _, name := range []string{"", "bad/name", strings.Repeat("a", 65), "secret.example"} {
		_, err := newComponentHealth([]healthstate.Component{&fixedHealthComponent{name: name}}, false, func() bool { return true })
		require.Error(t, err)
	}
	component := &fixedHealthComponent{name: "mcp", value: healthstate.ComponentSnapshot{Status: healthstate.StatusOK, State: "ok"}}
	_, err := newComponentHealth([]healthstate.Component{component, component}, false, func() bool { return true })
	require.Error(t, err)
	_, err = newComponentHealth(make([]healthstate.Component, 17), false, func() bool { return true })
	require.Error(t, err)
	h, err := newComponentHealth(nil, false, func() bool { return true })
	require.NoError(t, err)
	h.build.Version = strings.Repeat("v", 257)
	h.build.Flavor = strings.Repeat("f", 33)
	snapshot, err := h.snapshot(time.Now(), false)
	require.NoError(t, err)
	require.Empty(t, snapshot.Runtime.Version)
	require.Empty(t, snapshot.Runtime.Flavor)
	require.True(t, snapshot.Runtime.Limited)
	component.value.State = "error with sensitive message"
	bounded, err := boundedComponent(component, time.Now())
	require.NoError(t, err)
	require.Equal(t, "invalid_snapshot", bounded.State)
	require.True(t, bounded.Limited)
}

func TestComponentHealthEncodingBudgets(t *testing.T) {
	components := make([]healthstate.Component, 0, 16)
	for i := 0; i < 16; i++ {
		components = append(components, &fixedHealthComponent{name: fmt.Sprintf("component-%02d", i), value: healthstate.ComponentSnapshot{Status: healthstate.StatusOK, State: "discovered", Details: healthstate.MCPDetails{ToolsList: healthstate.MCPToolsList{ToolNames: []string{strings.Repeat("x", 16*1024)}}}}})
	}
	h, err := newComponentHealth(components, false, func() bool { return true })
	require.NoError(t, err)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health?details=true", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.LessOrEqual(t, w.Body.Len(), healthstate.MaxAggregateBytes)
	var response struct {
		Truncated  bool `json:"truncated"`
		Components map[string]struct {
			Limited bool            `json:"limited"`
			Details json.RawMessage `json:"details"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.True(t, response.Truncated)
	require.Len(t, response.Components, 16)
	require.Empty(t, response.Components["component-00"].Details)
	require.True(t, response.Components["component-00"].Limited)
	require.NotEmpty(t, response.Components["component-15"].Details)
	component := components[0].(*fixedHealthComponent)
	component.value.Details = healthstate.MCPDetails{ToolsList: healthstate.MCPToolsList{ToolNames: []string{strings.Repeat("x", healthstate.MaxComponentBytes)}}}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health/component-00", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.LessOrEqual(t, w.Body.Len(), healthstate.MaxComponentBytes)
	require.NotContains(t, w.Body.String(), `"details"`)
	require.Contains(t, w.Body.String(), `"limited":true`)
	component.value.Details = healthstate.QueueDetails{Utilization: math.NaN()}
	for _, path := range []string{"/health/component-00", "/health?details=true"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.JSONEq(t, `{"error":"snapshot_unavailable"}`, w.Body.String())
	}
}
