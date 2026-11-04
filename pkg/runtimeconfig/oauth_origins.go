package runtimeconfig

import (
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
)

func buildOAuthTrustedOrigins(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) ([]*url.URL, error) {
	entries, err := resolveMCPEntries(fs, lookupEnv, "mcp.oauth-trusted-origin", "MCP_OAUTH_TRUSTED_ORIGINS")
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	origins := make([]*url.URL, 0, len(entries))
	for _, raw := range entries {
		origin, err := parseOAuthTrustedOrigin(raw)
		if err != nil {
			return nil, fmt.Errorf("mcp.oauth-trusted-origin: %w", err)
		}
		origins = append(origins, origin)
	}
	return origins, nil
}

func buildOAuthForwardOrigins(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) ([]*url.URL, error) {
	entries, err := resolveMCPEntries(fs, lookupEnv, "harpoon.oauth-forward-origin", "HARPOON_OAUTH_FORWARD_ORIGINS")
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	origins := make([]*url.URL, 0, len(entries))
	for _, raw := range entries {
		origin, err := parseOAuthForwardOrigin(raw)
		if err != nil {
			return nil, fmt.Errorf("harpoon.oauth-forward-origin: %w", err)
		}
		origins = append(origins, origin)
	}
	return origins, nil
}

func parseOAuthForwardOrigin(raw string) (*url.URL, error) {
	origin, err := parseOAuthTrustedOrigin(raw)
	if err != nil {
		return nil, err
	}
	if origin.Scheme != "https" {
		return nil, fmt.Errorf("OAuth forwarding origin must use HTTPS")
	}
	if addr, err := netip.ParseAddr(origin.Hostname()); err == nil && addr.Zone() != "" {
		return nil, fmt.Errorf("OAuth forwarding origin must not contain an IPv6 zone identifier")
	}
	return origin, nil
}

func parseOAuthTrustedOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid origin: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.Opaque != "" || u.User != nil || (u.Path != "" && u.Path != "/") ||
		u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
		return nil, fmt.Errorf("expected an absolute HTTP(S) origin without credentials, path, query, or fragment")
	}
	if strings.Contains(u.Hostname(), ":") || strings.HasPrefix(u.Host, "[") {
		addr, err := netip.ParseAddr(u.Hostname())
		if err != nil || !addr.Is6() || !strings.HasPrefix(u.Host, "[") {
			return nil, fmt.Errorf("origin IPv6 host must use a valid bracketed address")
		}
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, fmt.Errorf("origin port must not be empty")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("origin port must be between 1 and 65535")
		}
	}
	u.Path = ""
	return u, nil
}
