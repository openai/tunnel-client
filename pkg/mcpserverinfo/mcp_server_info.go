// Package mcpserverinfo serializes the bounded MCP channel metadata that
// tunnel-client advertises to the tunnel control plane.
package mcpserverinfo

import (
	"encoding/json"
	"fmt"

	"github.com/openai/tunnel-client/pkg/types"
)

const (
	// HeaderName is the protected control-plane header carrying MCP channel
	// process-affinity metadata.
	HeaderName = "X-Tunnel-MCP-Server-Info"

	// MaxChannels bounds the number of channel declarations in one v1 header.
	MaxChannels = 32
	// MaxHeaderBytes bounds the serialized HTTP header value.
	MaxHeaderBytes = 4096
)

const currentVersion = 1

// Declaration is one canonical MCP channel name and whether its session work
// must remain on one tunnel-client process.
type Declaration struct {
	Name            string
	ProcessAffinity bool
}

type v1Payload struct {
	Version  int         `json:"version"`
	Channels []v1Channel `json:"channels"`
}

type v1Channel struct {
	Name            string `json:"name"`
	ProcessAffinity bool   `json:"proc_affinity,omitempty"`
}

// BuildV1 serializes the exact v1 header shape after validating that every
// declaration is canonical, unique, and within the protocol bounds.
func BuildV1(declarations []Declaration) (string, error) {
	return buildV1(declarations, MaxChannels, MaxHeaderBytes)
}

func buildV1(declarations []Declaration, maxChannels, maxHeaderBytes int) (string, error) {
	if len(declarations) == 0 {
		return "", fmt.Errorf("mcp server info: at least one channel declaration is required")
	}
	if len(declarations) > maxChannels {
		return "", fmt.Errorf(
			"mcp server info: channel declaration count %d exceeds maximum %d",
			len(declarations),
			maxChannels,
		)
	}

	channels := make([]v1Channel, 0, len(declarations))
	seen := make(map[types.Channel]struct{}, len(declarations))
	for _, declaration := range declarations {
		if declaration.Name == "" {
			return "", fmt.Errorf("mcp server info: channel name is required")
		}
		channel, err := types.NormalizeChannel(declaration.Name)
		if err != nil {
			return "", fmt.Errorf("mcp server info: %w", err)
		}
		if channel.String() != declaration.Name {
			return "", fmt.Errorf(
				"mcp server info: channel name %q is not canonical; use %q",
				declaration.Name,
				channel,
			)
		}
		if _, exists := seen[channel]; exists {
			return "", fmt.Errorf("mcp server info: duplicate channel declaration %q", channel)
		}
		seen[channel] = struct{}{}
		channels = append(channels, v1Channel{
			Name:            channel.String(),
			ProcessAffinity: declaration.ProcessAffinity,
		})
	}

	encoded, err := json.Marshal(v1Payload{
		Version:  currentVersion,
		Channels: channels,
	})
	if err != nil {
		return "", fmt.Errorf("mcp server info: marshal v1 payload: %w", err)
	}
	if len(encoded) > maxHeaderBytes {
		return "", fmt.Errorf(
			"mcp server info: serialized header size %d exceeds maximum %d bytes",
			len(encoded),
			maxHeaderBytes,
		)
	}
	return string(encoded), nil
}
