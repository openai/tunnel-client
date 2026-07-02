package controlplane

import (
	"context"

	"go.openai.org/api/tunnel-client/pkg/types"
)

// Responder posts JSON-RPC responses back to tunnel-service once the MCP server
// finishes handling a polled command.
type Responder interface {
	// PostResponse delivers the MCP JSON-RPC response (and any associated
	// response headers) for the provided request identifier to tunnel-service so
	// the originating connector call can complete. The returned TunnelServiceRequestID
	// reflects the X-Request-Id emitted by tunnel-service for this POST.
	PostResponse(ctx context.Context, requestID types.RequestID, response *types.TunnelResponse) (types.TunnelServiceRequestID, error)
}
