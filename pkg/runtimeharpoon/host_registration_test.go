package runtimeharpoon

import (
	"io"
	"log/slog"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon/hostbus"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon/internal/hostclassifier"
)

type hostRegistrationTestLifecycle struct{}

func (hostRegistrationTestLifecycle) Append(fx.Hook) {}

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
