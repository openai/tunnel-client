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
	// capability and process-affinity metadata.
	HeaderName = "X-Tunnel-MCP-Server-Info"

	// MaxChannels bounds the number of channel declarations in one header.
	MaxChannels = 32
	// MaxHeaderBytes bounds the serialized HTTP header value.
	MaxHeaderBytes = 4096
)

const (
	versionV1 = 1
	versionV2 = 2
)

// Declaration is one canonical MCP channel name and its advertised
// capabilities. Stateless and ProcessAffinity are independent: a channel may
// accept self-contained requests while still requiring one tunnel-client
// process for application-local state.
type Declaration struct {
	Name            string
	Stateless       bool
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

type v2Payload struct {
	Version  int         `json:"version"`
	Channels []v2Channel `json:"channels"`
}

type v2Channel struct {
	Name            string `json:"name"`
	Stateless       bool   `json:"stateless,omitempty"`
	ProcessAffinity bool   `json:"proc_affinity,omitempty"`
}

// Build serializes the narrowest header version that can represent the
// declarations. Headers remain v1 unless at least one channel advertises the
// v2-only stateless capability.
func Build(declarations []Declaration) (string, error) {
	for _, declaration := range declarations {
		if declaration.Stateless {
			return buildV2(declarations, MaxChannels, MaxHeaderBytes)
		}
	}
	return BuildV1(declarations)
}

// BuildV1 serializes the exact v1 header shape after validating that every
// declaration is canonical, unique, and within the protocol bounds.
func BuildV1(declarations []Declaration) (string, error) {
	return buildV1(declarations, MaxChannels, MaxHeaderBytes)
}

func buildV1(declarations []Declaration, maxChannels, maxHeaderBytes int) (string, error) {
	validated, err := validateDeclarations(declarations, maxChannels)
	if err != nil {
		return "", err
	}

	channels := make([]v1Channel, 0, len(validated))
	for _, declaration := range validated {
		channels = append(channels, v1Channel{
			Name:            declaration.Name,
			ProcessAffinity: declaration.ProcessAffinity,
		})
	}

	return marshal(v1Payload{
		Version:  versionV1,
		Channels: channels,
	}, versionV1, maxHeaderBytes)
}

func buildV2(declarations []Declaration, maxChannels, maxHeaderBytes int) (string, error) {
	validated, err := validateDeclarations(declarations, maxChannels)
	if err != nil {
		return "", err
	}

	channels := make([]v2Channel, 0, len(validated))
	for _, declaration := range validated {
		channels = append(channels, v2Channel(declaration))
	}

	return marshal(v2Payload{
		Version:  versionV2,
		Channels: channels,
	}, versionV2, maxHeaderBytes)
}

func validateDeclarations(declarations []Declaration, maxChannels int) ([]Declaration, error) {
	if len(declarations) == 0 {
		return nil, fmt.Errorf("mcp server info: at least one channel declaration is required")
	}
	if len(declarations) > maxChannels {
		return nil, fmt.Errorf(
			"mcp server info: channel declaration count %d exceeds maximum %d",
			len(declarations),
			maxChannels,
		)
	}

	validated := make([]Declaration, 0, len(declarations))
	seen := make(map[types.Channel]struct{}, len(declarations))
	for _, declaration := range declarations {
		if declaration.Name == "" {
			return nil, fmt.Errorf("mcp server info: channel name is required")
		}
		channel, err := types.NormalizeChannel(declaration.Name)
		if err != nil {
			return nil, fmt.Errorf("mcp server info: %w", err)
		}
		if channel.String() != declaration.Name {
			return nil, fmt.Errorf(
				"mcp server info: channel name %q is not canonical; use %q",
				declaration.Name,
				channel,
			)
		}
		if _, exists := seen[channel]; exists {
			return nil, fmt.Errorf("mcp server info: duplicate channel declaration %q", channel)
		}
		seen[channel] = struct{}{}
		validated = append(validated, Declaration{
			Name:            channel.String(),
			Stateless:       declaration.Stateless,
			ProcessAffinity: declaration.ProcessAffinity,
		})
	}
	return validated, nil
}

func marshal(payload any, version, maxHeaderBytes int) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("mcp server info: marshal v%d payload: %w", version, err)
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
