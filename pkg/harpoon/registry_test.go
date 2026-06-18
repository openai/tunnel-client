package harpoon

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRegistryRejectsInvalidLabel(t *testing.T) {
	registry, err := NewRegistry(discardLogger(), true, nil)
	require.NoError(t, err)

	parsed, err := url.Parse("https://example.com")
	require.NoError(t, err)

	err = registry.RegisterTarget(Target{Label: "bad label", BaseURL: parsed})
	require.Error(t, err)
}

func TestRegistryRejectsDuplicateLabel(t *testing.T) {
	parsed, err := url.Parse("https://example.com")
	require.NoError(t, err)

	registry, err := NewRegistry(discardLogger(), true, []Target{{Label: "auth", BaseURL: parsed}})
	require.NoError(t, err)

	err = registry.RegisterTarget(Target{Label: "auth", BaseURL: parsed})
	require.Error(t, err)
}

func TestRegistryRejectsDuplicateTargetURL(t *testing.T) {
	registry, err := NewRegistry(discardLogger(), true, []Target{{
		Label:   "auth",
		BaseURL: mustURL(t, "https://example.com/auth"),
	}})
	require.NoError(t, err)

	err = registry.RegisterTarget(Target{
		Label:          "auth-unix",
		BaseURL:        mustURL(t, "https://example.com/auth"),
		UnixSocketPath: "/tmp/auth.sock",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate target url")
}

func TestRegistryRejectsPlaintextWhenDisallowed(t *testing.T) {
	parsed, err := url.Parse("http://example.com")
	require.NoError(t, err)

	_, err = NewRegistry(discardLogger(), false, []Target{{Label: "auth", BaseURL: parsed}})
	require.Error(t, err)
}

func TestRegistryNormalizesOnlySchemeAndHostCase(t *testing.T) {
	root, err := url.Parse("hTTps://EXAMPLE.com////")
	require.NoError(t, err)
	withQueryAndFragment, err := url.Parse("https://example.com/bla////?A=1&a=2&a=3#frag")
	require.NoError(t, err)

	registry, err := NewRegistry(discardLogger(), true, []Target{
		{Label: "root", Description: "Root", BaseURL: root},
		{Label: "bla", Description: "Path", BaseURL: withQueryAndFragment},
	})
	require.NoError(t, err)

	targets := registry.Targets()
	require.Len(t, targets, 2)
	require.Equal(t, "https://example.com////", targets[0].BaseURL.String())
	require.Equal(t, "https://example.com/bla////?A=1&a=2&a=3#frag", targets[1].BaseURL.String())
}

func TestRegistryResolveReturnsExactTargetURL(t *testing.T) {
	u, err := url.Parse("https://example.com/bla?x=1#frag")
	require.NoError(t, err)

	registry, err := NewRegistry(discardLogger(), true, []Target{{Label: "svc", BaseURL: u}})
	require.NoError(t, err)

	resolved, err := registry.Resolve("svc")
	require.NoError(t, err)
	require.Equal(t, "https://example.com/bla?x=1#frag", resolved.String())
}

func TestRegistryWaitForTargetReturnsRegisteredTarget(t *testing.T) {
	registry, err := NewRegistry(discardLogger(), true, nil)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resultCh := make(chan Target, 1)
	errCh := make(chan error, 1)
	go func() {
		target, waitErr := registry.WaitForTarget(ctx, "auth")
		if waitErr != nil {
			errCh <- waitErr
			return
		}
		resultCh <- target
	}()

	require.NoError(t, registry.RegisterTarget(Target{
		Label:   "auth",
		BaseURL: mustURL(t, "https://example.com/auth"),
	}))

	select {
	case waitErr := <-errCh:
		require.NoError(t, waitErr)
	case target := <-resultCh:
		require.Equal(t, "auth", target.Label)
		require.Equal(t, "https://example.com/auth", target.BaseURL.String())
	case <-ctx.Done():
		t.Fatal("wait for target did not complete")
	}
}

func TestRegistryWaitForTargetReturnsContextError(t *testing.T) {
	registry, err := NewRegistry(discardLogger(), true, nil)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = registry.WaitForTarget(ctx, "auth")
	require.ErrorIs(t, err, context.Canceled)
}

func TestRegistryAllowsURLUsesExactMatchExceptSchemeHostCase(t *testing.T) {
	root, err := url.Parse("https://example.com")
	require.NoError(t, err)
	bla, err := url.Parse("https://example.com/bla")
	require.NoError(t, err)

	registry, err := NewRegistry(discardLogger(), true, []Target{
		{Label: "root", BaseURL: root},
		{Label: "bla", BaseURL: bla},
	})
	require.NoError(t, err)

	require.True(t, registry.AllowsURL(mustURL(t, "hTTps://EXAMPLE.com")))
	require.True(t, registry.AllowsURL(mustURL(t, "https://example.com/bla")))
	require.False(t, registry.AllowsURL(mustURL(t, "https://example.com/")))
	require.False(t, registry.AllowsURL(mustURL(t, "https://example.com/bla/")))
}

func TestRegistryPreservesDistinctEncodedPathAndQueryForms(t *testing.T) {
	u1 := mustURL(t, "https://example.com/a%2Fb?x=a+b")
	u2 := mustURL(t, "https://example.com/a/b?x=a%20b")

	registry, err := NewRegistry(discardLogger(), true, []Target{
		{Label: "enc", BaseURL: u1},
		{Label: "plain", BaseURL: u2},
	})
	require.NoError(t, err)

	require.True(t, registry.AllowsURL(mustURL(t, "https://example.com/a%2Fb?x=a+b")))
	require.True(t, registry.AllowsURL(mustURL(t, "https://example.com/a/b?x=a%20b")))
	require.False(t, registry.AllowsURL(mustURL(t, "https://example.com/a/b?x=a+b")))
}

func TestRegistryExplainBlockedRedirectDetectsSchemeMismatch(t *testing.T) {
	registry, err := NewRegistry(discardLogger(), true, []Target{
		{
			Label:   "oauth-auth-server-metadata-0",
			BaseURL: mustURL(t, "https://example.com/.well-known/oauth-authorization-server/"),
		},
	})
	require.NoError(t, err)

	details := registry.ExplainBlockedRedirect(mustURL(t, "http://example.com/.well-known/oauth-authorization-server"))
	require.NotNil(t, details)
	require.Equal(t, redirectMismatchSchemeHTTPSToHTTP, details.Kind)
	require.Equal(t, "https://example.com/.well-known/oauth-authorization-server/", details.ExpectedURL)
	require.Equal(t, "https", details.ExpectedScheme)
	require.Equal(t, "http", details.ActualScheme)
}

func TestRegistryExplainBlockedRedirectDetectsTrailingSlashPathMismatch(t *testing.T) {
	registry, err := NewRegistry(discardLogger(), true, []Target{
		{
			Label:   "oauth-auth-server-metadata-0",
			BaseURL: mustURL(t, "https://example.com/.well-known/oauth-authorization-server/"),
		},
	})
	require.NoError(t, err)

	details := registry.ExplainBlockedRedirect(mustURL(t, "https://example.com/.well-known/oauth-authorization-server"))
	require.NotNil(t, details)
	require.Equal(t, redirectMismatchPath, details.Kind)
	require.Equal(t, "https://example.com/.well-known/oauth-authorization-server/", details.ExpectedURL)
}

func TestRegistryExplainBlockedRedirectCacheInvalidatesOnRegister(t *testing.T) {
	registry, err := NewRegistry(discardLogger(), true, []Target{
		{
			Label:   "oauth-auth-server-metadata-0",
			BaseURL: mustURL(t, "https://example.com/.well-known/oauth-authorization-server/"),
		},
	})
	require.NoError(t, err)

	candidate := mustURL(t, "http://example.com/.well-known/oauth-authorization-server")

	first := registry.ExplainBlockedRedirect(candidate)
	require.NotNil(t, first)
	require.Equal(t, redirectMismatchSchemeHTTPSToHTTP, first.Kind)

	err = registry.RegisterTarget(Target{
		Label:   "oauth-auth-server-metadata-http-0",
		BaseURL: mustURL(t, "http://example.com/.well-known/oauth-authorization-server"),
	})
	require.NoError(t, err)

	second := registry.ExplainBlockedRedirect(candidate)
	require.Nil(t, second)
}

func TestRegistryExplainBlockedRedirectCacheEvictsOldEntries(t *testing.T) {
	registry, err := NewRegistry(discardLogger(), true, []Target{
		{
			Label:   "oauth-auth-server-metadata-0",
			BaseURL: mustURL(t, "https://example.com/.well-known/oauth-authorization-server/"),
		},
	})
	require.NoError(t, err)
	registry.explainCacheLimit = 2

	candidateA := mustURL(t, "http://example.com/.well-known/oauth-authorization-server?a=1")
	candidateB := mustURL(t, "http://example.com/.well-known/oauth-authorization-server?a=2")
	candidateC := mustURL(t, "http://example.com/.well-known/oauth-authorization-server?a=3")

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
	registry, err := NewRegistry(discardLogger(), true, []Target{
		{
			Label:   "oauth-auth-server-metadata-0",
			BaseURL: mustURL(t, "https://example.com/.well-known/oauth-authorization-server/"),
		},
	})
	require.NoError(t, err)

	candidate := mustURL(t, "https://example.com/.well-known/oauth-authorization-server?state="+strings.Repeat("a", maxRedirectExplainCacheKeyBytes*2))
	require.NotNil(t, registry.ExplainBlockedRedirect(candidate))

	require.Len(t, registry.explainCache, 1)
	for key := range registry.explainCache {
		require.LessOrEqual(t, len(key), len("sha256:")+64)
		require.True(t, strings.HasPrefix(key, "sha256:"))
	}
}

func TestSummarizeTargets(t *testing.T) {
	urlA, err := url.Parse("https://example.com/base/")
	require.NoError(t, err)
	urlB, err := url.Parse("https://example.org")
	require.NoError(t, err)

	registry, err := NewRegistry(discardLogger(), true, []Target{
		{Label: "auth", Description: "Auth server", BaseURL: urlA},
		{Label: "idp", Description: "Identity", BaseURL: urlB},
	})
	require.NoError(t, err)

	summary := registry.SummarizeTargets()

	require.Equal(t, []map[string]string{
		{"label": "auth", "url": "https://example.com/base/", "desc": "Auth server"},
		{"label": "idp", "url": "https://example.org", "desc": "Identity"},
	}, summary)
}

func TestRegistryRespectsLimit(t *testing.T) {
	parsed, err := url.Parse("https://example.com")
	require.NoError(t, err)

	registry, err := NewRegistryWithLimit(discardLogger(), true, []Target{{Label: "auth", BaseURL: parsed}}, 2)
	require.NoError(t, err)

	err = registry.RegisterTarget(Target{Label: "metrics", BaseURL: mustURL(t, "https://metrics.example.com")})
	require.NoError(t, err)

	err = registry.RegisterTarget(Target{Label: "logs", BaseURL: mustURL(t, "https://logs.example.com")})
	require.Error(t, err)
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	return parsed
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
