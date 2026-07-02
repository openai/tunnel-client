package e2e_test

import (
	"crypto/x509"
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
	const (
		controlPlaneURL = "http://127.0.0.1:1"
		mcpURL          = "https://127.0.0.1:1/mcp"
	)
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

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
