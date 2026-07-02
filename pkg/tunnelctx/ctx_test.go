package tunnelctx_test

import (
	"context"
	"testing"

	"github.com/openai/tunnel-client/pkg/tunnelctx"
	"github.com/openai/tunnel-client/pkg/types"
)

func TestContextIdentifierHelpers(t *testing.T) {
	t.Parallel()

	const (
		controlPlaneRequestID  = "cp-req-001"
		tunnelServiceRequestID = "ts-req-abc"
		requestID              = "req-123"
		sessionID              = "session-abc"
		shardToken             = "shard-xyz"
		channel                = types.ChannelHarpoon
	)

	ctx := context.Background()
	ctx = tunnelctx.ContextWithRequestID(ctx, requestID)
	ctx = tunnelctx.ContextWithSessionID(ctx, sessionID)
	ctx = tunnelctx.ContextWithControlPlaneCommandRequestID(ctx, types.ControlPlaneRequestID(controlPlaneRequestID))
	ctx = tunnelctx.ContextWithTunnelServiceRequestID(ctx, types.TunnelServiceRequestID(tunnelServiceRequestID))
	ctx = tunnelctx.ContextWithShardToken(ctx, shardToken)
	ctx = tunnelctx.ContextWithChannel(ctx, channel)

	session, ok := tunnelctx.SessionIDFromContext(ctx)
	if !ok || session != sessionID {
		t.Fatalf("expected session %q, ok=%v", sessionID, ok)
	}

	request, ok := tunnelctx.RequestIDFromContext(ctx)
	if !ok || request != requestID {
		t.Fatalf("expected request %q, ok=%v", requestID, ok)
	}

	controlPlaneRequestIDFromCtx, ok := tunnelctx.ControlPlaneCommandRequestIDFromContext(ctx)
	if !ok || controlPlaneRequestIDFromCtx.String() != controlPlaneRequestID {
		t.Fatalf("expected control plane command request %q, ok=%v", controlPlaneRequestID, ok)
	}

	tunnelServiceRequestIDFromCtx, ok := tunnelctx.TunnelServiceRequestIDFromContext(ctx)
	if !ok || tunnelServiceRequestIDFromCtx.String() != tunnelServiceRequestID {
		t.Fatalf("expected tunnel service request %q, ok=%v", tunnelServiceRequestID, ok)
	}

	shardTokenFromCtx, ok := tunnelctx.ShardTokenFromContext(ctx)
	if !ok || shardTokenFromCtx != shardToken {
		t.Fatalf("expected shard token %q, ok=%v", shardToken, ok)
	}

	channelFromCtx, ok := tunnelctx.ChannelFromContext(ctx)
	if !ok || channelFromCtx != channel {
		t.Fatalf("expected channel %q, ok=%v", channel, ok)
	}

	t.Run("empty values do not override", func(t *testing.T) {
		ctx := tunnelctx.ContextWithRequestID(ctx, "")
		ctx = tunnelctx.ContextWithSessionID(ctx, "")
		ctx = tunnelctx.ContextWithControlPlaneCommandRequestID(ctx, "")
		ctx = tunnelctx.ContextWithTunnelServiceRequestID(ctx, "")
		ctx = tunnelctx.ContextWithShardToken(ctx, "")
		ctx = tunnelctx.ContextWithChannel(ctx, "")

		session, ok := tunnelctx.SessionIDFromContext(ctx)
		if !ok || session != sessionID {
			t.Fatalf("expected existing session to remain, got %q ok=%v", session, ok)
		}

		request, ok := tunnelctx.RequestIDFromContext(ctx)
		if !ok || request != requestID {
			t.Fatalf("expected existing request to remain, got %q ok=%v", request, ok)
		}

		controlPlaneRequestIDFromCtx, ok := tunnelctx.ControlPlaneCommandRequestIDFromContext(ctx)
		if !ok || controlPlaneRequestIDFromCtx.String() != controlPlaneRequestID {
			t.Fatalf("expected existing control plane command request to remain, got %q ok=%v", controlPlaneRequestIDFromCtx, ok)
		}

		tunnelServiceRequestIDFromCtx, ok := tunnelctx.TunnelServiceRequestIDFromContext(ctx)
		if !ok || tunnelServiceRequestIDFromCtx.String() != tunnelServiceRequestID {
			t.Fatalf("expected existing tunnel service request to remain, got %q ok=%v", tunnelServiceRequestIDFromCtx, ok)
		}

		shardTokenFromCtx, ok := tunnelctx.ShardTokenFromContext(ctx)
		if !ok || shardTokenFromCtx != shardToken {
			t.Fatalf("expected existing shard token to remain, got %q ok=%v", shardTokenFromCtx, ok)
		}

		channelFromCtx, ok := tunnelctx.ChannelFromContext(ctx)
		if !ok || channelFromCtx != channel {
			t.Fatalf("expected existing channel to remain, got %q ok=%v", channelFromCtx, ok)
		}
	})
}
