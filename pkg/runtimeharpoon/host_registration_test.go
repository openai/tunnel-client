package runtimeharpoon

import (
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
