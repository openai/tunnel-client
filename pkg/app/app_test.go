package app

import (
	"testing"
	"time"

	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/config"
)

func TestCloudflaredStartTimeoutIncludesManagedRuntimeFetchBudget(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		ControlPlane: config.ControlPlaneConfig{
			PollTimeout:           2 * time.Second,
			PollDeadlineGuardrail: time.Second,
		},
		Cloudflared: config.CloudflaredConfig{
			Managed:      true,
			ReadyTimeout: 7 * time.Second,
		},
	}

	got := cloudflaredStartTimeout(cfg)
	want := fx.DefaultTimeout + 7*time.Second + 3*time.Second
	if got != want {
		t.Fatalf("expected managed startup timeout %s, got %s", want, got)
	}
}

func TestCloudflaredStartTimeoutSkipsFetchBudgetForStaticToken(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		ControlPlane: config.ControlPlaneConfig{
			PollTimeout:           2 * time.Second,
			PollDeadlineGuardrail: time.Second,
		},
		Cloudflared: config.CloudflaredConfig{
			Token:        "configured-token",
			Managed:      true,
			ReadyTimeout: 7 * time.Second,
		},
	}

	got := cloudflaredStartTimeout(cfg)
	want := fx.DefaultTimeout + 7*time.Second
	if got != want {
		t.Fatalf("expected static-token startup timeout %s, got %s", want, got)
	}
}
