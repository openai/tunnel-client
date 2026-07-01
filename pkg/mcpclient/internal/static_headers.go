package internal

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"go.openai.org/api/tunnel-client/pkg/headerscope"
)

// StaticHeadersRoundTripper adds operator-configured MCP headers only for the
// configured MCP server origin. Discovery headers are additionally gated by a
// request context marker so they do not leak to normal runtime traffic.
type StaticHeadersRoundTripper struct {
	base                  http.RoundTripper
	serverOrigin          string
	serverPath            string
	extraHeaders          map[string]string
	discoveryExtraHeaders map[string]string
}

// NewStaticHeadersRoundTripper constructs a scoped static-header round tripper.
func NewStaticHeadersRoundTripper(base http.RoundTripper, serverURL *url.URL, extraHeaders, discoveryExtraHeaders map[string]string) http.RoundTripper {
	if base == nil {
		panic("nil base RoundTripper")
	}
	if serverURL == nil || (len(extraHeaders) == 0 && len(discoveryExtraHeaders) == 0) {
		return base
	}
	return &StaticHeadersRoundTripper{
		base:                  base,
		serverOrigin:          originKey(serverURL),
		serverPath:            cleanPath(serverURL.EscapedPath()),
		extraHeaders:          cloneStringMap(extraHeaders),
		discoveryExtraHeaders: cloneStringMap(discoveryExtraHeaders),
	}
}

func (s *StaticHeadersRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return s.base.RoundTrip(req)
	}
	discovery := headerscope.IsMCPDiscovery(req.Context())
	if s.applies(req.URL, discovery) {
		if req.Header == nil {
			req.Header = make(http.Header)
		}
		applyHeaderMap(req.Header, s.extraHeaders)
		if discovery {
			applyHeaderMap(req.Header, s.discoveryExtraHeaders)
		}
	}
	return s.base.RoundTrip(req)
}

func (s *StaticHeadersRoundTripper) applies(reqURL *url.URL, discovery bool) bool {
	if s == nil || reqURL == nil || s.serverOrigin == "" {
		return false
	}
	if originKey(reqURL) != s.serverOrigin {
		return false
	}
	return discovery || cleanPath(reqURL.EscapedPath()) == s.serverPath
}

func applyHeaderMap(dst http.Header, headers map[string]string) {
	if len(headers) == 0 {
		return
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		dst.Set(key, headers[key])
	}
}

func originKey(u *url.URL) string {
	if u == nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	if scheme == "" || host == "" {
		return ""
	}
	return scheme + "://" + host
}

func cleanPath(path string) string {
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	trimmed := strings.TrimRight(path, "/")
	if trimmed == "" {
		return "/"
	}
	return trimmed
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}
