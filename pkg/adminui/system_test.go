package adminui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/proxy"
	"github.com/openai/tunnel-client/pkg/proxyhealth"
)

type stubProxySnapshot struct {
	routes   []proxy.RouteSummary
	identity []proxy.IdentityRecord
	health   []proxyhealth.RouteHealthSummary
}

func (s stubProxySnapshot) RouteSummaries() []proxy.RouteSummary {
	return s.routes
}

func (s stubProxySnapshot) HealthSummaries() []proxyhealth.RouteHealthSummary {
	return s.health
}

func (s stubProxySnapshot) IdentityMap() []proxy.IdentityRecord {
	return s.identity
}

func TestBuildStatusProxyRoutes(t *testing.T) {
	snapshot := stubProxySnapshot{
		routes: []proxy.RouteSummary{
			{Kind: string(proxy.RouteKindControlPlane), Name: "control", RouteMode: "direct", ProxySource: "none"},
			{Kind: string(proxy.RouteKindMCPChannel), Name: "main", RouteMode: "proxy", ProxySource: "env:HTTPS_PROXY"},
		},
	}
	status := buildStatus(routeParams{ProxyHealth: snapshot})
	if status.ControlPlaneRoute == nil {
		t.Fatalf("expected control plane route to be set")
	}
	if len(status.MCPRoutes) != 1 {
		t.Fatalf("expected 1 mcp route, got %d", len(status.MCPRoutes))
	}
}

func TestBuildHarpoonStatusProxyRoutes(t *testing.T) {
	snapshot := stubProxySnapshot{
		routes: []proxy.RouteSummary{
			{Kind: string(proxy.RouteKindHarpoon), Name: "target", RouteMode: "proxy", ProxySource: "env:HTTP_PROXY"},
		},
	}
	status := buildHarpoonStatus(nil, &config.HarpoonConfig{}, snapshot)
	if len(status.ProxyRoutes) != 1 {
		t.Fatalf("expected 1 harpoon proxy route, got %d", len(status.ProxyRoutes))
	}
}

func TestBuildSystemProxySnapshot(t *testing.T) {
	snapshot := stubProxySnapshot{
		identity: []proxy.IdentityRecord{{ProxyID: "id", ProxyURL: "http://proxy:8080", ProxySource: "env:HTTP_PROXY"}},
		health:   []proxyhealth.RouteHealthSummary{{HealthState: string(proxyhealth.HealthStateDirect)}},
	}
	system := buildSystem(routeParams{ProxyHealth: snapshot})
	if len(system.ProxyIdentityMap) != 1 {
		t.Fatalf("expected identity map")
	}
	if len(system.ProxyHealth) != 1 {
		t.Fatalf("expected proxy health entries")
	}
}

func TestBuildSystemIncludesMainChannelProbeStatus(t *testing.T) {
	probeState := mcpclient.NewProbeState()
	probeState.Set(errors.New(`calling "initialize": Unauthorized`))

	system := buildSystem(routeParams{MCPProbeState: probeState})
	if system.MainChannelProbeStatus != "auth-required" {
		t.Fatalf("expected auth-required probe status, got %q", system.MainChannelProbeStatus)
	}
	if system.MainChannelProbeError == "" {
		t.Fatalf("expected probe error to be surfaced")
	}
}

func TestBuildSystemIncludesMainChannelProbeTimeout(t *testing.T) {
	probeState := mcpclient.NewProbeState()
	probeState.Set(mcpclient.NewProbeTimeoutError(2*time.Second, context.DeadlineExceeded))

	system := buildSystem(routeParams{MCPProbeState: probeState})
	if system.MainChannelProbeStatus != "timeout" {
		t.Fatalf("expected timeout probe status, got %q", system.MainChannelProbeStatus)
	}
	if system.MainChannelProbeError == "" {
		t.Fatalf("expected probe error to be surfaced")
	}
}
