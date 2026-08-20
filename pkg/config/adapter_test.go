package config

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

const (
	adapterTestTunnelID = "tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	adapterTestAPIKey   = "sk_test_key"
)

var (
	fullOnlyAdapterFlags = []string{
		"admin-ui.log-buffer-events",
		"allow-remote-ui",
		"harpoon.capture-payloads",
		"open-web-ui",
		"proxy.check-interval",
	}
	cloudflaredAdapterFlags = []string{
		"cloudflared.managed",
		"cloudflared.path",
		"cloudflared.ready-timeout",
		"cloudflared.token",
	}
	adapterSharedEnvironmentVariables = []string{
		"CA_BUNDLE",
		"CONTROL_PLANE_API_KEY",
		"CONTROL_PLANE_BASE_URL",
		"CONTROL_PLANE_CLIENT_CERT",
		"CONTROL_PLANE_CLIENT_KEY",
		"CONTROL_PLANE_EXTRA_HEADERS",
		"CONTROL_PLANE_HTTP_PROXY",
		"CONTROL_PLANE_MAX_INFLIGHT_REQUESTS",
		"CONTROL_PLANE_ORGANIZATION_ID",
		"CONTROL_PLANE_POLL_CHANNELS",
		"CONTROL_PLANE_POLL_DEADLINE_GUARDRAIL",
		"CONTROL_PLANE_POLL_TIMEOUT",
		"CONTROL_PLANE_TUNNEL_ID",
		"CONTROL_PLANE_URL_PATH",
		"HARPOON_ADDITIONAL_TRANSPORTS",
		"HARPOON_ALLOW_PLAINTEXT_HTTP",
		"HARPOON_HOSTS_INCLUDE_LOOPBACK",
		"HARPOON_HOSTS_INCLUDE_PRIVATE",
		"HARPOON_HOSTS_INCLUDE_REGEX",
		"HARPOON_HOSTS_INCLUDE_SUFFIX",
		"HARPOON_HTTP_PROXY",
		"HARPOON_MAX_REDIRECTS",
		"HARPOON_MAX_RESPONSE_BYTES",
		"HARPOON_TARGETS",
		"HEALTH_LISTEN_ADDR",
		"HEALTH_UNIX_SOCKET",
		"HEALTH_URL_FILE",
		"HOME",
		"LOG_FILE",
		"LOG_FORMAT",
		"LOG_HTTP_RAW_UNSAFE",
		"LOG_LEVEL",
		"MCP_CLIENT_CERT",
		"MCP_CLIENT_KEY",
		"MCP_COMMAND",
		"MCP_CONNECTION_MAX_TTL",
		"MCP_DISCOVERY_EXTRA_HEADERS",
		"MCP_EXTRA_HEADERS",
		"MCP_HTTP_PROXY",
		"MCP_MAX_CONCURRENT_REQUESTS",
		"MCP_SERVER_URL",
		"MCP_STARTUP_WAIT_TIMEOUT",
		"OPENAI_API_KEY",
		"PID_FILE",
		"TUNNEL_CLIENT_CONFIG",
		"TUNNEL_CLIENT_HTTP_PROXY",
		"TUNNEL_CLIENT_PROFILE",
		"TUNNEL_CLIENT_PROFILE_DIR",
		"TUNNEL_CLIENT_PROFILE_FILE",
		"XDG_CONFIG_HOME",
	}
	// Standard proxy variables are not projected into Config by Load. They are
	// consumed by the shared EnvProxyConfigured helper, so keep their contract
	// inventory separate from loader environment coverage above.
	adapterStandardProxyEnvironmentVariables = []string{
		"HTTP_PROXY",
		"http_proxy",
		"HTTPS_PROXY",
		"https_proxy",
		"NO_PROXY",
		"no_proxy",
	}
)

type adapterFlagSurface struct {
	name                string
	shorthand           string
	usage               string
	defValue            string
	noOptDefVal         string
	valueType           string
	hidden              bool
	deprecated          string
	shorthandDeprecated string
}

func TestFullAdapterCoversEveryRuntimeConfigField(t *testing.T) {
	runtimeType := reflect.TypeOf(runtimeconfig.Config{})
	fullType := reflect.TypeOf(Config{})
	for runtimeField := range runtimeType.Fields() {
		if runtimeField.Name == "Harpoon" {
			// Harpoon keeps one full-only CapturePayloads bit in the public
			// compatibility adapter; runtimeCoreFromFull covers it below.
			continue
		}
		fullField, ok := fullType.FieldByName(runtimeField.Name)
		if !ok {
			t.Fatalf("full config adapter is missing runtime field %q", runtimeField.Name)
		}
		if fullField.Type != runtimeField.Type {
			t.Fatalf("full config field %q type = %s, want %s", runtimeField.Name, fullField.Type, runtimeField.Type)
		}
	}
	runtimeHarpoonType := reflect.TypeOf(runtimeconfig.HarpoonConfig{})
	fullHarpoonType := reflect.TypeOf(HarpoonConfig{})
	for runtimeField := range runtimeHarpoonType.Fields() {
		fullField, ok := fullHarpoonType.FieldByName(runtimeField.Name)
		if !ok {
			t.Fatalf("full Harpoon adapter is missing runtime field %q", runtimeField.Name)
		}
		if fullField.Type != runtimeField.Type {
			t.Fatalf("full Harpoon field %q type = %s, want %s", runtimeField.Name, fullField.Type, runtimeField.Type)
		}
	}
}

func TestFullAdapterCopiesEveryRuntimeConfigFieldValue(t *testing.T) {
	coreValue := adapterSentinelValue(t, reflect.TypeOf(runtimeconfig.Config{}), 1)
	core := coreValue.Interface().(runtimeconfig.Config)
	full := fullConfigFromRuntime(&core, CloudflaredConfig{}, AdminUIConfig{}, false, ProxyHealthConfig{})

	runtimeValue := reflect.ValueOf(core)
	fullValue := reflect.ValueOf(*full)
	for runtimeField := range reflect.TypeOf(core).Fields() {
		if runtimeField.Name == "Harpoon" {
			continue
		}
		want := runtimeValue.FieldByName(runtimeField.Name)
		if want.IsZero() {
			t.Fatalf("test sentinel for runtime field %q is zero", runtimeField.Name)
		}
		got := fullValue.FieldByName(runtimeField.Name)
		if !adapterValuesEqual(got, want) {
			t.Fatalf("full config adapter did not copy runtime field %q", runtimeField.Name)
		}
	}

	runtimeHarpoon := reflect.ValueOf(core.Harpoon)
	fullHarpoon := reflect.ValueOf(full.Harpoon)
	for runtimeField := range reflect.TypeOf(core.Harpoon).Fields() {
		want := runtimeHarpoon.FieldByName(runtimeField.Name)
		if want.IsZero() {
			t.Fatalf("test sentinel for runtime Harpoon field %q is zero", runtimeField.Name)
		}
		got := fullHarpoon.FieldByName(runtimeField.Name)
		if !adapterValuesEqual(got, want) {
			t.Fatalf("full Harpoon adapter did not copy runtime field %q", runtimeField.Name)
		}
	}
}

func TestRuntimeCoreFromFullCopiesEveryRuntimeConfigFieldValue(t *testing.T) {
	coreValue := adapterSentinelValue(t, reflect.TypeOf(runtimeconfig.Config{}), 1)
	core := coreValue.Interface().(runtimeconfig.Config)
	full := fullConfigFromRuntime(&core, CloudflaredConfig{}, AdminUIConfig{}, false, ProxyHealthConfig{})
	roundTrip := runtimeCoreFromFull(full)

	if !adapterValuesEqual(reflect.ValueOf(roundTrip), reflect.ValueOf(core)) {
		t.Fatalf("runtimeCoreFromFull did not preserve every shared runtime field:\nround-trip: %#v\noriginal: %#v", roundTrip, core)
	}
}

func TestFullConfigAddsOnlyExplicitFullOnlyFields(t *testing.T) {
	runtimeFields := exportedFieldNames(reflect.TypeOf(runtimeconfig.Config{}))
	fullFields := exportedFieldNames(reflect.TypeOf(Config{}))
	delete(runtimeFields, "Harpoon")
	delete(fullFields, "Harpoon")
	if got := sortedFieldDifference(fullFields, runtimeFields); !reflect.DeepEqual(got, []string{"AdminUI", "Cloudflared", "ProxyHealth"}) {
		t.Fatalf("full Config adds fields %v, want only [AdminUI Cloudflared ProxyHealth]", got)
	}

	runtimeHarpoonFields := exportedFieldNames(reflect.TypeOf(runtimeconfig.HarpoonConfig{}))
	fullHarpoonFields := exportedFieldNames(reflect.TypeOf(HarpoonConfig{}))
	if got := sortedFieldDifference(fullHarpoonFields, runtimeHarpoonFields); !reflect.DeepEqual(got, []string{"CapturePayloads"}) {
		t.Fatalf("full HarpoonConfig adds fields %v, want only [CapturePayloads]", got)
	}
}

func TestFullAndRuntimeFlagSurfacesDifferOnlyByExplicitExtensions(t *testing.T) {
	fullFlags := pflag.NewFlagSet("full", pflag.ContinueOnError)
	RegisterFlags(fullFlags)
	runtimeFlags := pflag.NewFlagSet("runtime", pflag.ContinueOnError)
	runtimeconfig.RegisterFlags(runtimeFlags, runtimeconfig.FlavorRuntime)
	runtimeCloudflaredFlags := pflag.NewFlagSet("runtime-cloudflared", pflag.ContinueOnError)
	runtimeconfig.RegisterFlags(runtimeCloudflaredFlags, runtimeconfig.FlavorRuntimeCloudflared)

	fullSurface := adapterFlagSurfaceSnapshot(fullFlags)
	runtimeSurface := adapterFlagSurfaceSnapshot(runtimeFlags)
	runtimeCloudflaredSurface := adapterFlagSurfaceSnapshot(runtimeCloudflaredFlags)

	assertAdapterFlagSurfaceSubset(t, runtimeSurface, runtimeCloudflaredSurface)
	assertAdapterFlagSurfaceSubset(t, runtimeSurface, fullSurface)
	assertAdapterFlagSurfaceSubset(t, runtimeCloudflaredSurface, fullSurface)
	assertAdapterFlagSurfaceAddsExactly(t, runtimeCloudflaredSurface, runtimeSurface, cloudflaredAdapterFlags)
	assertAdapterFlagSurfaceAddsExactly(t, fullSurface, runtimeCloudflaredSurface, fullOnlyAdapterFlags)
	assertAdapterFlagSurfaceAddsExactly(t, fullSurface, runtimeSurface, append(append([]string{}, cloudflaredAdapterFlags...), fullOnlyAdapterFlags...))
}

func adapterSentinelValue(t *testing.T, typ reflect.Type, seed int) reflect.Value {
	t.Helper()
	value := reflect.New(typ).Elem()
	switch typ.Kind() {
	case reflect.Bool:
		value.SetBool(true)
	case reflect.String:
		value.SetString(fmt.Sprintf("sentinel-%d", seed))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value.SetInt(int64(seed + 1))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		value.SetUint(uint64(seed + 1))
	case reflect.Float32, reflect.Float64:
		value.SetFloat(float64(seed + 1))
	case reflect.Complex64, reflect.Complex128:
		value.SetComplex(complex(float64(seed+1), float64(seed+2)))
	case reflect.Ptr:
		value.Set(reflect.New(typ.Elem()))
	case reflect.Func:
		value.Set(reflect.MakeFunc(typ, func([]reflect.Value) []reflect.Value {
			results := make([]reflect.Value, typ.NumOut())
			for i := range results {
				results[i] = reflect.Zero(typ.Out(i))
			}
			return results
		}))
	case reflect.Chan:
		value.Set(reflect.MakeChan(typ, 1))
	case reflect.Slice:
		slice := reflect.MakeSlice(typ, 1, 1)
		slice.Index(0).Set(adapterSentinelValue(t, typ.Elem(), seed+1))
		value.Set(slice)
	case reflect.Array:
		for i := 0; i < typ.Len(); i++ {
			value.Index(i).Set(adapterSentinelValue(t, typ.Elem(), seed+i+1))
		}
	case reflect.Map:
		key := adapterSentinelValue(t, typ.Key(), seed+1)
		element := adapterSentinelValue(t, typ.Elem(), seed+2)
		value.Set(reflect.MakeMapWithSize(typ, 1))
		value.SetMapIndex(key, element)
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" {
				continue
			}
			value.Field(i).Set(adapterSentinelValue(t, field.Type, seed+i+1))
		}
	default:
		t.Fatalf("unsupported adapter sentinel kind %s for %s", typ.Kind(), typ)
	}
	return value
}

func adapterValuesEqual(got reflect.Value, want reflect.Value) bool {
	if !got.IsValid() || !want.IsValid() {
		return got.IsValid() == want.IsValid()
	}
	if got.Type() != want.Type() {
		return false
	}
	switch want.Kind() {
	case reflect.Func:
		if got.IsNil() || want.IsNil() {
			return got.IsNil() == want.IsNil()
		}
		return got.Pointer() == want.Pointer()
	case reflect.Chan:
		if got.IsNil() || want.IsNil() {
			return got.IsNil() == want.IsNil()
		}
		return got.Pointer() == want.Pointer()
	case reflect.Ptr:
		if got.IsNil() || want.IsNil() {
			return got.IsNil() == want.IsNil()
		}
		return adapterValuesEqual(got.Elem(), want.Elem())
	case reflect.Interface:
		if got.IsNil() || want.IsNil() {
			return got.IsNil() == want.IsNil()
		}
		return adapterValuesEqual(got.Elem(), want.Elem())
	case reflect.Struct:
		for i := 0; i < want.NumField(); i++ {
			if want.Type().Field(i).PkgPath != "" {
				continue
			}
			if !adapterValuesEqual(got.Field(i), want.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Slice:
		if got.IsNil() != want.IsNil() || got.Len() != want.Len() {
			return false
		}
		for i := 0; i < want.Len(); i++ {
			if !adapterValuesEqual(got.Index(i), want.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Array:
		for i := 0; i < want.Len(); i++ {
			if !adapterValuesEqual(got.Index(i), want.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Map:
		if got.IsNil() != want.IsNil() || got.Len() != want.Len() {
			return false
		}
		iter := want.MapRange()
		for iter.Next() {
			gotValue := got.MapIndex(iter.Key())
			if !gotValue.IsValid() || !adapterValuesEqual(gotValue, iter.Value()) {
				return false
			}
		}
		return true
	default:
		if !got.CanInterface() || !want.CanInterface() {
			return false
		}
		return reflect.DeepEqual(got.Interface(), want.Interface())
	}
}

func exportedFieldNames(typ reflect.Type) map[string]struct{} {
	fields := make(map[string]struct{}, typ.NumField())
	for field := range typ.Fields() {
		if field.PkgPath == "" {
			fields[field.Name] = struct{}{}
		}
	}
	return fields
}

func sortedFieldDifference(left map[string]struct{}, right map[string]struct{}) []string {
	difference := make([]string, 0)
	for name := range left {
		if _, ok := right[name]; !ok {
			difference = append(difference, name)
		}
	}
	sort.Strings(difference)
	return difference
}

func adapterFlagSurfaceSnapshot(fs *pflag.FlagSet) map[string]adapterFlagSurface {
	surface := make(map[string]adapterFlagSurface)
	fs.VisitAll(func(flag *pflag.Flag) {
		surface[flag.Name] = adapterFlagSurface{
			name:                flag.Name,
			shorthand:           flag.Shorthand,
			usage:               flag.Usage,
			defValue:            flag.DefValue,
			noOptDefVal:         flag.NoOptDefVal,
			valueType:           flag.Value.Type(),
			hidden:              flag.Hidden,
			deprecated:          flag.Deprecated,
			shorthandDeprecated: flag.ShorthandDeprecated,
		}
	})
	return surface
}

func assertAdapterFlagSurfaceSubset(t *testing.T, want map[string]adapterFlagSurface, got map[string]adapterFlagSurface) {
	t.Helper()
	for name, wantFlag := range want {
		gotFlag, ok := got[name]
		if !ok {
			t.Fatalf("flag surface is missing shared flag %q", name)
		}
		if !reflect.DeepEqual(gotFlag, wantFlag) {
			t.Fatalf("flag %q metadata differs:\nwant: %#v\ngot:  %#v", name, wantFlag, gotFlag)
		}
	}
}

func assertAdapterFlagSurfaceAddsExactly(t *testing.T, larger map[string]adapterFlagSurface, smaller map[string]adapterFlagSurface, want []string) {
	t.Helper()
	got := make([]string, 0)
	for name := range larger {
		if _, ok := smaller[name]; !ok {
			got = append(got, name)
		}
	}
	sort.Strings(got)
	want = append([]string(nil), want...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flag surface adds %v, want exactly %v", got, want)
	}
}

func TestFullAndRuntimeLoadSharedEffectiveConfigParity(t *testing.T) {
	profile := writeAdapterConfig(t, `
config_version: 1
control_plane:
  tunnel_id: tunnel_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
  api_key: env:PROFILE_API_KEY
  base_url: https://profile.example.invalid
mcp:
  server_urls:
    - url: https://profile-mcp.example.invalid/mcp
health:
  listen_addr: 127.0.0.1:7777
`)
	cases := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{
			name: "required environment plus defaults",
			args: []string{"--control-plane.tunnel-id", adapterTestTunnelID, "--mcp.server-url", "https://mcp.example.invalid/mcp"},
			env:  map[string]string{"CONTROL_PLANE_API_KEY": adapterTestAPIKey},
		},
		{
			name: "environment",
			args: nil,
			env: map[string]string{
				"CONTROL_PLANE_API_KEY":   adapterTestAPIKey,
				"CONTROL_PLANE_TUNNEL_ID": adapterTestTunnelID,
				"MCP_SERVER_URL":          "https://env-mcp.example.invalid/mcp",
				"HEALTH_LISTEN_ADDR":      "127.0.0.1:8888",
			},
		},
		{
			name: "profile",
			args: []string{"--config", profile},
			env:  map[string]string{"PROFILE_API_KEY": adapterTestAPIKey},
		},
		{
			name: "flag over environment over profile",
			args: []string{
				"--config", profile,
				"--control-plane.tunnel-id", adapterTestTunnelID,
				"--mcp.server-url", "https://flag-mcp.example.invalid/mcp",
			},
			env: map[string]string{
				"CONTROL_PLANE_API_KEY":  adapterTestAPIKey,
				"CONTROL_PLANE_BASE_URL": "https://env.example.invalid",
				"HEALTH_LISTEN_ADDR":     "127.0.0.1:9999",
				"PROFILE_API_KEY":        adapterTestAPIKey,
			},
		},
	}
	for _, flavor := range adapterRuntimeFlavors() {
		flavor := flavor
		t.Run(string(flavor), func(t *testing.T) {
			for _, tc := range cases {
				tc := tc
				t.Run(tc.name, func(t *testing.T) {
					full, runtime := loadParityPairForFlavor(t, tc.args, lookupEnvMap(tc.env), flavor)
					assertSharedRuntimeParity(t, full, runtime)
				})
			}
		})
	}
}

func TestFullAndRuntimeAcceptDefaultEquivalentFullOnlyEnvironment(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{name: "ALLOW_REMOTE_UI", value: "false"},
		{name: "OPEN_WEB_UI", value: "false"},
		{name: "ADMIN_UI_LOG_BUFFER_EVENTS", value: "2000"},
		{name: "PROXY_CHECK_INTERVAL", value: "60s"},
		{name: "PROXY_CHECK_INTERVAL", value: "1m"},
		{name: "PROXY_CHECK_INTERVAL", value: "1m0s"},
		{name: "HARPOON_CAPTURE_PAYLOADS", value: "false"},
	}
	for _, flavor := range adapterRuntimeFlavors() {
		flavor := flavor
		t.Run(string(flavor), func(t *testing.T) {
			for _, tc := range cases {
				tc := tc
				t.Run(tc.name+"="+tc.value, func(t *testing.T) {
					args := []string{
						"--control-plane.tunnel-id", adapterTestTunnelID,
						"--mcp.server-url", "https://mcp.example.invalid/mcp",
					}
					lookup := lookupEnvMap(map[string]string{
						"CONTROL_PLANE_API_KEY": adapterTestAPIKey,
						tc.name:                 tc.value,
					})
					full, runtime := loadParityPairForFlavor(t, args, lookup, flavor)
					assertSharedRuntimeParity(t, full, runtime)
				})
			}
		})
	}
}

func TestFullAndRuntimeSharedProductionInputsParity(t *testing.T) {
	headerFile := writeAdapterSecret(t, "file-header-value\n")
	t.Run("headers proxies and repeatable flags", func(t *testing.T) {
		args := []string{
			"--control-plane.tunnel-id", adapterTestTunnelID,
			"--control-plane.poll-channel", "main",
			"--control-plane.poll-channel", "tools",
			"--control-plane.extra-headers", "X-Control-Env: env:CONTROL_HEADER",
			"--control-plane.extra-headers", "X-Control-File: file:" + headerFile,
			"--http-proxy", "env:GLOBAL_PROXY",
			"--control-plane.http-proxy", "env:CONTROL_PROXY",
			"--mcp.server-url", "channel=main,url=https://main-mcp.example.invalid/mcp",
			"--mcp.server-url", "channel=tools,url=https://tools-mcp.example.invalid/mcp",
			"--mcp.extra-headers", "X-MCP-Env: env:MCP_HEADER",
			"--mcp.extra-headers", "X-MCP-File: file:" + headerFile,
			"--mcp.discovery-extra-headers", "X-Discovery: env:DISCOVERY_HEADER",
			"--mcp.http-proxy", "env:MCP_PROXY",
			"--harpoon.target", "label=auth,url=https://auth.example.invalid/token",
			"--harpoon.target", "label=metadata,url=https://auth.example.invalid/.well-known/oauth-authorization-server",
			"--harpoon.http-proxy", "env:HARPOON_PROXY",
		}
		lookup := lookupEnvMap(map[string]string{
			"CONTROL_PLANE_API_KEY": adapterTestAPIKey,
			"CONTROL_HEADER":        "control-env-value",
			"MCP_HEADER":            "mcp-env-value",
			"DISCOVERY_HEADER":      "discovery-env-value",
			"GLOBAL_PROXY":          "http://global-proxy.example.invalid:8080",
			"CONTROL_PROXY":         "http://control-proxy.example.invalid:8080",
			"MCP_PROXY":             "http://mcp-proxy.example.invalid:8080",
			"HARPOON_PROXY":         "http://harpoon-proxy.example.invalid:8080",
		})
		for _, flavor := range adapterRuntimeFlavors() {
			flavor := flavor
			t.Run(string(flavor), func(t *testing.T) {
				full, runtime := loadParityPairForFlavor(t, args, lookup, flavor)
				assertSharedRuntimeParity(t, full, runtime)
			})
		}
	})

	t.Run("profile secret references TLS and mounted paths", func(t *testing.T) {
		certPath, keyPath := writeAdapterClientCertificate(t)
		apiKeyPath := writeAdapterSecret(t, adapterTestAPIKey+"\n")
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatalf("get working directory: %v", err)
		}
		relativeCertPath, err := filepath.Rel(cwd, certPath)
		if err != nil {
			t.Fatalf("relative cert path: %v", err)
		}
		relativeKeyPath, err := filepath.Rel(cwd, keyPath)
		if err != nil {
			t.Fatalf("relative key path: %v", err)
		}
		relativeAPIKeyPath, err := filepath.Rel(cwd, apiKeyPath)
		if err != nil {
			t.Fatalf("relative API key path: %v", err)
		}
		profile := writeAdapterConfig(t, `
config_version: 1
ca_bundle: `+relativeCertPath+`
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: file:`+relativeAPIKeyPath+`
  client_cert: `+relativeCertPath+`
  client_key: `+relativeKeyPath+`
  extra_headers:
    X-Profile-Secret: file:`+apiKeyPath+`
mcp:
  server_urls:
    - channel: main
      url: https://mcp.example.invalid/mcp
      client_cert: `+relativeCertPath+`
      client_key: `+relativeKeyPath+`
  extra_headers:
    X-MCP-Profile: file:`+apiKeyPath+`
health:
  url_file: relative/health.url
process:
  pid_file: relative/client.pid
`)
		for _, flavor := range adapterRuntimeFlavors() {
			flavor := flavor
			t.Run(string(flavor), func(t *testing.T) {
				full, runtime := loadParityPairForFlavor(t, []string{"--config", profile}, lookupEnvMap(nil), flavor)
				assertSharedRuntimeParity(t, full, runtime)
				if full.TLS == nil || full.TLS.Path != relativeCertPath {
					t.Fatalf("full TLS path = %#v, want %q", full.TLS, relativeCertPath)
				}
				if full.Health.URLFile != "relative/health.url" || full.Process.PIDFile != "relative/client.pid" {
					t.Fatalf("relative mounted paths changed: health=%q pid=%q", full.Health.URLFile, full.Process.PIDFile)
				}
			})
		}
	})
}

func TestFullAndRuntimeFlavorsPreserveExactProfileBytes(t *testing.T) {
	raw := []byte("# bytes are retained for diagnostics\n\nconfig_version: 1\ncontrol_plane:\n  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n  api_key: env:CONTROL_PLANE_API_KEY\nmcp:\n  server_urls:\n    - url: https://mcp.example.invalid/mcp\nadmin_ui:\n  allow_remote: false\n  open_browser: false\n  log_buffer_events: 2000\nharpoon:\n  capture_payloads: false\nproxy:\n  check_interval: 1m0s\n\n")
	profile := writeAdapterConfigBytes(t, raw)
	lookup := lookupEnvMap(map[string]string{"CONTROL_PLANE_API_KEY": adapterTestAPIKey})

	for _, flavor := range adapterRuntimeFlavors() {
		flavor := flavor
		t.Run(string(flavor), func(t *testing.T) {
			full, runtime := loadParityPairForFlavor(t, []string{"--config", profile}, lookup, flavor)
			assertSharedRuntimeParity(t, full, runtime)
			if !bytes.Equal(full.Runtime.ConfigFileContents, raw) {
				t.Fatalf("full config bytes changed:\nwant: %q\ngot:  %q", raw, full.Runtime.ConfigFileContents)
			}
			if !bytes.Equal(runtime.Runtime.ConfigFileContents, raw) {
				t.Fatalf("%s config bytes changed:\nwant: %q\ngot:  %q", flavor, raw, runtime.Runtime.ConfigFileContents)
			}
		})
	}
}

func TestFullAndRuntimeCloudflaredPreserveSharedConfigAndCompanionSettings(t *testing.T) {
	raw := []byte("config_version: 1\ncontrol_plane:\n  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n  api_key: env:CONTROL_PLANE_API_KEY\nmcp:\n  server_urls:\n    - url: https://mcp.example.invalid/mcp\ncloudflared:\n  token: env:PROFILE_CLOUDFLARED_TOKEN\n  managed: false\n  path: /profile/cloudflared\n  ready_timeout: 20s\n")
	profile := writeAdapterConfigBytes(t, raw)
	args := []string{"--config", profile, "--cloudflared.path", "/flag/cloudflared"}
	lookup := lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY":     adapterTestAPIKey,
		"PROFILE_CLOUDFLARED_TOKEN": "profile-token",
		"CLOUDFLARED_READY_TIMEOUT": "17s",
	})
	full, runtimeCloudflared := loadRuntimeCloudflaredParityPair(t, args, lookup)

	assertSharedRuntimeParity(t, full, &runtimeCloudflared.Runtime)
	if !reflect.DeepEqual(full.Cloudflared, runtimeCloudflared.Cloudflared) {
		t.Fatalf("cloudflared effective config differs:\nfull: %#v\nruntime-cloudflared: %#v", full.Cloudflared, runtimeCloudflared.Cloudflared)
	}
	if !bytes.Equal(full.Runtime.ConfigFileContents, raw) || !bytes.Equal(runtimeCloudflared.Runtime.Runtime.ConfigFileContents, raw) {
		t.Fatalf("cloudflared profile bytes changed:\nwant: %q\nfull: %q\nruntime-cloudflared: %q", raw, full.Runtime.ConfigFileContents, runtimeCloudflared.Runtime.Runtime.ConfigFileContents)
	}
}

func TestFullAndRuntimeCloudflaredEnvironmentParity(t *testing.T) {
	lookup := lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY":     adapterTestAPIKey,
		"CONTROL_PLANE_TUNNEL_ID":   adapterTestTunnelID,
		"MCP_SERVER_URL":            "https://mcp.example.invalid/mcp",
		"CLOUDFLARED_TUNNEL_TOKEN":  "env-token",
		"CLOUDFLARED_MANAGED":       "true",
		"CLOUDFLARED_PATH":          "/env/cloudflared",
		"CLOUDFLARED_READY_TIMEOUT": "23s",
	})
	full, runtimeCloudflared := loadRuntimeCloudflaredParityPair(t, nil, lookup)

	assertSharedRuntimeParity(t, full, &runtimeCloudflared.Runtime)
	if !reflect.DeepEqual(full.Cloudflared, runtimeCloudflared.Cloudflared) {
		t.Fatalf("cloudflared environment config differs:\nfull: %#v\nruntime-cloudflared: %#v", full.Cloudflared, runtimeCloudflared.Cloudflared)
	}
}

func TestFullAndRuntimeFlavorsSharedEnvironmentParity(t *testing.T) {
	certPath, keyPath := writeAdapterClientCertificate(t)
	sharedProfile := []byte("config_version: 1\ncontrol_plane:\n  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n  api_key: env:CONTROL_PLANE_API_KEY\nmcp:\n  server_urls:\n    - url: https://mcp.example.invalid/mcp\n")
	configPath := writeAdapterConfigBytes(t, sharedProfile)
	profileDir := t.TempDir()
	profilePath := filepath.Join(profileDir, "named.yaml")
	writeAdapterProfileFile(t, profilePath, sharedProfile)
	xdgConfigHome := t.TempDir()
	writeAdapterProfileFile(
		t,
		filepath.Join(xdgConfigHome, "tunnel-client", "xdg-default.yaml"),
		sharedProfile,
	)
	homeDir := t.TempDir()
	writeAdapterProfileFile(
		t,
		filepath.Join(homeDir, ".config", "tunnel-client", "home-default.yaml"),
		sharedProfile,
	)
	writeAdapterProfileFile(
		t,
		filepath.Join(homeDir, "profiles", "tilde-dir.yaml"),
		sharedProfile,
	)
	writeAdapterProfileFile(
		t,
		filepath.Join(homeDir, "profiles", "tilde-file.yaml"),
		sharedProfile,
	)

	cases := []struct {
		name string
		env  map[string]string
	}{
		{
			name: "scalar repeated and channel settings",
			env: map[string]string{
				"CONTROL_PLANE_API_KEY":                 adapterTestAPIKey,
				"CONTROL_PLANE_BASE_URL":                "https://control.example.invalid",
				"CONTROL_PLANE_URL_PATH":                "/gateway",
				"CONTROL_PLANE_TUNNEL_ID":               adapterTestTunnelID,
				"CONTROL_PLANE_ORGANIZATION_ID":         "org-test",
				"CONTROL_PLANE_MAX_INFLIGHT_REQUESTS":   "17",
				"CONTROL_PLANE_POLL_TIMEOUT":            "45s",
				"CONTROL_PLANE_POLL_DEADLINE_GUARDRAIL": "500ms",
				"CONTROL_PLANE_POLL_CHANNELS":           "main,tools,harpoon",
				"CONTROL_PLANE_EXTRA_HEADERS":           "X-Control: control",
				"LOG_LEVEL":                             "debug",
				"LOG_FORMAT":                            "json",
				"LOG_FILE":                              "relative/client.log",
				"LOG_HTTP_RAW_UNSAFE":                   "true",
				"HEALTH_LISTEN_ADDR":                    "127.0.0.1:7777",
				"HEALTH_URL_FILE":                       "relative/health.url",
				"PID_FILE":                              "relative/client.pid",
				"MCP_SERVER_URL":                        "channel=main,url=https://main-mcp.example.invalid/mcp",
				"MCP_COMMAND":                           "command=echo hello,channel=tools",
				"MCP_EXTRA_HEADERS":                     "X-MCP: mcp",
				"MCP_DISCOVERY_EXTRA_HEADERS":           "X-Discovery: discovery",
				"MCP_STARTUP_WAIT_TIMEOUT":              "2s",
				"MCP_CONNECTION_MAX_TTL":                "30s",
				"MCP_MAX_CONCURRENT_REQUESTS":           "7",
				"HARPOON_TARGETS":                       "label=auth,url=https://auth.example.invalid/token",
				"HARPOON_ALLOW_PLAINTEXT_HTTP":          "false",
				"HARPOON_MAX_RESPONSE_BYTES":            "2048",
				"HARPOON_MAX_REDIRECTS":                 "3",
				"HARPOON_ADDITIONAL_TRANSPORTS":         "http-streamable",
				"HARPOON_HOSTS_INCLUDE_SUFFIX":          "internal.example;corp.example",
				"HARPOON_HOSTS_INCLUDE_REGEX":           "^internal\\.example$",
				"HARPOON_HOSTS_INCLUDE_LOOPBACK":        "false",
				"HARPOON_HOSTS_INCLUDE_PRIVATE":         "false",
			},
		},
		{
			name: "TLS and component proxies",
			env: map[string]string{
				"CONTROL_PLANE_API_KEY":     adapterTestAPIKey,
				"CONTROL_PLANE_TUNNEL_ID":   adapterTestTunnelID,
				"MCP_SERVER_URL":            "https://mcp.example.invalid/mcp",
				"CA_BUNDLE":                 certPath,
				"TUNNEL_CLIENT_HTTP_PROXY":  "http://global-proxy.example.invalid:8080",
				"CONTROL_PLANE_CLIENT_CERT": certPath,
				"CONTROL_PLANE_CLIENT_KEY":  keyPath,
				"CONTROL_PLANE_HTTP_PROXY":  "http://control-proxy.example.invalid:8080",
				"MCP_CLIENT_CERT":           certPath,
				"MCP_CLIENT_KEY":            keyPath,
				"MCP_HTTP_PROXY":            "http://mcp-proxy.example.invalid:8080",
				"HARPOON_HTTP_PROXY":        "http://harpoon-proxy.example.invalid:8080",
			},
		},
		{
			name: "global proxy only",
			env: map[string]string{
				"CONTROL_PLANE_API_KEY":    adapterTestAPIKey,
				"CONTROL_PLANE_TUNNEL_ID":  adapterTestTunnelID,
				"MCP_SERVER_URL":           "https://mcp.example.invalid/mcp",
				"TUNNEL_CLIENT_HTTP_PROXY": "http://global-proxy.example.invalid:8080",
			},
		},
		{
			name: "openai fallback and unix health",
			env: map[string]string{
				"OPENAI_API_KEY":          adapterTestAPIKey,
				"CONTROL_PLANE_TUNNEL_ID": adapterTestTunnelID,
				"MCP_SERVER_URL":          "https://mcp.example.invalid/mcp",
				"HEALTH_UNIX_SOCKET":      filepath.Join(t.TempDir(), "health.sock"),
			},
		},
		{
			name: "config selector",
			env: map[string]string{
				"TUNNEL_CLIENT_CONFIG":  configPath,
				"CONTROL_PLANE_API_KEY": adapterTestAPIKey,
			},
		},
		{
			name: "named profile selector",
			env: map[string]string{
				"TUNNEL_CLIENT_PROFILE":     "named",
				"TUNNEL_CLIENT_PROFILE_DIR": profileDir,
				"CONTROL_PLANE_API_KEY":     adapterTestAPIKey,
			},
		},
		{
			name: "named profile selector through XDG default directory",
			env: map[string]string{
				"TUNNEL_CLIENT_PROFILE": "xdg-default",
				"XDG_CONFIG_HOME":       xdgConfigHome,
				"CONTROL_PLANE_API_KEY": adapterTestAPIKey,
			},
		},
		{
			name: "named profile selector through HOME default directory",
			env: map[string]string{
				"TUNNEL_CLIENT_PROFILE": "home-default",
				"HOME":                  homeDir,
				"CONTROL_PLANE_API_KEY": adapterTestAPIKey,
			},
		},
		{
			name: "named profile selector expands tilde profile directory",
			env: map[string]string{
				"TUNNEL_CLIENT_PROFILE":     "tilde-dir",
				"TUNNEL_CLIENT_PROFILE_DIR": "~/profiles",
				"HOME":                      homeDir,
				"CONTROL_PLANE_API_KEY":     adapterTestAPIKey,
			},
		},
		{
			name: "profile file selector",
			env: map[string]string{
				"TUNNEL_CLIENT_PROFILE_FILE": profilePath,
				"CONTROL_PLANE_API_KEY":      adapterTestAPIKey,
			},
		},
		{
			name: "profile file selector expands tilde",
			env: map[string]string{
				"TUNNEL_CLIENT_PROFILE_FILE": "~/profiles/tilde-file.yaml",
				"HOME":                       homeDir,
				"CONTROL_PLANE_API_KEY":      adapterTestAPIKey,
			},
		},
	}

	covered := make(map[string]struct{})
	for _, tc := range cases {
		tc := tc
		for name := range tc.env {
			if adapterSharedEnvironmentName(name) {
				covered[name] = struct{}{}
			}
		}
		t.Run(tc.name, func(t *testing.T) {
			for _, flavor := range adapterRuntimeFlavors() {
				flavor := flavor
				t.Run(string(flavor), func(t *testing.T) {
					full, runtime := loadParityPairForFlavor(t, nil, lookupEnvMap(tc.env), flavor)
					assertSharedRuntimeParity(t, full, runtime)
				})
			}
		})
	}
	if got, want := sortedFieldDifference(adapterSharedEnvironmentNames(), covered), []string{}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shared environment parity cases do not cover %v", got)
	}
}

func TestFullAndRuntimeStandardProxyEnvironmentDetectionParity(t *testing.T) {
	type proxyEnvCase struct {
		name string
		env  map[string]string
		want bool
	}
	testCases := make([]proxyEnvCase, 0, len(adapterStandardProxyEnvironmentVariables)+2)
	for _, name := range adapterStandardProxyEnvironmentVariables {
		testCases = append(testCases, proxyEnvCase{
			name: name,
			env:  map[string]string{name: "configured"},
			want: true,
		})
	}
	testCases = append(testCases,
		proxyEnvCase{
			name: "empty standard proxy values",
			env:  map[string]string{"HTTP_PROXY": "", "no_proxy": ""},
			want: false,
		},
		proxyEnvCase{
			name: "unrelated environment",
			env:  map[string]string{"ALL_PROXY": "configured"},
			want: false,
		},
	)

	covered := make(map[string]struct{}, len(adapterStandardProxyEnvironmentVariables))
	for _, testCase := range testCases {
		testCase := testCase
		for name := range testCase.env {
			if adapterStandardProxyEnvironmentName(name) {
				covered[name] = struct{}{}
			}
		}
		t.Run(testCase.name, func(t *testing.T) {
			lookup := lookupEnvMap(testCase.env)
			full := EnvProxyConfigured(lookup)
			runtime := runtimeconfig.EnvProxyConfigured(lookup)
			if full != runtime {
				t.Fatalf("proxy environment detection differs: full=%t runtime=%t", full, runtime)
			}
			if full != testCase.want {
				t.Fatalf("proxy environment detection = %t, want %t", full, testCase.want)
			}
		})
	}
	if got, want := sortedFieldDifference(adapterStandardProxyEnvironmentNames(), covered), []string{}; !reflect.DeepEqual(got, want) {
		t.Fatalf("standard proxy environment parity cases do not cover %v", got)
	}
}

func FuzzFullAndRuntimeSharedProfileParity(f *testing.F) {
	f.Add("https://api.example.invalid", "https://mcp.example.invalid/mcp", "struct-text", "127.0.0.1:8080", uint8(0))
	f.Add("https://api.example.invalid/gateway", "https://mcp.example.invalid/mcp", "json", "127.0.0.1:0", uint8(1))
	f.Fuzz(func(t *testing.T, baseURL string, mcpURL string, logFormat string, healthAddr string, flavorIndex uint8) {
		if len(baseURL) > 256 || len(mcpURL) > 256 || len(logFormat) > 64 || len(healthAddr) > 128 {
			t.Skip()
		}
		raw := []byte(fmt.Sprintf("config_version: 1\ncontrol_plane:\n  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n  api_key: env:CONTROL_PLANE_API_KEY\n  base_url: %q\nmcp:\n  server_urls:\n    - url: %q\nlog:\n  format: %q\nhealth:\n  listen_addr: %q\n", baseURL, mcpURL, logFormat, healthAddr))
		profile := writeAdapterConfigBytes(t, raw)
		flavors := adapterRuntimeFlavors()
		flavor := flavors[int(flavorIndex)%len(flavors)]
		lookup := lookupEnvMap(map[string]string{"CONTROL_PLANE_API_KEY": adapterTestAPIKey})

		full, fullErr := Load([]string{"--config", profile}, lookup)
		runtime, runtimeErr := runtimeconfig.Load([]string{"--config", profile}, flavor, lookup)
		if (fullErr == nil) != (runtimeErr == nil) {
			t.Fatalf("shared profile acceptance differs for %s:\nfull: %v\nruntime: %v\nprofile: %q", flavor, fullErr, runtimeErr, raw)
		}
		if fullErr != nil {
			if strings.Contains(fullErr.Error(), adapterTestAPIKey) || strings.Contains(runtimeErr.Error(), adapterTestAPIKey) {
				t.Fatalf("configuration error leaked API key: full=%v runtime=%v", fullErr, runtimeErr)
			}
			return
		}
		assertSharedRuntimeParity(t, full, runtime)
	})
}

func TestRuntimeAcceptsDisabledFullOnlyProfileValuesUnchanged(t *testing.T) {
	for _, interval := range []string{"60s", "1m", "1m0s"} {
		t.Run("proxy.check_interval="+interval, func(t *testing.T) {
			profile := writeAdapterConfig(t, `
config_version: 1
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: env:CONTROL_PLANE_API_KEY
mcp:
  server_urls:
    - url: https://mcp.example.invalid/mcp
admin_ui:
  allow_remote: false
  open_browser: false
  log_buffer_events: 2000
harpoon:
  capture_payloads: false
proxy:
  check_interval: `+interval+`
`)
			args := []string{"--config", profile}
			lookup := lookupEnvMap(map[string]string{"CONTROL_PLANE_API_KEY": adapterTestAPIKey})
			for _, flavor := range adapterRuntimeFlavors() {
				flavor := flavor
				t.Run(string(flavor), func(t *testing.T) {
					full, runtime := loadParityPairForFlavor(t, args, lookup, flavor)
					assertSharedRuntimeParity(t, full, runtime)
				})
			}
		})
	}
}

func TestRuntimeRejectsNonDefaultFullOnlyProfileValues(t *testing.T) {
	cases := map[string]string{
		"admin_ui.allow_remote":      "admin_ui:\n  allow_remote: true",
		"admin_ui.open_browser":      "admin_ui:\n  open_browser: true",
		"admin_ui.log_buffer_events": "admin_ui:\n  log_buffer_events: 2001",
		"harpoon.capture_payloads":   "harpoon:\n  capture_payloads: true",
		"proxy.check_interval":       "proxy:\n  check_interval: 61s",
	}
	for want, extension := range cases {
		t.Run(want, func(t *testing.T) {
			profile := writeAdapterConfig(t, `
config_version: 1
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: env:CONTROL_PLANE_API_KEY
mcp:
  server_urls:
    - url: https://mcp.example.invalid/mcp
`+extension+"\n")
			args := []string{"--config", profile}
			lookup := lookupEnvMap(map[string]string{"CONTROL_PLANE_API_KEY": adapterTestAPIKey})
			if _, err := Load(args, lookup); err != nil {
				t.Fatalf("full Load rejected its extension: %v", err)
			}
			_, err := runtimeconfig.Load(args, runtimeconfig.FlavorRuntime, lookup)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("runtime error = %v, want rejection mentioning %q", err, want)
			}
		})
	}
}

func TestFullOnlyExtensionPrecedenceAndCloudflaredAdapter(t *testing.T) {
	profile := writeAdapterConfig(t, `
config_version: 1
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: env:CONTROL_PLANE_API_KEY
mcp:
  server_urls:
    - url: https://mcp.example.invalid/mcp
admin_ui:
  allow_remote: true
  open_browser: true
  log_buffer_events: 321
harpoon:
  capture_payloads: true
proxy:
  check_interval: 45s
cloudflared:
  token: env:PROFILE_CLOUDFLARED_TOKEN
  path: /profile/cloudflared
  ready_timeout: 20s
`)
	cfg, err := Load([]string{
		"--config", profile,
		"--admin-ui.log-buffer-events", "456",
		"--proxy.check-interval", "30s",
		"--cloudflared.path", "/flag/cloudflared",
	}, lookupEnvMap(map[string]string{
		"CONTROL_PLANE_API_KEY":     adapterTestAPIKey,
		"PROFILE_CLOUDFLARED_TOKEN": "profile-token",
		"OPEN_WEB_UI":               "false",
		"HARPOON_CAPTURE_PAYLOADS":  "false",
	}))
	if err != nil {
		t.Fatalf("full Load returned error: %v", err)
	}
	if !cfg.AdminUI.AllowRemote || cfg.AdminUI.OpenBrowser || cfg.AdminUI.LogBufferEvents != 456 {
		t.Fatalf("unexpected full Admin UI extension: %#v", cfg.AdminUI)
	}
	if cfg.Harpoon.CapturePayloads {
		t.Fatalf("environment should override profile Harpoon capture")
	}
	if cfg.ProxyHealth.CheckInterval.String() != "30s" {
		t.Fatalf("unexpected proxy health interval: %s", cfg.ProxyHealth.CheckInterval)
	}
	if cfg.Cloudflared.Token != "profile-token" || cfg.Cloudflared.Path != "/flag/cloudflared" || cfg.Cloudflared.ReadyTimeout.String() != "20s" {
		t.Fatalf("unexpected Cloudflared adapter: %#v", cfg.Cloudflared)
	}
}

func TestFullProfileValidationKeepsFullOnlyFields(t *testing.T) {
	profile := []byte(`
config_version: 1
admin_ui:
  allow_remote: true
  open_browser: true
  log_buffer_events: 321
harpoon:
  capture_payloads: true
proxy:
  check_interval: 45s
cloudflared:
  managed: true
`)
	if err := ValidateProfileBytes("full.yaml", profile); err != nil {
		t.Fatalf("full profile validation rejected full-only fields: %v", err)
	}
	if err := runtimeconfig.ValidateProfileBytes("runtime.yaml", profile); err == nil {
		t.Fatal("runtime profile validation accepted non-default full-only fields")
	}
}

func adapterRuntimeFlavors() []runtimeconfig.Flavor {
	return []runtimeconfig.Flavor{runtimeconfig.FlavorRuntime, runtimeconfig.FlavorRuntimeCloudflared}
}

func adapterSharedEnvironmentNames() map[string]struct{} {
	names := make(map[string]struct{}, len(adapterSharedEnvironmentVariables))
	for _, name := range adapterSharedEnvironmentVariables {
		names[name] = struct{}{}
	}
	return names
}

func adapterSharedEnvironmentName(name string) bool {
	_, ok := adapterSharedEnvironmentNames()[name]
	return ok
}

func adapterStandardProxyEnvironmentNames() map[string]struct{} {
	names := make(map[string]struct{}, len(adapterStandardProxyEnvironmentVariables))
	for _, name := range adapterStandardProxyEnvironmentVariables {
		names[name] = struct{}{}
	}
	return names
}

func adapterStandardProxyEnvironmentName(name string) bool {
	_, ok := adapterStandardProxyEnvironmentNames()[name]
	return ok
}

func loadParityPairForFlavor(t *testing.T, args []string, lookup func(string) (string, bool), flavor runtimeconfig.Flavor) (*Config, *runtimeconfig.Config) {
	t.Helper()
	full, err := Load(args, lookup)
	if err != nil {
		t.Fatalf("full Load returned error: %v", err)
	}
	runtime, err := runtimeconfig.Load(args, flavor, lookup)
	if err != nil {
		t.Fatalf("%s Load returned error: %v", flavor, err)
	}
	return full, runtime
}

func loadRuntimeCloudflaredParityPair(t *testing.T, args []string, lookup func(string) (string, bool)) (*Config, *runtimeconfig.CloudflaredConfig) {
	t.Helper()
	full, err := Load(args, lookup)
	if err != nil {
		t.Fatalf("full Load returned error: %v", err)
	}
	fs := pflag.NewFlagSet("runtime-cloudflared", pflag.ContinueOnError)
	runtimeconfig.RegisterFlags(fs, runtimeconfig.FlavorRuntimeCloudflared)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("runtime-cloudflared flag parse returned error: %v", err)
	}
	runtime, err := runtimeconfig.LoadCloudflaredFromFlagSet(fs, lookup)
	if err != nil {
		t.Fatalf("runtime-cloudflared Load returned error: %v", err)
	}
	return full, runtime
}

func assertSharedRuntimeParity(t *testing.T, full *Config, runtime *runtimeconfig.Config) {
	t.Helper()
	got := runtimeCoreFromFull(full)
	want := *runtime
	gotTLSEvidence := tlsEvidence(got)
	wantTLSEvidence := tlsEvidence(want)
	clearLoadedTLS(&got)
	clearLoadedTLS(&want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shared effective config differs:\nfull: %#v\nruntime: %#v", got, want)
	}
	if !reflect.DeepEqual(gotTLSEvidence, wantTLSEvidence) {
		t.Fatalf("shared TLS evidence differs:\nfull: %#v\nruntime: %#v", gotTLSEvidence, wantTLSEvidence)
	}
}

func tlsEvidence(cfg runtimeconfig.Config) []string {
	evidence := make([]string, 0, 8)
	if cfg.TLS != nil {
		evidence = append(evidence, "bundle="+cfg.TLS.Path)
	}
	if cert := cfg.ControlPlane.ClientCertificate; cert != nil {
		evidence = append(evidence, "control-plane="+cert.CertPath+"|"+cert.KeyPath)
	}
	if cert := cfg.MCP.ClientCertificate; cert != nil {
		evidence = append(evidence, "mcp-default="+cert.CertPath+"|"+cert.KeyPath)
	}
	for _, binding := range cfg.MCP.ChannelBindings {
		if cert := binding.ClientCertificate; cert != nil {
			evidence = append(evidence, "mcp-"+binding.Channel.String()+"="+cert.CertPath+"|"+cert.KeyPath)
		}
	}
	return evidence
}

func clearLoadedTLS(cfg *runtimeconfig.Config) {
	cfg.TLS = nil
	cfg.ControlPlane.ClientCertificate = nil
	cfg.MCP.ClientCertificate = nil
	cfg.MCP.ChannelBindings = append([]runtimeconfig.MCPChannelBinding(nil), cfg.MCP.ChannelBindings...)
	for i := range cfg.MCP.ChannelBindings {
		cfg.MCP.ChannelBindings[i].ClientCertificate = nil
	}
}

func runtimeCoreFromFull(cfg *Config) runtimeconfig.Config {
	return runtimeconfig.Config{
		ControlPlane: cfg.ControlPlane,
		Logging:      cfg.Logging,
		Health:       cfg.Health,
		Process:      cfg.Process,
		MCP:          cfg.MCP,
		Harpoon: runtimeconfig.HarpoonConfig{
			AllowPlaintextHTTP:   cfg.Harpoon.AllowPlaintextHTTP,
			MaxResponseBytes:     cfg.Harpoon.MaxResponseBytes,
			MaxRedirects:         cfg.Harpoon.MaxRedirects,
			AdditionalTransports: cfg.Harpoon.AdditionalTransports,
			Targets:              cfg.Harpoon.Targets,
			HostClassifier:       cfg.Harpoon.HostClassifier,
			HTTPProxy:            cfg.Harpoon.HTTPProxy,
			HTTPProxySource:      cfg.Harpoon.HTTPProxySource,
		},
		TLS:     cfg.TLS,
		Runtime: cfg.Runtime,
	}
}

func writeAdapterSecret(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	return path
}

func writeAdapterClientCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "adapter-test"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "client.pem")
	keyPath := filepath.Join(dir, "client-key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	return certPath, keyPath
}

func writeAdapterConfig(t *testing.T, contents string) string {
	t.Helper()
	return writeAdapterConfigBytes(t, []byte(strings.TrimSpace(contents)+"\n"))
}

func writeAdapterConfigBytes(t *testing.T, contents []byte) string {
	t.Helper()
	return writeAdapterProfileFile(t, filepath.Join(t.TempDir(), "config.yaml"), contents)
}

func writeAdapterProfileFile(t *testing.T, path string, contents []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
