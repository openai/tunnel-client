package main

import (
	"testing"
	"time"
)

func TestConfigFromEnvironment(t *testing.T) {
	t.Setenv("CONTROL_PLANE_TUNNEL_ID", "tunnel_exampleaaaaaaaaaaaaaaaaaaaaaaaa")
	t.Setenv("CONTROL_PLANE_API_KEY", "sdk-example-key")
	t.Setenv("CONTROL_PLANE_BASE_URL", "https://example.invalid")
	t.Setenv("CONTROL_PLANE_ORGANIZATION_ID", "org_example")
	t.Setenv("CONTROL_PLANE_POLL_TIMEOUT", "250ms")
	t.Setenv("CONTROL_PLANE_EXTRA_HEADERS", "X-Test-One: one; X-Test-Two: two")

	cfg, err := configFromEnvironment()
	if err != nil {
		t.Fatalf("configFromEnvironment returned error: %v", err)
	}
	if cfg.TunnelID != "tunnel_exampleaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("unexpected tunnel ID: %q", cfg.TunnelID)
	}
	if cfg.APIKey != "sdk-example-key" {
		t.Fatalf("unexpected API key: %q", cfg.APIKey)
	}
	if cfg.ControlPlaneBaseURL != "https://example.invalid" {
		t.Fatalf("unexpected base URL: %q", cfg.ControlPlaneBaseURL)
	}
	if cfg.OrganizationID != "org_example" {
		t.Fatalf("unexpected organization ID: %q", cfg.OrganizationID)
	}
	if cfg.PollTimeout != 250*time.Millisecond {
		t.Fatalf("unexpected poll timeout: %s", cfg.PollTimeout)
	}
	if cfg.ControlPlaneExtraHeaders["X-Test-One"] != "one" {
		t.Fatalf("missing first extra header: %#v", cfg.ControlPlaneExtraHeaders)
	}
	if cfg.ControlPlaneExtraHeaders["X-Test-Two"] != "two" {
		t.Fatalf("missing second extra header: %#v", cfg.ControlPlaneExtraHeaders)
	}
}

func TestHeadersFromEnvironmentRejectsMalformedEntry(t *testing.T) {
	t.Setenv("CONTROL_PLANE_EXTRA_HEADERS", "missing-value")

	_, err := headersFromEnvironment("CONTROL_PLANE_EXTRA_HEADERS")
	if err == nil {
		t.Fatal("expected malformed header error")
	}
}
