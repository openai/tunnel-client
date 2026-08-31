package runtimeharpoon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"go.uber.org/fx"

	tclog "github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon/hostbus"
)

const (
	startupCatalogDigestVersion       = 1
	startupCatalogDigestPrefix        = "hmac-sha256:v1:"
	startupCatalogDigestKeyDomain     = "tunnel-client/harpoon/startup-catalog-digest/key/v1"
	startupCatalogDigestPayloadDomain = "tunnel-client/harpoon/startup-catalog-digest/payload/v1"
)

var errStartupCatalogDigestUnavailable = errors.New("harpoon: startup catalog digest unavailable")

// startupCatalogDigest is the privacy-safe projection emitted after the
// startup Harpoon catalog has finalized.
type startupCatalogDigest struct {
	Value       string
	TargetCount int
}

// startupCatalogDigestState owns the immutable digest captured at the startup
// registration boundary. It stores only the keyed digest and count, never the
// canonical catalog payload or target inputs.
type startupCatalogDigestState struct {
	once sync.Once
	mu   sync.RWMutex

	digest   startupCatalogDigest
	err      error
	captured bool
}

// NewStartupCatalogDigestState constructs the internal Fx-shared startup
// digest holder. It is exported only so the full-client adapter can provide
// the same runtime-owned state without exposing its contents.
func NewStartupCatalogDigestState() *startupCatalogDigestState {
	return &startupCatalogDigestState{}
}

// Capture computes and stores the first startup digest. Subsequent calls keep
// the original startup boundary intact. Only the keyed digest survives this
// call; the canonical payload is discarded by computeStartupCatalogDigest.
func (s *startupCatalogDigestState) Capture(registry *Registry, controlPlane *runtimeconfig.ControlPlaneConfig) error {
	if s == nil {
		return errStartupCatalogDigestUnavailable
	}
	s.once.Do(func() {
		digest, err := computeStartupCatalogDigest(registry, controlPlane)
		s.mu.Lock()
		s.digest = digest
		s.err = err
		s.captured = true
		s.mu.Unlock()
	})

	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.err
}

// Result returns only a successfully captured keyed digest.
func (s *startupCatalogDigestState) Result() (startupCatalogDigest, bool) {
	if s == nil {
		return startupCatalogDigest{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.captured || s.err != nil {
		return startupCatalogDigest{}, false
	}
	return s.digest, true
}

// startupCatalogDigestLogger prevents lifecycle wiring from emitting more than
// one startup digest if it is notified more than once.
type startupCatalogDigestLogger struct {
	once sync.Once
	err  error
}

type startupCatalogDigestLifecycleParams struct {
	fx.In

	Lifecycle      fx.Lifecycle
	Logger         *slog.Logger
	DigestState    *startupCatalogDigestState   `optional:"true"`
	StartupCatalog *hostbus.StartupCatalogState `optional:"true"`
}

type startupCatalogDigestPayload struct {
	Version int                          `json:"version"`
	Targets []startupCatalogDigestTarget `json:"targets"`
}

type startupCatalogDigestTarget struct {
	Label            string   `json:"label"`
	BaseURL          string   `json:"base_url"`
	OAuthAudienceURL string   `json:"oauth_audience_url"`
	UnixSocketPath   string   `json:"unix_socket_path"`
	Source           string   `json:"source"`
	Category         string   `json:"category"`
	Tags             []string `json:"tags"`
}

// Log emits the startup digest at most once. It intentionally returns only a
// generic error so callers cannot accidentally surface raw catalog values.
func (l *startupCatalogDigestLogger) Log(
	ctx context.Context,
	logger *slog.Logger,
	registry *Registry,
	controlPlane *runtimeconfig.ControlPlaneConfig,
) error {
	if l == nil {
		return errStartupCatalogDigestUnavailable
	}
	l.once.Do(func() {
		l.err = logStartupCatalogDigest(ctx, logger, registry, controlPlane)
	})
	return l.err
}

// LogDigest emits an already-captured startup digest at most once. The
// lifecycle path uses this form so it cannot accidentally resnapshot later
// registry mutations after startup finalization.
func (l *startupCatalogDigestLogger) LogDigest(ctx context.Context, logger *slog.Logger, digest startupCatalogDigest) error {
	if l == nil {
		return errStartupCatalogDigestUnavailable
	}
	l.once.Do(func() {
		l.err = logStartupCatalogDigestValue(ctx, logger, digest)
	})
	return l.err
}

// StartCatalogDigestLogging waits for the stricter startup-only Harpoon
// catalog barrier, then emits exactly one digest event for this process. It
// deliberately does not subscribe to later registry mutations.
func StartCatalogDigestLogging(p startupCatalogDigestLifecycleParams) error {
	// The optional dependencies keep isolated Harpoon Fx graphs useful without
	// OAuth or control-plane wiring. Production app graphs supply both.
	if p.Lifecycle == nil || p.Logger == nil || p.DigestState == nil || p.StartupCatalog == nil {
		return nil
	}

	logger := p.Logger.With(tclog.FieldComponent, tclog.ComponentHarpoon)
	ctx, cancel := context.WithCancel(context.Background())
	digestLogger := &startupCatalogDigestLogger{}
	p.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go waitAndLogStartupCatalogDigest(ctx, p.StartupCatalog, p.DigestState, digestLogger, logger)
			return nil
		},
		OnStop: func(context.Context) error {
			cancel()
			return nil
		},
	})
	return nil
}

func waitAndLogStartupCatalogDigest(
	ctx context.Context,
	startupCatalog *hostbus.StartupCatalogState,
	digestState *startupCatalogDigestState,
	digestLogger *startupCatalogDigestLogger,
	logger *slog.Logger,
) {
	if startupCatalog == nil || digestState == nil || startupCatalog.Wait(ctx) != nil || ctx.Err() != nil {
		return
	}
	digest, ok := digestState.Result()
	if !ok || ctx.Err() != nil {
		return
	}
	// LogDigest returns only a generic error. A missing/invalid captured digest
	// is not a startup catalog digest and therefore intentionally produces no
	// extra log.
	_ = digestLogger.LogDigest(ctx, logger, digest)
}

func logStartupCatalogDigest(
	ctx context.Context,
	logger *slog.Logger,
	registry *Registry,
	controlPlane *runtimeconfig.ControlPlaneConfig,
) error {
	if logger == nil {
		return errStartupCatalogDigestUnavailable
	}
	digest, err := computeStartupCatalogDigest(registry, controlPlane)
	if err != nil {
		return err
	}
	return logStartupCatalogDigestValue(ctx, logger, digest)
}

func logStartupCatalogDigestValue(
	ctx context.Context,
	logger *slog.Logger,
	digest startupCatalogDigest,
) error {
	if logger == nil || digest.Value == "" || digest.TargetCount < 0 {
		return errStartupCatalogDigestUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return errStartupCatalogDigestUnavailable
	}
	logger.InfoContext(ctx, "harpoon startup catalog digest",
		slog.String("catalog_digest", digest.Value),
		slog.Int("catalog_digest_version", startupCatalogDigestVersion),
		slog.Int("target_count", digest.TargetCount),
		slog.String("digest_scope", "startup"),
		slog.String("comparability_scope", "same_tunnel_and_runtime_key"),
	)
	return nil
}

func computeStartupCatalogDigest(
	registry *Registry,
	controlPlane *runtimeconfig.ControlPlaneConfig,
) (startupCatalogDigest, error) {
	if registry == nil || controlPlane == nil || controlPlane.APIKey == "" || controlPlane.TunnelID.String() == "" {
		return startupCatalogDigest{}, errStartupCatalogDigestUnavailable
	}

	targets := registry.Targets()
	payload, err := canonicalStartupCatalog(targets)
	if err != nil {
		return startupCatalogDigest{}, errStartupCatalogDigestUnavailable
	}

	derivedKey := deriveStartupCatalogDigestKey(controlPlane.APIKey, controlPlane.TunnelID.String())
	mac := hmac.New(sha256.New, derivedKey[:])
	writeDigestFrame(mac, startupCatalogDigestPayloadDomain)
	_, _ = mac.Write(payload)
	sum := mac.Sum(nil)

	return startupCatalogDigest{
		Value:       startupCatalogDigestPrefix + hex.EncodeToString(sum),
		TargetCount: len(targets),
	}, nil
}

func canonicalStartupCatalog(targets []Target) ([]byte, error) {
	canonical := make([]startupCatalogDigestTarget, 0, len(targets))
	for _, target := range targets {
		if target.BaseURL == nil {
			return nil, errStartupCatalogDigestUnavailable
		}
		tags := append([]string(nil), target.Tags...)
		sort.Strings(tags)
		canonical = append(canonical, startupCatalogDigestTarget{
			Label:            target.Label,
			BaseURL:          target.BaseURL.String(),
			OAuthAudienceURL: startupCatalogOAuthAudienceURL(target),
			UnixSocketPath:   target.UnixSocketPath,
			Source:           target.Source,
			Category:         target.Category,
			Tags:             tags,
		})
	}
	sort.Slice(canonical, func(i, j int) bool {
		return startupCatalogTargetLess(canonical[i], canonical[j])
	})
	payload, err := json.Marshal(startupCatalogDigestPayload{
		Version: startupCatalogDigestVersion,
		Targets: canonical,
	})
	if err != nil {
		return nil, errStartupCatalogDigestUnavailable
	}
	return payload, nil
}

func startupCatalogTargetLess(left, right startupCatalogDigestTarget) bool {
	switch {
	case left.Label != right.Label:
		return left.Label < right.Label
	case left.BaseURL != right.BaseURL:
		return left.BaseURL < right.BaseURL
	case left.OAuthAudienceURL != right.OAuthAudienceURL:
		return left.OAuthAudienceURL < right.OAuthAudienceURL
	case left.UnixSocketPath != right.UnixSocketPath:
		return left.UnixSocketPath < right.UnixSocketPath
	case left.Source != right.Source:
		return left.Source < right.Source
	case left.Category != right.Category:
		return left.Category < right.Category
	default:
		return startupCatalogTagsLess(left.Tags, right.Tags)
	}
}

// startupCatalogOAuthAudienceURL captures the exact protocol-visible audience
// spelling only for targets accepted by the OAuth audience tool. Other targets
// retain registry-normalized URL comparison so harmless routing spellings do
// not make otherwise equivalent catalogs look different.
func startupCatalogOAuthAudienceURL(target Target) string {
	if normalizeToken(target.Category) != "oauth" ||
		!hasAllTags(target.Tags, []string{"auth-server-metadata", "token-endpoint"}) {
		return ""
	}
	audienceURL := target.originalURL
	if audienceURL == nil {
		audienceURL = target.BaseURL
	}
	if audienceURL == nil {
		return ""
	}
	audienceScheme := strings.ToLower(audienceURL.Scheme)
	if (audienceScheme != "http" && audienceScheme != "https") ||
		audienceURL.Host == "" || audienceURL.User != nil || audienceURL.Fragment != "" {
		return ""
	}
	return audienceURL.String()
}

func startupCatalogTagsLess(left, right []string) bool {
	for idx := 0; idx < len(left) && idx < len(right); idx++ {
		if left[idx] != right[idx] {
			return left[idx] < right[idx]
		}
	}
	return len(left) < len(right)
}

func deriveStartupCatalogDigestKey(runtimeAPIKey, tunnelID string) [sha256.Size]byte {
	mac := hmac.New(sha256.New, []byte(runtimeAPIKey))
	writeDigestFrame(mac, startupCatalogDigestKeyDomain)
	writeDigestFrame(mac, tunnelID)
	var key [sha256.Size]byte
	copy(key[:], mac.Sum(nil))
	return key
}

func writeDigestFrame(mac interface{ Write([]byte) (int, error) }, value string) {
	_, _ = mac.Write([]byte(value))
	_, _ = mac.Write([]byte{0})
}
