package oauth

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsOptionalDiscoveryFailure(t *testing.T) {
	t.Parallel()

	t.Run("DisabledTransportIsOptional", func(t *testing.T) {
		t.Parallel()

		require.True(t, IsOptionalDiscoveryFailure(nil, nil, errors.New(`oauth discovery disabled for transport "stdio"`)))
	})

	t.Run("AllNotFoundAttemptsWithoutWWWAuthenticateAreOptional", func(t *testing.T) {
		t.Parallel()

		result := &DiscoveryResult{
			Attempts: []DiscoveryAttempt{
				{
					URL:        "http://localhost:3001/.well-known/oauth-protected-resource/mcp",
					Source:     DiscoverySourceWellKnownPath,
					Tried:      true,
					StatusCode: 404,
					Error:      "oauth discovery status 404",
				},
				{
					URL:        "http://localhost:3001/.well-known/oauth-protected-resource",
					Source:     DiscoverySourceWellKnownRoot,
					Tried:      true,
					StatusCode: 404,
					Error:      "oauth discovery invalid metadata",
				},
			},
		}

		require.True(t, IsOptionalDiscoveryFailure(result, &WWWAuthenticateProbeStatus{
			Attempted: true,
			Error:     "oauth discovery: WWW-Authenticate probe GET got status 200",
		}, errors.New("oauth discovery invalid metadata from http://localhost:3001/.well-known/oauth-protected-resource: decode protected resource metadata: invalid character '<' looking for beginning of value")))
	})

	t.Run("SuccessfulWWWAuthenticateProbeIsNotOptional", func(t *testing.T) {
		t.Parallel()

		result := &DiscoveryResult{
			Attempts: []DiscoveryAttempt{{
				URL:        "https://example.com/.well-known/oauth-protected-resource/mcp",
				Source:     DiscoverySourceWWWAuthenticate,
				Tried:      true,
				StatusCode: 404,
			}},
		}

		require.False(t, IsOptionalDiscoveryFailure(result, &WWWAuthenticateProbeStatus{
			Attempted: true,
			URL:       "https://example.com/.well-known/oauth-protected-resource/mcp",
		}, errors.New("oauth discovery status 404 from https://example.com/.well-known/oauth-protected-resource/mcp")))
	})

	t.Run("MixedStatusesAreNotOptional", func(t *testing.T) {
		t.Parallel()

		result := &DiscoveryResult{
			Attempts: []DiscoveryAttempt{
				{
					URL:        "https://example.com/.well-known/oauth-protected-resource/base",
					Source:     DiscoverySourceWellKnownPath,
					Tried:      true,
					StatusCode: 404,
				},
				{
					URL:        "https://example.com/.well-known/oauth-protected-resource",
					Source:     DiscoverySourceWellKnownRoot,
					Tried:      true,
					StatusCode: 500,
				},
			},
		}

		require.False(t, IsOptionalDiscoveryFailure(result, nil, errors.New("oauth discovery status 500 from https://example.com/.well-known/oauth-protected-resource")))
	})
}
