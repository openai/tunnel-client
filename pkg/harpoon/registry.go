package harpoon

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

var labelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

const defaultRegistryLimit = 10000

// Target describes a registered outbound HTTP target.
type Target struct {
	Label           string
	Description     string
	Source          string
	InclusionReason string
	BaseURL         *url.URL
}

// Registry stores allowed targets keyed by label.
type Registry struct {
	logger         *slog.Logger
	allowPlaintext bool
	limit          int
	mu             sync.RWMutex
	targets        map[string]Target
	ordered        []Target
}

// NewRegistry constructs a registry seeded with the provided targets and a default limit.
func NewRegistry(logger *slog.Logger, allowPlaintext bool, targets []Target) (*Registry, error) {
	return NewRegistryWithLimit(logger, allowPlaintext, targets, defaultRegistryLimit)
}

// NewRegistryWithLimit constructs a registry with a maximum number of targets.
func NewRegistryWithLimit(logger *slog.Logger, allowPlaintext bool, targets []Target, limit int) (*Registry, error) {
	if logger == nil {
		return nil, errors.New("harpoon: logger is required")
	}
	if limit <= 0 {
		return nil, errors.New("harpoon: registry limit must be positive")
	}
	registry := &Registry{
		logger:         logger,
		allowPlaintext: allowPlaintext,
		limit:          limit,
		targets:        make(map[string]Target, len(targets)),
		ordered:        make([]Target, 0, len(targets)),
	}
	for _, target := range targets {
		if err := registry.RegisterTarget(target); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// RegisterTarget adds a target to the registry after validation.
func (r *Registry) RegisterTarget(target Target) error {
	if r == nil {
		return errors.New("harpoon: registry is nil")
	}
	label := strings.TrimSpace(target.Label)
	if label == "" {
		return errors.New("harpoon: target label is required")
	}
	if !labelPattern.MatchString(label) {
		return fmt.Errorf("harpoon: invalid target label %q", label)
	}
	if target.BaseURL == nil {
		return fmt.Errorf("harpoon: target %q base URL is required", label)
	}
	if target.BaseURL.Scheme == "" || target.BaseURL.Host == "" {
		return fmt.Errorf("harpoon: target %q base URL must include scheme and host", label)
	}
	if !r.allowPlaintext && !isHTTPS(target.BaseURL) {
		return fmt.Errorf("harpoon: target %q base URL must use https", label)
	}
	escapedPath := target.BaseURL.EscapedPath()
	if escapedPath != "" && !strings.HasPrefix(escapedPath, "/") {
		return fmt.Errorf("harpoon: target %q base URL path must be absolute", label)
	}
	if hasTraversal(target.BaseURL.Path) {
		return fmt.Errorf("harpoon: target %q base URL contains invalid path segments", label)
	}

	normalized, err := normalizeURL(target.BaseURL)
	if err != nil {
		return fmt.Errorf("harpoon: target %q base URL is invalid: %w", label, err)
	}

	cleanTarget := Target{
		Label:           label,
		Description:     strings.TrimSpace(target.Description),
		Source:          strings.TrimSpace(target.Source),
		InclusionReason: strings.TrimSpace(target.InclusionReason),
		BaseURL:         normalized,
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.targets[label]; exists {
		return fmt.Errorf("harpoon: duplicate target label %q", label)
	}
	if len(r.targets) >= r.limit {
		return fmt.Errorf("harpoon: registry limit %d exceeded", r.limit)
	}
	r.targets[label] = cleanTarget
	r.ordered = append(r.ordered, cleanTarget)
	return nil
}

// Targets returns a copy of the registered targets in registration order.
func (r *Registry) Targets() []Target {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Target, len(r.ordered))
	copy(out, r.ordered)
	return out
}

// Count reports the number of registered targets.
func (r *Registry) Count() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.targets)
}

// Lookup returns the target for a label.
func (r *Registry) Lookup(label string) (Target, bool) {
	if r == nil {
		return Target{}, false
	}
	label = strings.TrimSpace(label)
	r.mu.RLock()
	defer r.mu.RUnlock()
	target, ok := r.targets[label]
	return target, ok
}

// Resolve returns the target URL for a label.
func (r *Registry) Resolve(label string) (*url.URL, error) {
	target, ok := r.Lookup(label)
	if !ok {
		return nil, fmt.Errorf("unknown target label %q", label)
	}
	if target.BaseURL == nil {
		return nil, fmt.Errorf("target %q has empty url", label)
	}
	resolved := *target.BaseURL
	return &resolved, nil
}

// AllowsURL reports whether the URL exactly matches any registered target after
// normalization.
func (r *Registry) AllowsURL(candidate *url.URL) bool {
	if r == nil || candidate == nil {
		return false
	}
	if candidate.Scheme == "" || candidate.Host == "" {
		return false
	}
	if !r.allowPlaintext && !isHTTPS(candidate) {
		return false
	}
	if hasTraversal(candidate.Path) {
		return false
	}
	candidateKey, err := normalizedURLKey(candidate)
	if err != nil {
		return false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, target := range r.targets {
		if target.BaseURL == nil {
			continue
		}
		targetKey, keyErr := normalizedURLKey(target.BaseURL)
		if keyErr != nil {
			continue
		}
		if candidateKey == targetKey {
			return true
		}
	}
	return false
}

func isHTTPS(u *url.URL) bool {
	return strings.EqualFold(u.Scheme, "https")
}

func normalizeURL(raw *url.URL) (*url.URL, error) {
	if raw == nil {
		return nil, errors.New("url is required")
	}
	if raw.Scheme == "" || raw.Host == "" {
		return nil, errors.New("url must include scheme and host")
	}

	normalized := *raw
	normalized.Scheme = strings.ToLower(normalized.Scheme)
	normalized.Host = strings.ToLower(normalized.Host)
	return &normalized, nil
}

func normalizedURLKey(raw *url.URL) (string, error) {
	normalized, err := normalizeURL(raw)
	if err != nil {
		return "", err
	}
	if normalized.Scheme == "" || normalized.Host == "" {
		return "", errors.New("url must include scheme and host")
	}
	return normalized.String(), nil
}

func hasTraversal(rawPath string) bool {
	segments := strings.Split(rawPath, "/")
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		unescaped, err := url.PathUnescape(segment)
		if err != nil {
			return true
		}
		if unescaped == "." || unescaped == ".." {
			return true
		}
	}
	return false
}

// SummarizeTargets returns a stable, log-friendly projection of targets.
func (r *Registry) SummarizeTargets() []map[string]string {
	if r == nil {
		return nil
	}
	targets := r.Targets()
	out := make([]map[string]string, 0, len(targets))
	for _, t := range targets {
		base := ""
		if t.BaseURL != nil {
			base = t.BaseURL.String()
		}
		out = append(out, map[string]string{
			"label": t.Label,
			"url":   base,
			"desc":  t.Description,
		})
	}
	return out
}
