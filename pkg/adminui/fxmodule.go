package adminui

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/clientinstance"
	"github.com/openai/tunnel-client/pkg/codexappserver"
	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane"
	"github.com/openai/tunnel-client/pkg/harpoon"
	"github.com/openai/tunnel-client/pkg/health"
	"github.com/openai/tunnel-client/pkg/httpguard"
	tclog "github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/metrics"
	"github.com/openai/tunnel-client/pkg/oauth"
	"github.com/openai/tunnel-client/pkg/proxy"
	"github.com/openai/tunnel-client/pkg/proxyhealth"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
	"github.com/openai/tunnel-client/pkg/version"
)

// Module hosts a minimal embedded UI on the existing admin/health server.
var Module = fx.Module(
	"adminui",
	fx.Provide(
		newConfiguredLogBuffer,
		NewRuntimeSnapshotProvider,
		codexappserver.NewBridge,
		func(buf *LogBuffer) tclog.Sink { return buf },
	),
	fx.Invoke(registerRoutes),
	fx.Invoke(registerStartup),
)

type routeParams struct {
	fx.In

	AdminMux       *http.ServeMux `name:"admin_mux"`
	Lifecycle      fx.Lifecycle
	Logger         *slog.Logger
	Buffer         *LogBuffer
	LevelControl   *tclog.LevelController
	Runtime        RuntimeSnapshotProvider
	MetricExporter metrics.MetricsExporter
	HealthService  health.Service
	LoggingConfig  *config.LoggingConfig
	ControlPlane   *config.ControlPlaneConfig
	MCPConfig      *config.MCPConfig
	HarpoonConfig  *config.HarpoonConfig
	AdminUIConfig  *config.AdminUIConfig
	MetadataState  *controlplane.MetadataState
	OAuthState     *oauth.DiscoveryState
	HarpoonBuffer  *harpoon.CallBuffer
	HarpoonReg     *harpoon.Registry
	CodexBridge    *codexappserver.Bridge
	StdioInfo      mcpclient.ChannelStdioRuntimeInfoProvider `optional:"true"`
	MCPProbeState  *mcpclient.ProbeState                     `optional:"true"`
	ProxyHealth    proxyhealth.Snapshotter                   `optional:"true"`
	TLSBundle      *tlsconfig.Bundle
}

type statusResponse struct {
	Version                           string                       `json:"version"`
	ClientInstanceID                  string                       `json:"client_instance_id"`
	StartedAt                         time.Time                    `json:"started_at"`
	UptimeSeconds                     int64                        `json:"uptime_seconds"`
	HealthListenAddr                  string                       `json:"health_listen_addr,omitempty"`
	ControlPlaneBaseURL               string                       `json:"control_plane_base_url,omitempty"`
	ControlPlaneTunnelID              string                       `json:"control_plane_tunnel_id,omitempty"`
	ControlPlaneMaxInflight           int                          `json:"control_plane_max_inflight,omitempty"`
	ControlPlanePollTimeout           string                       `json:"control_plane_poll_timeout,omitempty"`
	ControlPlanePollDeadlineGuardrail string                       `json:"control_plane_poll_deadline_guardrail,omitempty"`
	MCPServerURL                      string                       `json:"mcp_server_url,omitempty"`
	MCPResourceMetadataURLs           []string                     `json:"mcp_resource_metadata_urls,omitempty"`
	Channels                          []ChannelStatus              `json:"channels,omitempty"`
	ControlPlaneRoute                 *proxy.RouteSummary          `json:"control_plane_route,omitempty"`
	MCPRoutes                         []proxy.RouteSummary         `json:"mcp_routes,omitempty"`
	RawHTTPLoggingEnabled             bool                         `json:"raw_http_logging_enabled"`
	TunnelMetadata                    *controlplane.TunnelMetadata `json:"tunnel_metadata,omitempty"`
	MetadataError                     string                       `json:"tunnel_metadata_error,omitempty"`
	Warnings                          []string                     `json:"warnings,omitempty"`
}

func registerRoutes(p routeParams) error {
	if p.AdminMux == nil {
		return fmt.Errorf("adminui: admin mux is required")
	}
	if p.Buffer == nil {
		return fmt.Errorf("adminui: log buffer is required")
	}
	if p.Lifecycle == nil {
		return fmt.Errorf("adminui: lifecycle is required")
	}

	streamCtx, streamCancel := context.WithCancel(context.Background())
	p.Lifecycle.Append(fx.Hook{
		OnStop: func(context.Context) error {
			streamCancel()
			return nil
		},
	})

	gmux := httpguard.NewGuardedMux(
		p.AdminMux,
		p.AdminUIConfig != nil && p.AdminUIConfig.AllowRemote,
		"admin UI is restricted to loopback; set --allow-remote-ui to override",
	)

	gmux.HandleFunc("/", handleIndex)
	gmux.Handle("/assets/", handleAssets())
	gmux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, buildStatus(p))
	})
	gmux.HandleFunc("/api/system", handleSystem(p))
	gmux.HandleFunc("/api/oauth", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, buildOAuthStatus(p))
	})
	gmux.HandleFunc("/api/logs", handleLogsJSON(p.Buffer))
	gmux.HandleFunc("/api/log-level", handleLogLevel(p.LevelControl, p.Logger))
	gmux.HandleFunc("/api/logs/export", handleLogsExport(
		p.Buffer,
		p.Runtime,
		NewMetricsSnapshotProvider(p.MetricExporter),
		func() logExportAdminSnapshots {
			return logExportAdminSnapshots{
				Status: buildStatus(p),
				System: buildSystem(p),
				OAuth:  buildOAuthStatus(p),
				Harpoon: logExportHarpoonData{
					Status:  buildHarpoonStatus(p.HarpoonReg, p.HarpoonConfig, p.ProxyHealth),
					Targets: buildHarpoonTargets(p.HarpoonReg),
					Calls:   buildHarpoonCalls(p.HarpoonBuffer, p.HarpoonConfig, "", 100),
				},
			}
		},
	))
	gmux.HandleFunc("/api/logs/stream", handleLogsStream(p.Buffer, streamCtx))
	gmux.HandleFunc("/api/harpoon/status", handleHarpoonStatus(p.HarpoonReg, p.HarpoonConfig, p.ProxyHealth))
	gmux.HandleFunc("/api/harpoon/targets", handleHarpoonTargets(p.HarpoonReg))
	gmux.HandleFunc("/api/harpoon/calls", handleHarpoonCalls(p.HarpoonBuffer, p.HarpoonConfig))
	gmux.HandleFunc("/api/codex/status", handleCodexStatus(p))
	gmux.HandleFunc("/api/codex/events", handleCodexEvents(p))
	gmux.HandleFunc("/api/codex/events/stream", handleCodexEventsStream(p, streamCtx))
	gmux.Handle("/api/codex/login/device", adminSameOriginUnsafe(handleCodexLoginDevice(p)))
	gmux.Handle("/api/codex/login/cancel", adminSameOriginUnsafe(handleCodexLoginCancel(p)))
	gmux.Handle("/api/codex/thread/start", adminSameOriginUnsafe(handleCodexThreadStart(p)))
	gmux.Handle("/api/codex/turn/start", adminSameOriginUnsafe(handleCodexTurnStart(p)))
	gmux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// Record a single startup line in the in-memory buffer so the UI isn't empty.
	if p.Logger != nil {
		p.Logger.Info("admin ui enabled", slog.String("path", "/"))
	}

	return nil
}

func adminSameOriginUnsafe(fn http.HandlerFunc) http.Handler {
	return httpguard.SameOriginUnsafe(
		fn,
		"admin UI unsafe request must be same-origin",
	)
}

func newConfiguredLogBuffer(cfg *config.AdminUIConfig) *LogBuffer {
	capacity := defaultLogCapacity
	if cfg != nil && cfg.LogBufferEvents > 0 {
		capacity = cfg.LogBufferEvents
	}
	return NewLogBufferWithCapacity(capacity)
}

func buildStatus(p routeParams) statusResponse {
	startedAt := p.Buffer.StartedAt()
	uptime := time.Since(startedAt)
	if startedAt.IsZero() || uptime < 0 {
		uptime = 0
	}

	out := statusResponse{
		Version:          version.Version,
		ClientInstanceID: clientinstance.ID(),
		StartedAt:        startedAt,
		UptimeSeconds:    int64(uptime.Seconds()),
	}

	if p.HealthService != nil {
		if addr, err := p.HealthService.Addr(0); err == nil {
			out.HealthListenAddr = addr
		}
	}
	if p.ControlPlane != nil {
		if p.ControlPlane.BaseURL != nil {
			out.ControlPlaneBaseURL = p.ControlPlane.BaseURL.String()
		}
		out.ControlPlaneTunnelID = p.ControlPlane.TunnelID.String()
		out.ControlPlaneMaxInflight = p.ControlPlane.MaxInFlightRequests
		if p.ControlPlane.PollTimeout > 0 {
			out.ControlPlanePollTimeout = p.ControlPlane.PollTimeout.String()
		}
		if p.ControlPlane.PollDeadlineGuardrail > 0 {
			out.ControlPlanePollDeadlineGuardrail = p.ControlPlane.PollDeadlineGuardrail.String()
		}
	}
	if p.MCPConfig != nil {
		if mainBinding := p.MCPConfig.MainChannelBinding(); mainBinding != nil && mainBinding.ServerURL != nil {
			out.MCPServerURL = mainBinding.ServerURL.String()
			urls := oauth.BuildResourceMetadataURLs(mainBinding.ServerURL)
			out.MCPResourceMetadataURLs = make([]string, 0, len(urls))
			for _, url := range urls {
				if url == nil {
					continue
				}
				out.MCPResourceMetadataURLs = append(out.MCPResourceMetadataURLs, url.String())
			}
		}
	}
	out.Channels = BuildChannelStatuses(p.MCPConfig, p.HarpoonReg, p.StdioInfo, p.MCPProbeState)
	if p.ProxyHealth != nil {
		controlRoute, mcpRoutes := splitProxyRoutes(p.ProxyHealth.RouteSummaries())
		out.ControlPlaneRoute = controlRoute
		out.MCPRoutes = mcpRoutes
	}
	if p.LoggingConfig != nil {
		out.RawHTTPLoggingEnabled = p.LoggingConfig.HTTPRawUnsafe
		if p.LoggingConfig.HTTPRawUnsafe {
			out.Warnings = append(out.Warnings, "Raw HTTP logging is enabled; sensitive data may be exposed.")
		}
	}

	if p.MetadataState != nil {
		// Use a tiny timeout to avoid racing the 0-timeout timer path when the
		// metadata is already available (a 0-duration timer fires immediately and can
		// win the select, even if m.done is closed).
		if meta, err, ok := p.MetadataState.Wait(10 * time.Millisecond); ok {
			if err != nil {
				out.MetadataError = err.Error()
			} else {
				out.TunnelMetadata = meta
			}
		}
	}

	return out
}
