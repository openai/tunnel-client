package oauth

import (
	"net/http"
	"strings"
)

// IsOptionalDiscoveryFailure reports whether OAuth discovery finished, but the
// target simply does not advertise protected-resource metadata. This should not
// block tunnel readiness for plain MCP servers.
func IsOptionalDiscoveryFailure(
	result *DiscoveryResult,
	probe *WWWAuthenticateProbeStatus,
	err error,
) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(msg, "oauth discovery disabled for transport") ||
		strings.Contains(msg, "oauth discovery server url is not configured") {
		return true
	}

	if probe != nil && probe.URL != "" {
		return false
	}
	if result == nil || len(result.Attempts) == 0 {
		return false
	}

	sawNotFound := false
	for _, attempt := range result.Attempts {
		if !attempt.Tried || attempt.Selected {
			return false
		}
		if attempt.StatusCode != http.StatusNotFound {
			return false
		}
		sawNotFound = true
	}
	return sawNotFound
}
