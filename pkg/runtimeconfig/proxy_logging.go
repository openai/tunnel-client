package runtimeconfig

import (
	"log/slog"
	"net/url"
)

// ProxyLogFields builds log fields for proxy metadata, redacting credentials.
func ProxyLogFields(proxyURL *url.URL, source ProxySource) []any {
	fields := []any{
		slog.String("proxy_source", source.String()),
	}
	if proxyURL != nil {
		fields = append(fields, slog.String("proxy", RedactProxyURL(proxyURL)))
	}
	return fields
}
