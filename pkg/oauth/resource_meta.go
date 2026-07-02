package oauth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/openai/tunnel-client/pkg/headerscope"
	"github.com/openai/tunnel-client/pkg/version"
)

const defaultProtectedResourceMetadataURI = "/.well-known/oauth-protected-resource"

var resourceMetadataParamPattern = regexp.MustCompile(
	`(?i)resource_metadata\s*=\s*(?:"([^"]*)"|([^,\s]+))`,
)

// DiscoverySource labels how a metadata candidate was discovered.
type DiscoverySource string

const (
	DiscoverySourceWWWAuthenticate DiscoverySource = "www_authenticate"
	DiscoverySourceWellKnownPath   DiscoverySource = "well_known_path"
	DiscoverySourceWellKnownRoot   DiscoverySource = "well_known_root"
)

// DiscoveryCandidate represents a URL plus its discovery source.
type DiscoveryCandidate struct {
	URL    *url.URL        `json:"-"`
	Source DiscoverySource `json:"source"`
}

// DiscoveryAttempt captures one discovery attempt for UI/reporting.
type DiscoveryAttempt struct {
	URL        string          `json:"url"`
	Source     DiscoverySource `json:"source"`
	Tried      bool            `json:"tried,omitempty"`
	StatusCode int             `json:"status_code,omitempty"`
	Error      string          `json:"error,omitempty"`
	Selected   bool            `json:"selected,omitempty"`
}

// WWWAuthenticateProbeStatus captures the outcome of a WWW-Authenticate probe.
type WWWAuthenticateProbeStatus struct {
	Attempted bool   `json:"attempted"`
	URL       string `json:"url,omitempty"`
	Error     string `json:"error,omitempty"`
}

type wwwAuthenticateProbeResult struct {
	Attempted bool
	URL       *url.URL
	Error     string
}

func (p wwwAuthenticateProbeResult) status() *WWWAuthenticateProbeStatus {
	if !p.Attempted && p.URL == nil && p.Error == "" {
		return nil
	}
	status := &WWWAuthenticateProbeStatus{Attempted: p.Attempted}
	if p.URL != nil {
		status.URL = p.URL.String()
	}
	if p.Error != "" {
		status.Error = p.Error
	}
	return status
}

// BuildResourceMetadataURLs constructs the ordered list of candidate OAuth
// ProtectedResourceMetaData endpoints derived from the MCP server URL. It
// follows RFC 9728 discovery rules by prioritizing the path-specific well-known
// URI, then the root well-known URI.
func BuildResourceMetadataURLs(serverURL *url.URL) []*url.URL {
	candidates := buildWellKnownCandidates(serverURL)
	urls := make([]*url.URL, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.URL == nil {
			continue
		}
		urls = append(urls, candidate.URL)
	}
	return urls
}

func buildWellKnownCandidates(serverURL *url.URL) []DiscoveryCandidate {
	if serverURL == nil {
		return nil
	}

	base := &url.URL{
		Scheme: serverURL.Scheme,
		Host:   serverURL.Host,
		Path:   defaultProtectedResourceMetadataURI,
	}

	candidates := make([]DiscoveryCandidate, 0, 2)
	pathSuffix := strings.Trim(serverURL.EscapedPath(), "/")
	if pathSuffix != "" {
		normalizedWellKnown := strings.TrimPrefix(defaultProtectedResourceMetadataURI, "/")
		withPath := *base
		if strings.HasPrefix(pathSuffix, normalizedWellKnown) {
			withPath.Path = "/" + pathSuffix
		} else {
			withPath.Path = path.Join(base.Path, pathSuffix)
		}
		candidates = append(candidates, DiscoveryCandidate{
			URL:    &withPath,
			Source: DiscoverySourceWellKnownPath,
		})
	}

	candidates = append(candidates, DiscoveryCandidate{
		URL:    base,
		Source: DiscoverySourceWellKnownRoot,
	})

	return candidates
}

// BuildOAuthDiscoveryCandidates returns the ordered list of OAuth discovery candidates
// plus probe metadata for UI/reporting. It attempts WWW-Authenticate first, then
// the RFC 9728 well-known URLs.
func BuildOAuthDiscoveryCandidates(
	ctx context.Context,
	client *http.Client,
	serverURL *url.URL,
	logger *slog.Logger,
) ([]DiscoveryCandidate, *WWWAuthenticateProbeStatus, error) {
	if logger == nil {
		return nil, nil, fmt.Errorf("oauth discovery: logger is required")
	}
	if serverURL == nil {
		return nil, nil, nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	probe := probeWWWAuthenticateResourceMetadata(probeCtx, client, serverURL, logger)
	candidates := make([]DiscoveryCandidate, 0, 3)
	if probe.URL != nil {
		candidates = append(candidates, DiscoveryCandidate{
			URL:    probe.URL,
			Source: DiscoverySourceWWWAuthenticate,
		})
	}
	candidates = append(candidates, buildWellKnownCandidates(serverURL)...)
	return dedupeCandidates(candidates), probe.status(), nil
}

func probeWWWAuthenticateResourceMetadata(
	ctx context.Context,
	client *http.Client,
	serverURL *url.URL,
	logger *slog.Logger,
) wwwAuthenticateProbeResult {
	result := wwwAuthenticateProbeResult{Attempted: false}
	if client == nil {
		result.Error = "oauth discovery: http client is nil"
		return result
	}
	if serverURL == nil {
		result.Error = "oauth discovery: server URL is nil"
		return result
	}
	result.Attempted = true

	methods := []string{http.MethodPost, http.MethodGet}
	var lastErr error
	for _, method := range methods {
		parsed, err := tryWWWAuthenticateProbe(ctx, client, serverURL, logger, method)
		if err == nil {
			result.URL = parsed
			result.Error = ""
			return result
		}
		lastErr = err
	}

	if lastErr != nil {
		result.Error = lastErr.Error()
	}
	return result
}

func tryWWWAuthenticateProbe(
	ctx context.Context,
	client *http.Client,
	serverURL *url.URL,
	logger *slog.Logger,
	method string,
) (*url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, method, serverURL.String(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("oauth discovery: build WWW-Authenticate probe %s: %v", method, err)
	}
	req = req.WithContext(headerscope.WithMCPDiscovery(req.Context()))
	req.Header.Set("User-Agent", version.UserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		if logger != nil {
			logger.WarnContext(ctx, "oauth discovery WWW-Authenticate probe failed", slog.String("method", method), slog.String("error", err.Error()))
		}
		return nil, fmt.Errorf("oauth discovery: WWW-Authenticate probe %s failed: %v", method, err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		return nil, fmt.Errorf("oauth discovery: WWW-Authenticate probe %s got status %d", method, resp.StatusCode)
	}

	header := resp.Header.Get("WWW-Authenticate")
	if header == "" {
		return nil, fmt.Errorf("oauth discovery: WWW-Authenticate header missing (%s %d)", method, resp.StatusCode)
	}

	parsed, err := parseResourceMetadataFromWWWAuthenticate(header)
	if err != nil {
		return nil, fmt.Errorf("%s (%s %d)", err.Error(), method, resp.StatusCode)
	}

	return parsed, nil
}

func parseResourceMetadataFromWWWAuthenticate(header string) (*url.URL, error) {
	value := bearerResourceMetadataValue(header)
	if value == "" {
		return nil, fmt.Errorf("oauth discovery: resource_metadata missing in WWW-Authenticate Bearer challenge")
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("oauth discovery: parse resource_metadata URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("oauth discovery: resource_metadata must be absolute")
	}
	return parsed, nil
}

func bearerResourceMetadataValue(header string) string {
	currentScheme := ""
	for _, segment := range splitWWWAuthenticateSegments(header) {
		trimmed := strings.TrimSpace(segment)
		if trimmed == "" {
			continue
		}

		params := trimmed
		if scheme, rest, ok := splitAuthScheme(trimmed); ok {
			currentScheme = strings.ToLower(scheme)
			params = rest
		}
		if currentScheme != "bearer" {
			continue
		}

		match := resourceMetadataParamPattern.FindStringSubmatch(params)
		if len(match) == 0 {
			continue
		}
		if match[1] != "" {
			return match[1]
		}
		return match[2]
	}
	return ""
}

func splitWWWAuthenticateSegments(header string) []string {
	var segments []string
	start := 0
	inQuote := false
	escaped := false
	for i, r := range header {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && inQuote:
			escaped = true
		case r == '"':
			inQuote = !inQuote
		case r == ',' && !inQuote:
			segments = append(segments, header[start:i])
			start = i + 1
		}
	}
	segments = append(segments, header[start:])
	return segments
}

func splitAuthScheme(segment string) (string, string, bool) {
	fields := strings.Fields(segment)
	if len(fields) < 2 || strings.Contains(fields[0], "=") {
		return "", segment, false
	}
	return fields[0], strings.TrimSpace(strings.TrimPrefix(segment, fields[0])), true
}

func dedupeCandidates(candidates []DiscoveryCandidate) []DiscoveryCandidate {
	if len(candidates) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(candidates))
	out := make([]DiscoveryCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.URL == nil {
			continue
		}
		key := candidate.URL.String()
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

func candidatesToStrings(candidates []DiscoveryCandidate) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.URL == nil {
			continue
		}
		out = append(out, candidate.URL.String())
	}
	return out
}
