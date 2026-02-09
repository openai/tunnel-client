package harpoon

import (
	"sort"
	"strings"
)

var oauthRoleTagMapping = map[string][]string{
	"prmd-resource":          {"protected-resource-metadata", "resource"},
	"prmd-auth-server":       {"protected-resource-metadata", "authorization-server"},
	"prmd-source":            {"protected-resource-metadata", "source-url"},
	"auth-server-metadata":   {"auth-server-metadata"},
	"issuer":                 {"auth-server-metadata", "issuer"},
	"authorization-endpoint": {"auth-server-metadata", "authorization-endpoint"},
	"token-endpoint":         {"auth-server-metadata", "token-endpoint"},
	"jwks-uri":               {"auth-server-metadata", "jwks-uri"},
	"introspection-endpoint": {"auth-server-metadata", "introspection-endpoint"},
	"registration-endpoint":  {"auth-server-metadata", "registration-endpoint"},
}

func normalizeCategorySource(category, source string) (string, string) {
	category = normalizeToken(category)
	source = normalizeToken(source)
	if category == "" {
		category = source
	}
	if source == "" {
		source = category
	}
	if category != "" {
		source = category
	}
	return category, source
}

func normalizeToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		normalized := normalizeToken(tag)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out
}

func roleTags(role string) []string {
	normalized := normalizeToken(role)
	if normalized == "" {
		return nil
	}
	tags, ok := oauthRoleTagMapping[normalized]
	if !ok {
		return []string{normalized}
	}
	out := append([]string{}, tags...)
	return normalizeTags(out)
}
