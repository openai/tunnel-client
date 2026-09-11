package runtimehealth

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/metrics"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

func TestComponentHealthDiscoversOptionalFxProviders(t *testing.T) {
	for _, includeExtension := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "registered"}[includeExtension], func(t *testing.T) {
			meter := sdkmetric.NewMeterProvider()
			t.Cleanup(func() { require.NoError(t, meter.Shutdown(context.Background())) })
			var service Service
			var mux *http.ServeMux
			options := []fx.Option{
				fx.NopLogger,
				Module,
				fx.Supply(meter, &runtimeconfig.HealthConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil))),
				fx.Provide(func() metrics.MetricsExporter { return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}) }),
				fx.Populate(&service),
				fx.Invoke(func(p struct {
					fx.In
					Mux *http.ServeMux `name:"admin_mux"`
				}) {
					mux = p.Mux
				}),
			}
			components := []*fixedHealthComponent{
				{name: "future-storage", value: healthstate.ComponentSnapshot{Status: healthstate.StatusDegraded, State: "unavailable", Details: healthstate.QueueDetails{Depth: 2, Capacity: 8}}},
				{name: "future-extension", value: healthstate.ComponentSnapshot{Status: healthstate.StatusDisabled, State: "disabled"}},
			}
			if includeExtension {
				for _, component := range components {
					options = append(options, fx.Module(component.name, fx.Provide(fx.Annotate(
						func() healthstate.Component { return component },
						fx.ResultTags(`group:"runtime_health_components"`),
					))))
				}
			}
			app := fx.New(options...)
			require.NoError(t, app.Err())
			require.NotNil(t, service)
			get := func(path string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodGet, path, nil)
				r.RemoteAddr = "127.0.0.1:1234"
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				return w
			}
			compact := get("/health")
			require.Equal(t, http.StatusOK, compact.Code)
			require.NotContains(t, compact.Body.String(), `"components"`)
			for _, component := range components {
				require.Zero(t, component.reads, "construction and compact health must not read providers")
			}
			detailed := get("/health?details=true")
			require.Equal(t, http.StatusOK, detailed.Code)
			var aggregate struct {
				Ready      bool                       `json:"ready"`
				Components map[string]json.RawMessage `json:"components"`
			}
			require.NoError(t, json.Unmarshal(detailed.Body.Bytes(), &aggregate))
			require.True(t, aggregate.Ready, "diagnostic providers do not become readiness gates")
			if !includeExtension {
				require.Empty(t, aggregate.Components)
			}
			for _, component := range components {
				response := get("/health/" + component.name)
				if !includeExtension {
					require.Equal(t, http.StatusNotFound, response.Code)
					continue
				}
				require.Equal(t, http.StatusOK, response.Code)
				expected, err := json.Marshal(component.value)
				require.NoError(t, err)
				require.JSONEq(t, string(expected), string(aggregate.Components[component.name]))
				var individual map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &individual))
				require.JSONEq(t, `"`+component.name+`"`, string(individual["component"]))
				for _, key := range []string{"schema_version", "snapshot_at", "component"} {
					delete(individual, key)
				}
				encoded, err := json.Marshal(individual)
				require.NoError(t, err)
				require.JSONEq(t, string(expected), string(encoded))
				require.Equal(t, 2, component.reads)
			}
			ready := get("/readyz")
			require.Equal(t, http.StatusOK, ready.Code)
			require.Equal(t, "ready", ready.Body.String())
		})
	}
}
