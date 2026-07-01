package controlplane

import (
	"context"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/openai/tunnel-client/pkg/types"
)

// PolledCommand represents the unit of work returned by the control plane poll
// API. It mirrors the control-plane contract
// so downstream components can reason about request identity, timing, and
// metadata without depending on raw HTTP payloads.
type PolledCommand interface {
	// RequestID returns the opaque identifier assigned by tunnel-service.
	RequestID() types.RequestID
	// EnqueuedAt reports when tunnel-service enqueued the request (RFC3339).
	EnqueuedAt() time.Time
	// PolledAt reports when tunnel-client fetched the command from the control
	// plane. It is set by the poller and may be zero for legacy callers.
	PolledAt() time.Time
	// Headers exposes auxiliary fields (session identifiers, etc.) attached to
	// the request. Implementations should avoid mutating the returned map.
	Headers() http.Header
	// ShardToken returns the opaque shard token associated with the command, used
	// to route the response back to the originating control-plane shard.
	ShardToken() string
	// Channel returns the logical channel associated with the command.
	Channel() types.Channel
	// SessionID returns the optional MCP session identifier when the connector
	// supplied it, along with a boolean indicating whether it was present.
	SessionID() (string, bool)
}

// Fetcher abstracts the control-plane poll endpoint. Implementations should
// honor the provided limit and return at most that many commands so the poller
// can respect downstream backpressure. TunnelServiceRequestID is returned so
// callers can log/trace the control-plane request identifier associated with
// the poll.
type Fetcher interface {
	Poll(ctx context.Context, limit int) ([]PolledCommand, types.TunnelServiceRequestID, error)
}

// JsonRpcCommand augments PolledCommand with access to the JSON-RPC message.
type JsonRpcCommand interface {
	PolledCommand
	// Message returns the JSON-RPC request to forward to the MCP server.
	Message() jsonrpc.Message
}

// OauthDiscoveryCommand is a marker interface for OAuth discovery commands.
// It currently does not add any methods beyond PolledCommand but exists for
// type differentiation and future extension.
type OauthDiscoveryCommand interface {
	PolledCommand
	// IsOAuthDiscovery returns true for OAuth discovery commands. It exists to
	// make the discriminator explicit; without it, any PolledCommand would satisfy
	// this interface and could be accidentally treated as an OAuth discovery request.
	IsOAuthDiscovery() bool
}

// SessionTerminationCommand is a marker interface for explicit Streamable HTTP session cleanup.
type SessionTerminationCommand interface {
	PolledCommand
	IsSessionTermination() bool
}

// PolledCommandQueue carries polled commands between the control plane poller and dispatcher.
type PolledCommandQueue chan PolledCommand
