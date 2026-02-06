package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ProxySource describes where an effective proxy value originated.
type ProxySource string

const (
	ProxySourceNone        ProxySource = "none"
	ProxySourceEnvironment ProxySource = "environment"
	ProxySourceIgnored     ProxySource = "ignored"
)

func (s ProxySource) String() string {
	return string(s)
}

func parseProxyReference(flagName, raw string, lookupEnv func(string) (string, bool)) (*url.URL, ProxySource, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, "", errors.New("proxy value is required")
	}
	const envPrefix = "env:"
	if strings.HasPrefix(trimmed, envPrefix) {
		envVar := strings.TrimSpace(strings.TrimPrefix(trimmed, envPrefix))
		if envVar == "" {
			return nil, "", fmt.Errorf("invalid %s proxy: environment variable name is required after env:", flagName)
		}
		value, ok := lookupEnv(envVar)
		if !ok {
			return nil, "", fmt.Errorf("environment variable %s referenced by %s proxy is not set", envVar, flagName)
		}
		if value == "" {
			return nil, "", fmt.Errorf("environment variable %s referenced by %s proxy is empty", envVar, flagName)
		}
		parsed, err := parseHTTPProxyURL(value)
		if err != nil {
			return nil, "", fmt.Errorf("invalid %s proxy from env:%s: %w", flagName, envVar, err)
		}
		return parsed, ProxySource(fmt.Sprintf("env:%s", envVar)), nil
	}
	parsed, err := parseHTTPProxyURL(trimmed)
	if err != nil {
		return nil, "", fmt.Errorf("invalid %s proxy: %w", flagName, err)
	}
	return parsed, ProxySource(flagName), nil
}

func parseHTTPProxyURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("proxy URL must include scheme and host")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return nil, fmt.Errorf("proxy URL scheme must be http or https, got %q", parsed.Scheme)
	}
	return parsed, nil
}

func RedactProxyURL(proxyURL *url.URL) string {
	if proxyURL == nil {
		return ""
	}
	if proxyURL.Scheme == "" && proxyURL.Host == "" {
		return ""
	}
	return fmt.Sprintf("%s://%s", proxyURL.Scheme, proxyURL.Host)
}

func EnvProxyConfigured(lookupEnv func(string) (string, bool)) bool {
	if lookupEnv == nil {
		return false
	}
	envKeys := []string{
		"HTTP_PROXY",
		"http_proxy",
		"HTTPS_PROXY",
		"https_proxy",
		"NO_PROXY",
		"no_proxy",
	}
	for _, key := range envKeys {
		if val, ok := lookupEnv(key); ok && val != "" {
			return true
		}
	}
	return false
}
