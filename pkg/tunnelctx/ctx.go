package tunnelctx

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"go.openai.org/api/tunnel-client/pkg/types"
)

type contextKey struct{}

type identifiers struct {
	sessionID                    string
	requestID                    string
	controlPlaneCommandRequestID types.ControlPlaneRequestID
	tunnelServiceRequestID       types.TunnelServiceRequestID
	shardToken                   string
	rpcRequestID                 *jsonrpc.ID
	channel                      types.Channel
}

// ContextWithSessionID returns a child context that stores the provided MCP session identifier.
//
// An empty session identifier leaves the context unchanged.
func ContextWithSessionID(ctx context.Context, sessionID string) context.Context {
	return withIdentifiers(ctx, func(ids *identifiers) {
		if sessionID == "" {
			return
		}
		ids.sessionID = sessionID
	})
}

// ContextWithRequestID returns a child context that stores the provided MCP request identifier.
//
// An empty request identifier leaves the context unchanged.
func ContextWithRequestID(ctx context.Context, requestID string) context.Context {
	return withIdentifiers(ctx, func(ids *identifiers) {
		if requestID == "" {
			return
		}
		ids.requestID = requestID
	})
}

// ContextWithControlPlaneCommandRequestID returns a child context that stores the
// control plane command request identifier.
func ContextWithControlPlaneCommandRequestID(ctx context.Context, requestID types.ControlPlaneRequestID) context.Context {
	return withIdentifiers(ctx, func(ids *identifiers) {
		if requestID == "" {
			return
		}
		ids.controlPlaneCommandRequestID = requestID
	})
}

// ContextWithTunnelServiceRequestID returns a child context that stores the
// tunnel-service request identifier.
func ContextWithTunnelServiceRequestID(ctx context.Context, requestID types.TunnelServiceRequestID) context.Context {
	return withIdentifiers(ctx, func(ids *identifiers) {
		if requestID == "" {
			return
		}
		ids.tunnelServiceRequestID = requestID
	})
}

// ContextWithShardToken returns a child context that stores the provided shard token.
func ContextWithShardToken(ctx context.Context, shardToken string) context.Context {
	return withIdentifiers(ctx, func(ids *identifiers) {
		if shardToken == "" {
			return
		}
		ids.shardToken = shardToken
	})
}

// ContextWithRPCRequestID returns a child context that stores the provided JSON-RPC request identifier.
func ContextWithRPCRequestID(ctx context.Context, requestID jsonrpc.ID) context.Context {
	return withIdentifiers(ctx, func(ids *identifiers) {
		if !requestID.IsValid() {
			return
		}
		requestIDCopy := requestID
		ids.rpcRequestID = &requestIDCopy
	})
}

// ContextWithChannel returns a child context that stores the provided channel name.
//
// An empty channel name leaves the context unchanged.
func ContextWithChannel(ctx context.Context, channel types.Channel) context.Context {
	return withIdentifiers(ctx, func(ids *identifiers) {
		if channel == "" {
			return
		}
		ids.channel = channel
	})
}

// SessionIDFromContext extracts the MCP session identifier stored in the context, if present.
func SessionIDFromContext(ctx context.Context) (string, bool) {
	ids, ok := identifiersFromContext(ctx)
	if !ok || ids.sessionID == "" {
		return "", false
	}
	return ids.sessionID, true
}

// RequestIDFromContext extracts the MCP request identifier stored in the context, if present.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	ids, ok := identifiersFromContext(ctx)
	if !ok || ids.requestID == "" {
		return "", false
	}
	return ids.requestID, true
}

// ControlPlaneCommandRequestIDFromContext extracts the control plane command
// request identifier stored in the context, if present.
func ControlPlaneCommandRequestIDFromContext(ctx context.Context) (types.ControlPlaneRequestID, bool) {
	ids, ok := identifiersFromContext(ctx)
	if !ok || ids.controlPlaneCommandRequestID == "" {
		return "", false
	}
	return ids.controlPlaneCommandRequestID, true
}

// TunnelServiceRequestIDFromContext extracts the tunnel-service request identifier stored
// in the context, if present.
func TunnelServiceRequestIDFromContext(ctx context.Context) (types.TunnelServiceRequestID, bool) {
	ids, ok := identifiersFromContext(ctx)
	if !ok || ids.tunnelServiceRequestID == "" {
		return "", false
	}
	return ids.tunnelServiceRequestID, true
}

// ShardTokenFromContext extracts the shard token stored in the context, if present.
func ShardTokenFromContext(ctx context.Context) (string, bool) {
	ids, ok := identifiersFromContext(ctx)
	if !ok || ids.shardToken == "" {
		return "", false
	}
	return ids.shardToken, true
}

// RPCRequestIDFromContext extracts the JSON-RPC request identifier stored in the context, if present.
func RPCRequestIDFromContext(ctx context.Context) (jsonrpc.ID, bool) {
	ids, ok := identifiersFromContext(ctx)
	if !ok || ids.rpcRequestID == nil {
		return jsonrpc.ID{}, false
	}
	return *ids.rpcRequestID, true
}

// ChannelFromContext extracts the channel stored in the context, if present.
func ChannelFromContext(ctx context.Context) (types.Channel, bool) {
	ids, ok := identifiersFromContext(ctx)
	if !ok || ids.channel == "" {
		return "", false
	}
	return ids.channel, true
}

func withIdentifiers(ctx context.Context, update func(*identifiers)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	current, _ := identifiersFromContext(ctx)
	original := current
	update(&current)
	if current == original {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, current)
}

func identifiersFromContext(ctx context.Context) (identifiers, bool) {
	if ctx == nil {
		return identifiers{}, false
	}
	v := ctx.Value(contextKey{})
	if v == nil {
		return identifiers{}, false
	}
	ids, ok := v.(identifiers)
	return ids, ok
}
