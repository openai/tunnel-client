package internal

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	wiretypes "github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/types"
)

// buildBase constructs the common portion of a polled command from the raw
// wire payload and shared validation rules.
func buildBase(raw wiretypes.BaseRawPolledCommand, polledAt time.Time) (basePolledCommand, http.Header, error) {
	if raw.RequestID == "" {
		return basePolledCommand{}, nil, errors.New("missing request_id")
	}
	if raw.ShardToken == "" {
		return basePolledCommand{}, nil, errors.New("missing shard_token")
	}

	headers := make(http.Header)
	if raw.Headers != nil {
		for key, values := range raw.Headers {
			for _, value := range values {
				headers.Add(key, value)
			}
		}
	}

	channel, err := types.NormalizeChannel(raw.Channel)
	if err != nil {
		return basePolledCommand{}, nil, fmt.Errorf("invalid channel: %w", err)
	}

	base := basePolledCommand{
		requestID:  types.RequestID(raw.RequestID),
		enqueued:   raw.CreatedAt,
		polledAt:   polledAt,
		headers:    headers,
		shardToken: raw.ShardToken,
		sessionID:  mcpclient.SessionIDFromHeaders(headers),
		channel:    channel,
	}
	return base, headers, nil
}

func convertRawOauthDiscoveryCommand(raw wiretypes.RawOauthDiscoveryPolledCommand, polledAt time.Time) (*oauthDiscoveryCommand, error) {
	base, _, err := buildBase(raw.BaseRawPolledCommand, polledAt)
	if err != nil {
		return nil, err
	}
	return &oauthDiscoveryCommand{basePolledCommand: base}, nil
}

func convertRawSessionTerminationCommand(raw wiretypes.RawSessionTerminationPolledCommand, polledAt time.Time) (*sessionTerminationCommand, error) {
	base, _, err := buildBase(raw.BaseRawPolledCommand, polledAt)
	if err != nil {
		return nil, err
	}
	if _, ok := base.SessionID(); !ok {
		return nil, errors.New("missing Mcp-Session-Id header")
	}
	return &sessionTerminationCommand{basePolledCommand: base}, nil
}

func convertRawCommand(raw wiretypes.RawJSONRPCPolledCommand, polledAt time.Time) (*jsonRpcCommand, error) {
	// Ensure JSON is non-empty; empty object is acceptable.
	if len(raw.JSONRPC) == 0 {
		return nil, errors.New("missing jsonrpc payload")
	}

	msg, err := jsonrpc.DecodeMessage(raw.JSONRPC)
	if err != nil {
		return nil, fmt.Errorf("invalid jsonrpc payload: %w", err)
	}

	base, _, err := buildBase(raw.BaseRawPolledCommand, polledAt)
	if err != nil {
		return nil, err
	}
	return &jsonRpcCommand{basePolledCommand: base, message: msg}, nil
}
