package harpoon

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
)

var labelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

const defaultRegistryLimit = 10000

// Target describes a registered outbound HTTP target.
type Target struct {
	Label       string
	Description string
	BaseURL     *url.URL
}

// Registry stores allowed targets keyed by label.
type Registry struct {
	allowPlaintext bool
	limit          int
	mu             sync.RWMutex
	targets        map[string]Target
	ordered        []Target
}

// NewRegistry constructs a registry seeded with the provided targets and a default limit.
func NewRegistry(allowPlaintext bool, targets []Target) (*Registry, error) {
	return NewRegistryWithLimit(allowPlaintext, targets, defaultRegistryLimit)
}

// NewRegistryWithLimit constructs a registry with a maximum number of targets.
func NewRegistryWithLimit(allowPlaintext bool, targets []Target, limit int) (*Registry, error) {
	if limit <= 0 {
		return nil, errors.New("harpoon: registry limit must be positive")
	}
	registry := &Registry{
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
	if target.BaseURL.Fragment != "" || target.BaseURL.RawFragment != "" {
		return fmt.Errorf("harpoon: target %q base URL must not include fragments", label)
	}
	if target.BaseURL.RawQuery != "" {
		return fmt.Errorf("harpoon: target %q base URL must not include query parameters", label)
	}
	if !r.allowPlaintext && !isHTTPS(target.BaseURL) {
		return fmt.Errorf("harpoon: target %q base URL must use https", label)
	}
	if target.BaseURL.Path != "" && !strings.HasPrefix(target.BaseURL.Path, "/") {
		return fmt.Errorf("harpoon: target %q base URL path must be absolute", label)
	}
	if hasTraversal(target.BaseURL.Path) {
		return fmt.Errorf("harpoon: target %q base URL contains invalid path segments", label)
	}
	normalized := *target.BaseURL
	if normalized.Path == "" {
		normalized.Path = "/"
	}
	normalized.Path = path.Clean(normalized.Path)
	normalized.RawPath = ""

	cleanTarget := Target{
		Label:       label,
		Description: strings.TrimSpace(target.Description),
		BaseURL:     &normalized,
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

// Resolve joins the provided path with the target's base URL and validates the result.
func (r *Registry) Resolve(label, rawPath string) (*url.URL, error) {
	target, ok := r.Lookup(label)
	if !ok {
		return nil, fmt.Errorf("unknown target label %q", label)
	}
	resolved, err := joinTargetPath(target.BaseURL, rawPath)
	if err != nil {
		return nil, err
	}
	if !sameOrigin(resolved, target.BaseURL) {
		return nil, fmt.Errorf("path resolves outside target %q", label)
	}
	if !hasPathPrefix(target.BaseURL.Path, resolved.Path) {
		return nil, fmt.Errorf("path resolves outside target %q", label)
	}
	return resolved, nil
}

// AllowsURL reports whether the URL is within any registered target.
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
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, target := range r.targets {
		if sameOrigin(candidate, target.BaseURL) && hasPathPrefix(target.BaseURL.Path, candidate.Path) {
			return true
		}
	}
	return false
}

func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func isHTTPS(u *url.URL) bool {
	return strings.EqualFold(u.Scheme, "https")
}

func hasPathPrefix(basePath, fullPath string) bool {
	if basePath == "" {
		basePath = "/"
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	if fullPath == "" {
		fullPath = "/"
	}
	basePath = path.Clean(basePath)
	fullPath = path.Clean(fullPath)
	if basePath == "/" {
		return strings.HasPrefix(fullPath, "/")
	}
	if fullPath == basePath {
		return true
	}
	return strings.HasPrefix(fullPath, basePath+"/")
}

func joinTargetPath(base *url.URL, rawPath string) (*url.URL, error) {
	if base == nil {
		return nil, errors.New("base URL is required")
	}
	parsed, err := url.Parse(rawPath)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}
	if parsed.Scheme != "" || parsed.Host != "" {
		return nil, errors.New("path must be relative")
	}
	if parsed.Fragment != "" {
		return nil, errors.New("path must not include fragments")
	}
	if hasTraversal(parsed.Path) {
		return nil, errors.New("path contains traversal segments")
	}

	basePath := base.Path
	if basePath == "" {
		basePath = "/"
	}
	basePath = path.Clean(basePath)
	cleanRel := path.Clean("/" + strings.TrimPrefix(parsed.Path, "/"))
	if cleanRel == "/" && strings.TrimSpace(parsed.Path) == "" {
		cleanRel = ""
	}

	joinedPath := basePath
	if cleanRel != "" {
		joinedPath = path.Clean(strings.TrimSuffix(basePath, "/") + "/" + strings.TrimPrefix(cleanRel, "/"))
	}

	resolved := *base
	resolved.Path = joinedPath
	resolved.RawPath = ""
	resolved.RawQuery = parsed.RawQuery
	resolved.Fragment = ""
	return &resolved, nil
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
