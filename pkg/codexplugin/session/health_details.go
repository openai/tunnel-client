package session

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"

	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/healthurl"
)

// DiscoverHealthDetails advertises additive diagnostic links only after the
// local runtime confirms support. Older runtimes simply return no links.
func DiscoverHealthDetails(rawHealthURL string) (detailsURL, mcpURL string) {
	target, err := healthurl.Parse(rawHealthURL)
	if err != nil {
		return "", ""
	}
	if target.UnixSocketPath == "" {
		parsed, err := url.Parse(target.BaseURL)
		if err != nil || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return "", ""
		}
		host := parsed.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", ""
		}
	}
	client, err := target.HTTPClient(healthProbeTimeout)
	if err != nil {
		return "", ""
	}
	defer client.CloseIdleConnections()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Get(target.RequestURL("/health/mcp"))
	if err != nil {
		return "", ""
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", ""
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, healthstate.MaxComponentBytes+1))
	if err != nil || len(body) > healthstate.MaxComponentBytes {
		return "", ""
	}
	var snapshot struct {
		SchemaVersion int    `json:"schema_version"`
		Component     string `json:"component"`
	}
	if json.Unmarshal(body, &snapshot) != nil || snapshot.SchemaVersion != healthstate.SchemaVersion || snapshot.Component != "mcp" {
		return "", ""
	}
	return target.URL("/health?details=true"), target.URL("/health/mcp")
}
