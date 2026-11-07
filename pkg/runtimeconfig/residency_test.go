package runtimeconfig

import (
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResidencyEndpointCatalog(t *testing.T) {
	t.Parallel()
	// Expected public hostnames are independent of the production catalog so
	// accidental removal, misspelling, or cross-region mTLS mapping fails here.
	for _, tc := range []struct {
		residency  Residency
		host, mtls string
	}{
		{ResidencyGlobal, "api.openai.com", "mtls.api.openai.com"},
		{ResidencyUS, "us.api.openai.com", "mtls-us.api.openai.com"},
		{ResidencyEU, "eu.api.openai.com", "mtls-eu.api.openai.com"},
		{ResidencyAU, "au.api.openai.com", ""},
		{ResidencyCA, "ca.api.openai.com", ""},
		{ResidencyGB, "gb.api.openai.com", ""},
		{ResidencyIN, "in.api.openai.com", ""},
		{ResidencyJP, "jp.api.openai.com", ""},
		{ResidencyKR, "kr.api.openai.com", ""},
		{ResidencySG, "sg.api.openai.com", ""},
		{ResidencyAE, "ae.api.openai.com", ""},
	} {
		t.Run(string(tc.residency), func(t *testing.T) {
			t.Parallel()
			parsed, err := ParseResidency(strings.ToUpper(string(tc.residency)))
			require.NoError(t, err)
			require.Equal(t, tc.residency, parsed)
			endpoint, err := ResolveControlPlaneBaseURL("", parsed, false)
			require.NoError(t, err)
			require.Equal(t, "https://"+tc.host, endpoint.String())
			endpoint, err = ResolveControlPlaneBaseURL("", parsed, true)
			if tc.mtls == "" {
				require.ErrorContains(t, err, "no configured mTLS endpoint")
				require.Nil(t, endpoint)
			} else {
				require.NoError(t, err)
				require.Equal(t, "https://"+tc.mtls, endpoint.String())
			}
		})
	}
}

func TestResidencyRejectsUnknownAndEmptyValues(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", " ", "unknown", "uk", "no_constraint", "europe", "eu,us"} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			_, err := ParseResidency(value)
			require.ErrorContains(t, err, "control-plane.residency")
		})
	}
	endpoint, err := ResolveControlPlaneBaseURL("", Residency("future"), false)
	require.ErrorContains(t, err, "control-plane.residency")
	require.Nil(t, endpoint)
}

func TestResidencyValidatesExplicitBaseURLs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		base string
		mtls bool
		want string
	}{
		{"https://eu.api.openai.com", false, "https://eu.api.openai.com"},
		{"https://EU.API.OPENAI.COM/", false, "https://eu.api.openai.com"},
		{"https://eu.api.openai.com/", true, "https://mtls-eu.api.openai.com"},
		{"https://mtls-eu.api.openai.com", true, "https://mtls-eu.api.openai.com"},
		{"https://api.openai.com", false, ""},
		{"https://mtls.api.openai.com", true, ""},
		{"https://us.api.openai.com", false, ""},
		{"https://eu.api.openai.com.attacker.example", false, ""},
		{"http://eu.api.openai.com", false, ""},
		{"https://eu.api.openai.com:8443", false, ""},
		{"https://eu.api.openai.com/v1", false, ""},
		{"https://eu.api.openai.com?override=1", false, ""},
		{"https://eu.api.openai.com#fragment", false, ""},
		{"https://credential@eu.api.openai.com", false, ""},
		{"https://mtls-eu.api.openai.com", false, ""},
	} {
		t.Run(tc.base+"/"+tc.want, func(t *testing.T) {
			t.Parallel()
			endpoint, err := ResolveControlPlaneBaseURL(tc.base, ResidencyEU, tc.mtls)
			if tc.want == "" {
				require.ErrorContains(t, err, "conflicts")
				require.Nil(t, endpoint)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, endpoint.String())
		})
	}
}

func TestResidencyLegacyEndpointsRemainUnchanged(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		base string
		mtls bool
		want string
	}{
		{"", false, "https://api.openai.com"},
		{"", true, "https://mtls.api.openai.com"},
		{"https://api.openai.com/", true, "https://mtls.api.openai.com"},
		{"http://127.0.0.1:8000", false, "http://127.0.0.1:8000"},
		{"https://private.example", true, "https://private.example"},
		{"https://eu.api.openai.com", true, "https://eu.api.openai.com"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			endpoint, err := ResolveControlPlaneBaseURL(tc.base, "", tc.mtls)
			require.NoError(t, err)
			require.Equal(t, tc.want, endpoint.String())
		})
	}
}

func TestResidencyConfigurationPrecedenceAcrossFlavors(t *testing.T) {
	t.Parallel()
	for _, flavor := range []Flavor{FlavorFull, FlavorRuntime, FlavorRuntimeCloudflared} {
		t.Run(string(flavor), func(t *testing.T) {
			t.Parallel()
			cert, key := writeTempClientCertPair(t)
			profile := writeTempConfigFile(t, `config_version: 1
control_plane:
  residency: eu
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
mcp:
  server_urls:
    - channel: main
      url: https://mcp.example/mcp
`)
			for _, tc := range []struct {
				name  string
				flags []string
				env   map[string]string
				want  string
			}{
				{name: "yaml", want: "eu.api.openai.com"},
				{name: "environment", env: map[string]string{"CONTROL_PLANE_RESIDENCY": "us"}, want: "us.api.openai.com"},
				{name: "flags", flags: []string{"--control-plane.residency=global"}, env: map[string]string{"CONTROL_PLANE_RESIDENCY": "us"}, want: "api.openai.com"},
				{name: "mtls", flags: []string{"--control-plane.client-cert=file:" + cert, "--control-plane.client-key=file:" + key}, want: "mtls-eu.api.openai.com"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					env := map[string]string{"CONTROL_PLANE_API_KEY": "test-key"}
					maps.Copy(env, tc.env)
					cfg, err := Load(append([]string{"--profile-file", profile}, tc.flags...), flavor, lookupEnvMap(env))
					require.NoError(t, err)
					require.Equal(t, "https://"+tc.want, cfg.ControlPlane.BaseURL.String())
					require.NotEmpty(t, cfg.ControlPlane.Residency)
					require.Nil(t, cfg.MCP.ClientCertificate, "control-plane TLS identity must not become an MCP default")
				})
			}
		})
	}
}

func TestResidencyConfigurationRejectsEmptyAndConflictingOverrides(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		env  map[string]string
		want string
	}{
		{args: []string{"--control-plane.residency="}, want: "control-plane.residency"},
		{env: map[string]string{"CONTROL_PLANE_RESIDENCY": ""}, want: "control-plane.residency"},
		{args: []string{"--control-plane.residency=eu", "--control-plane.base-url=https://api.openai.com"}, want: "conflicts"},
		{args: []string{"--control-plane.residency=eu"}, env: map[string]string{"CONTROL_PLANE_BASE_URL": "https://us.api.openai.com"}, want: "conflicts"},
	} {
		t.Run(strings.Join(tc.args, " ")+tc.want, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{"CONTROL_PLANE_TUNNEL_ID": testTunnelID, "CONTROL_PLANE_API_KEY": testAPIKey, "MCP_SERVER_URL": "https://mcp.example/mcp"}
			maps.Copy(env, tc.env)
			_, err := Load(tc.args, FlavorRuntime, lookupEnvMap(env))
			require.ErrorContains(t, err, tc.want)
		})
	}
}
