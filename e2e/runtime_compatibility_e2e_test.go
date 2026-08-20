package e2e_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/testsupport/mockmcpserver"
	"github.com/openai/tunnel-client/testsupport/mockproxy"
)

// TestRuntimeCompatibilityMatchesFullClientSharedSurface launches the real
// complete client and customer runtime through the same profile-file and
// environment path. It compares only behavior that the runtime promises to
// retain; /ui remains a deliberate full-client-only surface.
func TestRuntimeCompatibilityMatchesFullClientSharedSurface(t *testing.T) {
	runtimeSkipUnixSignals(t)

	subjects := runtimeSubjectsWithBinaries(t, runtimeFullSubject(), runtimeCustomerSubject())
	profilePath, healthURLFile, pidFile := writeRuntimeCompatibilityProfile(t)
	observations := runRuntimeScenario(t, runtimeScenario{
		name:          "shared-surface",
		subjects:      subjects,
		profilePath:   profilePath,
		healthURLFile: healthURLFile,
		pidFile:       pidFile,
	})
	assertRuntimeParity(t, observations, "runtime")
}

// TestRuntimeCloudflaredCompatibilityMatchesFullClientSharedSurface applies
// the same production profile bytes to the full client and approved companion
// runtime while enabling the same deterministic bundled-cloudflared wrapper.
func TestRuntimeCloudflaredCompatibilityMatchesFullClientSharedSurface(t *testing.T) {
	runtimeSkipUnixSignals(t)

	profilePath, healthURLFile, pidFile := writeRuntimeCompatibilityProfile(t)
	subjects := runtimeSubjectsWithBinaries(t, runtimeFullSubject(), runtimeCloudflaredSubject())
	scenario := withRuntimeCloudflaredCompanion(t, runtimeScenario{
		name:          "cloudflared-companion",
		subjects:      subjects,
		profilePath:   profilePath,
		healthURLFile: healthURLFile,
		pidFile:       pidFile,
	})
	observations := runRuntimeScenario(t, scenario)
	assertRuntimeParity(t, observations, "runtime-cloudflared")
}

type runtimeParityTarget struct {
	subject  runtimeSubject
	decorate func(*testing.T, runtimeScenario) runtimeScenario
}

// withRuntimeCloudflaredCompanion layers the same deterministic companion
// fixture over any shared runtime scenario. This keeps cloudflared parity on
// the same Subject/Scenario/Observation path instead of maintaining a second
// copy of the nontrivial corpus.
func withRuntimeCloudflaredCompanion(t *testing.T, scenario runtimeScenario) runtimeScenario {
	t.Helper()

	cloudflaredPath := writeRuntimeArtifactCloudflaredWrapper(t)
	markerDir := t.TempDir()
	startedFile := filepath.Join(markerDir, "cloudflared.started")
	exitFile := filepath.Join(markerDir, "cloudflared.exit")
	signalFile := filepath.Join(markerDir, "cloudflared.signal")

	options := &scenario.options
	options.env = copyRuntimeEnvironment(options.env)
	for key, value := range map[string]string{
		"CLOUDFLARED_PATH":                   cloudflaredPath,
		"CLOUDFLARED_TUNNEL_TOKEN":           "runtime-artifact-cloudflared-token",
		"GO_WANT_RUNTIME_CLOUDFLARED_HELPER": "1",
		"RUNTIME_CLOUDFLARED_EXIT_FILE":      exitFile,
		"RUNTIME_CLOUDFLARED_SIGNAL_FILE":    signalFile,
		"RUNTIME_CLOUDFLARED_STARTED_FILE":   startedFile,
		"RUNTIME_E2E_HELPER_BINARY":          os.Args[0],
	} {
		options.env[key] = value
	}

	if options.readinessSignals == nil {
		options.readinessSignals = []string{
			runtimeArtifactMCPReadySignal,
			runtimeArtifactOAuthReadySignal,
		}
	} else {
		options.readinessSignals = append([]string(nil), options.readinessSignals...)
	}
	options.readinessSignals = append(options.readinessSignals, runtimeArtifactCloudflaredReadySignal)

	configure := options.configure
	options.configure = func(t *testing.T, run *runtimeSubjectRun) {
		if configure != nil {
			configure(t, run)
		}
		_ = os.Remove(startedFile)
		_ = os.Remove(exitFile)
		_ = os.Remove(signalFile)
	}
	afterReady := options.afterReady
	options.afterReady = func(t *testing.T, run *runtimeSubjectRun) {
		if afterReady != nil {
			afterReady(t, run)
		}
		_, err := os.Stat(startedFile)
		require.NoError(t, err, "fake cloudflared startup marker should exist after readiness")
	}
	afterShutdown := options.afterShutdown
	options.afterShutdown = func(t *testing.T, run *runtimeSubjectRun) {
		if afterShutdown != nil {
			afterShutdown(t, run)
		}
		_, err := os.Stat(signalFile)
		require.NoError(t, err, "fake cloudflared should receive the host shutdown signal")
	}

	return scenario
}

// TestRuntimeCompatibilityParityScenarios is deliberately table driven: every
// case runs both runtime flavors against the real full client through the same
// Subject / Scenario / Observation harness, so adding a new customer contract
// does not require another process runner or cloudflared-only corpus copy.
func TestRuntimeCompatibilityParityScenarios(t *testing.T) {
	runtimeSkipUnixSignals(t)

	subjects := runtimeSubjectsWithBinaries(
		t,
		runtimeFullSubject(),
		runtimeCustomerSubject(),
		runtimeCloudflaredSubject(),
	)
	fullSubject := subjects[0]
	targets := []runtimeParityTarget{
		{subject: subjects[1]},
		{subject: subjects[2], decorate: withRuntimeCloudflaredCompanion},
	}
	testCases := []struct {
		name     string
		scenario func(*testing.T) runtimeScenario
		assert   func(*testing.T, map[string]runtimeObservation, string)
	}{
		{
			name: "mixed_profile_environment_and_flags",
			scenario: func(t *testing.T) runtimeScenario {
				profilePath, healthURLFile, pidFile := writeRuntimeCompatibilityProfileWithValues(
					t,
					"http://profile-control.invalid",
					"http://profile-mcp.invalid",
					"tunnel_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				)
				return runtimeScenario{
					name:          "mixed-sources",
					profilePath:   profilePath,
					healthURLFile: healthURLFile,
					pidFile:       pidFile,
					options: runtimeRunOptions{
						configure: func(_ *testing.T, run *runtimeSubjectRun) {
							run.args = append(
								run.args,
								"--control-plane.base-url", run.fixture.controlPlane.BaseURL().String(),
								"--control-plane.tunnel-id", runtimeArtifactTunnelID,
								"--mcp.server-url", run.fixture.mcpServer.BaseURL().String(),
							)
						},
					},
				}
			},
		},
		{
			name: "proxy",
			scenario: func(t *testing.T) runtimeScenario {
				profilePath, healthURLFile, pidFile := writeRuntimeCompatibilityProfile(t)
				return runtimeScenario{
					name:          "proxy",
					profilePath:   profilePath,
					healthURLFile: healthURLFile,
					pidFile:       pidFile,
					options: runtimeRunOptions{
						fixtureFactory: func(t *testing.T) runtimeFixture {
							// Use TLS for MCP so the proxy exercises a real
							// CONNECT tunnel; HTTP streamable responses remain
							// unbuffered end-to-end and startup can become ready.
							fixture := newRuntimeFixture(t, mockmcpserver.WithTLSServer())
							proxy := mockproxy.New(
								mockproxy.WithRoute(fixture.controlPlane.BaseURL().Host, fixture.controlPlane.BaseURL()),
								mockproxy.WithRoute(fixture.mcpServer.BaseURL().Host, fixture.mcpServer.BaseURL()),
							)
							proxy.Start()
							t.Cleanup(proxy.Close)
							fixture.proxy = proxy
							return fixture
						},
						configure: func(t *testing.T, run *runtimeSubjectRun) {
							caPEM, err := run.fixture.mcpServer.TLSCertPEM()
							require.NoError(t, err)
							caPath := filepath.Join(t.TempDir(), run.subject.name+"-proxy-ca.pem")
							require.NoError(t, os.WriteFile(caPath, caPEM, 0o600))
							run.args = append(
								run.args,
								"--http-proxy", run.fixture.proxy.URL(),
								"--ca-bundle", caPath,
							)
						},
					},
				}
			},
			assert: func(t *testing.T, observations map[string]runtimeObservation, runtimeName string) {
				for _, subjectName := range []string{"full", runtimeName} {
					require.NotEmptyf(t, observations[subjectName].shared.proxyHTTP, "%s proxy scenario should route through the proxy", subjectName)
					require.Truef(t, runtimeProxySawRoute(observations[subjectName].shared.proxyHTTP, "control-plane"), "%s should proxy control-plane traffic", subjectName)
					require.Truef(t, runtimeProxySawRoute(observations[subjectName].shared.proxyHTTP, "mcp"), "%s should proxy MCP traffic", subjectName)
				}
			},
		},
		{
			name: "no_proxy",
			scenario: func(t *testing.T) runtimeScenario {
				profilePath, healthURLFile, pidFile := writeRuntimeCompatibilityProfile(t)
				return runtimeScenario{
					name:          "no-proxy",
					profilePath:   profilePath,
					healthURLFile: healthURLFile,
					pidFile:       pidFile,
					options: runtimeRunOptions{
						fixtureFactory: func(t *testing.T) runtimeFixture {
							fixture := newRuntimeFixture(t)
							proxy := mockproxy.New()
							proxy.Start()
							t.Cleanup(proxy.Close)
							fixture.proxy = proxy
							return fixture
						},
						configure: func(_ *testing.T, run *runtimeSubjectRun) {
							run.env["HTTP_PROXY"] = run.fixture.proxy.URL()
							run.env["http_proxy"] = run.fixture.proxy.URL()
							run.env["HTTPS_PROXY"] = run.fixture.proxy.URL()
							run.env["https_proxy"] = run.fixture.proxy.URL()
							run.env["NO_PROXY"] = "127.0.0.1,localhost"
							run.env["no_proxy"] = "127.0.0.1,localhost"
						},
					},
				}
			},
			assert: func(t *testing.T, observations map[string]runtimeObservation, runtimeName string) {
				require.Emptyf(t, observations[runtimeName].shared.proxyHTTP, "%s NO_PROXY should keep loopback fixtures direct", runtimeName)
				for _, observation := range observations["full"].shared.proxyHTTP {
					require.Equal(
						t,
						http.MethodConnect,
						observation.method,
						"full-only proxy health may probe the configured proxy, but shared NO_PROXY traffic must stay direct",
					)
				}
			},
		},
		{
			name: "tls_ca_bundle",
			scenario: func(t *testing.T) runtimeScenario {
				profilePath, healthURLFile, pidFile := writeRuntimeCompatibilityProfile(t)
				return runtimeScenario{
					name:          "tls-ca-bundle",
					profilePath:   profilePath,
					healthURLFile: healthURLFile,
					pidFile:       pidFile,
					options: runtimeRunOptions{
						fixtureFactory: func(t *testing.T) runtimeFixture {
							return newRuntimeFixture(t, mockmcpserver.WithTLSServer())
						},
						configure: func(t *testing.T, run *runtimeSubjectRun) {
							caPEM, err := run.fixture.mcpServer.TLSCertPEM()
							require.NoError(t, err)
							caPath := filepath.Join(t.TempDir(), run.subject.name+"-ca.pem")
							require.NoError(t, os.WriteFile(caPath, caPEM, 0o600))
							run.args = append(run.args, "--ca-bundle", caPath)
						},
					},
				}
			},
		},
		{
			name: "oauth_and_static_headers",
			scenario: func(t *testing.T) runtimeScenario {
				profilePath, healthURLFile, pidFile := writeRuntimeCompatibilityProfile(t)
				return runtimeScenario{
					name:          "oauth-headers",
					profilePath:   profilePath,
					healthURLFile: healthURLFile,
					pidFile:       pidFile,
					options: runtimeRunOptions{
						env: map[string]string{
							"RUNTIME_CONTROL_HEADER":   "control-static-value",
							"RUNTIME_MCP_HEADER":       "mcp-static-value",
							"RUNTIME_DISCOVERY_HEADER": "discovery-static-value",
						},
						args: []string{
							"--control-plane.extra-headers", "X-Control-Static: env:RUNTIME_CONTROL_HEADER",
							"--mcp.extra-headers", "X-MCP-Static: env:RUNTIME_MCP_HEADER",
							"--mcp.discovery-extra-headers", "X-Discovery-Static: env:RUNTIME_DISCOVERY_HEADER",
						},
						secrets: []string{
							"control-static-value",
							"mcp-static-value",
							"discovery-static-value",
						},
					},
				}
			},
			assert: func(t *testing.T, observations map[string]runtimeObservation, _ string) {
				full := observations["full"].shared
				require.True(t, runtimeHTTPHasControlHeader(full.controlPlaneHTTP, "control-static-value"))
				require.True(t, runtimeHTTPHasMCPHeader(full.mcpHTTP, http.MethodPost, "mcp-static-value"))
				require.True(t, runtimeHTTPHasDiscoveryHeader(full.mcpHTTP, "discovery-static-value"))
			},
		},
		{
			name: "transient_poll_failure_reconnect",
			scenario: func(t *testing.T) runtimeScenario {
				profilePath, healthURLFile, pidFile := writeRuntimeCompatibilityProfile(t)
				return runtimeScenario{
					name:          "transient-poll",
					profilePath:   profilePath,
					healthURLFile: healthURLFile,
					pidFile:       pidFile,
					options: runtimeRunOptions{
						configure: func(_ *testing.T, run *runtimeSubjectRun) {
							run.fixture.controlPlane.SetPollStatus(http.StatusServiceUnavailable)
						},
						afterStart: func(t *testing.T, run *runtimeSubjectRun) {
							ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
							defer cancel()
							err := run.fixture.controlPlane.WaitForPollFailures(ctx, 1)
							require.NoError(t, err, "runtime should receive an injected poll failure before recovery")
							run.fixture.controlPlane.SetPollStatus(0)
						},
					},
				}
			},
			assert: func(t *testing.T, observations map[string]runtimeObservation, runtimeName string) {
				require.GreaterOrEqual(t, observations["full"].pollFailures, 1)
				require.GreaterOrEqual(t, observations[runtimeName].pollFailures, 1)
			},
		},
		{
			name: "invalid_config_redaction",
			scenario: func(t *testing.T) runtimeScenario {
				profilePath, healthURLFile, pidFile := writeRuntimeCompatibilityProfileWithValues(
					t,
					"http://127.0.0.1:1",
					"http://127.0.0.1:1",
					runtimeArtifactTunnelID,
				)
				const secret = "runtime malformed secret"
				return runtimeScenario{
					name:          "invalid-config-redaction",
					profilePath:   profilePath,
					healthURLFile: healthURLFile,
					pidFile:       pidFile,
					options: runtimeRunOptions{
						fixtureFactory:       func(*testing.T) runtimeFixture { return runtimeFixture{} },
						env:                  map[string]string{"CONTROL_PLANE_API_KEY": secret},
						expectStartupFailure: true,
						failureContains:      "control plane API key is malformed",
						secrets:              []string{secret},
					},
				}
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			for _, target := range targets {
				target := target
				t.Run(target.subject.name, func(t *testing.T) {
					scenario := testCase.scenario(t)
					scenario.subjects = []runtimeSubject{fullSubject, target.subject}
					if target.decorate != nil {
						scenario = target.decorate(t, scenario)
					}
					observations := runRuntimeScenario(t, scenario)
					assertRuntimeParity(t, observations, target.subject.name)
					if testCase.assert != nil {
						testCase.assert(t, observations, target.subject.name)
					}
				})
			}
		})
	}
}

func runtimeProxySawRoute(observations []runtimeProxyObservation, route string) bool {
	for _, observation := range observations {
		if observation.route == route {
			return true
		}
	}
	return false
}

func runtimeHTTPHasControlHeader(observations []runtimeHTTPObservation, want string) bool {
	for _, observation := range observations {
		if observation.controlHeader == want {
			return true
		}
	}
	return false
}

func runtimeHTTPHasMCPHeader(observations []runtimeHTTPObservation, method, want string) bool {
	for _, observation := range observations {
		if observation.method == method && observation.mcpHeader == want {
			return true
		}
	}
	return false
}

func runtimeHTTPHasDiscoveryHeader(observations []runtimeHTTPObservation, want string) bool {
	for _, observation := range observations {
		if observation.path == "/.well-known/oauth-protected-resource" && observation.discoveryHeader == want {
			return true
		}
	}
	return false
}
