package oauth

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// discoveryOriginTransport enforces operator-selected origins at the request
// boundary, including after a caller's redirect callback has run. It preserves
// the caller's proxy, TLS, Unix-socket, and header-scoping transports.
type discoveryOriginTransport struct {
	base    http.RoundTripper
	origins []url.URL
}

func (t *discoveryOriginTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || !validDiscoveryURL(req.URL) {
		return nil, fmt.Errorf("oauth discovery: invalid metadata URL; require HTTP(S) without credentials or fragments")
	}
	for i := range t.origins {
		if sameURLOrigin(req.URL, &t.origins[i]) {
			return t.base.RoundTrip(req)
		}
	}
	return nil, fmt.Errorf("oauth discovery: destination origin is not trusted; configure --mcp.oauth-trusted-origin for additional metadata or authorization servers")
}

func withTrustedDiscoveryOrigins(client *http.Client, origins []*url.URL) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	cloned := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	transport := &discoveryOriginTransport{base: base}
	for _, origin := range origins {
		if origin != nil {
			// Store only the authority, independently of mutable candidate URLs.
			transport.origins = append(transport.origins, url.URL{Scheme: origin.Scheme, Host: origin.Host})
		}
	}
	cloned.Transport = transport
	return &cloned
}

func validDiscoveryURL(u *url.URL) bool {
	if u == nil || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.Hostname() == "" {
		return false
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		return err == nil && n > 0 && n <= 65535
	}
	return !strings.HasSuffix(u.Host, ":")
}
