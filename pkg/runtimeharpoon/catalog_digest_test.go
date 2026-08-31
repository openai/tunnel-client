package runtimeharpoon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon/hostbus"
	"github.com/openai/tunnel-client/pkg/types"
)

func TestStartupCatalogDigestIsDeterministicAcrossRegistrationOrder(t *testing.T) {
	t.Parallel()

	first := newCatalogDigestRegistry(t,
		catalogDigestTarget("beta", "https://EXAMPLE.test/b?z=2", "second", []string{"oauth", "token-endpoint"}, ""),
		catalogDigestTarget("alpha", "https://auth.example.test/a", "first", []string{"group=x", "oauth"}, ""),
	)
	second := newCatalogDigestRegistry(t,
		catalogDigestTarget("alpha", "https://AUTH.EXAMPLE.TEST/a", "changed description", []string{"oauth", "group=x"}, ""),
		catalogDigestTarget("beta", "https://example.TEST/b?z=2", "changed description", []string{"token-endpoint", "oauth"}, ""),
	)

	firstDigest := mustStartupCatalogDigest(t, first, "runtime-secret", "tunnel_0123456789abcdef0123456789abcdef")
	secondDigest := mustStartupCatalogDigest(t, second, "runtime-secret", "tunnel_0123456789abcdef0123456789abcdef")

	require.Equal(t, firstDigest, secondDigest)
	require.Equal(t, 2, firstDigest.TargetCount)
}

func TestStartupCatalogDigestChangesForEffectiveCatalogFields(t *testing.T) {
	t.Parallel()

	base := catalogDigestTarget("auth", "https://auth.example.test/oauth", "description", []string{"oauth", "token-endpoint"}, "")
	baseDigest := mustStartupCatalogDigest(t, newCatalogDigestRegistry(t, base), "runtime-secret", "tunnel_0123456789abcdef0123456789abcdef")

	tests := []struct {
		name   string
		target Target
	}{
		{name: "label", target: catalogDigestTarget("auth-2", "https://auth.example.test/oauth", "description", []string{"oauth", "token-endpoint"}, "")},
		{name: "url", target: catalogDigestTarget("auth", "https://auth.example.test/other", "description", []string{"oauth", "token-endpoint"}, "")},
		{name: "tags", target: catalogDigestTarget("auth", "https://auth.example.test/oauth", "description", []string{"oauth", "registration-endpoint"}, "")},
		{name: "unix socket", target: catalogDigestTarget("auth", "https://auth.example.test/oauth", "description", []string{"oauth", "token-endpoint"}, "/var/run/oauth.sock")},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			digest := mustStartupCatalogDigest(t, newCatalogDigestRegistry(t, tt.target), "runtime-secret", "tunnel_0123456789abcdef0123456789abcdef")
			require.NotEqual(t, baseDigest.Value, digest.Value)
		})
	}
}

func TestStartupCatalogDigestDistinguishesExactOAuthAudienceURL(t *testing.T) {
	t.Parallel()

	tags := []string{"oauth", "auth-server-metadata", "token-endpoint"}
	schemeSpelling := catalogDigestTarget("auth", "https://auth.example.test/oauth/token", "", tags, "")
	// url.Parse canonicalizes schemes, but embedders may construct URL values
	// directly before registration.
	schemeSpelling.BaseURL.Scheme = "hTTps"
	for _, tt := range []struct {
		name   string
		first  Target
		second Target
	}{
		{
			name:   "scheme spelling",
			first:  schemeSpelling,
			second: catalogDigestTarget("auth", "https://auth.example.test/oauth/token", "", tags, ""),
		},
		{
			name:   "host spelling",
			first:  catalogDigestTarget("auth", "https://AUTH.example.test/oauth/token", "", tags, ""),
			second: catalogDigestTarget("auth", "https://auth.example.test/oauth/token", "", tags, ""),
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			first := newCatalogDigestRegistry(t, tt.first)
			second := newCatalogDigestRegistry(t, tt.second)

			require.Equal(t, first.Targets()[0].BaseURL.String(), second.Targets()[0].BaseURL.String())
			firstExact, ok := first.ExactURL("auth")
			require.True(t, ok)
			secondExact, ok := second.ExactURL("auth")
			require.True(t, ok)
			require.NotEqual(t, firstExact.String(), secondExact.String())

			firstDigest := mustStartupCatalogDigest(t, first, "runtime-secret", "tunnel_0123456789abcdef0123456789abcdef")
			secondDigest := mustStartupCatalogDigest(t, second, "runtime-secret", "tunnel_0123456789abcdef0123456789abcdef")
			require.NotEqual(t, firstDigest.Value, secondDigest.Value)
		})
	}
}

func TestStartupCatalogDigestIgnoresNonEffectiveMetadata(t *testing.T) {
	t.Parallel()

	base := catalogDigestTarget("auth", "https://auth.example.test/oauth", "first description", []string{"oauth"}, "")
	base.InclusionReason = "first reason"
	changed := catalogDigestTarget("auth", "https://auth.example.test/oauth", "second description", []string{"oauth"}, "")
	changed.InclusionReason = "second reason"

	require.Equal(
		t,
		mustStartupCatalogDigest(t, newCatalogDigestRegistry(t, base), "runtime-secret", "tunnel_0123456789abcdef0123456789abcdef").Value,
		mustStartupCatalogDigest(t, newCatalogDigestRegistry(t, changed), "runtime-secret", "tunnel_0123456789abcdef0123456789abcdef").Value,
	)
}

func TestStartupCatalogDigestIsScopedToRuntimeKeyAndTunnel(t *testing.T) {
	t.Parallel()

	registry := newCatalogDigestRegistry(t, catalogDigestTarget("auth", "https://auth.example.test/oauth", "", []string{"oauth"}, ""))
	base := mustStartupCatalogDigest(t, registry, "runtime-secret", "tunnel_0123456789abcdef0123456789abcdef")

	require.NotEqual(t, base.Value, mustStartupCatalogDigest(t, registry, "other-runtime-secret", "tunnel_0123456789abcdef0123456789abcdef").Value)
	require.NotEqual(t, base.Value, mustStartupCatalogDigest(t, registry, "runtime-secret", "tunnel_fedcba9876543210fedcba9876543210").Value)
}

func TestStartupCatalogDigestKnownVector(t *testing.T) {
	t.Parallel()

	registry := newCatalogDigestRegistry(t, catalogDigestTarget("auth", "hTTps://AUTH.example.test/oauth", "", []string{"oauth", "auth-server-metadata", "token-endpoint"}, ""))
	digest := mustStartupCatalogDigest(t, registry, "runtime-secret", "tunnel_0123456789abcdef0123456789abcdef")

	require.Equal(t, "hmac-sha256:v1:1f77d6ee470e14926c13f5bd91e6130bc95a9a510f6f6bafee04f84964dc8ad1", digest.Value)
}

func TestStartupCatalogDigestLogDoesNotExposeCatalogOrKeyInputs(t *testing.T) {
	t.Parallel()

	const (
		runtimeKey  = "sk-runtime-secret-value"
		label       = "private-auth-label"
		rawURL      = "https://alice:password@secret.example.test/private/path?token=secret-query"
		audienceURL = "https://audience-secret.example.test/private/audience?token=audience-query"
		socketPath  = "/private/socket/path.sock"
	)
	registry := newCatalogDigestRegistry(t,
		catalogDigestTarget(label, rawURL, "private description", []string{"secret-tag"}, socketPath),
		catalogDigestTarget("private-audience-label", audienceURL, "audience description", []string{"auth-server-metadata", "token-endpoint"}, ""),
	)
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	require.NoError(t, logStartupCatalogDigest(context.Background(), logger, registry, catalogDigestControlPlane(runtimeKey, "tunnel_0123456789abcdef0123456789abcdef")))

	var record map[string]any
	require.NoError(t, json.NewDecoder(&output).Decode(&record))
	require.Equal(t, "harpoon startup catalog digest", record["msg"])
	require.Equal(t, float64(1), record["catalog_digest_version"])
	require.Equal(t, float64(2), record["target_count"])
	require.Equal(t, "startup", record["digest_scope"])
	require.Equal(t, "same_tunnel_and_runtime_key", record["comparability_scope"])
	require.Regexp(t, regexp.MustCompile(`^hmac-sha256:v1:[0-9a-f]{64}$`), record["catalog_digest"])

	logged := output.String()
	for _, secret := range []string{
		runtimeKey,
		label,
		rawURL,
		audienceURL,
		"secret.example.test",
		"audience-secret.example.test",
		"/private/path",
		"/private/audience",
		"secret-query",
		"audience-query",
		socketPath,
		"secret-tag",
		"private description",
		"private-audience-label",
		"audience description",
	} {
		require.NotContains(t, logged, secret)
	}
}

func TestStartupCatalogDigestLoggerEmitsOnlyOnce(t *testing.T) {
	t.Parallel()

	registry := newCatalogDigestRegistry(t, catalogDigestTarget("auth", "https://auth.example.test/oauth", "", nil, ""))
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	digestLogger := &startupCatalogDigestLogger{}
	controlPlane := catalogDigestControlPlane("runtime-secret", "tunnel_0123456789abcdef0123456789abcdef")

	require.NoError(t, digestLogger.Log(context.Background(), logger, registry, controlPlane))
	require.NoError(t, digestLogger.Log(context.Background(), logger, registry, controlPlane))
	require.Equal(t, 1, strings.Count(output.String(), "harpoon startup catalog digest"))
}

func TestStartupCatalogDigestLifecycleWaitsForFinalizationAndIgnoresLaterMutations(t *testing.T) {
	t.Parallel()

	registry := newCatalogDigestRegistry(t, catalogDigestTarget("auth", "https://auth.example.test/oauth", "", nil, ""))
	controlPlane := catalogDigestControlPlane("runtime-secret", "tunnel_0123456789abcdef0123456789abcdef")
	digestState := NewStartupCatalogDigestState()
	require.NoError(t, digestState.Capture(registry, controlPlane))
	startupCatalog := hostbus.NewStartupCatalogState()
	writer := newCatalogDigestSignalWriter()
	logger := slog.New(slog.NewJSONHandler(writer, nil))
	lifecycle := &catalogDigestTestLifecycle{}

	require.NoError(t, StartCatalogDigestLogging(startupCatalogDigestLifecycleParams{
		Lifecycle:      lifecycle,
		Logger:         logger,
		DigestState:    digestState,
		StartupCatalog: startupCatalog,
	}))
	require.Len(t, lifecycle.hooks, 1)
	require.NoError(t, lifecycle.hooks[0].OnStart(context.Background()))
	require.NotContains(t, writer.String(), "harpoon startup catalog digest")

	require.NoError(t, registry.RegisterTarget(catalogDigestTarget("later", "https://later.example.test/oauth", "", nil, "")))
	startupCatalog.Complete(nil)
	writer.WaitForDigest(t)

	startupCatalog.Complete(nil)
	require.NoError(t, lifecycle.hooks[0].OnStop(context.Background()))

	logged := writer.String()
	require.Equal(t, 1, strings.Count(logged, "harpoon startup catalog digest"))
	var record map[string]any
	require.NoError(t, json.NewDecoder(strings.NewReader(logged)).Decode(&record))
	require.Equal(t, float64(1), record["target_count"])
}

func TestStartupCatalogDigestLogsEmptyFinalizedCatalog(t *testing.T) {
	t.Parallel()

	registry := newCatalogDigestRegistry(t)
	digestState := NewStartupCatalogDigestState()
	require.NoError(t, digestState.Capture(registry, catalogDigestControlPlane("runtime-secret", "tunnel_0123456789abcdef0123456789abcdef")))
	startupCatalog := hostbus.NewStartupCatalogState()
	startupCatalog.Complete(nil)
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	waitAndLogStartupCatalogDigest(
		context.Background(),
		startupCatalog,
		digestState,
		&startupCatalogDigestLogger{},
		logger,
	)

	var record map[string]any
	require.NoError(t, json.NewDecoder(&output).Decode(&record))
	require.Equal(t, "harpoon startup catalog digest", record["msg"])
	require.Equal(t, float64(0), record["target_count"])
}

func TestStartupCatalogDigestSkipsFailedFinalization(t *testing.T) {
	t.Parallel()

	registry := newCatalogDigestRegistry(t, catalogDigestTarget("auth", "https://auth.example.test/oauth", "", nil, ""))
	digestState := NewStartupCatalogDigestState()
	require.NoError(t, digestState.Capture(registry, catalogDigestControlPlane("runtime-secret", "tunnel_0123456789abcdef0123456789abcdef")))
	startupCatalog := hostbus.NewStartupCatalogState()
	startupCatalog.Complete(errors.New("hard startup registration failure"))
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	waitAndLogStartupCatalogDigest(
		context.Background(),
		startupCatalog,
		digestState,
		&startupCatalogDigestLogger{},
		logger,
	)

	require.Empty(t, output.String())
}

func TestStartupCatalogDigestSkipsShutdownCancellation(t *testing.T) {
	t.Parallel()

	registry := newCatalogDigestRegistry(t, catalogDigestTarget("auth", "https://auth.example.test/oauth", "", nil, ""))
	digestState := NewStartupCatalogDigestState()
	require.NoError(t, digestState.Capture(registry, catalogDigestControlPlane("runtime-secret", "tunnel_0123456789abcdef0123456789abcdef")))
	startupCatalog := hostbus.NewStartupCatalogState()
	startupCatalog.Complete(nil)
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	waitAndLogStartupCatalogDigest(
		ctx,
		startupCatalog,
		digestState,
		&startupCatalogDigestLogger{},
		logger,
	)

	require.Empty(t, output.String())
}

func TestStartupCatalogDigestStateKeepsFirstCapturedBoundary(t *testing.T) {
	t.Parallel()

	registry := newCatalogDigestRegistry(t, catalogDigestTarget("auth", "https://auth.example.test/oauth", "", nil, ""))
	controlPlane := catalogDigestControlPlane("runtime-secret", "tunnel_0123456789abcdef0123456789abcdef")
	digestState := NewStartupCatalogDigestState()
	require.NoError(t, digestState.Capture(registry, controlPlane))
	require.NoError(t, registry.RegisterTarget(catalogDigestTarget("later", "https://later.example.test/oauth", "", nil, "")))
	require.NoError(t, digestState.Capture(registry, controlPlane))

	digest, ok := digestState.Result()
	require.True(t, ok)
	require.Equal(t, 1, digest.TargetCount)
}

func TestStartupCatalogDigestReturnsGenericErrors(t *testing.T) {
	t.Parallel()

	registry := newCatalogDigestRegistry(t, catalogDigestTarget("private-label", "https://secret.example.test/private", "", nil, ""))
	_, err := computeStartupCatalogDigest(registry, catalogDigestControlPlane("", "tunnel_0123456789abcdef0123456789abcdef"))
	require.ErrorIs(t, err, errStartupCatalogDigestUnavailable)
	require.NotContains(t, err.Error(), "private-label")
	require.NotContains(t, err.Error(), "secret.example.test")
}

func newCatalogDigestRegistry(t *testing.T, targets ...Target) *Registry {
	t.Helper()
	registry, err := NewRegistry(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), true, targets)
	require.NoError(t, err)
	return registry
}

func catalogDigestTarget(label, rawURL, description string, tags []string, unixSocketPath string) Target {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	return Target{
		Label:          label,
		Description:    description,
		Category:       "oauth",
		Source:         "oauth",
		Tags:           tags,
		BaseURL:        parsed,
		UnixSocketPath: unixSocketPath,
	}
}

func mustStartupCatalogDigest(t *testing.T, registry *Registry, runtimeKey, tunnelID string) startupCatalogDigest {
	t.Helper()
	digest, err := computeStartupCatalogDigest(registry, catalogDigestControlPlane(runtimeKey, tunnelID))
	require.NoError(t, err)
	return digest
}

func catalogDigestControlPlane(runtimeKey, tunnelID string) *runtimeconfig.ControlPlaneConfig {
	return &runtimeconfig.ControlPlaneConfig{
		APIKey:   runtimeKey,
		TunnelID: types.TunnelID(tunnelID),
	}
}

type catalogDigestTestLifecycle struct {
	hooks []fx.Hook
}

func (l *catalogDigestTestLifecycle) Append(hook fx.Hook) {
	l.hooks = append(l.hooks, hook)
}

type catalogDigestSignalWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	seen chan struct{}
	once sync.Once
}

func newCatalogDigestSignalWriter() *catalogDigestSignalWriter {
	return &catalogDigestSignalWriter{seen: make(chan struct{})}
}

func (w *catalogDigestSignalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buf.Write(p)
	w.mu.Unlock()
	if bytes.Contains(p, []byte("harpoon startup catalog digest")) {
		w.once.Do(func() { close(w.seen) })
	}
	return n, err
}

func (w *catalogDigestSignalWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func (w *catalogDigestSignalWriter) WaitForDigest(t *testing.T) {
	t.Helper()
	select {
	case <-w.seen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for startup catalog digest")
	}
}
