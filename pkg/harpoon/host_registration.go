package harpoon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"

	"go.uber.org/fx"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/harpoon/hostbus"
	"go.openai.org/api/tunnel-client/pkg/harpoon/internal/hostclassifier"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
)

const autoRegistrationPrefix = "oauth"

type hostBusSubscriberOut struct {
	fx.Out

	Subscriber chan hostbus.URLBundle `name:"harpoon_hostbus_subscriber"`
}

type hostBusSubscriberIn struct {
	fx.In

	Subscriber chan hostbus.URLBundle `name:"harpoon_hostbus_subscriber"`
}

func newHostBus(p hostBusSubscriberIn) (hostbus.HostRegistrationBus, error) {
	return hostbus.New(p.Subscriber)
}

type hostRegistrationParams struct {
	fx.In

	Lifecycle  fx.Lifecycle
	Logger     *slog.Logger
	Registry   *Registry
	Config     *config.HarpoonConfig
	Bus        hostbus.HostRegistrationBus
	Subscriber chan hostbus.URLBundle `name:"harpoon_hostbus_subscriber"`
}

func startHostRegistration(p hostRegistrationParams) error {
	if p.Registry == nil || p.Config == nil || p.Lifecycle == nil {
		return nil
	}
	if p.Subscriber == nil {
		return errors.New("harpoon host registration: subscriber channel is required")
	}
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With(tclog.FieldComponent, tclog.ComponentHarpoon)
	classifier := hostclassifier.NewHostClassifier(p.Config.HostClassifier)

	ctx, cancel := context.WithCancel(context.Background())
	p.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case bundle, ok := <-p.Subscriber:
						if !ok {
							return
						}
						if err := registerHostBundle(bundle, classifier, p.Registry, logger); err != nil {
							logger.Warn("harpoon host auto-registration skipped", slog.String("error", err.Error()))
						}
					}
				}
			}()
			return nil
		},
		OnStop: func(context.Context) error {
			cancel()
			if p.Bus != nil {
				_ = p.Bus.Close()
			}
			return nil
		},
	})
	return nil
}

func registerHostBundle(bundle hostbus.URLBundle, classifier *hostclassifier.HostClassifier, registry *Registry, logger *slog.Logger) error {
	if registry == nil || classifier == nil {
		return nil
	}
	if logger == nil {
		return errors.New("logger is required")
	}
	for idx, record := range bundle.URLs {
		if record.URL == nil {
			logger.Info("harpoon host auto-registration skipped: missing url")
			continue
		}
		private, reason := classifier.IsPrivateHost(record.URL.Hostname())
		if !private {
			logger.Info("harpoon host auto-registration skipped: not private",
				slog.String("url", safeURL(record.URL)),
				slog.String("host", record.URL.Hostname()),
			)
			continue
		}
		baseLabel := buildAutoLabel(record, idx)
		if baseLabel == "" {
			logger.Warn("harpoon host auto-registration skipped: empty label",
				slog.String("url", safeURL(record.URL)),
				slog.String("inclusion_reason", reason),
			)
			continue
		}
		label, collisionCount := resolveAutoLabelCollision(baseLabel, registry)
		if label == "" {
			logger.Warn("harpoon host auto-registration skipped: no available label",
				slog.String("base_label", baseLabel),
				slog.String("url", safeURL(record.URL)),
				slog.String("inclusion_reason", reason),
			)
			continue
		}
		if collisionCount > 0 {
			logger.Info("harpoon host auto-registration resolved label collision",
				slog.String("base_label", baseLabel),
				slog.String("label", label),
				slog.Int("collision_count", collisionCount),
			)
		}
		target := Target{
			Label:           label,
			Description:     record.Description,
			Category:        tagValue(record.Tags, hostbus.TagKeySource),
			Source:          tagValue(record.Tags, hostbus.TagKeySource),
			Tags:            roleTags(tagValue(record.Tags, hostbus.TagKeyRole)),
			InclusionReason: reason,
			BaseURL:         record.URL,
		}
		if err := registry.RegisterTarget(target); err != nil {
			logger.Warn("harpoon host auto-registration failed",
				slog.String("label", label),
				slog.String("url", safeURL(record.URL)),
				slog.String("inclusion_reason", reason),
				slog.String("error", err.Error()),
			)
			continue
		}
		logger.Info("harpoon host auto-registered",
			slog.String("label", label),
			slog.String("url", safeURL(record.URL)),
			slog.String("source", target.Source),
			slog.String("inclusion_reason", reason),
		)
	}
	return nil
}

func buildAutoLabel(record hostbus.URLRecord, fallbackIndex int) string {
	role := tagValue(record.Tags, hostbus.TagKeyRole)
	index := tagValue(record.Tags, hostbus.TagKeyIndex)
	parts := []string{autoRegistrationPrefix}
	if role != "" {
		parts = append(parts, role)
	}
	if index == "" && fallbackIndex >= 0 {
		index = fmt.Sprintf("%d", fallbackIndex)
	}
	if index != "" {
		parts = append(parts, index)
	}
	return sanitizeLabel(strings.Join(parts, "-"))
}

func resolveAutoLabelCollision(baseLabel string, registry *Registry) (string, int) {
	if baseLabel == "" || registry == nil {
		return "", 0
	}

	for i := 0; i < 10_000; i++ {
		candidate := baseLabel
		if i > 0 {
			candidate = labelWithNumericSuffix(baseLabel, i)
		}
		if candidate == "" {
			continue
		}
		if _, exists := registry.Lookup(candidate); exists {
			continue
		}
		return candidate, i
	}
	return "", 0
}

func labelWithNumericSuffix(baseLabel string, suffix int) string {
	if baseLabel == "" || suffix <= 0 {
		return baseLabel
	}

	suffixPart := fmt.Sprintf("-%d", suffix)
	maxBaseLen := 64 - len(suffixPart)
	if maxBaseLen < 1 {
		return ""
	}

	trimmed := baseLabel
	if len(trimmed) > maxBaseLen {
		trimmed = strings.TrimRight(trimmed[:maxBaseLen], "-_")
		if trimmed == "" {
			trimmed = "x"
		}
	}
	return trimmed + suffixPart
}

func tagValue(tags []hostbus.Tag, key hostbus.TagKey) string {
	for _, tag := range tags {
		if tag.Key == key {
			return tag.Value
		}
	}
	return ""
}

var labelSanitizePattern = regexp.MustCompile(`[^a-z0-9_-]+`)

func sanitizeLabel(raw string) string {
	label := strings.ToLower(strings.TrimSpace(raw))
	label = labelSanitizePattern.ReplaceAllString(label, "-")
	label = strings.Trim(label, "-_")
	if label == "" {
		return ""
	}
	if !isLabelStartValid(label[0]) {
		label = "x" + label
	}
	if len(label) > 64 {
		label = label[:64]
	}
	return label
}

func isLabelStartValid(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

func safeURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.String()
}
