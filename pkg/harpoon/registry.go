package harpoon

import (
	"log/slog"

	runtimeharpoon "github.com/openai/tunnel-client/pkg/runtimeharpoon"
)

// The runtime package owns routing and allowlist behavior. Keep full-client
// names as aliases so existing callers remain source-compatible.
type Target = runtimeharpoon.Target
type Registry = runtimeharpoon.Registry

// NewRegistry constructs a registry backed by the shared runtime core.
func NewRegistry(logger *slog.Logger, allowPlaintext bool, targets []Target) (*Registry, error) {
	return runtimeharpoon.NewRegistry(logger, allowPlaintext, targets)
}

// NewRegistryWithLimit constructs a registry with a maximum number of targets.
func NewRegistryWithLimit(logger *slog.Logger, allowPlaintext bool, targets []Target, limit int) (*Registry, error) {
	return runtimeharpoon.NewRegistryWithLimit(logger, allowPlaintext, targets, limit)
}
