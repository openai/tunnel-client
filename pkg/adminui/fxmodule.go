package adminui

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"go.uber.org/fx"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/controlplane"
	"go.openai.org/api/tunnel-client/pkg/health"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
	"go.openai.org/api/tunnel-client/pkg/version"
)

// Module hosts a minimal embedded UI on the existing admin/health server.
var Module = fx.Module(
	"adminui",
	fx.Provide(
		NewLogBuffer,
		func(buf *LogBuffer) tclog.Sink { return buf },
	),
	fx.Invoke(registerRoutes),
)

type routeParams struct {
	fx.In

	AdminMux      *http.ServeMux `name:"admin_mux"`
	Logger        *slog.Logger
	Buffer        *LogBuffer
	HealthService health.Service
	LoggingConfig *config.LoggingConfig
	ControlPlane  *config.ControlPlaneConfig
	MCPConfig     *config.MCPConfig
	MetadataState *controlplane.MetadataState
}

type statusResponse struct {
	Version                 string                       `json:"version"`
	StartedAt               time.Time                    `json:"started_at"`
	UptimeSeconds           int64                        `json:"uptime_seconds"`
	HealthListenAddr        string                       `json:"health_listen_addr,omitempty"`
	ControlPlaneBaseURL     string                       `json:"control_plane_base_url,omitempty"`
	ControlPlaneTunnelID    string                       `json:"control_plane_tunnel_id,omitempty"`
	ControlPlaneMaxInflight int                          `json:"control_plane_max_inflight,omitempty"`
	ControlPlanePollTimeout string                       `json:"control_plane_poll_timeout,omitempty"`
	MCPServerURL            string                       `json:"mcp_server_url,omitempty"`
	RawHTTPLoggingEnabled   bool                         `json:"raw_http_logging_enabled"`
	TunnelMetadata          *controlplane.TunnelMetadata `json:"tunnel_metadata,omitempty"`
	MetadataError           string                       `json:"tunnel_metadata_error,omitempty"`
	Warnings                []string                     `json:"warnings,omitempty"`
}

func registerRoutes(p routeParams) error {
	if p.AdminMux == nil {
		return fmt.Errorf("adminui: admin mux is required")
	}
	if p.Buffer == nil {
		return fmt.Errorf("adminui: log buffer is required")
	}

	p.AdminMux.HandleFunc("/", handleIndex)
	p.AdminMux.Handle("/assets/", handleAssets())
	p.AdminMux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, buildStatus(p))
	})
	p.AdminMux.HandleFunc("/api/logs", handleLogsJSON(p.Buffer))
	p.AdminMux.HandleFunc("/api/logs/stream", handleLogsStream(p.Buffer))
	p.AdminMux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// Record a single startup line in the in-memory buffer so the UI isn't empty.
	if p.Logger != nil {
		p.Logger.Info("admin ui enabled", slog.String("path", "/"))
	}

	return nil
}

func buildStatus(p routeParams) statusResponse {
	startedAt := p.Buffer.StartedAt()
	uptime := time.Since(startedAt)
	if startedAt.IsZero() || uptime < 0 {
		uptime = 0
	}

	out := statusResponse{
		Version:       version.Version,
		StartedAt:     startedAt,
		UptimeSeconds: int64(uptime.Seconds()),
	}

	if p.HealthService != nil {
		out.HealthListenAddr = p.HealthService.Addr()
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
	}
	if p.MCPConfig != nil && p.MCPConfig.ServerURL != nil {
		out.MCPServerURL = p.MCPConfig.ServerURL.String()
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
