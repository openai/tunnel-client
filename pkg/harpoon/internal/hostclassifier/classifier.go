package hostclassifier

import (
	"net/netip"
	"net/url"
	"regexp"
	"strings"

	"github.com/openai/tunnel-client/pkg/config"
)

// HostClassifier decides whether a host should be treated as private. Private hosts are routed by Harpoon.
type HostClassifier struct {
	includeLoopback bool
	includePrivate  bool
	suffixes        []string
	regexes         []*regexp.Regexp
}

// NewHostClassifier builds a classifier from Harpoon config.
func NewHostClassifier(cfg config.HarpoonHostClassifierConfig) *HostClassifier {
	classifier := &HostClassifier{
		includeLoopback: true,
		includePrivate:  true,
	}
	classifier.includeLoopback = cfg.IncludeLoopback
	classifier.includePrivate = cfg.IncludePrivate
	classifier.suffixes = normalizeSuffixes(cfg.IncludeSuffix)
	classifier.regexes = compileHostRegexes(cfg.IncludeRegex)
	return classifier
}

// IsPrivateURL reports whether the URL host should be treated as private.
func (c *HostClassifier) IsPrivateURL(u *url.URL) (bool, string) {
	if u == nil {
		return false, ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return false, "unsupported-scheme"
	}
	return c.IsPrivateHost(u.Hostname())
}

// IsPrivateHost reports whether the host should be treated as private along with the match reason.
func (c *HostClassifier) IsPrivateHost(host string) (bool, string) {
	if c == nil {
		return false, ""
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false, ""
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")

	if addr, err := netip.ParseAddr(host); err == nil {
		if c.includeLoopback && addr.IsLoopback() {
			return true, "loopback"
		}
		if c.includePrivate && addr.IsPrivate() {
			return true, "private-ip"
		}
	}

	for _, suffix := range c.suffixes {
		if suffix == "" {
			continue
		}
		trimmed := strings.TrimPrefix(suffix, ".")
		if host == trimmed || strings.HasSuffix(host, suffix) {
			return true, "suffix:" + suffix
		}
	}

	for _, re := range c.regexes {
		if re != nil && re.MatchString(host) {
			return true, "regex:" + re.String()
		}
	}

	return false, ""
}

func normalizeSuffixes(values []string) []string {
	out := make([]string, 0, len(values))
	for _, raw := range values {
		suffix := strings.TrimSpace(strings.ToLower(raw))
		if suffix == "" {
			continue
		}
		if !strings.HasPrefix(suffix, ".") {
			suffix = "." + suffix
		}
		out = append(out, suffix)
	}
	return out
}

func compileHostRegexes(values []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(values))
	for _, raw := range values {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}
		re, err := regexp.Compile("(?i:" + pattern + ")")
		if err != nil {
			continue
		}
		out = append(out, re)
	}
	return out
}
