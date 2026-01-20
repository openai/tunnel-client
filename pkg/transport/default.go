package transport

import "net/http"

// CloneDefault returns an isolated copy of the default HTTP transport so callers
// can customize behavior without mutating shared state.
func CloneDefault() http.RoundTripper {
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		return base.Clone()
	}
	return http.DefaultTransport
}
