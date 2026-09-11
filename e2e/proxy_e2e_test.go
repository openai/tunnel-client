package e2e_test

import (
	"context"
	"crypto/x509"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
	"github.com/openai/tunnel-client/pkg/types"
	harnesspkg "github.com/openai/tunnel-client/testsupport/e2e"
	"github.com/openai/tunnel-client/testsupport/mockmcpserver"
	"github.com/openai/tunnel-client/testsupport/mockproxy"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

func TestProxyE2ESucceedsThroughProxy(t *testing.T) {
	proxy := mockproxy.New()
	proxy.Start()
	t.Cleanup(proxy.Close)

	const (
		controlPlaneHost = "control-plane.test"
		mcpHost          = "example.com"
	)

	runSimpleToolScenarioWithHarnessOptions(
		t,
		[]harnesspkg.HarnessOption{
			harnesspkg.WithPreserveClientURLs(),
			harnesspkg.WithClientConfig(func(cfg *config.Config) {
				cfg.ControlPlane.BaseURL = mustParseURL(t, "http://"+controlPlaneHost)
				cfg.ControlPlane.HTTPProxy = mustParseURL(t, proxy.URL())
				cfg.ControlPlane.HTTPProxySource = config.ProxySource("control-plane.http-proxy")
				cfg.MCP.TransportKind = config.MCPTransportHTTPStreamable
				cfg.MCP.ServerURL = mustParseURL(t, "https://"+mcpHost+"/mcp")
				cfg.MCP.HTTPProxy = mustParseURL(t, proxy.URL())
				cfg.MCP.HTTPProxySource = config.ProxySource("mcp.http-proxy")
				cfg.MCP.ChannelBindings = []config.MCPChannelBinding{{
					Channel:         types.DefaultChannel,
					TransportKind:   config.MCPTransportHTTPStreamable,
					ServerURL:       cfg.MCP.ServerURL,
					HTTPProxy:       mustParseURL(t, proxy.URL()),
					HTTPProxySource: config.ProxySource("mcp.http-proxy"),
				}}
			}),
			harnesspkg.WithBeforeClientStart(func(h *harnesspkg.Harness) {
				if h.ControlPlane.BaseURL() != nil {
					proxy.SetRoute(controlPlaneHost, h.ControlPlane.BaseURL())
				}
				if h.MCP.BaseURL() != nil {
					proxy.SetRoute(mcpHost+":443", h.MCP.BaseURL())
				}
				certPEM, err := h.MCP.TLSCertPEM()
				if err != nil {
					t.Fatalf("mock MCP server TLS cert unavailable: %v", err)
				}
				pool := x509.NewCertPool()
				if ok := pool.AppendCertsFromPEM(certPEM); !ok {
					t.Fatalf("failed to append MCP server cert to pool")
				}
				h.SetTLSBundle(&tlsconfig.Bundle{Path: "mock-mcp.pem", RootCAs: pool})
			}),
		},
		nil,
		mockmcpserver.WithTLSServer(),
	)

	records := proxy.Records()
	assertProxyRecord(t, records, "CONNECT", mcpHost+":443")
	assertProxyRecord(t, records, "GET", controlPlaneHost)
}

func TestProxyE2EFailsWithoutProxy(t *testing.T) {
	t.Parallel()

	const childEnv = "TUNNEL_CLIENT_E2E_NO_PROXY_CHILD"
	if os.Getenv(childEnv) != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(30 * time.Second)
		if parentDeadline, ok := t.Deadline(); ok {
			deadline = parentDeadline.Add(-time.Second)
		}
		ctx, cancel := context.WithDeadline(t.Context(), deadline)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable,
			"-test.run=^TestProxyE2EFailsWithoutProxy$", "-test.count=1", "-test.v=true")
		cmd.WaitDelay = time.Second
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if runtime.GOOS == "windows" {
				key = strings.ToUpper(key)
			}
			switch key {
			case "HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy",
				childEnv, "GO_TEST_WRAP", "XML_OUTPUT_FILE", "COVERAGE_OUTPUT_FILE":
				continue
			}
			cmd.Env = append(cmd.Env, entry)
		}
		cmd.Env = append(cmd.Env, childEnv+"=1", "GO_TEST_WRAP=0",
			"HTTP_PROXY=", "http_proxy=", "HTTPS_PROXY=", "https_proxy=", "NO_PROXY=", "no_proxy=")
		// Keep child coverage in the normal collector without sharing output files.
		if os.Getenv("COVERAGE_OUTPUT_FILE") != "" {
			cmd.Env = append(cmd.Env, "COVERAGE_OUTPUT_FILE="+filepath.Join(t.TempDir(), "child.coverage"))
		}
		if coverDir := flag.Lookup("test.gocoverdir"); coverDir != nil && coverDir.Value.String() != "" {
			cmd.Args = append(cmd.Args, "-test.gocoverdir="+coverDir.Value.String())
		}
		// A short launch directory avoids Windows CreateProcess path limits;
		// the test runner restores the child's runfiles directory during init.
		if runtime.GOOS == "windows" && os.Getenv("TEST_SRCDIR") != "" {
			cmd.Dir = os.TempDir()
			cmd.Env = append(cmd.Env, "GO_TEST_RUN_FROM_BAZEL=1")
		}
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("no-proxy child failed: %v\n%s", err, output)
		}
		if !strings.Contains(string(output), "\n--- PASS: TestProxyE2EFailsWithoutProxy (") {
			t.Fatalf("no-proxy child did not complete the scenario:\n%s", output)
		}
		return
	}
	for _, key := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy"} {
		if os.Getenv(key) != "" {
			t.Fatalf("no-proxy child inherited %s", key)
		}
	}
	const (
		controlPlaneURL = "http://127.0.0.1:1"
		mcpURL          = "https://127.0.0.1:1/mcp"
	)

	harness := harnesspkg.NewHarness(t,
		harnesspkg.WithPreserveClientURLs(),
		harnesspkg.WithScenarioTimeout(2*time.Second),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithInitializationPhaseCommands(),
			mocktunnelservice.WithAllowPendingCommands(),
		),
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.ControlPlane.BaseURL = mustParseURL(t, controlPlaneURL)
			cfg.MCP.TransportKind = config.MCPTransportHTTPStreamable
			cfg.MCP.ServerURL = mustParseURL(t, mcpURL)
			cfg.MCP.ChannelBindings = []config.MCPChannelBinding{{
				Channel:       types.DefaultChannel,
				TransportKind: config.MCPTransportHTTPStreamable,
				ServerURL:     cfg.MCP.ServerURL,
			}}
		}),
		harnesspkg.WithMCPOptions(mockmcpserver.WithTLSServer()),
	)
	if harness.ControlPlane != nil {
		harness.ControlPlane.AllowPending()
	}

	if err := harness.ExecuteScenario(t); err == nil {
		t.Fatalf("expected scenario failure without proxy")
	}
}

func assertProxyRecord(t *testing.T, records []mockproxy.RequestRecord, method, host string) {
	t.Helper()
	for _, record := range records {
		if record.Method == method && record.Host == host {
			return
		}
	}
	t.Fatalf("expected proxy record for %s %s", method, host)
}
