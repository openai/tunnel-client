package runtimeconfig

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const templateConfigFixture = `config_version: 2
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: env:TEST_API_KEY
  poll_channels: [harpoon]
harpoon:
  targets:
    - label: case-details
      description: Fetch one case
      template:
        version: 1
        origin: https://private.example.invalid
        method: GET
        path_template: /cases/{case_id}
        parameters:
          case_id:
            type: string
            required: true
            pattern: '^[A-Za-z0-9_-]+$'
            min_length: 1
            max_length: 64
            reserved_values: [search, all]
        query:
          view: detailed
        headers:
          Authorization: env:TEST_TEMPLATE_AUTH
        allowed_headers: [Accept]
        follow_redirects: false
    - label: legacy
      url: https://private.example.invalid/legacy
`

func TestLoadTemplateConfigPreservesStructuredPolicyAcrossFlavors(t *testing.T) {
	for _, flavor := range []Flavor{FlavorRuntime, FlavorRuntimeCloudflared, FlavorFull} {
		t.Run(string(flavor), func(t *testing.T) {
			configPath := writeRuntimeConfig(t, templateConfigFixture)
			cfg, err := Load([]string{"--config", configPath}, flavor, lookupEnvMap(map[string]string{
				"TEST_API_KEY":       testAPIKey,
				"TEST_TEMPLATE_AUTH": "Bearer secret;with,delimiters=preserved",
			}))
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Harpoon.Targets) != 2 {
				t.Fatalf("got %d targets, want template and legacy targets", len(cfg.Harpoon.Targets))
			}
			target := cfg.Harpoon.Targets[0]
			if target.BaseURL != nil || target.UnixSocketPath != "" || target.Template == nil {
				t.Fatal("template was converted to an exact URL")
			}
			want := &HarpoonTargetTemplate{
				Version: 1, Origin: "https://private.example.invalid", Method: "GET", PathTemplate: "/cases/{case_id}",
				Parameters: map[string]HarpoonTemplateParameter{"case_id": {
					Type: "string", Required: true, Pattern: "^[A-Za-z0-9_-]+$", MinLength: 1, MaxLength: 64, ReservedValues: []string{"search", "all"},
				}},
				Query: map[string]string{"view": "detailed"}, Headers: map[string]string{"Authorization": "Bearer secret;with,delimiters=preserved"},
				AllowedHeaders: []string{"Accept"},
			}
			if !reflect.DeepEqual(target.Template, want) {
				t.Fatal("loaded template policy differs from YAML")
			}
			if target.Label != "case-details" || target.Description != "Fetch one case" {
				t.Fatalf("unexpected template identity: %q %q", target.Label, target.Description)
			}
			legacy := cfg.Harpoon.Targets[1]
			if legacy.Template != nil || legacy.BaseURL == nil || legacy.BaseURL.String() != "https://private.example.invalid/legacy" {
				t.Fatal("legacy target changed")
			}
			if !strings.Contains(string(cfg.Runtime.ConfigFileContents), "env:TEST_TEMPLATE_AUTH") || strings.Contains(string(cfg.Runtime.ConfigFileContents), "Bearer secret") {
				t.Fatal("raw configuration should retain the unresolved header reference")
			}
		})
	}
}

func TestLoadTemplateParameterMetadataAcrossFlavors(t *testing.T) {
	t.Parallel()
	contents := strings.Replace(templateConfigFixture, "            required: true\n", "            required: true\n            description: Case identifier from the profile response\n            examples: [CASE-123, CASE-456]\n", 1)
	for _, flavor := range []Flavor{FlavorRuntime, FlavorRuntimeCloudflared, FlavorFull} {
		t.Run(string(flavor), func(t *testing.T) {
			t.Parallel()
			cfg, err := Load([]string{"--config", writeRuntimeConfig(t, contents)}, flavor, lookupEnvMap(map[string]string{
				"TEST_API_KEY": testAPIKey, "TEST_TEMPLATE_AUTH": "Bearer secret",
			}))
			if err != nil {
				t.Fatal(err)
			}
			parameter := cfg.Harpoon.Targets[0].Template.Parameters["case_id"]
			if parameter.Description != "Case identifier from the profile response" || !reflect.DeepEqual(parameter.Examples, []string{"CASE-123", "CASE-456"}) {
				t.Fatal("parameter metadata was not preserved by the config loader")
			}
			encoded, err := json.Marshal(parameter)
			if err != nil {
				t.Fatal(err)
			}
			var decoded HarpoonTemplateParameter
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(parameter, decoded) {
				t.Fatal("parameter metadata did not survive JSON serialization")
			}
			if !strings.Contains(string(encoded), `"description":`) || !strings.Contains(string(encoded), `"examples":`) {
				t.Fatal("parameter metadata did not use the public JSON field names")
			}
		})
	}
}

func TestTemplateConfigStrictSchemaAndVersion(t *testing.T) {
	cases := []struct{ name, from, to, want string }{
		{"missing config version", "config_version: 2\n", "", "requires config_version: 2"},
		{"legacy config version", "config_version: 2", "config_version: 1", "requires config_version: 2"},
		{"future config version", "config_version: 2", "config_version: 3", "unsupported config_version 3"},
		{"missing template version", "        version: 1\n", "", "template version must be 1"},
		{"future template version", "        version: 1", "        version: 2", "template version must be 1"},
		{"url and template", "      template:", "      url: https://other.example.invalid\n      template:", "cannot be combined"},
		{"empty url and template", "      template:", "      url: ''\n      template:", "cannot be combined"},
		{"socket and template", "      template:", "      unix_socket: /tmp/upstream.sock\n      template:", "cannot be combined"},
		{"unknown template field", "        method: GET", "        methods: GET", "field methods not found"},
		{"unknown parameter field", "            max_length: 64", "            maxLength: 64", "field maxLength not found"},
		{"unknown metadata field", "            max_length: 64", "            max_length: 64\n            example: CASE-123", "field example not found"},
		{"duplicate description", "            max_length: 64", "            max_length: 64\n            description: first\n            description: second", "already defined"},
		{"duplicate examples field", "            max_length: 64", "            max_length: 64\n            examples: [CASE-123]\n            examples: [CASE-456]", "already defined"},
		{"object example", "            max_length: 64", "            max_length: 64\n            examples: [{id: CASE-123}]", "cannot unmarshal"},
		{"duplicate template key", "        method: GET", "        method: GET\n        method: POST", "already defined"},
		{"duplicate query key", "          view: detailed", "          view: detailed\n          view: summary", "already defined"},
		{"duplicate header key", "          Authorization: env:TEST_TEMPLATE_AUTH", "          Authorization: env:A\n          Authorization: env:B", "already defined"},
		{"duplicate parameter key", "            max_length: 64", "            max_length: 64\n            max_length: 4096", "already defined"},
		{"additional document", "config_version: 2", "config_version: 2\n---\nconfig_version: 2", "exactly one YAML document"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contents := strings.Replace(templateConfigFixture, tc.from, tc.to, 1)
			for _, validate := range []func(string, []byte) error{ValidateProfileBytes, ValidateFullProfileBytes} {
				err := validate("template.yaml", []byte(contents))
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("got %v, want error containing %q", err, tc.want)
				}
			}
		})
	}
}

func TestTemplateConfigSecretReferencesAndErrors(t *testing.T) {
	for _, tc := range []struct{ name, value, want string }{
		{"file", "Bearer from-file\n", ""},
		{"empty file", "\n", "resolved value is empty"},
		{"multiline file", "Bearer secret\nInjected: yes\n", "cannot contain CR or LF"},
		{"invalid byte file", "Bearer secret\x00suffix", "invalid HTTP header value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secretPath := filepath.Join(t.TempDir(), "secret")
			if err := os.WriteFile(secretPath, []byte(tc.value), 0o600); err != nil {
				t.Fatal(err)
			}
			contents := strings.Replace(templateConfigFixture, "env:TEST_TEMPLATE_AUTH", "file:"+secretPath, 1)
			cfg, err := Load([]string{"--config", writeRuntimeConfig(t, contents)}, FlavorRuntime, lookupEnvMap(map[string]string{"TEST_API_KEY": testAPIKey}))
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "Bearer secret") {
					t.Fatalf("expected redacted error containing %q; got %v", tc.want, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Harpoon.Targets[0].Template.Headers["Authorization"] != "Bearer from-file" {
				t.Fatal("file header reference was not resolved")
			}
		})
	}
	for _, tc := range []struct {
		name, reference, want string
		env                   map[string]string
	}{
		{"missing env", "env:TEST_TEMPLATE_AUTH", "is not set", nil},
		{"empty env", "env:TEST_TEMPLATE_AUTH", "resolved value is empty", map[string]string{"TEST_TEMPLATE_AUTH": ""}},
		{"invalid env name", "env:NOT-VALID", "environment variable name is invalid", nil},
		{"missing file", "file:/nonexistent/template-secret", "header value file", nil},
		{"case variant headers", "env:TEST_TEMPLATE_AUTH\n          authorization: other", "conflicting values", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contents := strings.Replace(templateConfigFixture, "env:TEST_TEMPLATE_AUTH", tc.reference, 1)
			env := map[string]string{"TEST_API_KEY": testAPIKey}
			maps.Copy(env, tc.env)
			_, err := Load([]string{"--config", writeRuntimeConfig(t, contents)}, FlavorRuntime, lookupEnvMap(env))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestTemplateProfileValidationRequiresPolicyValidator(t *testing.T) {
	for name, validate := range map[string]func(string, []byte) error{
		"runtime": ValidateProfileBytes,
		"full":    ValidateFullProfileBytes,
	} {
		t.Run(name, func(t *testing.T) {
			err := validate("template.yaml", []byte(templateConfigFixture))
			if err == nil || !strings.Contains(err.Error(), "template policy validator is required") {
				t.Fatalf("validation without a policy validator returned %v", err)
			}
		})
	}
	for name, validate := range map[string]func(string, []byte, TemplatePolicyValidator) error{
		"runtime": ValidateProfileBytesWithTemplateValidator,
		"full":    ValidateFullProfileBytesWithTemplateValidator,
	} {
		t.Run(name+" with validator", func(t *testing.T) {
			called := false
			err := validate("template.yaml", []byte(templateConfigFixture), func(policy *HarpoonTargetTemplate) error {
				called = true
				if policy.Headers["Authorization"] != "x" || policy.PathTemplate != "/cases/{case_id}" {
					t.Fatal("validator must receive the policy with only secret references replaced")
				}
				return os.ErrInvalid
			})
			if !called || err == nil || !strings.Contains(err.Error(), os.ErrInvalid.Error()) {
				t.Fatalf("policy validator rejection was not enforced: %v", err)
			}
		})
	}
}

func TestLegacyYAMLProfilesKeepFirstDocumentBehavior(t *testing.T) {
	const exact = `control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: env:TEST_API_KEY
  poll_channels: [harpoon]
harpoon:
  targets:
    - label: first
      url: https://private.example.invalid/first
`
	for _, version := range []string{"", "config_version: 1\n", "config_version: 2\n"} {
		for _, trailing := range []string{"---\n", "---\nunknown_field: ignored\n", "---\ninvalid: [\n"} {
			t.Run(version+trailing, func(t *testing.T) {
				contents := version + exact + trailing
				path := writeRuntimeConfig(t, contents)
				for _, validate := range []func(string, []byte) error{ValidateProfileBytes, ValidateFullProfileBytes} {
					err := validate(path, []byte(contents))
					if version == "config_version: 2\n" {
						if err == nil || !strings.Contains(err.Error(), "exactly one YAML document") {
							t.Fatalf("version 2 accepted trailing document: %v", err)
						}
					} else if err != nil {
						t.Fatalf("legacy profile rejected ignored trailing document: %v", err)
					}
				}
				cfg, err := Load([]string{"--config", path}, FlavorRuntime, lookupEnvMap(map[string]string{"TEST_API_KEY": testAPIKey}))
				if version == "config_version: 2\n" {
					if err == nil || !strings.Contains(err.Error(), "exactly one YAML document") {
						t.Fatalf("version 2 load accepted trailing document: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("legacy load rejected ignored trailing document: %v", err)
				}
				if len(cfg.Harpoon.Targets) != 1 || cfg.Harpoon.Targets[0].Label != "first" {
					t.Fatal("legacy load did not retain only the first document")
				}
			})
		}
	}
}

func TestTemplateConfigHigherPrecedenceReplacesEntireList(t *testing.T) {
	for _, tc := range []struct {
		name      string
		flags     []string
		env       map[string]string
		wantLabel string
	}{
		{"environment", nil, map[string]string{"HARPOON_TARGETS": "label=env,url=https://env.example.invalid"}, "env"},
		{"flags override environment", []string{"--harpoon.target", "label=flag,url=https://flag.example.invalid"}, map[string]string{"HARPOON_TARGETS": "label=env,url=https://env.example.invalid"}, "flag"},
		{"empty environment clears list", nil, map[string]string{"HARPOON_TARGETS": ""}, ""},
		{"empty flag clears list", []string{"--harpoon.target="}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A valid MCP main binding permits an intentionally empty Harpoon list.
			contents := strings.Replace(templateConfigFixture, "  poll_channels: [harpoon]", "  poll_channels: [main]\nmcp:\n  server_urls:\n    - url: https://mcp.example.invalid", 1)
			args := append([]string{"--config", writeRuntimeConfig(t, contents)}, tc.flags...)
			env := map[string]string{"TEST_API_KEY": testAPIKey}
			maps.Copy(env, tc.env)
			// No template secret is supplied: overridden values must not be resolved.
			cfg, err := Load(args, FlavorRuntime, lookupEnvMap(env))
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantLabel == "" {
				if len(cfg.Harpoon.Targets) != 0 {
					t.Fatal("expected an empty target list")
				}
				return
			}
			if len(cfg.Harpoon.Targets) != 1 || cfg.Harpoon.Targets[0].Label != tc.wantLabel || cfg.Harpoon.Targets[0].Template != nil {
				t.Fatal("higher-precedence targets did not replace the entire YAML target list")
			}
		})
	}
}

func TestLegacyYAMLTargetsRemainCompatible(t *testing.T) {
	for _, version := range []string{"", "config_version: 1\n", "config_version: 2\n"} {
		t.Run(strings.TrimSpace(version), func(t *testing.T) {
			contents := version + `control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: env:TEST_API_KEY
  poll_channels: [harpoon]
harpoon:
  targets:
    - label: exact
      url: env:TEST_EXACT_URL
      unix_socket: env:TEST_SOCKET
      description: 'one, two; three'
`
			cfg, err := Load([]string{"--config", writeRuntimeConfig(t, contents)}, FlavorRuntime, lookupEnvMap(map[string]string{
				"TEST_API_KEY": testAPIKey, "TEST_EXACT_URL": "https://private.example.invalid/exact?a=b,c;d", "TEST_SOCKET": "/tmp/exact.sock",
			}))
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Harpoon.Targets) != 1 {
				t.Fatal("missing exact target")
			}
			target := cfg.Harpoon.Targets[0]
			if target.Template != nil || target.BaseURL.String() != "https://private.example.invalid/exact?a=b,c;d" || target.UnixSocketPath != "/tmp/exact.sock" || target.Description != "one, two; three" {
				t.Fatal("exact target values were not preserved")
			}
		})
	}
}
