package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRuntimeOAuthOriginCompatibility(t *testing.T) {
	runtimeSkipUnixSignals(t)
	t.Parallel()

	subjects := runtimeSubjectsWithBinaries(t, runtimeFullSubject(), runtimeCustomerSubject(), runtimeCloudflaredSubject())
	for _, tc := range []struct {
		name          string
		separateHosts bool
		trustMetadata bool
		trustIssuer   bool
		useYAML       bool
	}{
		{name: "legacy_same_origin_without_new_options"},
		{name: "separate_origins_blocked_by_default", separateHosts: true},
		{name: "trusted_metadata_cannot_grant_issuer_trust", separateHosts: true, trustMetadata: true},
		{name: "separate_origins_trusted_by_flags", separateHosts: true, trustMetadata: true, trustIssuer: true},
		{name: "separate_origins_trusted_by_yaml", separateHosts: true, trustMetadata: true, trustIssuer: true, useYAML: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			observations := runRuntimeScenario(t, subjects, func(t *testing.T) runtimeScenario {
				profilePath, healthURLFile, pidFile := writeRuntimeCompatibilityProfile(t)
				var metadataHits, issuerHits, fallbackHits atomic.Int32
				return runtimeScenario{
					name:          tc.name,
					profilePath:   profilePath,
					healthURLFile: healthURLFile,
					pidFile:       pidFile,
					options: runtimeRunOptions{
						configure: func(t *testing.T, run *runtimeSubjectRun) {
							mcp := httptest.NewUnstartedServer(nil)
							t.Cleanup(mcp.Close)
							mcpOrigin := "http://" + mcp.Listener.Addr().String()
							metadataOrigin, issuerOrigin := mcpOrigin, mcpOrigin
							var metadata, issuer *httptest.Server
							if tc.separateHosts {
								metadata = httptest.NewUnstartedServer(nil)
								t.Cleanup(metadata.Close)
								metadataOrigin = "http://" + metadata.Listener.Addr().String()
								issuer = httptest.NewUnstartedServer(nil)
								t.Cleanup(issuer.Close)
								issuerOrigin = "http://" + issuer.Listener.Addr().String()
							}
							metadataHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
								metadataHits.Add(1)
								if r.Method != http.MethodGet || r.URL.Path != "/metadata" {
									http.NotFound(w, r)
									return
								}
								w.Header().Set("Content-Type", "application/json")
								_ = json.NewEncoder(w).Encode(map[string]any{
									"resource":              mcpOrigin,
									"authorization_servers": []string{issuerOrigin + "/issuer"},
								})
							})
							issuerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
								issuerHits.Add(1)
								if r.Method != http.MethodGet || r.URL.Path != "/issuer/.well-known/oauth-authorization-server" {
									http.NotFound(w, r)
									return
								}
								w.Header().Set("Content-Type", "application/json")
								_ = json.NewEncoder(w).Encode(map[string]any{
									"issuer":                 issuerOrigin + "/issuer",
									"authorization_endpoint": issuerOrigin + "/authorize",
									"token_endpoint":         issuerOrigin + "/token",
								})
							})
							backend := httputil.NewSingleHostReverseProxy(run.fixture.mcpServer.BaseURL())
							mcp.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
								switch {
								case r.URL.Path == "/" && r.Method == http.MethodPost && r.ContentLength == 0:
									w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/metadata"`, metadataOrigin))
									w.WriteHeader(http.StatusUnauthorized)
								case r.URL.Path == "/metadata":
									metadataHandler.ServeHTTP(w, r)
								case r.URL.Path == "/issuer/.well-known/oauth-authorization-server":
									issuerHandler.ServeHTTP(w, r)
								case r.URL.Path == "/.well-known/oauth-protected-resource":
									fallbackHits.Add(1)
									w.Header().Set("Content-Type", "application/json")
									_ = json.NewEncoder(w).Encode(map[string]any{
										"resource":              mcpOrigin,
										"authorization_servers": []string{issuerOrigin + "/issuer"},
									})
								default:
									backend.ServeHTTP(w, r)
								}
							})
							if tc.separateHosts {
								metadata.Config.Handler, issuer.Config.Handler = metadataHandler, issuerHandler
								metadata.Start()
								issuer.Start()
							}
							mcp.Start()
							run.args = append(run.args, "--mcp.server-url", mcpOrigin)

							var trusted []string
							if tc.trustMetadata {
								trusted = append(trusted, metadataOrigin)
							}
							if tc.trustIssuer {
								trusted = append(trusted, issuerOrigin)
							}
							if tc.useYAML {
								addRuntimeOAuthTrustedOriginsToProfile(t, profilePath, trusted)
							} else {
								for _, origin := range trusted {
									run.args = append(run.args, "--mcp.oauth-trusted-origin", origin)
								}
							}
						},
						afterShutdown: func(t *testing.T, _ *runtimeSubjectRun) {
							// The harness waits for completed OAuth discovery and process
							// shutdown, so zero-request assertions need no timing delay.
							if !tc.separateHosts || tc.trustMetadata {
								require.EqualValues(t, 1, metadataHits.Load(), "advertised metadata should be fetched exactly once")
								require.Zero(t, fallbackHits.Load(), "successful advertised metadata should skip the fallback")
							} else {
								require.Zero(t, metadataHits.Load(), "untrusted metadata must not receive a request")
								require.EqualValues(t, 1, fallbackHits.Load(), "legacy same-origin fallback should still succeed")
							}
							if !tc.separateHosts || tc.trustIssuer {
								require.EqualValues(t, 1, issuerHits.Load(), "trusted issuer metadata should be fetched exactly once")
							} else {
								require.Zero(t, issuerHits.Load(), "metadata must not authorize an untrusted issuer")
							}
						},
					},
				}
			})
			for _, name := range []string{"runtime", "runtime-cloudflared"} {
				assertRuntimeParity(t, map[string]runtimeObservation{"full": observations["full"], name: observations[name]}, name)
			}
		})
	}
}

func addRuntimeOAuthTrustedOriginsToProfile(t *testing.T, path string, origins []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var profile map[string]any
	require.NoError(t, yaml.Unmarshal(data, &profile))
	profile["mcp"].(map[string]any)["oauth_trusted_origins"] = origins
	data, err = yaml.Marshal(profile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
}
