package harpoon

import (
	"io"
	"log/slog"
	"net/url"
	"reflect"
	"testing"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/harpoon/hostbus"
	"go.openai.org/api/tunnel-client/pkg/harpoon/internal/hostclassifier"
)

func TestRegisterHostBundleRespectsClassifier(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, true, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	classifier := hostclassifier.NewHostClassifier(config.HarpoonHostClassifierConfig{
		IncludeSuffix:  []string{"internal"},
		IncludePrivate: false,
	})

	bundle := hostbus.URLBundle{
		URLs: []hostbus.URLRecord{
			{URL: mustParseURLForHostRegistration(t, "https://api.internal/v1#frag"), Description: "internal"},
			{URL: mustParseURLForHostRegistration(t, "https://public.example.com/v1"), Description: "public"},
		},
	}

	if err := registerHostBundle(bundle, classifier, registry, logger); err != nil {
		t.Fatalf("register bundle: %v", err)
	}

	if target, ok := registry.Lookup("oauth-0"); !ok {
		t.Fatalf("expected auto-registered label oauth-0")
	} else if target.InclusionReason == "" {
		t.Fatalf("expected inclusion reason to be set")
	} else if target.BaseURL == nil || target.BaseURL.String() != "https://api.internal/v1#frag" {
		t.Fatalf("expected fragment to be preserved in target URL, got %v", target.BaseURL)
	}
	if _, ok := registry.Lookup("oauth-1"); ok {
		t.Fatalf("unexpected registration for public host")
	}
}

func TestRegisterHostBundleDerivesCategoryAndTags(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, true, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	classifier := hostclassifier.NewHostClassifier(config.HarpoonHostClassifierConfig{
		IncludeSuffix:  []string{"internal"},
		IncludePrivate: false,
	})

	bundle := hostbus.URLBundle{
		URLs: []hostbus.URLRecord{
			{
				URL:  mustParseURLForHostRegistration(t, "https://auth.internal/issuer"),
				Tags: []hostbus.Tag{{Key: hostbus.TagKeySource, Value: "oauth"}, {Key: hostbus.TagKeyRole, Value: "issuer"}},
			},
			{
				URL:  mustParseURLForHostRegistration(t, "https://auth.internal/custom"),
				Tags: []hostbus.Tag{{Key: hostbus.TagKeySource, Value: "oauth"}, {Key: hostbus.TagKeyRole, Value: "Custom-Role"}},
			},
		},
	}

	if err := registerHostBundle(bundle, classifier, registry, logger); err != nil {
		t.Fatalf("register bundle: %v", err)
	}

	target, ok := registry.Lookup("oauth-issuer-0")
	if !ok {
		t.Fatalf("expected label oauth-issuer-0")
	}
	if target.Category != "oauth" || target.Source != "oauth" {
		t.Fatalf("expected category/source oauth, got %q/%q", target.Category, target.Source)
	}
	expectedTags := []string{"auth-server-metadata", "issuer"}
	if !reflect.DeepEqual(target.Tags, expectedTags) {
		t.Fatalf("unexpected tags: got %v want %v", target.Tags, expectedTags)
	}

	customTarget, ok := registry.Lookup("oauth-custom-role-1")
	if !ok {
		t.Fatalf("expected label oauth-custom-role-1")
	}
	customExpected := []string{"custom-role"}
	if !reflect.DeepEqual(customTarget.Tags, customExpected) {
		t.Fatalf("unexpected custom tags: got %v want %v", customTarget.Tags, customExpected)
	}
}

func TestBuildAutoLabelUsesRoleIndex(t *testing.T) {
	label := buildAutoLabel(hostbus.URLRecord{
		Tags: []hostbus.Tag{{Key: hostbus.TagKeyRole, Value: "registration-endpoint"}, {Key: hostbus.TagKeyIndex, Value: "2"}},
	}, 0)

	if label != "oauth-registration-endpoint-2" {
		t.Fatalf("unexpected label: %q", label)
	}
}

func TestRegisterHostBundleAddsDeterministicSuffixOnCollision(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, true, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	classifier := hostclassifier.NewHostClassifier(config.HarpoonHostClassifierConfig{
		IncludeSuffix:  []string{"internal"},
		IncludePrivate: false,
	})

	bundle := hostbus.URLBundle{
		URLs: []hostbus.URLRecord{
			{
				URL:  mustParseURLForHostRegistration(t, "https://first.internal/issuer"),
				Tags: []hostbus.Tag{{Key: hostbus.TagKeyRole, Value: "issuer"}, {Key: hostbus.TagKeyIndex, Value: "0"}},
			},
			{
				URL:  mustParseURLForHostRegistration(t, "https://second.internal/issuer"),
				Tags: []hostbus.Tag{{Key: hostbus.TagKeyRole, Value: "issuer"}, {Key: hostbus.TagKeyIndex, Value: "0"}},
			},
			{
				URL:  mustParseURLForHostRegistration(t, "https://third.internal/issuer"),
				Tags: []hostbus.Tag{{Key: hostbus.TagKeyRole, Value: "issuer"}, {Key: hostbus.TagKeyIndex, Value: "0"}},
			},
		},
	}

	if err := registerHostBundle(bundle, classifier, registry, logger); err != nil {
		t.Fatalf("register bundle: %v", err)
	}

	if _, ok := registry.Lookup("oauth-issuer-0"); !ok {
		t.Fatalf("expected label oauth-issuer-0")
	}
	if _, ok := registry.Lookup("oauth-issuer-0-1"); !ok {
		t.Fatalf("expected label oauth-issuer-0-1")
	}
	if _, ok := registry.Lookup("oauth-issuer-0-2"); !ok {
		t.Fatalf("expected label oauth-issuer-0-2")
	}
}

func TestRegisterHostBundleDerivedEndpointsPrivateHostsOnly(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, true, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	classifier := hostclassifier.NewHostClassifier(config.HarpoonHostClassifierConfig{
		IncludeSuffix:  []string{"internal"},
		IncludePrivate: false,
	})

	bundle := hostbus.URLBundle{
		URLs: []hostbus.URLRecord{
			{
				URL:  mustParseURLForHostRegistration(t, "https://auth.internal/authorize"),
				Tags: []hostbus.Tag{{Key: hostbus.TagKeyRole, Value: "authorization-endpoint"}, {Key: hostbus.TagKeyIndex, Value: "0"}},
			},
			{
				URL:  mustParseURLForHostRegistration(t, "https://public.example.com/token"),
				Tags: []hostbus.Tag{{Key: hostbus.TagKeyRole, Value: "token-endpoint"}, {Key: hostbus.TagKeyIndex, Value: "0"}},
			},
		},
	}

	if err := registerHostBundle(bundle, classifier, registry, logger); err != nil {
		t.Fatalf("register bundle: %v", err)
	}

	if _, ok := registry.Lookup("oauth-authorization-endpoint-0"); !ok {
		t.Fatalf("expected private derived endpoint to be registered")
	}
	if _, ok := registry.Lookup("oauth-token-endpoint-0"); ok {
		t.Fatalf("did not expect public derived endpoint registration")
	}
}

func mustParseURLForHostRegistration(t *testing.T, raw string) *url.URL {
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return parsed
}
