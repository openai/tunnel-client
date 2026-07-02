package adminui

import (
	"net/http"
	"time"

	"go.openai.org/api/tunnel-client/pkg/mcpclient"
	"go.openai.org/api/tunnel-client/pkg/proxy"
	"go.openai.org/api/tunnel-client/pkg/proxyhealth"
	"go.openai.org/api/tunnel-client/pkg/tlsconfig"
)

type systemResponse struct {
	TLS                    tlsconfig.TrustReport            `json:"tls"`
	ProxyIdentityMap       []proxy.IdentityRecord           `json:"proxy_identity_map,omitempty"`
	ProxyHealth            []proxyhealth.RouteHealthSummary `json:"proxy_health,omitempty"`
	MainChannelProbeStatus string                           `json:"main_channel_probe_status,omitempty"`
	MainChannelProbeError  string                           `json:"main_channel_probe_error,omitempty"`
}

type harpoonProxyRoutesResponse struct {
	Routes []proxy.RouteSummary `json:"routes,omitempty"`
}

func buildSystem(p routeParams) systemResponse {
	response := systemResponse{}
	response.TLS = tlsconfig.BuildTrustReport(p.TLSBundle)
	if p.ProxyHealth != nil {
		response.ProxyIdentityMap = p.ProxyHealth.IdentityMap()
		response.ProxyHealth = p.ProxyHealth.HealthSummaries()
	}
	if p.MCPProbeState != nil {
		if !p.MCPProbeState.IsDone() {
			response.MainChannelProbeStatus = "pending"
		} else if _, err, ok := p.MCPProbeState.Wait(10 * time.Millisecond); ok {
			if err == nil {
				response.MainChannelProbeStatus = "ok"
			} else if mcpclient.IsAuthRequiredProbeError(err) {
				response.MainChannelProbeStatus = "auth-required"
				response.MainChannelProbeError = err.Error()
			} else if mcpclient.IsTimeoutProbeError(err) {
				response.MainChannelProbeStatus = "timeout"
				response.MainChannelProbeError = err.Error()
			} else {
				response.MainChannelProbeStatus = "failed"
				response.MainChannelProbeError = err.Error()
			}
		}
	}
	return response
}

func splitProxyRoutes(routes []proxy.RouteSummary) (*proxy.RouteSummary, []proxy.RouteSummary) {
	var controlPlane *proxy.RouteSummary
	mcpRoutes := make([]proxy.RouteSummary, 0)
	for _, route := range routes {
		switch route.Kind {
		case string(proxy.RouteKindControlPlane):
			copy := route
			controlPlane = &copy
		case string(proxy.RouteKindMCPChannel):
			mcpRoutes = append(mcpRoutes, route)
		}
	}
	return controlPlane, mcpRoutes
}

func harpoonProxyRoutes(snapshot proxyhealth.Snapshotter) []proxy.RouteSummary {
	if snapshot == nil {
		return nil
	}
	routes := snapshot.RouteSummaries()
	out := make([]proxy.RouteSummary, 0)
	for _, route := range routes {
		if route.Kind == string(proxy.RouteKindHarpoon) {
			out = append(out, route)
		}
	}
	return out
}

func handleSystem(p routeParams) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, buildSystem(p))
	}
}
