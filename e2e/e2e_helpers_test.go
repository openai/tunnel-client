package e2e_test

import (
	"net/url"
	"testing"
)

func mustParseURL(tb testing.TB, raw string) *url.URL {
	tb.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		tb.Fatalf("parse url %q: %v", raw, err)
	}
	return parsed
}
