package runtimehealth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/openai/tunnel-client/pkg/clientinstance"
	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/version"
)

var processStartedAt = time.Now()

type componentHealth struct {
	components  map[string]healthstate.Component
	names       []string
	showDetails bool
	ready       func() bool
	startedAt   time.Time
	lifecycle   atomic.Int32
	build       version.BuildMetadata
}

func newComponentHealth(components []healthstate.Component, showDetails bool, ready func() bool) (*componentHealth, error) {
	if len(components) > healthstate.MaxComponents {
		return nil, errors.New("health: too many components")
	}
	h := &componentHealth{components: make(map[string]healthstate.Component), showDetails: showDetails, ready: ready, startedAt: processStartedAt, build: version.CurrentBuildMetadata()}
	for _, component := range components {
		if component == nil || !healthIdentifier(component.Name(), 64) {
			return nil, errors.New("health: invalid component name")
		}
		name := component.Name()
		if _, exists := h.components[name]; exists {
			return nil, fmt.Errorf("health: duplicate component %s", name)
		}
		h.components[name] = component
		h.names = append(h.names, name)
	}
	sort.Strings(h.names)
	return h, nil
}

func healthIdentifier(value string, limit int) bool {
	if len(value) == 0 || len(value) > limit {
		return false
	}
	for _, ch := range value {
		if ch < 'a' || ch > 'z' {
			if (ch < '0' || ch > '9') && ch != '-' && ch != '_' {
				return false
			}
		}
	}
	return true
}

func (h *componentHealth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeHealthError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if len(r.URL.RawQuery) > 128 {
		writeHealthError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeHealthError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	now := time.Now()
	if r.URL.Path != "/health" {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			writeHealthError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/health/")
		component, ok := h.components[name]
		if !ok {
			writeHealthError(w, http.StatusNotFound, "unknown_component")
			return
		}
		value, err := boundedComponent(component, now)
		if err != nil {
			writeHealthError(w, http.StatusInternalServerError, "snapshot_unavailable")
			return
		}
		response := healthstate.ComponentResponse{SchemaVersion: healthstate.SchemaVersion, SnapshotAt: now.UTC(), Component: name, ComponentSnapshot: value}
		data, err := json.Marshal(response)
		if err != nil || len(data) > healthstate.MaxComponentBytes {
			writeHealthError(w, http.StatusInternalServerError, "snapshot_unavailable")
			return
		}
		_, _ = w.Write(data)
		return
	}
	details := h.showDetails
	if r.URL.ForceQuery || (r.URL.RawQuery != "" && len(query) == 0) {
		writeHealthError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	if len(query) != 0 {
		values, ok := query["details"]
		if !ok || len(query) != 1 || len(values) != 1 || (values[0] != "true" && values[0] != "false") {
			writeHealthError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		details = values[0] == "true"
	}
	response, err := h.snapshot(now, details)
	if err != nil {
		writeHealthError(w, http.StatusInternalServerError, "snapshot_unavailable")
		return
	}
	data, err := json.Marshal(response)
	if err == nil && len(data) > healthstate.MaxAggregateBytes {
		response.Truncated = true
		for _, name := range h.names {
			value := response.Components[name]
			value.Details = nil
			value.Limited = true
			response.Components[name] = value
			data, err = json.Marshal(response)
			if err != nil || len(data) <= healthstate.MaxAggregateBytes {
				break
			}
		}
	}
	if err != nil || len(data) > healthstate.MaxAggregateBytes {
		writeHealthError(w, http.StatusInternalServerError, "snapshot_unavailable")
		return
	}
	_, _ = w.Write(data)
}

func (h *componentHealth) snapshot(now time.Time, details bool) (healthstate.Snapshot, error) {
	state := "starting"
	switch h.lifecycle.Load() {
	case 1:
		state = "running"
	case 2:
		state = "draining"
	}
	uptime := now.Sub(h.startedAt).Seconds()
	if uptime < 0 {
		uptime = 0
	}
	runtime := healthstate.Runtime{InstanceID: clientinstance.ID(), Version: h.build.Version, Flavor: h.build.Flavor, StartedAt: h.startedAt.UTC(), UptimeSeconds: uptime, Lifecycle: state}
	if len(runtime.Version) > 256 || !utf8.ValidString(runtime.Version) {
		runtime.Version = ""
		runtime.Limited = true
	}
	if len(runtime.Flavor) > 32 || !utf8.ValidString(runtime.Flavor) {
		runtime.Flavor = ""
		runtime.Limited = true
	}
	result := healthstate.Snapshot{SchemaVersion: healthstate.SchemaVersion, Live: true, Ready: h.ready(), SnapshotAt: now.UTC(), Runtime: runtime}
	if details {
		result.Components = make(map[string]healthstate.ComponentSnapshot, len(h.names))
		for _, name := range h.names {
			value, err := boundedComponent(h.components[name], now)
			if err != nil {
				return healthstate.Snapshot{}, err
			}
			result.Components[name] = value
		}
	}
	return result, nil
}

func boundedComponent(component healthstate.Component, now time.Time) (healthstate.ComponentSnapshot, error) {
	snapshot := component.Snapshot(now)
	validStatus := snapshot.Status == healthstate.StatusOK || snapshot.Status == healthstate.StatusDegraded || snapshot.Status == healthstate.StatusUnknown || snapshot.Status == healthstate.StatusDisabled
	if !validStatus || !healthIdentifier(snapshot.State, 32) || (snapshot.ReasonCode != "" && !healthIdentifier(snapshot.ReasonCode, 64)) {
		return healthstate.ComponentSnapshot{Status: healthstate.StatusUnknown, State: "invalid_snapshot", ReasonCode: "invalid_snapshot", Limited: true}, nil
	}
	if snapshot.ObservedAt != nil {
		utc := snapshot.ObservedAt.UTC()
		snapshot.ObservedAt = &utc
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return healthstate.ComponentSnapshot{}, err
	}
	// Reserve space for the component endpoint envelope as well as details.
	if len(encoded) > healthstate.MaxComponentBytes-512 {
		snapshot.Details = nil
		snapshot.Limited = true
	}
	return snapshot, nil
}

func writeHealthError(w http.ResponseWriter, status int, reason string) {
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":%q}`, reason)
}
