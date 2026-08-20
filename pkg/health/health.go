// Package health preserves the full-client health API while the shared
// listener and readiness implementation lives in runtimehealth.
package health

import (
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/cloudflared"
	"github.com/openai/tunnel-client/pkg/runtimehealth"
)

// Service remains the full-client health listener contract.
type Service = runtimehealth.Service

// HealthMuxModule adapts the optional full-client cloudflared state into the
// runtime-safe readiness-gate contract, then delegates all health behavior to
// the canonical runtimehealth module.
var HealthMuxModule = fx.Module(
	"health_full_adapter",
	fx.Provide(
		fx.Annotate(
			fullReadinessGate,
			fx.ResultTags(`group:"runtime_readiness_gates"`),
		),
	),
	runtimehealth.Module,
)

type readinessGateParams struct {
	fx.In

	CloudflaredState *cloudflared.State `optional:"true"`
}

func fullReadinessGate(p readinessGateParams) runtimehealth.ReadinessGate {
	return p.CloudflaredState
}
