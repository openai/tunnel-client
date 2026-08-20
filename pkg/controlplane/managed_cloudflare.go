package controlplane

import "context"

// ManagedCloudflareTunnelMetadata is the non-secret provider metadata returned
// with a managed Cloudflare runtime token. These values are for validation and
// future runtime use; callers must not log them together with the token.
type ManagedCloudflareTunnelMetadata struct {
	TunnelID  string `json:"tunnel_id"`
	Name      string `json:"name"`
	AccountID string `json:"account_id"`
	CreatedAt string `json:"created_at,omitempty"`
}

// ManagedCloudflareTunnelRuntime contains the provider metadata and in-memory
// token needed to launch bundled cloudflared. RuntimeToken must never be
// logged, persisted, or passed through argv.
type ManagedCloudflareTunnelRuntime struct {
	CloudflareTunnel ManagedCloudflareTunnelMetadata
	RuntimeToken     string `json:"-"`
}

// ManagedCloudflareTunnelFetcher retrieves the runtime-only Cloudflare
// connection material for the configured logical tunnel.
type ManagedCloudflareTunnelFetcher interface {
	FetchManagedCloudflareTunnel(ctx context.Context) (*ManagedCloudflareTunnelRuntime, error)
}
