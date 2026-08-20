package proxy

import "log/slog"

// LogFields returns structured log fields for a route.
func LogFields(route Route) []any {
	fields := []any{
		slog.String("route_mode", string(route.RouteMode)),
		slog.String("proxy_source", route.ProxySource.String()),
	}
	if route.ProxyURLRedact != "" {
		fields = append(fields, slog.String("proxy_url", route.ProxyURLRedact))
	}
	if route.ProxyID != "" {
		fields = append(fields, slog.String("proxy_id", route.ProxyID))
	}
	return fields
}
