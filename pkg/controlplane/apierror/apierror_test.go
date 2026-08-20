package apierror

import (
	"strings"
	"testing"
)

func TestParseAddsMitigationForStructuredTunnelError(t *testing.T) {
	info := Parse([]byte(`{"error":{"message":"org required","type":"invalid_request_error","code":"tunnel_active_organization_required"}}`))

	if info.Code != "tunnel_active_organization_required" {
		t.Fatalf("unexpected code: %q", info.Code)
	}
	if info.Message != "org required" {
		t.Fatalf("unexpected message: %q", info.Message)
	}
	if !strings.Contains(info.Mitigation, "control_plane.organization_id") {
		t.Fatalf("expected organization config mitigation, got %q", info.Mitigation)
	}
	if detail := Detail(info); !strings.Contains(detail, "mitigation:") {
		t.Fatalf("expected mitigation in detail, got %q", detail)
	}
}

func TestParsePreservesUnknownCodesWithoutMitigation(t *testing.T) {
	info := Parse([]byte(`{"error":{"message":"missing cert","type":"invalid_request_error","code":"certificate_required"}}`))

	if info.Code != "certificate_required" {
		t.Fatalf("unexpected code: %q", info.Code)
	}
	if info.Mitigation != "" {
		t.Fatalf("unexpected mitigation: %q", info.Mitigation)
	}
	if detail := Detail(info); detail != "certificate_required: missing cert" {
		t.Fatalf("unexpected detail: %q", detail)
	}
}
