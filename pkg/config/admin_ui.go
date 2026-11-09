package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

const maxAdminUIBasePathBytes = 2048

// NormalizeAdminUIBasePath validates an optional canonical path prefix for the
// embedded web UI. Empty and "/" select the root; one trailing slash is removed.
// Non-root prefixes contain slash-separated unreserved ASCII segments and must
// not overlap the health, metrics, or Harpoon namespaces.
func NormalizeAdminUIBasePath(raw string) (string, error) {
	if len(raw) > maxAdminUIBasePathBytes {
		return "", fmt.Errorf("must be at most %d bytes", maxAdminUIBasePathBytes)
	}
	if raw == "" || raw == "/" {
		return "", nil
	}
	if !strings.HasPrefix(raw, "/") {
		return "", errors.New("must start with / or be empty")
	}
	basePath := strings.TrimSuffix(raw, "/")
	segments := strings.Split(basePath[1:], "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New("must not contain empty, . or .. path segments")
		}
		for i := 0; i < len(segment); i++ {
			c := segment[i]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~' {
				continue
			}
			return "", errors.New("path segments must contain only unreserved ASCII characters (letters, digits, -, ., _, ~)")
		}
	}
	switch segments[0] {
	case "health", "healthz", "readyz", "metrics", "harpoon":
		return "", fmt.Errorf("must not overlap the /%s namespace", segments[0])
	}
	return basePath, nil
}

// ParseAdminUIPublicOrigin validates and normalizes an optional exact HTTP(S)
// browser origin for the embedded web UI. Empty input returns nil. The result
// uses a lowercase scheme and ASCII hostname, canonical IP literals and numeric
// ports, and omits default ports. It never includes credentials, a path, query,
// or fragment.
func ParseAdminUIPublicOrigin(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	origin, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("must be a valid HTTP(S) origin")
	}
	scheme := strings.ToLower(origin.Scheme)
	if (scheme != "http" && scheme != "https") || origin.Hostname() == "" ||
		origin.Opaque != "" || origin.User != nil || (origin.Path != "" && origin.Path != "/") ||
		origin.RawPath != "" || origin.RawQuery != "" || origin.ForceQuery || strings.Contains(raw, "#") {
		return nil, errors.New("must be an absolute HTTP(S) origin without credentials, path, query, or fragment")
	}
	if strings.Contains(origin.Host, "*") {
		return nil, errors.New("must not contain a wildcard hostname")
	}
	hostname := origin.Hostname()
	for i := 0; i < len(hostname); i++ {
		if hostname[i] >= 0x80 {
			return nil, errors.New("hostname must be ASCII; use the browser's punycode hostname for internationalized domains")
		}
	}
	hostname = strings.ToLower(hostname)
	if strings.Contains(hostname, ":") || strings.HasPrefix(origin.Host, "[") {
		addr, err := netip.ParseAddr(hostname)
		if err != nil || !addr.Is6() || !strings.HasPrefix(origin.Host, "[") {
			return nil, errors.New("IPv6 hostname must be a valid bracketed address")
		}
		if addr.Zone() != "" {
			return nil, errors.New("must not contain an IPv6 zone identifier")
		}
		hostname = addr.String()
	} else if addr, err := netip.ParseAddr(hostname); err == nil {
		hostname = addr.String()
	}
	if strings.HasSuffix(origin.Host, ":") {
		return nil, errors.New("port must not be empty")
	}
	port := origin.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("port must be between 1 and 65535")
		}
		port = strconv.Itoa(n)
		if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
			port = ""
		}
	}
	host := hostname
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	return &url.URL{Scheme: scheme, Host: host}, nil
}
