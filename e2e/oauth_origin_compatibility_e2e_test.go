package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

type runtimeOAuthProbeRecord struct {
	ProbeID           string     `json:"probe_id"`
	Origin            string     `json:"origin"`
	ProbeType         string     `json:"probe_type"`
	Method            string     `json:"method"`
	StartedAt         time.Time  `json:"started_at"`
	ResponseAt        *time.Time `json:"response_at"`
	StatusCode        *int       `json:"status_code"`
	CompletedAt       *time.Time `json:"completed_at"`
	Disposition       string     `json:"disposition"`
	ResponseRequestID *string    `json:"response_request_id"`
}

type runtimeOAuthProbeDetails struct {
	DiscoveryComplete bool                      `json:"discovery_complete"`
	ActiveProbes      []runtimeOAuthProbeRecord `json:"active_probes"`
	RecentProbes      []runtimeOAuthProbeRecord `json:"recent_probes"`
}

type runtimeOAuthProbeComponent struct {
	Status     string                   `json:"status"`
	State      string                   `json:"state"`
	ReasonCode string                   `json:"reason_code"`
	Details    runtimeOAuthProbeDetails `json:"details"`
}

// TestRuntimeOAuthStartupProbeDiagnostics exercises the built binaries while a
// valid metadata response is held behind recoverable WWW-Authenticate probes.
func TestRuntimeOAuthStartupProbeDiagnostics(t *testing.T) {
	runtimeSkipUnixSignals(t)
	t.Parallel()

	const (
		discoveryHeader = "X-E2E-Discovery"
		postResponseID  = "probe-post-415"
		getResponseID   = "probe-get-405"
		prmdResponseID  = "probe-prmd-200"
		initResponseID  = "mcp-init-200"
	)
	subjects := runtimeSubjectsWithBinaries(t, runtimeFullSubject(), runtimeCustomerSubject(), runtimeCloudflaredSubject())
	runRuntimeScenario(t, subjects, func(t *testing.T) runtimeScenario {
		profilePath, healthURLFile, pidFile := writeRuntimeCompatibilityProfile(t)
		metadataEntered := make(chan struct{})
		metadataRelease := make(chan struct{})
		var metadataReleased atomic.Bool
		var postHits, getHits, metadataHits, rootHits, initHits, initStatus atomic.Int32
		var pollHits, earlyPolls, metadataCancellations, handlerErrors atomic.Int32
		var activeMetadataProbeID string
		releaseMetadata := func() {
			if metadataReleased.CompareAndSwap(false, true) {
				close(metadataRelease)
			}
		}
		return runtimeScenario{
			name: "oauth-startup-probe-diagnostics", profilePath: profilePath, healthURLFile: healthURLFile, pidFile: pidFile,
			options: runtimeRunOptions{
				configure: func(t *testing.T, run *runtimeSubjectRun) {
					type startupInitializeKey struct{}
					backend := httputil.NewSingleHostReverseProxy(run.fixture.mcpServer.BaseURL())
					backend.ModifyResponse = func(response *http.Response) error {
						if marked, _ := response.Request.Context().Value(startupInitializeKey{}).(bool); marked {
							response.Header.Set("X-Request-Id", initResponseID)
							initStatus.Store(int32(response.StatusCode))
						}
						return nil
					}
					mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						isDiscovery := r.Header.Get(discoveryHeader) == "1"
						switch {
						case r.URL.Path == "/mcp" && r.Method == http.MethodPost && isDiscovery && r.ContentLength == 0:
							postHits.Add(1)
							w.Header().Set("X-Request-Id", postResponseID)
							w.WriteHeader(http.StatusUnsupportedMediaType)
						case r.URL.Path == "/mcp" && r.Method == http.MethodGet && isDiscovery && r.Header.Get("Accept") == "application/json":
							getHits.Add(1)
							w.Header().Set("X-Request-Id", getResponseID)
							w.WriteHeader(http.StatusMethodNotAllowed)
						case r.URL.Path == "/.well-known/oauth-protected-resource/mcp":
							if metadataHits.Add(1) == 1 {
								close(metadataEntered)
							}
							select {
							case <-metadataRelease:
								w.Header().Set("Content-Type", "application/json")
								w.Header().Set("X-Request-Id", prmdResponseID)
								if err := json.NewEncoder(w).Encode(map[string]string{"resource": "http://" + r.Host + "/mcp"}); err != nil {
									handlerErrors.Add(1)
								}
							case <-r.Context().Done():
								metadataCancellations.Add(1)
							}
						case r.URL.Path == "/.well-known/oauth-protected-resource":
							rootHits.Add(1)
							http.NotFound(w, r)
						default:
							if r.URL.Path == "/mcp" && r.Method == http.MethodPost && isDiscovery {
								body, err := io.ReadAll(r.Body)
								if err != nil {
									handlerErrors.Add(1)
									http.Error(w, "fixture request read failed", http.StatusBadRequest)
									return
								}
								r.Body = io.NopCloser(bytes.NewReader(body))
								var message struct {
									Method string `json:"method"`
								}
								if json.Unmarshal(body, &message) == nil && message.Method == "initialize" {
									initHits.Add(1)
									r = r.WithContext(context.WithValue(r.Context(), startupInitializeKey{}, true))
								}
							}
							backend.ServeHTTP(w, r)
						}
					}))
					t.Cleanup(mcp.Close)

					controlBackend := httputil.NewSingleHostReverseProxy(run.fixture.controlPlane.BaseURL())
					control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if strings.HasSuffix(r.URL.Path, "/poll") {
							pollHits.Add(1)
							if !metadataReleased.Load() {
								earlyPolls.Add(1)
							}
						}
						controlBackend.ServeHTTP(w, r)
					}))
					t.Cleanup(control.Close)
					// Release the handler before server cleanup even if an assertion fails.
					t.Cleanup(releaseMetadata)
					run.args = append(run.args,
						"--mcp.server-url", mcp.URL+"/mcp",
						"--mcp.discovery-extra-headers", discoveryHeader+": 1",
						"--control-plane.base-url", control.URL,
					)
				},
				afterStart: func(t *testing.T, run *runtimeSubjectRun) {
					healthURL := waitForRuntimeArtifactHealthURL(t, run.proc, run.healthURLFile)
					ctx, cancel := context.WithTimeout(t.Context(), runtimeArtifactSignalTimeout)
					defer cancel()
					select {
					case <-metadataEntered:
					case <-ctx.Done():
						t.Fatalf("metadata request did not follow the POST and GET probes: %s", run.proc.output.String())
					}
					client := &http.Client{Timeout: 2 * time.Second}
					defer client.CloseIdleConnections()
					status, body := runtimeArtifactResponse(t, client, healthURL+"/health?details=true")
					require.Equal(t, http.StatusOK, status, body)
					var snapshot struct {
						SchemaVersion int  `json:"schema_version"`
						Live          bool `json:"live"`
						Ready         bool `json:"ready"`
						Components    struct {
							OAuth   runtimeOAuthProbeComponent `json:"oauth"`
							Harpoon struct {
								State   string `json:"state"`
								Details struct {
									CatalogState string `json:"catalog_state"`
								} `json:"details"`
							} `json:"harpoon"`
						} `json:"components"`
					}
					require.NoError(t, json.Unmarshal([]byte(body), &snapshot))
					require.Equal(t, 1, snapshot.SchemaVersion)
					require.True(t, snapshot.Live)
					require.False(t, snapshot.Ready)
					require.Equal(t, "pending", snapshot.Components.OAuth.State)
					require.False(t, snapshot.Components.OAuth.Details.DiscoveryComplete)
					require.Equal(t, "pending", snapshot.Components.Harpoon.State)
					require.Equal(t, "pending", snapshot.Components.Harpoon.Details.CatalogState)
					require.Len(t, snapshot.Components.OAuth.Details.RecentProbes, 2)
					post := requireRuntimeOAuthCompletedProbe(t, snapshot.Components.OAuth.Details.RecentProbes, postResponseID, "www_authenticate", http.MethodPost, http.StatusUnsupportedMediaType, "fallback")
					get := requireRuntimeOAuthCompletedProbe(t, snapshot.Components.OAuth.Details.RecentProbes, getResponseID, "www_authenticate", http.MethodGet, http.StatusMethodNotAllowed, "fallback")
					require.NotEqual(t, post.ProbeID, get.ProbeID)
					require.Len(t, snapshot.Components.OAuth.Details.ActiveProbes, 1)
					active := snapshot.Components.OAuth.Details.ActiveProbes[0]
					require.NotEmpty(t, active.ProbeID)
					require.NotEqual(t, post.ProbeID, active.ProbeID)
					require.NotEqual(t, get.ProbeID, active.ProbeID)
					require.Equal(t, "startup", active.Origin)
					require.Equal(t, "protected_resource_metadata", active.ProbeType)
					require.Equal(t, http.MethodGet, active.Method)
					require.False(t, active.StartedAt.IsZero())
					require.Nil(t, active.ResponseAt)
					require.Nil(t, active.StatusCode)
					require.Nil(t, active.CompletedAt)
					require.Equal(t, "pending", active.Disposition)
					require.Nil(t, active.ResponseRequestID)
					activeMetadataProbeID = active.ProbeID
					require.Empty(t, run.fixture.controlPlane.DeliveredCommands())
					require.Zero(t, earlyPolls.Load())
					// The shared WWW-Authenticate budget is one second. Hold only the
					// metadata request and release it immediately after this snapshot.
					releaseMetadata()
				},
				afterReady: func(t *testing.T, run *runtimeSubjectRun) {
					healthURL := waitForRuntimeArtifactHealthURL(t, run.proc, run.healthURLFile)
					client := &http.Client{Timeout: 2 * time.Second}
					defer client.CloseIdleConnections()
					component := readRuntimeOAuthProbeComponent(t, client, healthURL)
					require.Equal(t, "ok", component.Status)
					require.Equal(t, "complete", component.State)
					require.True(t, component.Details.DiscoveryComplete)
					require.Empty(t, component.Details.ActiveProbes)
					require.Len(t, component.Details.RecentProbes, 3)
					post := requireRuntimeOAuthCompletedProbe(t, component.Details.RecentProbes, postResponseID, "www_authenticate", http.MethodPost, http.StatusUnsupportedMediaType, "fallback")
					get := requireRuntimeOAuthCompletedProbe(t, component.Details.RecentProbes, getResponseID, "www_authenticate", http.MethodGet, http.StatusMethodNotAllowed, "fallback")
					metadata := requireRuntimeOAuthCompletedProbe(t, component.Details.RecentProbes, prmdResponseID, "protected_resource_metadata", http.MethodGet, http.StatusOK, "accepted")
					require.Equal(t, activeMetadataProbeID, metadata.ProbeID)
					require.NotEqual(t, post.ProbeID, get.ProbeID)
					require.NotEqual(t, post.ProbeID, metadata.ProbeID)
					require.NotEqual(t, get.ProbeID, metadata.ProbeID)
					for _, probe := range component.Details.RecentProbes {
						if probe.ResponseRequestID != nil {
							require.NotEqual(t, initResponseID, *probe.ResponseRequestID, "MCP initialization is separate from OAuth discovery")
						}
					}
					status, body := runtimeArtifactResponse(t, client, healthURL+"/health/harpoon")
					require.Equal(t, http.StatusOK, status, body)
					var harpoon struct {
						State   string `json:"state"`
						Details struct {
							CatalogState string `json:"catalog_state"`
						} `json:"details"`
					}
					require.NoError(t, json.Unmarshal([]byte(body), &harpoon))
					require.Equal(t, "settled", harpoon.State)
					require.Equal(t, "settled", harpoon.Details.CatalogState)
				},
				afterShutdown: func(t *testing.T, _ *runtimeSubjectRun) {
					require.EqualValues(t, 1, postHits.Load())
					require.EqualValues(t, 1, getHits.Load())
					require.EqualValues(t, 1, metadataHits.Load())
					require.Zero(t, rootHits.Load(), "accepted path metadata must skip the root fallback")
					require.EqualValues(t, 1, initHits.Load())
					require.EqualValues(t, http.StatusOK, initStatus.Load())
					require.Positive(t, pollHits.Load())
					require.Zero(t, earlyPolls.Load(), "polling must wait until the metadata response is released")
					require.Zero(t, metadataCancellations.Load(), "the fixture must complete within the real discovery deadline")
					require.Zero(t, handlerErrors.Load())
				},
			},
		}
	})
}

func readRuntimeOAuthProbeComponent(t *testing.T, client *http.Client, healthURL string) runtimeOAuthProbeComponent {
	t.Helper()
	status, body := runtimeArtifactResponse(t, client, healthURL+"/health/oauth")
	require.Equal(t, http.StatusOK, status, body)
	var response struct {
		SchemaVersion int    `json:"schema_version"`
		Component     string `json:"component"`
		runtimeOAuthProbeComponent
	}
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	require.Equal(t, 1, response.SchemaVersion)
	require.Equal(t, "oauth", response.Component)
	return response.runtimeOAuthProbeComponent
}

func requireRuntimeOAuthCompletedProbe(t *testing.T, probes []runtimeOAuthProbeRecord, responseID, probeType, method string, status int, disposition string) runtimeOAuthProbeRecord {
	t.Helper()
	var matched []runtimeOAuthProbeRecord
	for _, probe := range probes {
		if probe.ResponseRequestID != nil && *probe.ResponseRequestID == responseID {
			matched = append(matched, probe)
		}
	}
	require.Len(t, matched, 1, "response ID %s must identify one completed probe", responseID)
	probe := matched[0]
	require.NotEmpty(t, probe.ProbeID)
	require.Equal(t, "startup", probe.Origin)
	require.Equal(t, probeType, probe.ProbeType)
	require.Equal(t, method, probe.Method)
	require.False(t, probe.StartedAt.IsZero())
	require.NotNil(t, probe.ResponseAt)
	require.False(t, probe.ResponseAt.IsZero())
	require.NotNil(t, probe.StatusCode)
	require.Equal(t, status, *probe.StatusCode)
	require.NotNil(t, probe.CompletedAt)
	require.False(t, probe.CompletedAt.IsZero())
	require.Equal(t, disposition, probe.Disposition)
	return probe
}

func TestRuntimeOAuthDiscoveryRecoversAfterMissingPathAndRootTimeout(t *testing.T) {
	runtimeSkipUnixSignals(t)
	t.Parallel()

	subjects := runtimeSubjectsWithBinaries(t, runtimeFullSubject(), runtimeCustomerSubject(), runtimeCloudflaredSubject())
	for _, tc := range []struct {
		name        string
		bodyTimeout bool
	}{
		{name: "request_timeout"},
		{name: "body_timeout", bodyTimeout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			runRuntimeScenario(t, subjects, func(t *testing.T) runtimeScenario {
				profilePath, healthURLFile, pidFile := writeRuntimeCompatibilityProfile(t)
				firstTimeout := make(chan struct{})
				recoverMetadata := make(chan struct{})
				var pathHits, rootHits atomic.Int32
				return runtimeScenario{
					name: tc.name, profilePath: profilePath, healthURLFile: healthURLFile, pidFile: pidFile,
					options: runtimeRunOptions{
						configure: func(t *testing.T, run *runtimeSubjectRun) {
							backend := httputil.NewSingleHostReverseProxy(run.fixture.mcpServer.BaseURL())
							mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
								switch r.URL.Path {
								case "/.well-known/oauth-protected-resource/services/mcp":
									pathHits.Add(1)
									http.NotFound(w, r)
								case "/.well-known/oauth-protected-resource":
									if rootHits.Add(1) == 1 {
										if tc.bodyTimeout {
											w.Header().Set("Content-Type", "application/json")
											w.WriteHeader(http.StatusOK)
											w.(http.Flusher).Flush()
										}
										// Exercise the runtime's real discovery deadline. The
										// handler waits for cancellation instead of a timer.
										<-r.Context().Done()
										close(firstTimeout)
										return
									}
									select {
									case <-recoverMetadata:
										w.Header().Set("Content-Type", "application/json")
										_ = json.NewEncoder(w).Encode(map[string]string{"resource": "http://" + r.Host + "/services/mcp"})
									case <-r.Context().Done():
									}
								default:
									backend.ServeHTTP(w, r)
								}
							}))
							t.Cleanup(mcp.Close)
							run.args = append(run.args, "--mcp.server-url", mcp.URL+"/services/mcp")
						},
						afterStart: func(t *testing.T, run *runtimeSubjectRun) {
							waitForRuntimeArtifactOutput(t, run.proc, "successful MCP initialization", runtimeArtifactMCPReadySignal)
							ctx, cancel := context.WithTimeout(t.Context(), runtimeArtifactSignalTimeout)
							defer cancel()
							select {
							case <-firstTimeout:
							case <-ctx.Done():
								t.Fatalf("root metadata request did not time out: %s", run.proc.output.String())
							}
							require.Positive(t, pathHits.Load(), "discovery must try the path-specific metadata URL before the root fallback")
							healthURL := waitForRuntimeArtifactHealthURL(t, run.proc, run.healthURLFile)
							client := &http.Client{Timeout: 2 * time.Second}
							defer client.CloseIdleConnections()
							status, body := runtimeArtifactResponse(t, client, healthURL+"/readyz")
							require.Equal(t, http.StatusServiceUnavailable, status)
							require.Equal(t, "startup catalog pending", strings.TrimSpace(body))
							for _, request := range run.fixture.controlPlane.ReceivedHTTPRequests() {
								require.False(t, strings.HasSuffix(request.Path, "/poll"), "polling must wait for OAuth discovery to recover")
							}
							require.Empty(t, run.fixture.controlPlane.DeliveredCommands())
							close(recoverMetadata)
						},
						afterReady: func(t *testing.T, run *runtimeSubjectRun) {
							// The shared harness drains initialize, initialized, and
							// tools/call through this same process, then checks /readyz.
							require.Contains(t, run.proc.output.String(), "OAuth discovery failed; retrying")
							require.GreaterOrEqual(t, pathHits.Load(), int32(2))
							require.GreaterOrEqual(t, rootHits.Load(), int32(2))
						},
					},
				}
			})
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
