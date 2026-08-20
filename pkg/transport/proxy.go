package transport

import (
	"fmt"
	"net/http"
	"net/url"
)

// ApplyProxy clones the provided RoundTripper with a fixed proxy when proxyURL is set.
// Explicit proxies bypass environment proxy variables and NO_PROXY.
func ApplyProxy(base http.RoundTripper, proxyURL *url.URL) (http.RoundTripper, error) {
	if proxyURL == nil {
		return base, nil
	}
	if base == nil {
		return nil, fmt.Errorf("base transport is nil")
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("unsupported transport type %T", base)
	}
	cloned := transport.Clone()
	cloned.Proxy = func(*http.Request) (*url.URL, error) {
		return proxyURL, nil
	}
	return cloned, nil
}
