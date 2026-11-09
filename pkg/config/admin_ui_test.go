package config

import (
	"maps"
	"strings"
	"testing"
)

func TestNormalizeAdminUIBasePath(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "default", raw: "", want: ""},
		{name: "root", raw: "/", want: ""},
		{name: "single segment", raw: "/tunnel", want: "/tunnel"},
		{name: "trailing slash", raw: "/tunnel/", want: "/tunnel"},
		{name: "nested unreserved segments", raw: "/Apps/tunnel_1.2-rc~1/", want: "/Apps/tunnel_1.2-rc~1"},
		{name: "reserved prefix is not namespace", raw: "/health-checks", want: "/health-checks"},
		{name: "case sensitive namespace", raw: "/Health", want: "/Health"},
		{name: "maximum length", raw: "/" + strings.Repeat("a", maxAdminUIBasePathBytes-1), want: "/" + strings.Repeat("a", maxAdminUIBasePathBytes-1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeAdminUIBasePath(tc.raw)
			if err != nil {
				t.Fatalf("NormalizeAdminUIBasePath(%q) returned error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeAdminUIBasePath(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestNormalizeAdminUIBasePathRejectsNonCanonicalOrReservedPaths(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"tunnel", "//", "//tunnel", "/tunnel//nested", "/tunnel//",
		"/.", "/..", "/tunnel/./nested", "/tunnel/../nested",
		"/tunnel%2fother", "/%2e", "/tunnel?query", "/tunnel#fragment",
		"/tunnel/*", "/tunnel/{rest...}", "/tunnel/with space", "/tunnel/é", "/tunnel\\nested",
		"/health", "/health/details", "/healthz", "/healthz/nested", "/readyz", "/readyz/nested",
		"/metrics", "/metrics/nested", "/harpoon", "/harpoon/nested",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if got, err := NormalizeAdminUIBasePath(raw); err == nil {
				t.Fatalf("NormalizeAdminUIBasePath(%q) = %q, want error", raw, got)
			}
		})
	}
	for name, raw := range map[string]string{
		"overlong":                       "/" + strings.Repeat("a", maxAdminUIBasePathBytes),
		"overlong before trailing slash": "/" + strings.Repeat("a", maxAdminUIBasePathBytes-1) + "/",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := NormalizeAdminUIBasePath(raw); err == nil {
				t.Fatal("NormalizeAdminUIBasePath accepted an overlong input")
			}
		})
	}
}

func TestParseAdminUIPublicOrigin(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		raw  string
		want string
	}{
		{raw: "", want: ""},
		{raw: "https://admin.example.test", want: "https://admin.example.test"},
		{raw: "HTTPS://Admin.Example.TEST:0443/", want: "https://admin.example.test"},
		{raw: "http://admin.example.test:080", want: "http://admin.example.test"},
		{raw: "https://admin.example.test:08443", want: "https://admin.example.test:8443"},
		{raw: "https://admin.example.test.:443", want: "https://admin.example.test."},
		{raw: "https://xn--bcher-kva.example", want: "https://xn--bcher-kva.example"},
		{raw: "http://127.0.0.1:8080", want: "http://127.0.0.1:8080"},
		{raw: "http://[2001:0db8:0:0:0:0:0:1]:080/", want: "http://[2001:db8::1]"},
		{raw: "https://[2001:db8::1]:8443", want: "https://[2001:db8::1]:8443"},
		{raw: "http://localhost:1", want: "http://localhost:1"},
		{raw: "http://localhost:65535", want: "http://localhost:65535"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			got, err := ParseAdminUIPublicOrigin(tc.raw)
			if err != nil {
				t.Fatalf("ParseAdminUIPublicOrigin(%q) returned error: %v", tc.raw, err)
			}
			if tc.want == "" {
				if got != nil {
					t.Fatalf("ParseAdminUIPublicOrigin(%q) = %v, want nil", tc.raw, got)
				}
				return
			}
			if got == nil || got.String() != tc.want {
				t.Fatalf("ParseAdminUIPublicOrigin(%q) = %v, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseAdminUIPublicOriginRejectsNonOrigins(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"admin.example.test", "//admin.example.test", "ftp://admin.example.test", "https:admin.example.test",
		"https://", "https://user@admin.example.test", "https://user:pass@admin.example.test",
		"https://admin.example.test/ui", "https://admin.example.test//", "https://admin.example.test/%2f",
		"https://admin.example.test?", "https://admin.example.test?query", "https://admin.example.test#", "https://admin.example.test#fragment",
		"https://*.example.test", "https://admin.*.test", "https://bücher.example", "https://K.example",
		"http://[fe80::1%25eth0]", "http://[not-an-ip]", "http://2001:db8::1",
		"https://admin.example.test:", "https://admin.example.test:0", "https://admin.example.test:65536", "https://admin.example.test:abc",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if got, err := ParseAdminUIPublicOrigin(raw); err == nil {
				t.Fatalf("ParseAdminUIPublicOrigin(%q) = %v, want error", raw, got)
			}
		})
	}
}

func TestAdminUIPublicOriginParseErrorDoesNotReflectCredentials(t *testing.T) {
	t.Parallel()

	const password = "SYNTHETIC_PASSWORD_6f38"
	raw := "https://synthetic-user:" + password + "%zz@admin.example.test"
	_, err := Load(
		[]string{"--control-plane.tunnel-id", adapterTestTunnelID, "--mcp.server-url", "https://mcp.example.invalid/mcp"},
		lookupEnvMap(map[string]string{
			"CONTROL_PLANE_API_KEY":  adapterTestAPIKey,
			"ADMIN_UI_PUBLIC_ORIGIN": raw,
		}),
	)
	if err == nil {
		t.Fatal("malformed public origin was accepted")
	}
	message := err.Error()
	if !strings.Contains(message, "admin-ui.public-origin") {
		t.Fatalf("error does not identify public origin setting: %q", message)
	}
	if strings.Contains(message, password) || strings.Contains(message, raw) {
		t.Fatalf("error reflects malformed origin credentials: %q", message)
	}
}

func TestAdminUIPathAndOriginPrecedence(t *testing.T) {
	t.Parallel()

	profile := writeAdapterConfig(t, `
config_version: 1
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: env:CONTROL_PLANE_API_KEY
mcp:
  server_urls:
    - url: https://mcp.example.invalid/mcp
admin_ui:
  base_path: /yaml/
  public_origin: https://yaml.example.test/
`)
	for _, tc := range []struct {
		name       string
		profile    bool
		args       []string
		env        map[string]string
		wantPath   string
		wantOrigin string
	}{
		{name: "defaults"},
		{name: "yaml", profile: true, wantPath: "/yaml", wantOrigin: "https://yaml.example.test"},
		{name: "environment over yaml", profile: true, env: map[string]string{"ADMIN_UI_BASE_PATH": "/env/", "ADMIN_UI_PUBLIC_ORIGIN": "https://env.example.test:443"}, wantPath: "/env", wantOrigin: "https://env.example.test"},
		{name: "flags over environment", profile: true, args: []string{"--admin-ui.base-path", "/flag/", "--admin-ui.public-origin", "https://flag.example.test:443/"}, env: map[string]string{"ADMIN_UI_BASE_PATH": "/env", "ADMIN_UI_PUBLIC_ORIGIN": "https://env.example.test"}, wantPath: "/flag", wantOrigin: "https://flag.example.test"},
		{name: "empty environment clears yaml", profile: true, env: map[string]string{"ADMIN_UI_BASE_PATH": "", "ADMIN_UI_PUBLIC_ORIGIN": ""}},
		{name: "empty flags clear environment and yaml", profile: true, args: []string{"--admin-ui.base-path=", "--admin-ui.public-origin="}, env: map[string]string{"ADMIN_UI_BASE_PATH": "/env", "ADMIN_UI_PUBLIC_ORIGIN": "https://env.example.test"}},
		{name: "root flag clears prefix", profile: true, args: []string{"--admin-ui.base-path", "/"}, wantOrigin: "https://yaml.example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := []string{"--control-plane.tunnel-id", adapterTestTunnelID, "--mcp.server-url", "https://mcp.example.invalid/mcp"}
			if tc.profile {
				args = append(args, "--config", profile)
			}
			args = append(args, tc.args...)
			env := map[string]string{"CONTROL_PLANE_API_KEY": adapterTestAPIKey}
			maps.Copy(env, tc.env)
			cfg, err := Load(args, lookupEnvMap(env))
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}
			if cfg.AdminUI.BasePath != tc.wantPath {
				t.Fatalf("BasePath = %q, want %q", cfg.AdminUI.BasePath, tc.wantPath)
			}
			gotOrigin := ""
			if cfg.AdminUI.PublicOrigin != nil {
				gotOrigin = cfg.AdminUI.PublicOrigin.String()
			}
			if gotOrigin != tc.wantOrigin {
				t.Fatalf("PublicOrigin = %q, want %q", gotOrigin, tc.wantOrigin)
			}
		})
	}
}

func TestAdminUIPathAndOriginValidationThroughEachSource(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		flag  string
		env   string
		yaml  string
		value string
		want  string
	}{
		{name: "base path", flag: "admin-ui.base-path", env: "ADMIN_UI_BASE_PATH", yaml: "base_path", value: "/health/details", want: "admin-ui.base-path"},
		{name: "public origin", flag: "admin-ui.public-origin", env: "ADMIN_UI_PUBLIC_ORIGIN", yaml: "public_origin", value: "https://admin.example.test/ui", want: "admin-ui.public-origin"},
	} {
		for _, source := range []string{"flag", "environment", "yaml"} {
			t.Run(tc.name+"/"+source, func(t *testing.T) {
				t.Parallel()
				args := []string{"--control-plane.tunnel-id", adapterTestTunnelID, "--mcp.server-url", "https://mcp.example.invalid/mcp"}
				env := map[string]string{"CONTROL_PLANE_API_KEY": adapterTestAPIKey}
				switch source {
				case "flag":
					args = append(args, "--"+tc.flag, tc.value)
				case "environment":
					env[tc.env] = tc.value
				case "yaml":
					profile := writeAdapterConfig(t, "config_version: 1\nadmin_ui:\n  "+tc.yaml+": "+tc.value+"\n")
					args = append(args, "--config", profile)
				}
				_, err := Load(args, lookupEnvMap(env))
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("Load error = %v, want rejection mentioning %q", err, tc.want)
				}
			})
		}
	}
}

func TestAdminUIEmptyOverridesSkipInvalidLowerPrecedence(t *testing.T) {
	t.Parallel()

	profile := writeAdapterConfig(t, "config_version: 1\nadmin_ui:\n  base_path: /health\n  public_origin: https://admin.example.test/ui\n")
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
	}{
		{name: "empty environment clears invalid yaml", env: map[string]string{"ADMIN_UI_BASE_PATH": "", "ADMIN_UI_PUBLIC_ORIGIN": ""}},
		{name: "empty flags clear invalid environment and yaml", args: []string{"--admin-ui.base-path=", "--admin-ui.public-origin="}, env: map[string]string{"ADMIN_UI_BASE_PATH": "/metrics", "ADMIN_UI_PUBLIC_ORIGIN": "https://admin.example.test/api"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := []string{"--config", profile, "--control-plane.tunnel-id", adapterTestTunnelID, "--mcp.server-url", "https://mcp.example.invalid/mcp"}
			args = append(args, tc.args...)
			env := map[string]string{"CONTROL_PLANE_API_KEY": adapterTestAPIKey}
			maps.Copy(env, tc.env)
			cfg, err := Load(args, lookupEnvMap(env))
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}
			if cfg.AdminUI.BasePath != "" || cfg.AdminUI.PublicOrigin != nil {
				t.Fatalf("AdminUI = %#v, want empty BasePath and nil PublicOrigin", cfg.AdminUI)
			}
		})
	}
}
