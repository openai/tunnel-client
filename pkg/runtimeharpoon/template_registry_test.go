package runtimeharpoon

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

func TestTemplateRegistryDoesNotAuthorizeOriginOrRenderedURLs(t *testing.T) {
	cfg := templateTestConfig()
	registry, err := NewRegistry(runtimeRegistryTestLogger(), false, []Target{{Label: "resource", Template: cfg}})
	require.NoError(t, err)
	require.Equal(t, 1, registry.Count())
	target, ok := registry.Lookup("resource")
	require.True(t, ok)
	require.Nil(t, target.Template, "mutable operator configuration must not survive registration")
	require.NotNil(t, target.template)
	rendered, err := target.template.Render(map[string]any{"resourceId": "one"})
	require.NoError(t, err)
	for _, raw := range []string{cfg.Origin, cfg.Origin + "/", rendered.String(), cfg.Origin + "/resource/two", cfg.Origin + "/admin"} {
		candidate := runtimeRegistryTestURL(t, raw)
		require.False(t, registry.AllowsURL(candidate), raw)
		_, found := registry.TargetForURL(candidate)
		require.False(t, found, raw)
	}
	_, err = registry.Resolve("resource")
	require.ErrorContains(t, err, "call_target_template")
	_, ok = registry.ExactURL("resource")
	require.False(t, ok)
	require.Empty(t, registry.targetURLKeys)
}

func TestTemplateRegistryReregistrationPreservesCompiledPolicy(t *testing.T) {
	cfg := templateTestConfig()
	cfg.Headers = map[string]string{"Authorization": "Bearer fixed-secret"}
	first, err := NewRegistry(runtimeRegistryTestLogger(), false, []Target{{Label: "resource", Template: cfg}})
	require.NoError(t, err)
	second, err := NewRegistry(runtimeRegistryTestLogger(), false, first.Targets())
	require.NoError(t, err)
	target, ok := second.Lookup("resource")
	require.True(t, ok)
	require.NotNil(t, target.template)
	require.Nil(t, target.Template)
	rendered, err := target.template.Render(map[string]any{"resourceId": "one"})
	require.NoError(t, err)
	require.Equal(t, "https://inventory.example:8443/resource/one", rendered.String())
	require.Equal(t, "Bearer fixed-secret", target.template.FixedHeaders().Get("Authorization"))
	require.False(t, second.AllowsURL(runtimeRegistryTestURL(t, cfg.Origin)))
	require.False(t, second.AllowsURL(rendered))
	_, err = second.Resolve("resource")
	require.ErrorContains(t, err, "call_target_template")
	_, ok = second.ExactURL("resource")
	require.False(t, ok)
	require.Equal(t,
		mustStartupCatalogDigest(t, first, "runtime-key", "tunnel-id"),
		mustStartupCatalogDigest(t, second, "runtime-key", "tunnel-id"),
	)
}

func TestTemplateRegistryPreservesLegacyTargetsOnSameOrigin(t *testing.T) {
	for _, templateFirst := range []bool{false, true} {
		name := "legacy first"
		if templateFirst {
			name = "template first"
		}
		t.Run(name, func(t *testing.T) {
			cfg := templateTestConfig()
			// Sharing origin metadata must not cause a transport collision: only
			// the explicit legacy target grants exact URL/socket routing.
			template := Target{Label: "resource", Template: cfg}
			legacy := Target{Label: "legacy", BaseURL: runtimeRegistryTestURL(t, cfg.Origin), UnixSocketPath: "/tmp/harpoon-template-test.sock"}
			targets := []Target{legacy, template}
			if templateFirst {
				targets = []Target{template, legacy}
			}
			registry, err := NewRegistry(runtimeRegistryTestLogger(), false, targets)
			require.NoError(t, err)
			require.Equal(t, 2, registry.Count())
			resolved, err := registry.Resolve("legacy")
			require.NoError(t, err)
			require.True(t, registry.AllowsURL(resolved))
			selected, ok := registry.TargetForURL(resolved)
			require.True(t, ok)
			require.Equal(t, "legacy", selected.Label)
			require.Equal(t, legacy.UnixSocketPath, selected.UnixSocketPath)
			exact, ok := registry.ExactURL("legacy")
			require.True(t, ok)
			require.Equal(t, cfg.Origin, exact.String())
			require.False(t, registry.AllowsURL(runtimeRegistryTestURL(t, cfg.Origin+"/resource/one")))
		})
	}
}

func TestTemplateRegistryRejectsAmbiguousOrInsecureTargets(t *testing.T) {
	for name, target := range map[string]Target{
		"exact URL and template": {Label: "resource", Template: templateTestConfig(), BaseURL: runtimeRegistryTestURL(t, "https://other.example")},
		"socket and template":    {Label: "resource", Template: templateTestConfig(), UnixSocketPath: "/tmp/harpoon-template-test.sock"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewRegistry(runtimeRegistryTestLogger(), false, []Target{target})
			require.Error(t, err)
		})
	}
	cfg := templateTestConfig()
	cfg.Origin = "http://inventory.example"
	_, err := NewRegistry(runtimeRegistryTestLogger(), true, []Target{{Label: "resource", Template: cfg}})
	require.Error(t, err, "legacy plaintext opt-in must not weaken template HTTPS policy")
}

func TestTemplateRegistryKeepsCompiledPolicyImmutable(t *testing.T) {
	cfg := templateTestConfig()
	cfg.Headers = map[string]string{"Authorization": "Bearer fixed-secret"}
	cfg.Query = map[string]string{"version": "1"}
	cfg.AllowedHeaders = []string{"Accept"}
	registry, err := NewRegistry(runtimeRegistryTestLogger(), false, []Target{{Label: "resource", Template: cfg}})
	require.NoError(t, err)
	before := mustStartupCatalogDigest(t, registry, "runtime-key", "tunnel-id")
	cfg.Origin, cfg.PathTemplate = "https://other.example", "/changed/{resourceId}"
	cfg.Headers["Authorization"] = "Bearer changed-secret"
	cfg.Query["version"] = "2"
	cfg.AllowedHeaders[0] = "X-Other"
	cfg.Parameters["resourceId"].ReservedValues[0] = "one"
	delete(cfg.Parameters, "resourceId")
	target, ok := registry.Lookup("resource")
	require.True(t, ok)
	parameters := map[string]any{"resourceId": "one"}
	rendered, err := target.template.Render(parameters)
	require.NoError(t, err)
	require.Equal(t, "https://inventory.example:8443/resource/one?version=1", rendered.String())
	req, err := http.NewRequest(http.MethodGet, rendered.String(), nil)
	require.NoError(t, err)
	req.Header = target.template.FixedHeaders()
	require.NoError(t, target.template.ValidateRequest(req, parameters))
	require.Equal(t, "Bearer fixed-secret", req.Header.Get("Authorization"))
	_, err = target.template.ValidateCallerHeaders(map[string]string{"Accept": "application/json"})
	require.NoError(t, err)
	_, err = target.template.ValidateCallerHeaders(map[string]string{"X-Other": "value"})
	require.Error(t, err)
	after := mustStartupCatalogDigest(t, registry, "runtime-key", "tunnel-id")
	require.Equal(t, before, after)
}

func TestTemplateCatalogDigestCoversSameOriginPolicyChanges(t *testing.T) {
	digest := func(t *testing.T, cfg *runtimeconfig.HarpoonTargetTemplate) startupCatalogDigest {
		t.Helper()
		registry, err := NewRegistry(runtimeRegistryTestLogger(), false, []Target{{Label: "resource", Template: cfg}})
		require.NoError(t, err)
		result := mustStartupCatalogDigest(t, registry, "runtime-key", "tunnel-id")
		target, ok := registry.Lookup("resource")
		require.True(t, ok)
		require.NotContains(t, result.Value, target.template.PolicyDigest(), "emit only the keyed catalog fingerprint")
		require.NotContains(t, result.Value, "inventory.example")
		require.NotContains(t, result.Value, "credential")
		return result
	}
	original := digest(t, templateTestConfig())
	for name, modify := range map[string]func(*runtimeconfig.HarpoonTargetTemplate){
		"route": func(c *runtimeconfig.HarpoonTargetTemplate) { c.PathTemplate = "/other/{resourceId}" },
		"query": func(c *runtimeconfig.HarpoonTargetTemplate) { c.Query = map[string]string{"fixed": "value"} },
		"schema": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.MaxLength = 32
			c.Parameters["resourceId"] = p
		},
		"reserved identifiers": func(c *runtimeconfig.HarpoonTargetTemplate) {
			p := c.Parameters["resourceId"]
			p.ReservedValues = []string{"other"}
			c.Parameters["resourceId"] = p
		},
		"fixed header": func(c *runtimeconfig.HarpoonTargetTemplate) {
			c.Headers = map[string]string{"Authorization": "Bearer private-credential"}
		},
		"caller header": func(c *runtimeconfig.HarpoonTargetTemplate) { c.AllowedHeaders = []string{"Accept"} },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := templateTestConfig()
			modify(cfg)
			require.NotEqual(t, original.Value, digest(t, cfg).Value)
		})
	}
}
