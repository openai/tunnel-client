package adminui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

func TestTemplateConfigExportRedactsEveryFixedHeader(t *testing.T) {
	cfg := &config.Config{
		Runtime: config.RuntimeConfig{
			ConfigFile: "templates.yaml",
			ConfigFileContents: []byte(`config_version: 2
harpoon:
  targets:
    - label: resource
      template:
        headers:
          Authorization: Bearer literal-credential
          X-Session: private-session-value
          X-Custom: private-custom-value
          X-Referenced: env:PRIVATE_HEADER_REFERENCE
          X-File: file:/private/credential-location
`),
		},
		Harpoon: config.HarpoonConfig{Targets: []config.HarpoonTarget{{
			Label: "resource",
			Template: &runtimeconfig.HarpoonTargetTemplate{Headers: map[string]string{
				"Authorization": "Bearer resolved-credential",
				"X-Session":     "resolved-session-value",
			}},
		}}},
	}
	for name, snapshot := range map[string]any{
		"source YAML":             buildConfigFileSnapshot(cfg),
		"effective configuration": buildEffectiveConfigSnapshot(cfg),
	} {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{
				"literal-credential", "private-session-value", "private-custom-value",
				"PRIVATE_HEADER_REFERENCE", "/private/credential-location", "resolved-credential", "resolved-session-value",
			} {
				if strings.Contains(string(data), secret) {
					t.Fatalf("%s export contains a template header value", name)
				}
			}
		})
	}
	contents := buildConfigFileSnapshot(cfg).Contents.(map[string]any)
	harpoon := contents["harpoon"].(map[string]any)
	target := harpoon["targets"].([]any)[0].(map[string]any)
	headers := target["template"].(map[string]any)["headers"].(map[string]any)
	for key, value := range headers {
		if value != "[REDACTED]" {
			t.Fatalf("header %s was not completely redacted", key)
		}
	}
}

func TestTemplateHeaderEnvironmentValuesAreRedactedInSupportArchive(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Runtime: config.RuntimeConfig{ConfigFileContents: []byte(`config_version: 2
harpoon:
  targets:
    - label: legacy
      url: https://example.invalid
    - label: case
      template:
        headers:
          X-Session: env:HARPOON_SESSION_HEADER
          X-Custom: 'ENV: CUSTOM_HEADER_VALUE'
    - label: profile
      template:
        headers:
          Accept: env:APPLICATION_HEADER_VALUE
`)}}
	runtime := collectLogExportRuntime(nil, []string{
		"HARPOON_SESSION_HEADER=private-session-credential",
		"CUSTOM_HEADER_VALUE=private-custom-credential",
		"APPLICATION_HEADER_VALUE=private-application-credential",
		"HARPOON_MAX_RESPONSE_BYTES=2048",
	}, sensitiveRuntimeEnvReferencesFromConfig(cfg))
	for _, name := range []string{"HARPOON_SESSION_HEADER", "CUSTOM_HEADER_VALUE", "APPLICATION_HEADER_VALUE"} {
		if runtime.Environment[name] != "[REDACTED]" {
			t.Fatalf("template header environment reference %s was not redacted", name)
		}
	}
	if runtime.Environment["HARPOON_MAX_RESPONSE_BYTES"] != "2048" {
		t.Fatal("non-secret runtime setting should remain available")
	}
	archive, err := buildLogsArchive(nil, time.Now().UTC(), time.Minute, 10, runtime, metricsSnapshot{}, logExportAdminSnapshots{})
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range readTarGzForTest(t, archive) {
		for _, secret := range []string{"private-session-credential", "private-custom-credential", "private-application-credential"} {
			if strings.Contains(contents, secret) {
				t.Fatalf("support archive %s contains a template credential", name)
			}
		}
	}
}
