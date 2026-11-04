package runtimeharpoon

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon/hostbus"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon/internal/hostclassifier"
)

type hostRegistrationTestLifecycle struct{}

func (hostRegistrationTestLifecycle) Append(fx.Hook) {}

type hostRegistrationCaptureLifecycle struct {
	hooks []fx.Hook
}

func (l *hostRegistrationCaptureLifecycle) Append(hook fx.Hook) {
	l.hooks = append(l.hooks, hook)
}

func TestStartHostRegistrationRequiresLogger(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, false, nil)
	require.NoError(t, err)

	err = StartHostRegistration(HostRegistrationParams{
		Lifecycle:  hostRegistrationTestLifecycle{},
		Registry:   registry,
		Config:     &runtimeconfig.HarpoonConfig{},
		Subscriber: make(chan hostbus.URLBundle),
	})
	require.EqualError(t, err, "harpoon host registration: logger is required")
}

func TestRegisterHostBundleDisallowsClassifierOnlyPrivateMetadataRecords(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, false, nil)
	require.NoError(t, err)
	classifier := hostclassifier.NewHostClassifier(runtimeconfig.HarpoonHostClassifierConfig{
		IncludePrivate: true,
	})

	record := runtimeOAuthURLRecordForTest(t, "https://10.0.0.1/register", "registration-endpoint")
	record.DisallowPrivateHostRegistration = true

	require.NoError(t, registerHostBundle(
		hostbus.URLBundle{URLs: []hostbus.URLRecord{record}},
		classifier,
		registry,
		logger,
	))
	_, ok := registry.Lookup("oauth-registration-endpoint-0")
	require.False(t, ok, "disallowed private metadata must not register through the classifier")
}

func TestRegisterHostBundleAllowsDisallowedRecordOnlyOnExactProtectedResourceOrigin(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, false, nil)
	require.NoError(t, err)
	classifier := hostclassifier.NewHostClassifier(runtimeconfig.HarpoonHostClassifierConfig{
		IncludePrivate: true,
	})

	seed := runtimeOAuthURLRecordForTest(t, "https://10.0.0.1/mcp", "prmd-resource")
	endpoint := runtimeOAuthURLRecordForTest(t, "https://10.0.0.1/register", "registration-endpoint")
	endpoint.DisallowPrivateHostRegistration = true

	require.NoError(t, registerHostBundle(
		hostbus.URLBundle{URLs: []hostbus.URLRecord{seed, endpoint}},
		classifier,
		registry,
		logger,
	))
	_, ok := registry.Lookup("oauth-registration-endpoint-0")
	require.True(t, ok, "exact protected-resource origin should remain eligible")
}

func TestRegisterHostBundleOAuthForwardingOrigins(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		origin string
		url    string
		allow  bool
	}{
		{name: "private origin", origin: "https://10.0.0.1", url: "https://10.0.0.1/token", allow: true},
		{name: "public origin", origin: "https://auth.example.com", url: "https://auth.example.com/token?tenant=one", allow: true},
		{name: "canonical default port", origin: "https://AUTH.example.com:0443/", url: "https://auth.example.com/token", allow: true},
		{name: "explicit default port", origin: "https://auth.example.com", url: "https://AUTH.example.com:443/token", allow: true},
		{name: "canonical IPv6", origin: "https://[2001:db8::1]", url: "https://[2001:0db8:0:0:0:0:0:1]:443/token", allow: true},
		{name: "unapproved private origin", origin: "https://auth.example.com", url: "https://10.0.0.1/token"},
		{name: "unapproved public origin", origin: "https://auth.example.com", url: "https://other.example.com/token"},
		{name: "subdomain", origin: "https://auth.example.com", url: "https://child.auth.example.com/token"},
		{name: "different port", origin: "https://auth.example.com", url: "https://auth.example.com:8443/token"},
		{name: "HTTP", origin: "https://auth.example.com", url: "http://auth.example.com/token"},
		{name: "trailing dot", origin: "https://auth.example.com", url: "https://auth.example.com./token"},
		{name: "IPv6 zone", origin: "https://[fe80::1]", url: "https://[fe80::1%25ethA]/token"},
		{name: "credentials", origin: "https://auth.example.com", url: "https://user:password@auth.example.com/token"},
		{name: "fragment", origin: "https://auth.example.com", url: "https://auth.example.com/token#secret"},
		{name: "port zero", origin: "https://auth.example.com", url: "https://auth.example.com:0/token"},
		{name: "port out of range", origin: "https://auth.example.com", url: "https://auth.example.com:65536/token"},
		{name: "empty port", origin: "https://auth.example.com", url: "https://auth.example.com:/token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			// Plaintext is enabled here to ensure the new policy itself still
			// rejects HTTP, independently of the registry's transport check.
			registry, err := NewRegistry(logger, true, nil)
			require.NoError(t, err)
			classifier := hostclassifier.NewHostClassifier(runtimeconfig.HarpoonHostClassifierConfig{IncludePrivate: true})
			origins, err := newOAuthForwardingOriginPolicy([]*url.URL{mustParseURLForHostRegistration(t, tc.origin)})
			require.NoError(t, err)
			record := oauthURLRecordForTest(t, tc.url, "token-endpoint", "0", "auth-server:oracle:0")
			record.DisallowPrivateHostRegistration = true

			require.NoError(t, registerHostBundleWithForwardingOrigins(hostbus.URLBundle{URLs: []hostbus.URLRecord{record}}, classifier, registry, logger, origins))
			target, ok := registry.Lookup("oauth-token-endpoint-0")
			require.Equal(t, tc.allow, ok)
			require.True(t, record.DisallowPrivateHostRegistration, "approval must not change discovery's trust annotations")
			if ok {
				require.Equal(t, "oauth", target.Source)
				require.Equal(t, "oauth", target.Category)
				require.ElementsMatch(t, []string{"auth-server-metadata", "token-endpoint", "group=auth-server:oracle:0"}, target.Tags)
				require.Equal(t, forwardingRegistrationScope, target.InclusionReason)
				require.Empty(t, target.UnixSocketPath)
			}
		})
	}
}

func TestOAuthForwardingOriginsDoNotSeedProtectedResourcePolicy(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, false, nil)
	require.NoError(t, err)
	classifier := hostclassifier.NewHostClassifier(runtimeconfig.HarpoonHostClassifierConfig{})
	origins, err := newOAuthForwardingOriginPolicy([]*url.URL{mustParseURLForHostRegistration(t, "https://auth.example.com")})
	require.NoError(t, err)
	seed := runtimeOAuthURLRecordForTest(t, "https://auth.example.com/mcp", "prmd-resource")
	seed.DisallowPrivateHostRegistration = true
	bundle := hostbus.URLBundle{URLs: []hostbus.URLRecord{
		seed,
		runtimeOAuthURLRecordForTest(t, "https://child.auth.example.com/register", "registration-endpoint"),
		runtimeOAuthURLRecordForTest(t, "https://auth.example.com:8443/token", "token-endpoint"),
	}}

	require.NoError(t, registerHostBundleWithForwardingOrigins(bundle, classifier, registry, logger, origins))
	require.Equal(t, 1, registry.Count())
	_, ok := registry.Lookup("oauth-prmd-resource-0")
	require.True(t, ok)
	require.True(t, bundle.URLs[0].DisallowPrivateHostRegistration)
}

func TestOAuthForwardingOriginsOnlyAdmitOAuthRecords(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, false, nil)
	require.NoError(t, err)
	classifier := hostclassifier.NewHostClassifier(runtimeconfig.HarpoonHostClassifierConfig{IncludePrivate: true})
	origins, err := newOAuthForwardingOriginPolicy([]*url.URL{mustParseURLForHostRegistration(t, "https://10.0.0.1")})
	require.NoError(t, err)
	record := hostbus.URLRecord{URL: mustParseURLForHostRegistration(t, "https://10.0.0.1/token"), DisallowPrivateHostRegistration: true}

	require.NoError(t, registerHostBundleWithForwardingOrigins(hostbus.URLBundle{URLs: []hostbus.URLRecord{record}}, classifier, registry, logger, origins))
	require.Zero(t, registry.Count())
}

func TestStartHostRegistrationUsesImmutableOAuthForwardingOrigins(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, false, nil)
	require.NoError(t, err)
	subscriber := make(chan hostbus.URLBundle, 2)
	bus, err := hostbus.New(subscriber)
	require.NoError(t, err)
	lifecycle := &hostRegistrationCaptureLifecycle{}
	origin := mustParseURLForHostRegistration(t, "https://auth.example.com")
	require.NoError(t, StartHostRegistration(HostRegistrationParams{
		Lifecycle:  lifecycle,
		Logger:     logger,
		Registry:   registry,
		Config:     &runtimeconfig.HarpoonConfig{OAuthForwardOrigins: []*url.URL{origin}},
		Bus:        bus,
		Subscriber: subscriber,
	}))
	// Later mutation of caller-owned config must not grant another origin.
	origin.Host = "other.example.com"
	require.Len(t, lifecycle.hooks, 1)
	require.NoError(t, lifecycle.hooks[0].OnStart(context.Background()))
	t.Cleanup(func() { require.NoError(t, lifecycle.hooks[0].OnStop(context.Background())) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, raw := range []string{"https://auth.example.com/token", "https://other.example.com/token"} {
		record := runtimeOAuthURLRecordForTest(t, raw, "token-endpoint")
		record.DisallowPrivateHostRegistration = true
		require.NoError(t, hostbus.PublishAndWait(ctx, bus, hostbus.URLBundle{URLs: []hostbus.URLRecord{record}}))
	}
	require.Equal(t, 1, registry.Count())
	target, ok := registry.Lookup("oauth-token-endpoint-0")
	require.True(t, ok)
	require.Equal(t, "https://auth.example.com/token", target.BaseURL.String())
}

func TestOAuthForwardingPolicyRejectsInvalidProgrammaticOrigins(t *testing.T) {
	t.Parallel()

	for _, origin := range []*url.URL{
		nil,
		{Scheme: "http", Host: "auth.example.com"},
		{Scheme: "https", Host: "auth.example.com", Path: "/token"},
		{Scheme: "https", Host: "auth.example.com", RawPath: "/"},
		{Scheme: "https", Host: "auth.example.com", User: url.UserPassword("user", "secret")},
		{Scheme: "https", Host: "auth.example.com", Fragment: "secret"},
		{Scheme: "https", Host: "auth.example.com", RawFragment: "secret"},
		{Scheme: "https", Host: "auth.example.com", RawQuery: "tenant=one"},
		{Scheme: "https", Host: "auth.example.com", ForceQuery: true},
		{Scheme: "https", Host: "auth.example.com", Opaque: "//other.example.com"},
		{Scheme: "https", Host: "auth.example.com:invalid"},
		{Scheme: "https", Host: "auth.example.com:"},
		{Scheme: "https", Host: "auth.example.com:65536"},
		{Scheme: "https", Host: "2001:db8::1"},
		{Scheme: "https", Host: "[fe80::1%ethA]"},
	} {
		_, err := newOAuthForwardingOriginPolicy([]*url.URL{origin})
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
	}
}

func TestRegisterHostBundleLogsPlaintextLoopbackRemediation(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	registry, err := NewRegistry(logger, false, nil)
	require.NoError(t, err)
	classifier := hostclassifier.NewHostClassifier(runtimeconfig.HarpoonHostClassifierConfig{
		IncludeLoopback: true,
	})

	record := runtimeOAuthURLRecordForTest(t, "http://127.0.0.1:8765/.well-known/oauth-protected-resource/mcp", "prmd-source")
	require.NoError(t, registerHostBundle(
		hostbus.URLBundle{URLs: []hostbus.URLRecord{record}},
		classifier,
		registry,
		logger,
	))

	require.Zero(t, registry.Count(), "plaintext loopback target must remain rejected by default")
	require.Contains(t, logs.String(), "harpoon host auto-registration failed")
	require.Contains(t, logs.String(), "base URL must use https")
	require.Contains(t, logs.String(), "--harpoon.allow-plaintext-http")
	require.Contains(t, logs.String(), "HARPOON_ALLOW_PLAINTEXT_HTTP=true")
}

func TestStartupCatalogCaptureExcludesEarlierQueuedDynamicBundle(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, false, nil)
	require.NoError(t, err)
	subscriber := make(chan hostbus.URLBundle, 2)
	bus, err := hostbus.New(subscriber)
	require.NoError(t, err)
	startupCatalog := hostbus.NewStartupCatalogState()
	digestState := NewStartupCatalogDigestState()
	lifecycle := &hostRegistrationCaptureLifecycle{}

	require.NoError(t, StartHostRegistration(HostRegistrationParams{
		Lifecycle:      lifecycle,
		Logger:         logger,
		Registry:       registry,
		Config:         &runtimeconfig.HarpoonConfig{HostClassifier: runtimeconfig.HarpoonHostClassifierConfig{IncludePrivate: true}},
		ControlPlane:   catalogDigestControlPlane("runtime-secret", "tunnel_0123456789abcdef0123456789abcdef"),
		DigestState:    digestState,
		StartupCatalog: startupCatalog,
		Bus:            bus,
		Subscriber:     subscriber,
	}))
	require.Len(t, lifecycle.hooks, 1)
	require.NoError(t, lifecycle.hooks[0].OnStart(context.Background()))
	defer func() {
		startupCatalog.Complete(nil)
		require.NoError(t, lifecycle.hooks[0].OnStop(context.Background()))
	}()

	dynamic := hostbus.URLBundle{URLs: []hostbus.URLRecord{
		runtimeOAuthURLRecordForTest(t, "https://10.0.0.1/oauth", "prmd-resource"),
	}}
	require.NoError(t, bus.Publish(context.Background(), dynamic))
	require.NoError(t, hostbus.PublishAndWait(context.Background(), bus, hostbus.URLBundle{}))

	digest, ok := digestState.Result()
	require.True(t, ok)
	require.Equal(t, 0, digest.TargetCount)
	require.Eventually(t, func() bool {
		return registry.Count() == 1
	}, time.Second, 10*time.Millisecond)
}

func runtimeOAuthURLRecordForTest(t *testing.T, rawURL, role string) hostbus.URLRecord {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	return hostbus.URLRecord{
		URL: parsed,
		Tags: []hostbus.Tag{
			{Key: hostbus.TagKeySource, Value: "oauth"},
			{Key: hostbus.TagKeyRole, Value: role},
			{Key: hostbus.TagKeyIndex, Value: "0"},
		},
	}
}
