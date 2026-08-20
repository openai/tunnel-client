package mcpclient

import (
	"net/http"
	"testing"
)

func TestFindHeaderValue(t *testing.T) {

	testCases := []struct {
		name    string
		headers http.Header
		target  string
		want    *string
	}{
		{
			name:    "returns nil when headers map empty",
			headers: http.Header{},
			target:  "X-Test",
			want:    nil,
		},
		{
			name:    "returns nil when header missing",
			headers: http.Header{"Some-Other": {"value"}},
			target:  "X-Test",
			want:    nil,
		},
		{
			name:    "returns first value when header present",
			headers: http.Header{"X-Test": {"value-1", "value-2"}},
			target:  "X-Test",
			want:    ptr("value-1"),
		},
		{
			name: "handles case insensitive lookup",
			headers: func() http.Header {
				h := make(http.Header)
				h.Set("mcp-session-id", "session-123")
				return h
			}(),
			target: HeaderSessionID,
			want:   ptr("session-123"),
		},
		{
			name:    "handles directly stored lowercase keys",
			headers: http.Header{"mcp-session-id": {"session-direct"}},
			target:  HeaderSessionID,
			want:    ptr("session-direct"),
		},
		{
			name:    "returns nil for empty header value",
			headers: http.Header{"X-Test": {""}},
			target:  "X-Test",
			want:    nil,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := FindHeaderValue(tc.headers, tc.target)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("expected nil, got %q", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("expected %q, got nil", *tc.want)
			case tc.want != nil && got != nil && *tc.want != *got:
				t.Fatalf("expected %q, got %q", *tc.want, *got)
			}
		})
	}
}

func TestSessionIDFromHeaders(t *testing.T) {
	testCases := []struct {
		name    string
		headers http.Header
		want    *string
	}{
		{
			name:    "returns nil when missing",
			headers: http.Header{},
			want:    nil,
		},
		{
			name:    "returns session id when present",
			headers: http.Header{HeaderSessionID: {"session-123"}},
			want:    ptr("session-123"),
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := SessionIDFromHeaders(tc.headers)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("expected nil, got %q", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("expected %q, got nil", *tc.want)
			case tc.want != nil && got != nil && *tc.want != *got:
				t.Fatalf("expected %q, got %q", *tc.want, *got)
			}
		})
	}
}

func ptr(v string) *string { return &v }
