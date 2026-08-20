package runtimeharpoon

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var headerRewriteKeys = map[string]struct{}{
	"Content-Location": {},
	"Link":             {},
	"Location":         {},
}

var urlPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

// urlRewriter rewrites absolute http/https URLs to Harpoon URLs using known
// targets. Matching is exact after URL normalization (host/scheme case only).
type urlRewriter struct {
	targetsByURL map[string][]Target
}

// URLRewriter is the shared URL transformation helper exposed to thin
// adapters without duplicating rewrite behavior.
type URLRewriter = urlRewriter

func newURLRewriter(targets []Target) *urlRewriter {
	if len(targets) == 0 {
		return &urlRewriter{}
	}
	targetsByURL := make(map[string][]Target, len(targets))
	for _, target := range targets {
		if target.BaseURL == nil {
			continue
		}
		scheme := strings.ToLower(target.BaseURL.Scheme)
		if scheme != "http" && scheme != "https" {
			continue
		}
		key, err := normalizedURLKey(target.BaseURL)
		if err != nil {
			continue
		}
		targetsByURL[key] = append(targetsByURL[key], target)
	}
	return &urlRewriter{targetsByURL: targetsByURL}
}

// NewURLRewriter builds a shared URL transformation helper.
func NewURLRewriter(targets []Target) *URLRewriter {
	return newURLRewriter(targets)
}

var preferredOAuthTagByJSONKey = map[string]string{
	"authorization_servers":  "authorization-server",
	"introspection_endpoint": "introspection-endpoint",
	"issuer":                 "issuer",
	"jwks_uri":               "jwks-uri",
	"registration_endpoint":  "registration-endpoint",
	"revocation_endpoint":    "revocation-endpoint",
	"token_endpoint":         "token-endpoint",
}

func (r *urlRewriter) RewriteURLString(raw string) (string, bool) {
	return r.rewriteURLStringWithHint(raw, "")
}

func (r *urlRewriter) rewriteURLStringWithHint(raw string, jsonKey string) (string, bool) {
	if r == nil || raw == "" {
		return raw, false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return raw, false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return raw, false
	}
	key, err := normalizedURLKey(parsed)
	if err != nil {
		return raw, false
	}
	candidates := r.targetsByURL[key]
	if len(candidates) == 0 {
		return raw, false
	}
	// RFC 9728 requires protected-resource metadata's resource value to remain
	// identical to the resource identifier from which the metadata URL was derived.
	// Keep generic, non-PRMD JSON fields named resource eligible for rewriting.
	if jsonKey == "resource" {
		for _, candidate := range candidates {
			if hasAllTags(candidate.Tags, []string{"protected-resource-metadata", "resource"}) {
				return raw, false
			}
		}
	}
	if target, ok := preferredTargetForJSONKey(candidates, jsonKey); ok {
		return "harpoon://" + target.Label, true
	}
	return "harpoon://" + candidates[0].Label, true
}

func preferredTargetForJSONKey(candidates []Target, jsonKey string) (Target, bool) {
	preferredTag := preferredOAuthTagByJSONKey[jsonKey]
	if preferredTag == "" {
		return Target{}, false
	}
	for _, candidate := range candidates {
		for _, tag := range candidate.Tags {
			if normalizeToken(tag) == preferredTag {
				return candidate, true
			}
		}
	}
	return Target{}, false
}

func transformJSONBody(body []byte, rewriter *urlRewriter) ([]byte, bool) {
	if len(body) == 0 || rewriter == nil || !json.Valid(body) {
		return body, false
	}
	// Intentionally rely on json unmarshal/marshal to avoid maintaining a custom
	// JSON tokenizer/parser in Harpoon.
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, false
	}
	updated, changed := rewriteJSONValue(payload, rewriter, "")
	if !changed {
		return body, false
	}
	encoded, err := json.Marshal(updated)
	if err != nil {
		return body, false
	}
	return encoded, true
}

// TransformJSONBody rewrites allowlisted URLs in a JSON response body.
func TransformJSONBody(body []byte, rewriter *URLRewriter) ([]byte, bool) {
	return transformJSONBody(body, rewriter)
}

func rewriteJSONValue(value any, rewriter *urlRewriter, jsonKey string) (any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		for key, val := range typed {
			updated, ok := rewriteJSONValue(val, rewriter, key)
			if ok {
				typed[key] = updated
				changed = true
			}
		}
		return typed, changed
	case []any:
		changed := false
		for idx, val := range typed {
			updated, ok := rewriteJSONValue(val, rewriter, jsonKey)
			if ok {
				typed[idx] = updated
				changed = true
			}
		}
		return typed, changed
	case string:
		if rewriter == nil {
			return typed, false
		}
		if rewritten, ok := rewriter.rewriteURLStringWithHint(typed, jsonKey); ok {
			return rewritten, true
		}
		return typed, false
	default:
		return typed, false
	}
}

func transformHeaders(headers http.Header, rewriter *urlRewriter) (http.Header, bool) {
	if headers == nil {
		return nil, false
	}
	changed := false
	out := make(http.Header, len(headers))
	for key, values := range headers {
		if len(values) == 0 {
			continue
		}
		copied := make([]string, len(values))
		if shouldRewriteHeader(key) {
			for i, val := range values {
				newVal, updated := rewriteHeaderValue(val, rewriter)
				if updated {
					changed = true
				}
				copied[i] = newVal
			}
		} else {
			copy(copied, values)
		}
		out[key] = copied
	}
	return out, changed
}

// TransformHeaders rewrites allowlisted URLs in response headers.
func TransformHeaders(headers http.Header, rewriter *URLRewriter) (http.Header, bool) {
	return transformHeaders(headers, rewriter)
}

func shouldRewriteHeader(key string) bool {
	if key == "" {
		return false
	}
	_, ok := headerRewriteKeys[http.CanonicalHeaderKey(key)]
	return ok
}

func rewriteHeaderValue(value string, rewriter *urlRewriter) (string, bool) {
	if value == "" || rewriter == nil {
		return value, false
	}
	changed := false
	out := urlPattern.ReplaceAllStringFunc(value, func(match string) string {
		replaced, ok := rewriter.RewriteURLString(match)
		if ok {
			changed = true
			return replaced
		}
		return match
	})
	return out, changed
}
