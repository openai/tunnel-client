package headerscope

import "context"

type discoveryContextKey struct{}

// WithMCPDiscovery marks requests that are part of MCP discovery or startup
// probing, where connector-provided headers are not available yet.
func WithMCPDiscovery(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, discoveryContextKey{}, true)
}

// IsMCPDiscovery reports whether the request context should receive
// discovery-scoped MCP static headers.
func IsMCPDiscovery(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(discoveryContextKey{}).(bool)
	return v
}
