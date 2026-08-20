package healthurl

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	tctransport "github.com/openai/tunnel-client/pkg/transport"
)

const unixScheme = "http+unix"

type Target struct {
	BaseURL        string
	RequestBaseURL string
	UnixSocketPath string
}

func BuildUnixBaseURL(socketPath string) string {
	return unixScheme + "://" + base64.RawURLEncoding.EncodeToString([]byte(socketPath))
}

func Parse(rawURL string) (Target, error) {
	baseURL := NormalizeBaseURL(rawURL)
	if baseURL == "" {
		return Target{}, fmt.Errorf("health URL is empty")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return Target{}, err
	}
	if parsed.Scheme != unixScheme {
		return Target{
			BaseURL:        baseURL,
			RequestBaseURL: baseURL,
		}, nil
	}

	socketPathBytes, err := base64.RawURLEncoding.DecodeString(parsed.Host)
	if err != nil {
		return Target{}, fmt.Errorf("decode health unix socket path: %w", err)
	}
	socketPath := string(socketPathBytes)
	if socketPath == "" {
		return Target{}, fmt.Errorf("health unix socket path is empty")
	}

	return Target{
		BaseURL:        baseURL,
		RequestBaseURL: "http://localhost",
		UnixSocketPath: socketPath,
	}, nil
}

func NormalizeBaseURL(rawHealthURL string) string {
	value := strings.TrimSpace(rawHealthURL)
	if value == "" {
		return ""
	}
	value = strings.TrimSuffix(value, "/healthz")
	value = strings.TrimSuffix(value, "/readyz")
	return strings.TrimRight(value, "/")
}

func (t Target) URL(path string) string {
	return strings.TrimRight(t.BaseURL, "/") + path
}

func (t Target) RequestURL(path string) string {
	return strings.TrimRight(t.RequestBaseURL, "/") + path
}

func (t Target) HTTPClient(timeout time.Duration) (*http.Client, error) {
	client := &http.Client{Timeout: timeout}
	if t.UnixSocketPath == "" {
		return client, nil
	}

	transport, err := tctransport.ApplyUnixSocketPath(tctransport.CloneDefault(), t.UnixSocketPath)
	if err != nil {
		return nil, err
	}
	client.Transport = transport
	return client, nil
}
