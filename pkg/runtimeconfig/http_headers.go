package runtimeconfig

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// A NUL cannot occur in an OS environment value, so this prefix distinguishes
// a structured config-file fallback from the public comma/semicolon list
// syntax without reserving a user-reachable value.
const encodedExtraHeaderMapPrefix = "\x00tunnel-client-extra-header-map-v1:"

// NormalizeExtraHeaders validates operator-supplied HTTP headers and returns a
// canonical copy. HTTP field names are case-insensitive, so conflicting case
// variants are rejected instead of depending on Go map iteration order.
func NormalizeExtraHeaders(source string, headers map[string]string) (map[string]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}

	normalized := make(map[string]string, len(headers))
	for name, value := range headers {
		if !validHTTPHeaderFieldName(name) {
			return nil, fmt.Errorf("%s contains invalid HTTP header name %q", source, name)
		}
		canonicalName := http.CanonicalHeaderKey(name)
		if !validHTTPHeaderFieldValue(value) {
			return nil, fmt.Errorf("%s contains invalid HTTP header value for %q", source, canonicalName)
		}
		if existing, ok := normalized[canonicalName]; ok {
			if existing != value {
				return nil, fmt.Errorf("%s contains conflicting values for case-insensitive HTTP header %q", source, canonicalName)
			}
			continue
		}
		normalized[canonicalName] = value
	}
	return normalized, nil
}

func encodeExtraHeaderMap(source string, headers map[string]string) (string, error) {
	normalized, err := NormalizeExtraHeaders(source, headers)
	if err != nil {
		return "", err
	}

	// []byte values make JSON use base64, preserving allowed non-UTF-8 HTTP
	// header bytes instead of replacing them during marshaling.
	encoded := make(map[string][]byte, len(normalized))
	for name, value := range normalized {
		encoded[name] = []byte(value)
	}
	payload, err := json.Marshal(encoded)
	if err != nil {
		return "", fmt.Errorf("encode %s: %w", source, err)
	}
	return encodedExtraHeaderMapPrefix + string(payload), nil
}

func decodeExtraHeaderMap(source, raw string, lookupEnv func(string) (string, bool)) (map[string]string, error) {
	encoded := make(map[string][]byte)
	if err := json.Unmarshal([]byte(strings.TrimPrefix(raw, encodedExtraHeaderMapPrefix)), &encoded); err != nil {
		return nil, fmt.Errorf("decode %s: invalid internal header map", source)
	}

	headers := make(map[string]string, len(encoded))
	for name, value := range encoded {
		headers[name] = string(value)
	}
	normalized, err := NormalizeExtraHeaders(source, headers)
	if err != nil {
		return nil, err
	}
	for name, value := range normalized {
		resolved, err := resolveHeaderValue(source+"."+name, value, lookupEnv)
		if err != nil {
			return nil, err
		}
		normalized[name] = resolved
	}
	return NormalizeExtraHeaders(source, normalized)
}

// These checks mirror the rules used by net/http.Transport before it sends a
// request. net/http does not export its header validators.
func validHTTPHeaderFieldName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func validHTTPHeaderFieldValue(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < ' ' && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}
