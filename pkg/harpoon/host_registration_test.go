package harpoon

import (
	"io"
	"log/slog"
	"net/url"
	"reflect"
	"testing"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/harpoon/hostbus"
	"github.com/openai/tunnel-client/pkg/harpoon/internal/hostclassifier"
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

func TestRegisterHostBundleIncludesGroupPublicTag(t *testing.T) {
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
				URL: mustParseURLForHostRegistration(t, "https://auth.internal/oauth"),
				Tags: []hostbus.Tag{
					{Key: hostbus.TagKeySource, Value: "oauth"},
					{Key: hostbus.TagKeyRole, Value: "prmd-auth-server"},
					{Key: hostbus.TagKeyIndex, Value: "0"},
					{Key: hostbus.TagKeyGroup, Value: "auth-server:0"},
				},
			},
		},
	}

	if err := registerHostBundle(bundle, classifier, registry, logger); err != nil {
		t.Fatalf("register bundle: %v", err)
	}

	target, ok := registry.Lookup("oauth-prmd-auth-server-0")
	if !ok {
		t.Fatalf("expected label oauth-prmd-auth-server-0")
	}
	expectedTags := []string{"authorization-server", "group=auth-server:0", "protected-resource-metadata"}
	if !reflect.DeepEqual(target.Tags, expectedTags) {
		t.Fatalf("unexpected tags: got %v want %v", target.Tags, expectedTags)
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

func TestRegisterHostBundleAllowsOAuthEndpointsOnProtectedResourceHost(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, true, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	classifier := hostclassifier.NewHostClassifier(config.HarpoonHostClassifierConfig{
		IncludePrivate: false,
	})

	const (
		resourceHost = "location-mcp.internal.preproduction.smp.bigco-example.com"
		issuerHost   = "idp.bigco-example.com"
		groupTag     = "auth-server:a1b2c3d4e5f6:0"
	)
	bundle := hostbus.URLBundle{
		URLs: []hostbus.URLRecord{
			oauthURLRecordForTest(t, "https://"+resourceHost+"/mcp", "prmd-resource", "0", ""),
			oauthURLRecordForTest(
				t,
				"https://"+resourceHost+"/.well-known/oauth-protected-resource/mcp",
				"prmd-source",
				"0",
				"",
			),
			oauthURLRecordForTest(t, "https://"+resourceHost, "prmd-auth-server", "0", groupTag),
			oauthURLRecordForTest(
				t,
				"https://"+resourceHost+"/.well-known/oauth-authorization-server",
				"auth-server-metadata",
				"0",
				groupTag,
			),
			oauthURLRecordForTest(t, "https://"+resourceHost+"/register", "registration-endpoint", "0", groupTag),
			oauthURLRecordForTest(t, "https://"+issuerHost+"/oauth2/default/token", "token-endpoint", "0", groupTag),
		},
	}

	if err := registerHostBundle(bundle, classifier, registry, logger); err != nil {
		t.Fatalf("register bundle: %v", err)
	}

	assertRegisteredTargetForHostRegistration(t, registry, "oauth-prmd-resource-0", "https://"+resourceHost+"/mcp", registrationScope)
	assertRegisteredTargetForHostRegistration(
		t,
		registry,
		"oauth-prmd-source-0",
		"https://"+resourceHost+"/.well-known/oauth-protected-resource/mcp",
		registrationScope,
	)
	assertRegisteredTargetForHostRegistration(t, registry, "oauth-prmd-auth-server-0", "https://"+resourceHost, registrationScope)
	assertRegisteredTargetForHostRegistration(
		t,
		registry,
		"oauth-auth-server-metadata-0",
		"https://"+resourceHost+"/.well-known/oauth-authorization-server",
		registrationScope,
	)
	assertRegisteredTargetForHostRegistration(
		t,
		registry,
		"oauth-registration-endpoint-0",
		"https://"+resourceHost+"/register",
		registrationScope,
	)
	if _, ok := registry.Lookup("oauth-token-endpoint-0"); ok {
		t.Fatalf("did not expect external issuer token endpoint registration")
	}
}

func TestRegisterHostBundleAllowsPRMDResourceHostWhenMetadataIsOffHost(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, true, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	classifier := hostclassifier.NewHostClassifier(config.HarpoonHostClassifierConfig{
		IncludePrivate: false,
	})

	const (
		resourceHost = "location-mcp.internal.preproduction.smp.bigco-example.com"
		metadataHost = "metadata.bigco-example.net"
		issuerHost   = "idp.bigco-example.com"
		groupTag     = "auth-server:a1b2c3d4e5f6:0"
	)
	bundle := hostbus.URLBundle{
		URLs: []hostbus.URLRecord{
			oauthURLRecordForTest(t, "https://"+resourceHost+"/mcp", "prmd-resource", "0", ""),
			oauthURLRecordForTest(
				t,
				"https://"+metadataHost+"/.well-known/oauth-protected-resource/mcp",
				"prmd-source",
				"0",
				"",
			),
			oauthURLRecordForTest(t, "https://"+resourceHost, "prmd-auth-server", "0", groupTag),
			oauthURLRecordForTest(
				t,
				"https://"+resourceHost+"/.well-known/oauth-authorization-server",
				"auth-server-metadata",
				"0",
				groupTag,
			),
			oauthURLRecordForTest(t, "https://"+resourceHost+"/register", "registration-endpoint", "0", groupTag),
			oauthURLRecordForTest(t, "https://"+issuerHost+"/oauth2/default/token", "token-endpoint", "0", groupTag),
		},
	}

	if err := registerHostBundle(bundle, classifier, registry, logger); err != nil {
		t.Fatalf("register bundle: %v", err)
	}

	assertRegisteredTargetForHostRegistration(t, registry, "oauth-prmd-resource-0", "https://"+resourceHost+"/mcp", registrationScope)
	assertRegisteredTargetForHostRegistration(
		t,
		registry,
		"oauth-prmd-source-0",
		"https://"+metadataHost+"/.well-known/oauth-protected-resource/mcp",
		registrationScope,
	)
	assertRegisteredTargetForHostRegistration(
		t,
		registry,
		"oauth-registration-endpoint-0",
		"https://"+resourceHost+"/register",
		registrationScope,
	)
	if _, ok := registry.Lookup("oauth-token-endpoint-0"); ok {
		t.Fatalf("did not expect external issuer token endpoint registration")
	}
}

func TestRegisterHostBundleDoesNotSeedOAuthPolicyFromOverbroadPRMDResource(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, true, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	classifier := hostclassifier.NewHostClassifier(config.HarpoonHostClassifierConfig{
		IncludePrivate: false,
	})

	const groupTag = "auth-server:a1b2c3d4e5f6:0"
	bundle := hostbus.URLBundle{
		URLs: []hostbus.URLRecord{
			oauthURLRecordForTest(t, "https://com/mcp", "prmd-resource", "0", ""),
			oauthURLRecordForTest(
				t,
				"https://metadata.bigco-example.net/.well-known/oauth-protected-resource/mcp",
				"prmd-source",
				"0",
				"",
			),
			oauthURLRecordForTest(t, "https://idp.bigco-example.com/oauth2/default/token", "token-endpoint", "0", groupTag),
		},
	}

	if err := registerHostBundle(bundle, classifier, registry, logger); err != nil {
		t.Fatalf("register bundle: %v", err)
	}

	assertRegisteredTargetForHostRegistration(
		t,
		registry,
		"oauth-prmd-source-0",
		"https://metadata.bigco-example.net/.well-known/oauth-protected-resource/mcp",
		registrationScope,
	)
	if _, ok := registry.Lookup("oauth-token-endpoint-0"); ok {
		t.Fatalf("did not expect PRMD resource suffix to allow unrelated public OAuth endpoint")
	}
}

func mustParseURLForHostRegistration(t *testing.T, raw string) *url.URL {
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return parsed
}

func oauthURLRecordForTest(t *testing.T, raw string, role string, index string, group string) hostbus.URLRecord {
	t.Helper()
	tags := []hostbus.Tag{
		{Key: hostbus.TagKeySource, Value: oauthSource},
		{Key: hostbus.TagKeyRole, Value: role},
		{Key: hostbus.TagKeyIndex, Value: index},
	}
	if group != "" {
		tags = append(tags, hostbus.Tag{Key: hostbus.TagKeyGroup, Value: group})
	}
	return hostbus.URLRecord{
		URL:         mustParseURLForHostRegistration(t, raw),
		Description: role,
		Tags:        tags,
	}
}

func assertRegisteredTargetForHostRegistration(t *testing.T, registry *Registry, label string, expectedURL string, expectedReason string) {
	t.Helper()
	target, ok := registry.Lookup(label)
	if !ok {
		t.Fatalf("expected registered label %q", label)
	}
	if target.BaseURL == nil || target.BaseURL.String() != expectedURL {
		t.Fatalf("target %q url mismatch: got %v want %q", label, target.BaseURL, expectedURL)
	}
	if target.InclusionReason != expectedReason {
		t.Fatalf("target %q inclusion reason mismatch: got %q want %q", label, target.InclusionReason, expectedReason)
	}
}
