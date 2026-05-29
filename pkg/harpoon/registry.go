package harpoon

import (
	"context"
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
const defaultRedirectExplainCacheLimit = 256

// Target describes a registered outbound HTTP target.
type Target struct {
	Label           string
	Description     string
	Category        string
	Source          string
	Tags            []string
	InclusionReason string
	BaseURL         *url.URL
	UnixSocketPath  string
}

// Registry stores allowed targets keyed by label.
type Registry struct {
	logger            *slog.Logger
	allowPlaintext    bool
	limit             int
	mu                sync.RWMutex
	targets           map[string]Target
	ordered           []Target
	targetURLKeys     map[string]struct{}
	explainCacheLimit int
	explainCache      map[string]redirectExplainCacheEntry
	explainCacheOrder []string
	stateCh           chan struct{}
}

type redirectMismatchDetails struct {
	Kind           redirectMismatchKind
	ExpectedURL    string
	ExpectedScheme string
	ActualScheme   string
	Reason         string
}

type redirectMismatchKind string

const (
	redirectMismatchSchemeHTTPToHTTPS redirectMismatchKind = "scheme_mismatch_http_to_https"
	redirectMismatchSchemeHTTPSToHTTP redirectMismatchKind = "scheme_mismatch_https_to_http"
	redirectMismatchPath              redirectMismatchKind = "path_mismatch"
	redirectMismatchQuery             redirectMismatchKind = "query_mismatch"
	redirectMismatchHost              redirectMismatchKind = "host_mismatch"
	redirectMismatchOther             redirectMismatchKind = "not_allowlisted_other"
)

type redirectExplainCacheEntry struct {
	hasDetails bool
	details    redirectMismatchDetails
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
		logger:            logger,
		allowPlaintext:    allowPlaintext,
		limit:             limit,
		targets:           make(map[string]Target, len(targets)),
		ordered:           make([]Target, 0, len(targets)),
		targetURLKeys:     make(map[string]struct{}, len(targets)),
		explainCacheLimit: defaultRedirectExplainCacheLimit,
		explainCache:      make(map[string]redirectExplainCacheEntry),
		explainCacheOrder: make([]string, 0, defaultRedirectExplainCacheLimit),
		stateCh:           make(chan struct{}),
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

	category, source := normalizeCategorySource(target.Category, target.Source)
	cleanTarget := Target{
		Label:           label,
		Description:     strings.TrimSpace(target.Description),
		Category:        category,
		Source:          source,
		Tags:            normalizeTags(target.Tags),
		InclusionReason: strings.TrimSpace(target.InclusionReason),
		BaseURL:         normalized,
		UnixSocketPath:  strings.TrimSpace(target.UnixSocketPath),
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
	r.targetURLKeys[normalized.String()] = struct{}{}
	r.clearExplainCacheLocked()
	r.signalStateChangeLocked()
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

// WaitForTarget blocks until a target with the provided label is registered or ctx expires.
func (r *Registry) WaitForTarget(ctx context.Context, label string) (Target, error) {
	if r == nil {
		return Target{}, errors.New("harpoon: registry is nil")
	}
	if ctx == nil {
		return Target{}, errors.New("harpoon: context is required")
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return Target{}, errors.New("harpoon: target label is required")
	}

	for {
		r.mu.RLock()
		target, ok := r.targets[label]
		state := r.stateCh
		r.mu.RUnlock()
		if ok {
			return target, nil
		}

		select {
		case <-ctx.Done():
			return Target{}, ctx.Err()
		case <-state:
		}
	}
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

func (r *Registry) signalStateChangeLocked() {
	if r.stateCh == nil {
		r.stateCh = make(chan struct{})
		return
	}
	close(r.stateCh)
	r.stateCh = make(chan struct{})
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

// TargetForURL returns the configured target whose URL exactly matches candidate.
func (r *Registry) TargetForURL(candidate *url.URL) (Target, bool) {
	if r == nil || candidate == nil {
		return Target{}, false
	}
	if candidate.Scheme == "" || candidate.Host == "" {
		return Target{}, false
	}
	if !r.allowPlaintext && !isHTTPS(candidate) {
		return Target{}, false
	}
	if hasTraversal(candidate.Path) {
		return Target{}, false
	}
	candidateKey, err := normalizedURLKey(candidate)
	if err != nil {
		return Target{}, false
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
			return target, true
		}
	}
	return Target{}, false
}

// ExplainBlockedRedirect classifies why a redirect target missed the allow list.
func (r *Registry) ExplainBlockedRedirect(candidate *url.URL) *redirectMismatchDetails {
	if r == nil || candidate == nil {
		return nil
	}
	if candidate.Scheme == "" || candidate.Host == "" {
		return &redirectMismatchDetails{Kind: redirectMismatchOther, Reason: "redirect target not in allow list"}
	}
	if hasTraversal(candidate.Path) {
		return &redirectMismatchDetails{Kind: redirectMismatchOther, Reason: "redirect target not in allow list"}
	}

	candidateKey, err := normalizedURLKey(candidate)
	if err != nil {
		return &redirectMismatchDetails{Kind: redirectMismatchOther, Reason: "redirect target not in allow list"}
	}
	candidateMatch := analyzeRedirectURL(candidate)

	r.mu.RLock()
	if _, ok := r.targetURLKeys[candidateKey]; ok {
		r.mu.RUnlock()
		r.storeRedirectExplanation(candidateKey, nil)
		return nil
	}
	if cached, ok := r.explainCache[candidateKey]; ok {
		r.mu.RUnlock()
		return cloneRedirectMismatchDetails(cached)
	}

	var pathMismatch *Target
	var queryMismatch *Target
	var hostMismatch *Target
	hostMismatchCount := 0
	var result *redirectMismatchDetails

	for _, target := range r.ordered {
		if target.BaseURL == nil {
			continue
		}
		targetMatch := analyzeRedirectURL(target.BaseURL)
		if targetMatch.host == candidateMatch.host &&
			targetMatch.comparablePath == candidateMatch.comparablePath &&
			targetMatch.query == candidateMatch.query &&
			targetMatch.scheme != candidateMatch.scheme {
			result = &redirectMismatchDetails{
				Kind:           schemeMismatchKind(targetMatch.scheme, candidateMatch.scheme),
				ExpectedURL:    target.BaseURL.String(),
				ExpectedScheme: targetMatch.scheme,
				ActualScheme:   candidateMatch.scheme,
				Reason:         "redirect target not in allow list",
			}
			break
		}

		if pathMismatch == nil &&
			targetMatch.host == candidateMatch.host &&
			targetMatch.scheme == candidateMatch.scheme &&
			targetMatch.query == candidateMatch.query &&
			targetMatch.exactPath != candidateMatch.exactPath {
			targetCopy := target
			pathMismatch = &targetCopy
		}

		if queryMismatch == nil &&
			targetMatch.host == candidateMatch.host &&
			targetMatch.scheme == candidateMatch.scheme &&
			targetMatch.comparablePath == candidateMatch.comparablePath &&
			targetMatch.query != candidateMatch.query {
			targetCopy := target
			queryMismatch = &targetCopy
		}

		if targetMatch.host != candidateMatch.host &&
			targetMatch.scheme == candidateMatch.scheme &&
			targetMatch.comparablePath == candidateMatch.comparablePath &&
			targetMatch.query == candidateMatch.query {
			targetCopy := target
			hostMismatch = &targetCopy
			hostMismatchCount++
		}
	}
	r.mu.RUnlock()

	if result == nil && pathMismatch != nil {
		result = &redirectMismatchDetails{
			Kind:        redirectMismatchPath,
			ExpectedURL: pathMismatch.BaseURL.String(),
			Reason:      "redirect target not in allow list",
		}
	}
	if result == nil && queryMismatch != nil {
		result = &redirectMismatchDetails{
			Kind:        redirectMismatchQuery,
			ExpectedURL: queryMismatch.BaseURL.String(),
			Reason:      "redirect target not in allow list",
		}
	}
	if result == nil && hostMismatch != nil && hostMismatchCount == 1 {
		result = &redirectMismatchDetails{
			Kind:        redirectMismatchHost,
			ExpectedURL: hostMismatch.BaseURL.String(),
			Reason:      "redirect target not in allow list",
		}
	}
	if result == nil {
		result = &redirectMismatchDetails{
			Kind:   redirectMismatchOther,
			Reason: "redirect target not in allow list",
		}
	}
	r.storeRedirectExplanation(candidateKey, result)
	return result
}

func isHTTPS(u *url.URL) bool {
	return strings.EqualFold(u.Scheme, "https")
}

type redirectURLMatch struct {
	scheme         string
	host           string
	exactPath      string
	comparablePath string
	query          string
}

func analyzeRedirectURL(raw *url.URL) redirectURLMatch {
	exactPath := raw.EscapedPath()
	if exactPath == "" {
		exactPath = "/"
	}
	return redirectURLMatch{
		scheme:         strings.ToLower(raw.Scheme),
		host:           strings.ToLower(raw.Host),
		exactPath:      exactPath,
		comparablePath: comparableRedirectPath(exactPath),
		query:          raw.RawQuery,
	}
}

func comparableRedirectPath(path string) string {
	if path == "" {
		return "/"
	}
	trimmed := strings.TrimRight(path, "/")
	if trimmed == "" {
		return "/"
	}
	return trimmed
}

func schemeMismatchKind(expectedScheme, actualScheme string) redirectMismatchKind {
	switch {
	case expectedScheme == "http" && actualScheme == "https":
		return redirectMismatchSchemeHTTPToHTTPS
	case expectedScheme == "https" && actualScheme == "http":
		return redirectMismatchSchemeHTTPSToHTTP
	default:
		return redirectMismatchOther
	}
}

func (r *Registry) storeRedirectExplanation(candidateKey string, details *redirectMismatchDetails) {
	if r == nil || candidateKey == "" {
		return
	}
	entry := redirectExplainCacheEntry{}
	if details != nil {
		entry.hasDetails = true
		entry.details = *details
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.explainCache[candidateKey]; !exists {
		r.explainCacheOrder = append(r.explainCacheOrder, candidateKey)
		for len(r.explainCacheOrder) > r.effectiveExplainCacheLimit() {
			evictedKey := r.explainCacheOrder[0]
			r.explainCacheOrder = r.explainCacheOrder[1:]
			delete(r.explainCache, evictedKey)
		}
	}
	r.explainCache[candidateKey] = entry
}

func cloneRedirectMismatchDetails(entry redirectExplainCacheEntry) *redirectMismatchDetails {
	if !entry.hasDetails {
		return nil
	}
	details := entry.details
	return &details
}

func (r *Registry) effectiveExplainCacheLimit() int {
	if r == nil || r.explainCacheLimit <= 0 {
		return defaultRedirectExplainCacheLimit
	}
	return r.explainCacheLimit
}

func (r *Registry) clearExplainCacheLocked() {
	clear(r.explainCache)
	r.explainCacheOrder = r.explainCacheOrder[:0]
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
