package runtimeharpoon

import (
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegistryExplainBlockedRedirectCacheEvictsOldEntries(t *testing.T) {
	registry, err := NewRegistry(runtimeRegistryTestLogger(), true, []Target{{
		Label:   "oauth-auth-server-metadata-0",
		BaseURL: runtimeRegistryTestURL(t, "https://example.com/.well-known/oauth-authorization-server/"),
	}})
	require.NoError(t, err)
	registry.explainCacheLimit = 2

	candidateA := runtimeRegistryTestURL(t, "http://example.com/.well-known/oauth-authorization-server?a=1")
	candidateB := runtimeRegistryTestURL(t, "http://example.com/.well-known/oauth-authorization-server?a=2")
	candidateC := runtimeRegistryTestURL(t, "http://example.com/.well-known/oauth-authorization-server?a=3")

	require.NotNil(t, registry.ExplainBlockedRedirect(candidateA))
	require.NotNil(t, registry.ExplainBlockedRedirect(candidateB))
	require.NotNil(t, registry.ExplainBlockedRedirect(candidateC))

	require.Len(t, registry.explainCache, 2)
	keyA, err := normalizedURLKey(candidateA)
	require.NoError(t, err)
	keyB, err := normalizedURLKey(candidateB)
	require.NoError(t, err)
	keyC, err := normalizedURLKey(candidateC)
	require.NoError(t, err)

	_, hasA := registry.explainCache[keyA]
	_, hasB := registry.explainCache[keyB]
	_, hasC := registry.explainCache[keyC]
	require.False(t, hasA)
	require.True(t, hasB)
	require.True(t, hasC)
	require.Equal(t, []string{keyB, keyC}, registry.explainCacheOrder)
}

func TestRegistryExplainBlockedRedirectCachesHashForOversizedURL(t *testing.T) {
	registry, err := NewRegistry(runtimeRegistryTestLogger(), true, []Target{{
		Label:   "oauth-auth-server-metadata-0",
		BaseURL: runtimeRegistryTestURL(t, "https://example.com/.well-known/oauth-authorization-server/"),
	}})
	require.NoError(t, err)

	candidate := runtimeRegistryTestURL(t, "https://example.com/.well-known/oauth-authorization-server?state="+strings.Repeat("a", maxRedirectExplainCacheKeyBytes*2))
	require.NotNil(t, registry.ExplainBlockedRedirect(candidate))

	require.Len(t, registry.explainCache, 1)
	for key := range registry.explainCache {
		require.LessOrEqual(t, len(key), len("sha256:")+64)
		require.True(t, strings.HasPrefix(key, "sha256:"))
	}
}

func runtimeRegistryTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func runtimeRegistryTestURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	return parsed
}
