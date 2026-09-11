package proxyhealth

import (
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/proxy"
)

const maxHealthRoutes = 16

// componentHealth stores only bounded route keys and never exports route names,
// URLs, identity maps, certificate material, or detailed check errors.
type componentHealth struct {
	mu      sync.RWMutex
	checker *Checker
	keys    []string
}

func newComponentHealth() *componentHealth                       { return &componentHealth{} }
func asComponentHealth(h *componentHealth) healthstate.Component { return h }
func (*componentHealth) Name() string                            { return "proxy" }

func attachComponentHealth(h *componentHealth, checker *Checker) {
	keys := make([]string, 0, maxHealthRoutes)
	checker.statusMu.Lock()
	checker.healthProxied, checker.healthPending, checker.healthFailed = 0, 0, 0
	checker.healthLastCheck = time.Time{}
	for key, status := range checker.routeStatus {
		if status.lastCheck.After(checker.healthLastCheck) {
			checker.healthLastCheck = status.lastCheck
		}
		if status.healthState != HealthStateDirect {
			checker.healthProxied++
			if status.lastCheck.IsZero() {
				checker.healthPending++
			} else if status.healthState == HealthStateUnhealthy {
				checker.healthFailed++
			}
		}
		index := sort.SearchStrings(keys, key)
		if index >= maxHealthRoutes {
			continue
		}
		if len(keys) < maxHealthRoutes {
			keys = append(keys, "")
		}
		copy(keys[index+1:], keys[index:len(keys)-1])
		keys[index] = key
	}
	checker.healthCountsInitialized = true
	checker.statusMu.Unlock()
	h.mu.Lock()
	h.checker, h.keys = checker, keys
	h.mu.Unlock()
}

func (h *componentHealth) Snapshot(time.Time) healthstate.ComponentSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	result := healthstate.ComponentSnapshot{Status: healthstate.StatusUnknown, State: "pending"}
	details := healthstate.ProxyDetails{Routes: make([]healthstate.ProxyRouteDetails, 0, len(h.keys))}
	if h.checker == nil {
		result.Details = details
		return result
	}
	h.checker.statusMu.RLock()
	defer h.checker.statusMu.RUnlock()
	details.RouteCount = len(h.checker.routeStatus)
	result.Limited = details.RouteCount > len(h.keys)
	if !h.checker.healthLastCheck.IsZero() {
		observed := h.checker.healthLastCheck.UTC()
		result.ObservedAt = &observed
	}
	proxied, pending, failed := h.checker.healthProxied > 0, h.checker.healthPending > 0, h.checker.healthFailed > 0
	for i, key := range h.keys {
		status := h.checker.routeStatus[key]
		if status == nil {
			continue
		}
		route := healthstate.ProxyRouteDetails{Label: "route-" + strconv.Itoa(i+1), Kind: "unknown", State: "pending"}
		switch status.route.Kind {
		case proxy.RouteKindControlPlane, proxy.RouteKindMCPChannel, proxy.RouteKindHarpoon:
			route.Kind = string(status.route.Kind)
		}
		if status.healthState == HealthStateDirect {
			route.State = "direct"
		} else {
			if status.lastCheck.IsZero() {
				route.State = "pending"
			} else if status.healthState == HealthStateHealthy {
				route.State = "healthy"
			} else {
				route.State, route.FailureCategory = "unhealthy", "check_failed"
				if len(status.history) > 0 {
					switch status.history[len(status.history)-1].ErrorPhase {
					case "tcp":
						route.FailureCategory = "tcp_failed"
					case "connect":
						route.FailureCategory = "connect_failed"
					}
					if status.history[len(status.history)-1].tlsFailure {
						route.FailureCategory = "tls_failed"
					}
				}
			}
		}
		if !status.lastCheck.IsZero() {
			checked := status.lastCheck.UTC()
			route.LastCheck = &checked
		}
		if !status.lastSuccess.IsZero() {
			success := status.lastSuccess.UTC()
			route.LastSuccess = &success
		}
		details.Routes = append(details.Routes, route)
	}
	result.Status, result.State = healthstate.StatusOK, "healthy"
	if !proxied {
		result.Status, result.State = healthstate.StatusDisabled, "direct"
	}
	if pending {
		result.Status, result.State = healthstate.StatusUnknown, "pending"
	}
	if failed {
		result.Status, result.State, result.ReasonCode = healthstate.StatusDegraded, "unhealthy", "route_check_failed"
	}
	result.Details = details
	return result
}
