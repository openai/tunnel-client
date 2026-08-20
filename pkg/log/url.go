package log

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// RedactURL returns only the URL origin for logging. URL userinfo, opaque data,
// path, query, and fragment components can contain credentials or other
// sensitive values and are intentionally omitted.
func RedactURL(u *url.URL) string {
	if u == nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}

// RedactURLString parses raw as a URL and returns its origin for logging.
func RedactURLString(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return RedactURL(parsed)
}

// ErrorForLog strips URL details from net/url errors while preserving wrapper
// and joined-error context. Callers remain responsible for not embedding raw
// URLs in ordinary error strings.
func ErrorForLog(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	visitURLErrors(err, func(urlErr *url.Error) {
		message = redactURLErrorForLog(message, urlErr)
	})
	return redactMalformedRedirectLocations(message)
}

func redactURLErrorForLog(message string, urlErr *url.Error) string {
	if urlErr == nil || urlErr.URL == "" {
		return message
	}
	replacement := RedactURLString(urlErr.URL)
	if replacement == "" {
		replacement = "<redacted>"
	}

	// Parsing errors can repeat URL-derived bytes in their cause, such as an
	// invalid port or escape. Keep the operation while suppressing that detail.
	if strings.EqualFold(urlErr.Op, "parse") {
		redacted := fmt.Sprintf("%s %q: invalid URL", urlErr.Op, replacement)
		if updated := strings.ReplaceAll(message, urlErr.Error(), redacted); updated != message {
			return updated
		}
	}
	return strings.ReplaceAll(message, strconv.Quote(urlErr.URL), strconv.Quote(replacement))
}

// net/http reports a malformed redirect Location inside the ordinary error
// wrapped by *url.Error. Redact that separately because it is not exposed as a
// second *url.Error and can contain server-controlled credentials or tokens.
func redactMalformedRedirectLocations(message string) string {
	const prefix = "failed to parse Location header "
	for searchFrom := 0; searchFrom < len(message); {
		prefixIndex := strings.Index(message[searchFrom:], prefix)
		if prefixIndex < 0 {
			break
		}
		quotedStart := searchFrom + prefixIndex + len(prefix)
		location, quotedEnd, ok := consumeQuotedString(message, quotedStart)
		if !ok {
			searchFrom = quotedStart
			continue
		}
		replacement := RedactURLString(location)
		if replacement == "" {
			replacement = "<redacted>"
		}
		lineEnd := strings.IndexByte(message[quotedEnd:], '\n')
		if lineEnd < 0 {
			lineEnd = len(message)
		} else {
			lineEnd += quotedEnd
		}
		redacted := prefix + strconv.Quote(replacement) + ": invalid URL"
		prefixStart := quotedStart - len(prefix)
		message = message[:prefixStart] + redacted + message[lineEnd:]
		searchFrom = prefixStart + len(redacted)
	}
	return message
}

func consumeQuotedString(message string, start int) (string, int, bool) {
	if start >= len(message) || message[start] != '"' {
		return "", start, false
	}
	for end := start + 1; end < len(message); end++ {
		if message[end] != '"' {
			continue
		}
		backslashes := 0
		for i := end - 1; i >= start && message[i] == '\\'; i-- {
			backslashes++
		}
		if backslashes%2 != 0 {
			continue
		}
		quoted := message[start : end+1]
		value, err := strconv.Unquote(quoted)
		if err != nil {
			return "", end + 1, false
		}
		return value, end + 1, true
	}
	return "", len(message), false
}

func visitURLErrors(err error, visit func(*url.Error)) {
	if err == nil {
		return
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, child := range wrapped.Unwrap() {
			visitURLErrors(child, visit)
		}
	case interface{ Unwrap() error }:
		visitURLErrors(wrapped.Unwrap(), visit)
	}
	if urlErr, ok := err.(*url.Error); ok {
		visit(urlErr)
	}
}
