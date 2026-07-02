package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/openai/tunnel-client/pkg/config"
)

// RouteKind describes the type of outbound route.
type RouteKind string

const (
	RouteKindControlPlane RouteKind = "control_plane"
	RouteKindMCPChannel   RouteKind = "mcp_channel"
	RouteKindHarpoon      RouteKind = "harpoon_target"
)

// RouteMode captures whether a route is direct or proxied.
type RouteMode string

const (
	RouteModeDirect RouteMode = "direct"
	RouteModeProxy  RouteMode = "proxy"
)

// Route captures resolved proxy metadata for an outbound route.
type Route struct {
	Kind            RouteKind
	Name            string
	TargetURL       *url.URL
	TargetHostPort  string
	ProxyURL        *url.URL
	ProxySource     config.ProxySource
	RouteMode       RouteMode
	ProxyID         string
	ProxyURLRedact  string
	ProxyHostPort   string
	ProxyURLPresent bool
}

// RouteSummary is a JSON-friendly representation of a route.
type RouteSummary struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Target      string `json:"target,omitempty"`
	RouteMode   string `json:"route_mode"`
	ProxySource string `json:"proxy_source"`
	ProxyURL    string `json:"proxy_url,omitempty"`
	ProxyID     string `json:"proxy_id,omitempty"`
}

// IdentityRecord maps proxy IDs to redacted URLs and sources.
type IdentityRecord struct {
	ProxyID     string `json:"proxy_id"`
	ProxyURL    string `json:"proxy_url"`
	ProxySource string `json:"proxy_source"`
}

// ResolveRoute determines the effective proxy route metadata.
func ResolveRoute(kind RouteKind, name string, target *url.URL, proxyURL *url.URL, proxySource config.ProxySource, lookupEnv func(string) (string, bool)) Route {
	resolvedProxy := proxyURL
	resolvedSource := proxySource
	if resolvedProxy == nil {
		envProxy, envSource := envProxyForURL(target, lookupEnv)
		if envProxy != nil {
			resolvedProxy = envProxy
			resolvedSource = envSource
		}
	}

	routeMode := RouteModeDirect
	proxyRedacted := ""
	proxyID := ""
	proxyHostPort := ""
	proxyPresent := false
	if resolvedProxy != nil {
		proxyPresent = true
		routeMode = RouteModeProxy
		proxyRedacted = NormalizeProxyURL(resolvedProxy)
		proxyID = HashProxyID(proxyRedacted)
		proxyHostPort = HostPortForURL(resolvedProxy)
	}

	if resolvedProxy == nil {
		resolvedSource = config.ProxySourceNone
	}

	return Route{
		Kind:            kind,
		Name:            name,
		TargetURL:       target,
		TargetHostPort:  HostPortForURL(target),
		ProxyURL:        resolvedProxy,
		ProxySource:     resolvedSource,
		RouteMode:       routeMode,
		ProxyID:         proxyID,
		ProxyURLRedact:  proxyRedacted,
		ProxyHostPort:   proxyHostPort,
		ProxyURLPresent: proxyPresent,
	}
}

// Summary converts a Route into a JSON-friendly summary.
func Summary(route Route) RouteSummary {
	summary := RouteSummary{
		Kind:        string(route.Kind),
		Name:        route.Name,
		Target:      route.TargetHostPort,
		RouteMode:   string(route.RouteMode),
		ProxySource: route.ProxySource.String(),
		ProxyURL:    route.ProxyURLRedact,
		ProxyID:     route.ProxyID,
	}
	return summary
}

// BuildIdentityMap returns a de-duplicated list of proxy identity records.
func BuildIdentityMap(routes []Route) []IdentityRecord {
	seen := make(map[string]IdentityRecord)
	for _, route := range routes {
		if route.ProxyID == "" || route.ProxyURLRedact == "" {
			continue
		}
		if _, ok := seen[route.ProxyID]; ok {
			continue
		}
		seen[route.ProxyID] = IdentityRecord{
			ProxyID:     route.ProxyID,
			ProxyURL:    route.ProxyURLRedact,
			ProxySource: route.ProxySource.String(),
		}
	}
	out := make([]IdentityRecord, 0, len(seen))
	for _, record := range seen {
		out = append(out, record)
	}
	return out
}

// NormalizeProxyURL returns a redacted, normalized proxy URL string.
func NormalizeProxyURL(proxyURL *url.URL) string {
	if proxyURL == nil {
		return ""
	}
	scheme := strings.ToLower(proxyURL.Scheme)
	host := strings.ToLower(proxyURL.Host)
	if scheme == "" && host == "" {
		return ""
	}
	return scheme + "://" + host
}

// HashProxyID returns a stable hash of a normalized proxy URL.
func HashProxyID(normalized string) string {
	if normalized == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(hash[:])
}

// HostPortForURL returns host:port with defaults based on scheme.
func HostPortForURL(target *url.URL) string {
	if target == nil {
		return ""
	}
	if target.Host == "" {
		return ""
	}
	if target.Port() != "" {
		return target.Host
	}
	scheme := strings.ToLower(target.Scheme)
	switch scheme {
	case "https":
		return target.Host + ":443"
	case "http":
		return target.Host + ":80"
	default:
		return target.Host
	}
}

func envProxyForURL(target *url.URL, lookupEnv func(string) (string, bool)) (*url.URL, config.ProxySource) {
	if target == nil || lookupEnv == nil {
		return nil, config.ProxySourceNone
	}
	proxyValue, sourceVar := envProxyValue(target.Scheme, lookupEnv)
	if proxyValue == "" {
		return nil, config.ProxySourceNone
	}
	if proxyBypassHost(firstEnvValue(lookupEnv, "NO_PROXY", "no_proxy"), target) {
		return nil, config.ProxySourceNone
	}
	proxyURL, err := parseProxyURL(proxyValue)
	if err != nil {
		return nil, config.ProxySourceNone
	}
	if sourceVar == "" {
		return proxyURL, config.ProxySourceEnvironment
	}
	return proxyURL, config.ProxySource("env:" + sourceVar)
}

func firstEnvValue(lookupEnv func(string) (string, bool), keys ...string) string {
	for _, key := range keys {
		if val, ok := lookupEnv(key); ok && strings.TrimSpace(val) != "" {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

func envProxyValue(scheme string, lookupEnv func(string) (string, bool)) (string, string) {
	if strings.EqualFold(scheme, "https") {
		if val, ok := lookupEnv("HTTPS_PROXY"); ok && strings.TrimSpace(val) != "" {
			return strings.TrimSpace(val), "HTTPS_PROXY"
		}
		if val, ok := lookupEnv("https_proxy"); ok && strings.TrimSpace(val) != "" {
			return strings.TrimSpace(val), "https_proxy"
		}
		if val, ok := lookupEnv("HTTP_PROXY"); ok && strings.TrimSpace(val) != "" {
			return strings.TrimSpace(val), "HTTP_PROXY"
		}
		if val, ok := lookupEnv("http_proxy"); ok && strings.TrimSpace(val) != "" {
			return strings.TrimSpace(val), "http_proxy"
		}
		return "", ""
	}
	if val, ok := lookupEnv("HTTP_PROXY"); ok && strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val), "HTTP_PROXY"
	}
	if val, ok := lookupEnv("http_proxy"); ok && strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val), "http_proxy"
	}
	return "", ""
}

func parseProxyURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" {
		parsed, err = url.Parse("http://" + raw)
		if err != nil {
			return nil, err
		}
	}
	if parsed.Host == "" {
		return nil, errors.New("proxy URL missing host")
	}
	return parsed, nil
}

func proxyBypassHost(noProxy string, target *url.URL) bool {
	if noProxy == "" || target == nil {
		return false
	}
	host := strings.ToLower(target.Hostname())
	if host == "" {
		return false
	}
	port := target.Port()
	for _, entry := range strings.Split(noProxy, ",") {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		if trimmed == "*" {
			return true
		}
		trimmed = strings.ToLower(trimmed)
		if strings.Contains(trimmed, ":") {
			if hostPortMatch(trimmed, host, port) {
				return true
			}
			continue
		}
		if ip := net.ParseIP(trimmed); ip != nil {
			if ip.String() == host {
				return true
			}
			continue
		}
		if trimmed == host {
			return true
		}
		trimmed = strings.TrimPrefix(trimmed, ".")
		if strings.HasSuffix(host, "."+trimmed) {
			return true
		}
	}
	return false
}

func hostPortMatch(entry, host, port string) bool {
	if host == "" {
		return false
	}
	if port == "" {
		return entry == host
	}
	return entry == host+":"+port
}
