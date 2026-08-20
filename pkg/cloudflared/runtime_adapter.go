package cloudflared

import (
	"log/slog"

	"go.uber.org/fx"

	cloudflaredruntime "github.com/openai/tunnel-client/pkg/cloudflared/runtime"
	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane"
)

// Manifest, State, and Supervisor remain available from the historical
// full-client package while the implementation lives in the runtime-safe
// package used by both binaries.
type Manifest = cloudflaredruntime.Manifest
type State = cloudflaredruntime.State
type Supervisor = cloudflaredruntime.Supervisor

// Module is the canonical runtime-safe supervisor module. The full config
// package aliases its shared Cloudflared settings to runtimeconfig, so no
// conversion provider or second implementation is needed here.
var Module = cloudflaredruntime.Module

// BundledVersion returns the pinned cloudflared version shipped by supported
// tunnel-client distributions.
func BundledVersion() string {
	return cloudflaredruntime.BundledVersion()
}

// BundledManifest returns the parsed, checked-in provenance manifest.
func BundledManifest() Manifest {
	return cloudflaredruntime.BundledManifest()
}

// NewState preserves the full-client constructor while delegating to the
// runtime-safe implementation.
func NewState(cfg *config.CloudflaredConfig) *State {
	return cloudflaredruntime.NewState(cfg)
}

type supervisorParams struct {
	fx.In

	Config                *config.CloudflaredConfig
	State                 *State
	Logger                *slog.Logger
	ManagedRuntimeFetcher controlplane.ManagedCloudflareTunnelFetcher `optional:"true"`
}

// NewSupervisor preserves the full-client constructor while delegating to the
// runtime-safe implementation.
func NewSupervisor(p supervisorParams) (*Supervisor, error) {
	return cloudflaredruntime.NewSupervisor(cloudflaredruntime.SupervisorParams{
		Config:                p.Config,
		State:                 p.State,
		Logger:                p.Logger,
		ManagedRuntimeFetcher: p.ManagedRuntimeFetcher,
	})
}
