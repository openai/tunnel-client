package mcpclient

import (
	"net/http"
	"strings"
)

const (
	HeaderSessionID       = "Mcp-Session-Id"
	HeaderLastEventID     = "Last-Event-ID"
	HeaderProtocolVersion = "Mcp-Protocol-Version"
)

// SessionIDFromHeaders searches the provided headers map for the MCP session
// identifier and returns it when present.
func SessionIDFromHeaders(headers http.Header) *string {
	return FindHeaderValue(headers, HeaderSessionID)
}

// FindHeaderValue returns the value of a header in case-insensitive fashion.
// Nil is returned when the header is absent or the map is empty.
func FindHeaderValue(headers http.Header, target string) *string {
	if len(headers) == 0 {
		return nil
	}
	v := headers.Get(target)
	if v != "" {
		return &v
	}

	for key, values := range headers {
		if !strings.EqualFold(key, target) || len(values) == 0 || values[0] == "" {
			continue
		}
		v = values[0]
		return &v
	}

	return nil
}
