package runtimeharpoon

import (
	"sync"
	"time"

	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon/hostbus"
)

// Health observes the registry after construction, avoiding a dependency cycle
// between the local health listener and the Harpoon service using that listener.
type Health struct {
	mu       sync.RWMutex
	registry *Registry
	catalog  *hostbus.StartupCatalogState
}

func NewHealth() *Health                              { return &Health{} }
func (*Health) Name() string                          { return "harpoon" }
func HealthComponent(h *Health) healthstate.Component { return h }

type healthParams struct {
	fx.In
	Health   *Health
	Registry *Registry
	Catalog  *hostbus.StartupCatalogState `optional:"true"`
}

func AttachHealth(p healthParams) {
	p.Health.mu.Lock()
	p.Health.registry, p.Health.catalog = p.Registry, p.Catalog
	p.Health.mu.Unlock()
}

func (h *Health) Snapshot(time.Time) healthstate.ComponentSnapshot {
	h.mu.RLock()
	registry, catalog := h.registry, h.catalog
	h.mu.RUnlock()
	details := healthstate.HarpoonDetails{CatalogState: "pending"}
	result := healthstate.ComponentSnapshot{Status: healthstate.StatusUnknown, State: "pending"}
	if registry != nil {
		details.TargetCount = registry.Count()
	}
	if catalog == nil {
		details.CatalogState = "unobserved"
		result.ReasonCode = "catalog_unobserved"
	} else if done, observed, err := catalog.Snapshot(); done {
		result.ObservedAt = &observed
		result.Status, result.State = healthstate.StatusOK, "settled"
		details.CatalogState = "settled"
		if err != nil {
			result.Status, result.State, result.ReasonCode = healthstate.StatusDegraded, "failed", "catalog_failed"
			details.CatalogState = "failed"
		}
	}
	result.Details = details
	return result
}
