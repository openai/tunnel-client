package e2e_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/testsupport/mockmcpserver"
	"github.com/openai/tunnel-client/testsupport/mockproxy"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

// runtimeSubject is one real host binary that participates in a compatibility
// scenario. Keeping the binary boundary here is intentional: these tests
// exercise the same argv, environment, profile bytes, and OS signals that a
// customer deployment uses instead of calling package constructors in-process.
type runtimeSubject struct {
	name        string
	packagePath string
	binaryName  string
	flavor      string
	binary      string

	// Signals emitted only after the binary's Fx start path has completed.
	// Full client emits an explicit started event after slower UI startup;
	// runtime entrypoints use a no-op Fx event logger.
	startupSignals []string
}

func runtimeFullSubject() runtimeSubject {
	return runtimeSubject{
		name:           "full",
		packagePath:    "./cmd/client",
		binaryName:     "tunnel-client",
		flavor:         "full",
		startupSignals: []string{"🟢 tunnel-client started"},
	}
}

func runtimeCustomerSubject() runtimeSubject {
	return runtimeSubject{
		name:        "runtime",
		packagePath: "./cmd/client-runtime",
		binaryName:  "tunnel-client-runtime",
		flavor:      "runtime",
	}
}

func runtimeCloudflaredSubject() runtimeSubject {
	return runtimeSubject{
		name:        "runtime-cloudflared",
		packagePath: "./cmd/client-runtime-cloudflared",
		binaryName:  "tunnel-client-runtime-cloudflared",
		flavor:      "runtime-cloudflared",
	}
}

// runtimeSubjectsWithBinaries builds each host binary once for a table of
// scenarios. A subject with binary already set is left alone so callers can
// also pass Bazel runfiles or a prebuilt artifact.
func runtimeSubjectsWithBinaries(t *testing.T, subjects ...runtimeSubject) []runtimeSubject {
	t.Helper()

	out := make([]runtimeSubject, len(subjects))
	copy(out, subjects)
	for i := range out {
		if out[i].binary != "" {
			continue
		}
		out[i].binary = buildRuntimeArtifact(t, out[i].packagePath, out[i].binaryName, out[i].flavor)
	}
	return out
}

type runtimeFixture struct {
	controlPlane *mocktunnelservice.MockTunnelService
	mcpServer    *mockmcpserver.MockMCPServer
	proxy        *mockproxy.ProxyServer
}

func newRuntimeFixture(t *testing.T, mcpOptions ...mockmcpserver.Option) runtimeFixture {
	t.Helper()

	controlPlane, mcpServer := newRuntimeArtifactMocksWithMCPOptions(t, mcpOptions...)
	return runtimeFixture{
		controlPlane: controlPlane,
		mcpServer:    mcpServer,
	}
}

type runtimeScenario struct {
	name          string
	subjects      []runtimeSubject
	profilePath   string
	healthURLFile string
	pidFile       string
	options       runtimeRunOptions
}

// runtimeRunOptions is the scenario adapter surface. fixtureFactory gives each
// binary isolated servers while configure and lifecycle hooks make fault
// injection deterministic without teaching the generic harness about a
// particular scenario.
type runtimeRunOptions struct {
	env                  map[string]string
	args                 []string
	readinessSignals     []string
	fixtureFactory       func(*testing.T) runtimeFixture
	configure            func(*testing.T, *runtimeSubjectRun)
	afterStart           func(*testing.T, *runtimeSubjectRun)
	afterReady           func(*testing.T, *runtimeSubjectRun)
	afterShutdown        func(*testing.T, *runtimeSubjectRun)
	expectedResponses    int
	expectedToolRequests int
	expectStartupFailure bool
	failureContains      string
	secrets              []string
}

type runtimeSubjectRun struct {
	subject       runtimeSubject
	fixture       runtimeFixture
	profilePath   string
	healthURLFile string
	pidFile       string
	env           map[string]string
	args          []string
	proc          *runtimeArtifactProcess
}

type runtimeObservation struct {
	shared           runtimeSharedObservation
	uiStatus         int
	uiBody           string
	output           string
	exitClean        bool
	shutdownDuration time.Duration
	failureKind      string
	pollFailures     int
}

type runtimeSharedObservation struct {
	readyStatus         int
	readyBody           string
	healthStatus        int
	healthBody          string
	metricsStatus       int
	metricsHasLiveness  bool
	metricsHasReadiness bool
	responses           []runtimeResponseObservation
	toolNames           []string
	controlPlaneHTTP    []runtimeHTTPObservation
	mcpHTTP             []runtimeHTTPObservation
	proxyHTTP           []runtimeProxyObservation
}

type runtimeResponseObservation struct {
	requestID    string
	responseCode int
	responseType string
}

// runtimeHTTPObservation intentionally keeps only compatibility-contract
// details. Poll cadence and SDK-generated headers are timing/version noise;
// custom headers, auth presence, method, and path are customer-visible.
type runtimeHTTPObservation struct {
	method           string
	path             string
	rawQuery         string
	controlHeader    string
	mcpHeader        string
	discoveryHeader  string
	hasAuthorization bool
}

type runtimeProxyObservation struct {
	method string
	route  string
}

func runRuntimeScenario(t *testing.T, scenario runtimeScenario) map[string]runtimeObservation {
	t.Helper()

	require.NotEmpty(t, scenario.subjects, "scenario needs at least one subject")
	observations := make(map[string]runtimeObservation, len(scenario.subjects))
	for _, subject := range scenario.subjects {
		subject := subject
		t.Run(subject.name, func(t *testing.T) {
			observations[subject.name] = runRuntimeSubject(t, subject, scenario)
		})
	}
	return observations
}

func runRuntimeSubject(t *testing.T, subject runtimeSubject, scenario runtimeScenario) runtimeObservation {
	t.Helper()

	fixtureFactory := scenario.options.fixtureFactory
	if fixtureFactory == nil {
		fixtureFactory = func(t *testing.T) runtimeFixture {
			return newRuntimeFixture(t)
		}
	}
	fixture := fixtureFactory(t)

	env := copyRuntimeEnvironment(scenario.options.env)
	if scenario.profilePath != "" {
		env["TUNNEL_CLIENT_PROFILE_FILE"] = scenario.profilePath
	}
	if fixture.controlPlane != nil && fixture.controlPlane.BaseURL() != nil {
		env["RUNTIME_COMPAT_CONTROL_PLANE_URL"] = fixture.controlPlane.BaseURL().String()
	}
	if fixture.mcpServer != nil && fixture.mcpServer.BaseURL() != nil {
		env["RUNTIME_COMPAT_MCP_URL"] = fixture.mcpServer.BaseURL().String()
	}

	args := append([]string{"run"}, scenario.options.args...)
	run := &runtimeSubjectRun{
		subject:       subject,
		fixture:       fixture,
		profilePath:   scenario.profilePath,
		healthURLFile: scenario.healthURLFile,
		pidFile:       scenario.pidFile,
		env:           env,
		args:          args,
	}
	if scenario.options.configure != nil {
		scenario.options.configure(t, run)
	}

	binary := subject.binary
	if binary == "" {
		binary = buildRuntimeArtifact(t, subject.packagePath, subject.binaryName, subject.flavor)
	}
	run.proc = startRuntimeArtifactWithEnv(t, binary, run.env, run.args...)

	if scenario.options.expectStartupFailure {
		return observeRuntimeStartupFailure(t, run, scenario.options)
	}

	require.NotNil(t, fixture.controlPlane, "successful runtime scenario needs a control-plane fixture")
	require.NotNil(t, fixture.mcpServer, "successful runtime scenario needs an MCP fixture")

	if scenario.options.afterStart != nil {
		scenario.options.afterStart(t, run)
	}

	healthBaseURL := waitForRuntimeArtifactHealthURL(t, run.proc, run.healthURLFile)
	waitForRuntimeArtifactOutput(t, run.proc, "PID file", runtimeArtifactPIDFileSignal)
	assertRuntimePIDFile(t, run.proc, run.pidFile)
	waitForRuntimeArtifactIdle(t, run.proc, fixture.controlPlane)

	readinessSignals := scenario.options.readinessSignals
	if readinessSignals == nil {
		readinessSignals = []string{
			runtimeArtifactMCPReadySignal,
			runtimeArtifactOAuthReadySignal,
		}
	}
	waitForRuntimeArtifactOutput(t, run.proc, "shared readiness", readinessSignals...)
	if len(subject.startupSignals) > 0 {
		waitForRuntimeArtifactOutput(t, run.proc, "completed startup", subject.startupSignals...)
	}
	if scenario.options.afterReady != nil {
		scenario.options.afterReady(t, run)
	}

	observation := observeRuntimeSubject(t, healthBaseURL, fixture, scenario.options)
	expectedAPIKey := runtimeArtifactAPIKey
	if configured, ok := run.env["CONTROL_PLANE_API_KEY"]; ok {
		expectedAPIKey = configured
	}
	require.True(
		t,
		runtimeControlPlaneSawAPIKey(fixture.controlPlane, expectedAPIKey),
		"control-plane mock never observed the env-resolved API key",
	)
	assertRuntimeOutputRedacted(t, run.proc.output.String(), append([]string{expectedAPIKey, "Bearer " + expectedAPIKey}, scenario.options.secrets...)...)

	startedShutdown := time.Now()
	require.NoError(t, run.proc.cmd.Process.Signal(syscall.SIGTERM))
	waitErr, exited := run.proc.wait(10 * time.Second)
	observation.shutdownDuration = time.Since(startedShutdown)
	observation.exitClean = exited && waitErr == nil
	require.Truef(t, exited, "%s did not exit after its shutdown signal; output:\n%s", subject.name, run.proc.output.String())
	require.NoErrorf(t, waitErr, "%s did not shut down cleanly after its shutdown signal; output:\n%s", subject.name, run.proc.output.String())
	assertRuntimeRemoved(t, run.pidFile, "PID file")
	assertRuntimeRemoved(t, run.healthURLFile, "health URL file")
	if fixture.controlPlane != nil {
		observation.pollFailures = fixture.controlPlane.PollFailures()
	}
	observation.output = run.proc.output.String()

	if scenario.options.afterShutdown != nil {
		scenario.options.afterShutdown(t, run)
	}
	return observation
}

func observeRuntimeStartupFailure(t *testing.T, run *runtimeSubjectRun, options runtimeRunOptions) runtimeObservation {
	t.Helper()

	waitErr, exited := run.proc.wait(10 * time.Second)
	require.Truef(t, exited, "%s did not exit for invalid startup config; output:\n%s", run.subject.name, run.proc.output.String())
	require.Error(t, waitErr, "invalid startup config should exit non-zero")
	output := run.proc.output.String()
	if options.failureContains != "" {
		require.Contains(t, output, options.failureContains)
	}
	assertRuntimeOutputRedacted(t, output, options.secrets...)
	return runtimeObservation{
		output:      output,
		failureKind: options.failureContains,
	}
}

func observeRuntimeSubject(
	t *testing.T,
	healthBaseURL string,
	fixture runtimeFixture,
	options runtimeRunOptions,
) runtimeObservation {
	t.Helper()

	client := &http.Client{Timeout: 2 * time.Second}
	readyStatus, readyBody := runtimeArtifactResponse(t, client, healthBaseURL+"/readyz")
	healthStatus, healthBody := runtimeArtifactResponse(t, client, healthBaseURL+"/healthz")
	metricsStatus, metricsBody := runtimeArtifactResponse(t, client, healthBaseURL+"/metrics")
	uiStatus, uiBody := runtimeArtifactResponse(t, client, healthBaseURL+"/ui")

	require.Equal(t, http.StatusOK, readyStatus, "readyz should be ready")
	normalizedReadyBody := strings.TrimSpace(readyBody)
	require.Equal(t, "ready", normalizedReadyBody)
	require.Equal(t, http.StatusOK, healthStatus, "healthz should be live")
	require.Equal(t, "live", strings.TrimSpace(healthBody))
	require.Equal(t, http.StatusOK, metricsStatus, "metrics should be available")
	require.Contains(t, metricsBody, "liveness")
	require.Contains(t, metricsBody, "readiness")

	expectedResponses := options.expectedResponses
	if expectedResponses == 0 {
		expectedResponses = 3
	}
	responses := fixture.controlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	require.Len(t, responses, expectedResponses)
	responseSignatures := make([]runtimeResponseObservation, 0, len(responses))
	for _, response := range responses {
		responseSignatures = append(responseSignatures, runtimeResponseObservation{
			requestID:    response.RequestID,
			responseCode: response.ResponseCode,
			responseType: response.ResponseType,
		})
	}
	sort.Slice(responseSignatures, func(i, j int) bool {
		return responseSignatures[i].requestID < responseSignatures[j].requestID
	})

	expectedToolRequests := options.expectedToolRequests
	if expectedToolRequests == 0 {
		expectedToolRequests = 1
	}
	requests := fixture.mcpServer.ReceivedRequests()
	require.Len(t, requests, expectedToolRequests)
	toolNames := make([]string, 0, len(requests))
	for _, request := range requests {
		toolNames = append(toolNames, request.Tool)
	}
	sort.Strings(toolNames)

	return runtimeObservation{
		shared: runtimeSharedObservation{
			readyStatus:         readyStatus,
			readyBody:           normalizedReadyBody,
			healthStatus:        healthStatus,
			healthBody:          strings.TrimSpace(healthBody),
			metricsStatus:       metricsStatus,
			metricsHasLiveness:  strings.Contains(metricsBody, "liveness"),
			metricsHasReadiness: strings.Contains(metricsBody, "readiness"),
			responses:           responseSignatures,
			toolNames:           toolNames,
			controlPlaneHTTP:    runtimeControlPlaneHTTPObservations(fixture.controlPlane),
			mcpHTTP:             runtimeMCPHTTPObservations(fixture.mcpServer),
			proxyHTTP:           runtimeProxyObservations(fixture.proxy),
		},
		uiStatus: uiStatus,
		uiBody:   uiBody,
	}
}

func assertRuntimeParity(t *testing.T, observations map[string]runtimeObservation, runtimeName string) {
	t.Helper()

	require.Len(t, observations, 2)
	full := observations["full"]
	customerRuntime := observations[runtimeName]
	if full.failureKind != "" || customerRuntime.failureKind != "" {
		require.Equalf(t, full.failureKind, customerRuntime.failureKind, "full client and %s startup failure diverged", runtimeName)
		return
	}

	// Proxy-health is intentionally full-only and can emit startup CONNECT
	// probes while the shared runtime traffic is already ready. Filter only
	// full-side CONNECT observations that have no runtime counterpart; all
	// remaining proxy traffic stays in the exact shared-surface comparison.
	fullShared := full.shared
	runtimeShared := customerRuntime.shared
	fullShared.proxyHTTP = filterFullOnlyProxyHealthConnects(fullShared.proxyHTTP, runtimeShared.proxyHTTP)
	runtimeShared.proxyHTTP = normalizeRuntimeProxyObservations(runtimeShared.proxyHTTP)
	require.Equalf(t, fullShared, runtimeShared, "full client and %s shared behavior diverged", runtimeName)
	require.True(t, full.exitClean, "full client should shut down cleanly after its shutdown signal")
	require.Truef(t, customerRuntime.exitClean, "%s should shut down cleanly after its shutdown signal", runtimeName)
	require.LessOrEqual(t, full.shutdownDuration, 10*time.Second)
	require.LessOrEqual(t, customerRuntime.shutdownDuration, 10*time.Second)

	require.Equal(t, http.StatusOK, full.uiStatus, "full client should keep its admin UI")
	require.Contains(t, full.uiBody, "tunnel-client")
	require.Equal(t, http.StatusNotFound, customerRuntime.uiStatus, "customer runtime must not expose the admin UI")
}

func filterFullOnlyProxyHealthConnects(
	full []runtimeProxyObservation,
	runtime []runtimeProxyObservation,
) []runtimeProxyObservation {
	runtimeObservations := make(map[runtimeProxyObservation]struct{}, len(runtime))
	for _, observation := range runtime {
		runtimeObservations[observation] = struct{}{}
	}

	filtered := make([]runtimeProxyObservation, 0, len(full))
	for _, observation := range full {
		if observation.method == http.MethodConnect {
			if _, shared := runtimeObservations[observation]; !shared {
				continue
			}
		}
		filtered = append(filtered, observation)
	}
	return normalizeRuntimeProxyObservations(filtered)
}

func normalizeRuntimeProxyObservations(observations []runtimeProxyObservation) []runtimeProxyObservation {
	if len(observations) == 0 {
		return nil
	}
	return observations
}

func writeRuntimeCompatibilityProfile(t *testing.T) (profilePath, healthURLFile, pidFile string) {
	t.Helper()
	return writeRuntimeCompatibilityProfileWithValues(
		t,
		"env:RUNTIME_COMPAT_CONTROL_PLANE_URL",
		"env:RUNTIME_COMPAT_MCP_URL",
		runtimeArtifactTunnelID,
	)
}

func writeRuntimeCompatibilityProfileWithValues(
	t *testing.T,
	controlPlaneURL string,
	mcpURL string,
	tunnelID string,
) (profilePath, healthURLFile, pidFile string) {
	t.Helper()

	dir := t.TempDir()
	profilePath = filepath.Join(dir, "runtime-compatibility.yaml")
	healthURLFile = filepath.Join(dir, "health.url")
	pidFile = filepath.Join(dir, "tunnel-client.pid")
	profile := strings.Join([]string{
		"config_version: 1",
		"control_plane:",
		"  base_url: " + runtimeArtifactYAMLScalar(controlPlaneURL),
		"  tunnel_id: " + runtimeArtifactYAMLScalar(tunnelID),
		"  api_key: env:CONTROL_PLANE_API_KEY",
		"mcp:",
		"  server_urls:",
		"    - channel: main",
		"      url: " + runtimeArtifactYAMLScalar(mcpURL),
		"health:",
		"  listen_addr: 127.0.0.1:0",
		"  url_file: " + runtimeArtifactYAMLScalar(healthURLFile),
		"process:",
		"  pid_file: " + runtimeArtifactYAMLScalar(pidFile),
		"admin_ui:",
		"  open_browser: false",
		"log:",
		"  level: info",
		"  format: struct-text",
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(profilePath, []byte(profile), 0o600))
	return profilePath, healthURLFile, pidFile
}

func assertRuntimePIDFile(t *testing.T, proc *runtimeArtifactProcess, pidFile string) {
	t.Helper()

	pidBytes, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)
	require.Equal(t, proc.cmd.Process.Pid, pid)
}

func runtimeControlPlaneSawAPIKey(controlPlane *mocktunnelservice.MockTunnelService, apiKey string) bool {
	if controlPlane == nil {
		return false
	}
	for _, request := range controlPlane.ReceivedHTTPRequests() {
		if request.Headers.Get("Authorization") == "Bearer "+apiKey {
			return true
		}
	}
	return false
}

func assertRuntimeRemoved(t *testing.T, path, description string) {
	t.Helper()

	_, err := os.Stat(path)
	require.ErrorIsf(t, err, os.ErrNotExist, "%s should be removed on SIGTERM", description)
}

func assertRuntimeOutputRedacted(t *testing.T, output string, secrets ...string) {
	t.Helper()

	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		require.NotContainsf(t, output, secret, "runtime output leaked %q", secret)
	}
}

func copyRuntimeEnvironment(src map[string]string) map[string]string {
	out := make(map[string]string, len(src)+3)
	for key, value := range src {
		out[key] = value
	}
	return out
}

func runtimeControlPlaneHTTPObservations(controlPlane *mocktunnelservice.MockTunnelService) []runtimeHTTPObservation {
	if controlPlane == nil {
		return nil
	}
	requests := controlPlane.ReceivedHTTPRequests()
	observations := make([]runtimeHTTPObservation, 0, len(requests))
	for _, request := range requests {
		observations = append(observations, runtimeHTTPObservation{
			method:           request.Method,
			path:             request.Path,
			rawQuery:         request.RawQuery,
			controlHeader:    request.Headers.Get("X-Control-Static"),
			hasAuthorization: request.Headers.Get("Authorization") != "",
		})
	}
	return uniqueRuntimeHTTPObservations(observations)
}

func runtimeMCPHTTPObservations(mcpServer *mockmcpserver.MockMCPServer) []runtimeHTTPObservation {
	if mcpServer == nil {
		return nil
	}
	requests := mcpServer.ReceivedHTTPRequests()
	observations := make([]runtimeHTTPObservation, 0, len(requests))
	for _, request := range requests {
		observations = append(observations, runtimeHTTPObservation{
			method:          request.Method,
			path:            request.Path,
			rawQuery:        request.RawQuery,
			mcpHeader:       request.Headers.Get("X-MCP-Static"),
			discoveryHeader: request.Headers.Get("X-Discovery-Static"),
		})
	}
	return uniqueRuntimeHTTPObservations(observations)
}

func uniqueRuntimeHTTPObservations(observations []runtimeHTTPObservation) []runtimeHTTPObservation {
	if len(observations) == 0 {
		return nil
	}
	seen := make(map[string]runtimeHTTPObservation, len(observations))
	for _, observation := range observations {
		key := fmt.Sprintf(
			"%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t",
			observation.method,
			observation.path,
			observation.rawQuery,
			observation.controlHeader,
			observation.mcpHeader,
			observation.discoveryHeader,
			observation.hasAuthorization,
		)
		seen[key] = observation
	}
	out := make([]runtimeHTTPObservation, 0, len(seen))
	for _, observation := range seen {
		out = append(out, observation)
	}
	sort.Slice(out, func(i, j int) bool {
		left := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t", out[i].method, out[i].path, out[i].rawQuery, out[i].controlHeader, out[i].mcpHeader, out[i].discoveryHeader, out[i].hasAuthorization)
		right := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t", out[j].method, out[j].path, out[j].rawQuery, out[j].controlHeader, out[j].mcpHeader, out[j].discoveryHeader, out[j].hasAuthorization)
		return left < right
	})
	return out
}

func runtimeProxyObservations(proxy *mockproxy.ProxyServer) []runtimeProxyObservation {
	if proxy == nil {
		return nil
	}
	records := proxy.Records()
	seen := make(map[string]runtimeProxyObservation, len(records))
	for _, record := range records {
		route := "mcp"
		if strings.Contains(record.URL, "/v1/tunnels/") {
			route = "control-plane"
		}
		observation := runtimeProxyObservation{method: record.Method, route: route}
		seen[observation.method+"\x00"+observation.route] = observation
	}
	out := make([]runtimeProxyObservation, 0, len(seen))
	for _, observation := range seen {
		out = append(out, observation)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].route != out[j].route {
			return out[i].route < out[j].route
		}
		return out[i].method < out[j].method
	})
	return out
}

func runtimeSkipUnixSignals(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("runtime host-binary compatibility checks require Unix process signals")
	}
}
