package adminui

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
	"time"

	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/codexplugin"
	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/health"
	"github.com/openai/tunnel-client/pkg/healthurl"
	tclog "github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/oauth"
)

type startupParams struct {
	fx.In

	Lifecycle     fx.Lifecycle
	Logger        *slog.Logger
	Config        *config.Config
	HealthConfig  *config.HealthConfig
	AdminUIConfig *config.AdminUIConfig
	HealthService health.Service
	ProbeState    *mcpclient.ProbeState
	OAuthState    *oauth.DiscoveryState
}

type startupSummary struct {
	HealthURL              string
	HealthURLFile          string
	UIURL                  string
	MetricsURL             string
	ConfigSource           string
	ProfileName            string
	ProfilePath            string
	TunnelID               string
	MCPTargetKind          string
	MCPTargetValue         string
	FirstFailingDependency string
	CodexDetected          bool
	CodexHome              string
	CodexPluginInstalled   bool
	CodexPluginDir         string
	CodexPluginInstallHint string
}

func registerStartup(p startupParams) error {
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With(tclog.FieldComponent, "adminui")

	if p.Lifecycle == nil {
		return fmt.Errorf("adminui: lifecycle is required")
	}
	if p.HealthConfig == nil {
		return fmt.Errorf("adminui: health config is required")
	}
	if p.AdminUIConfig == nil {
		return fmt.Errorf("adminui: admin UI config is required")
	}
	if p.HealthService == nil {
		return fmt.Errorf("adminui: health service is required")
	}

	p.Lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			boundAddr := ""
			if p.HealthService != nil {
				addr, err := p.HealthService.Addr(2 * time.Second)
				if err != nil {
					return fmt.Errorf("adminui: health address unavailable: %w", err)
				}
				boundAddr = addr
			}
			baseURL := buildAdminBaseURL(p.HealthConfig, boundAddr)
			if baseURL == "" {
				healthAddr := ""
				if p.HealthService != nil {
					if addr, err := p.HealthService.Addr(0); err == nil {
						healthAddr = addr
					}
				}
				return fmt.Errorf("adminui: health URL could not be determined (health_addr=%s)", healthAddr)
			}

			uiURL := baseURL + "/ui"
			summary := buildStartupSummary(p.Config, baseURL, p.ProbeState, p.OAuthState, codexplugin.Detect(os.LookupEnv))
			// Put the URL directly in the message so it pops in terminal output,
			// even when users don't expand structured fields.
			logger.InfoContext(ctx, "🌐 WEB UI: "+uiURL,
				slog.String("ui_url", uiURL),
				slog.String("health_url", baseURL),
				slog.String("metrics_url", baseURL+"/metrics"),
			)
			logger.InfoContext(ctx, "tunnel-client startup summary",
				slog.String("health_url", summary.HealthURL),
				slog.String("health_url_file", summary.HealthURLFile),
				slog.String("ui_url", summary.UIURL),
				slog.String("metrics_url", summary.MetricsURL),
				slog.String("config_source", summary.ConfigSource),
				slog.String("profile_name", summary.ProfileName),
				slog.String("profile_path", summary.ProfilePath),
				slog.String("tunnel_id", summary.TunnelID),
				slog.String("mcp_target_kind", summary.MCPTargetKind),
				slog.String("mcp_target_value", summary.MCPTargetValue),
				slog.String("first_failing_dependency", summary.FirstFailingDependency),
				slog.Bool("codex_detected", summary.CodexDetected),
				slog.String("codex_home", summary.CodexHome),
				slog.Bool("codex_plugin_installed", summary.CodexPluginInstalled),
				slog.String("codex_plugin_dir", summary.CodexPluginDir),
				slog.String("codex_plugin_install_hint", summary.CodexPluginInstallHint),
			)
			if summary.CodexDetected && !summary.CodexPluginInstalled && summary.CodexPluginInstallHint != "" {
				logger.InfoContext(ctx, "Codex detected without Tunnel MCP plugin",
					slog.String("next_step", summary.CodexPluginInstallHint),
					slog.String("plugin_dir", summary.CodexPluginDir),
				)
			}

			if p.AdminUIConfig.OpenBrowser {
				if err := openBrowser(uiURL); err != nil {
					logger.WarnContext(ctx, "failed to open web UI in browser", slog.String("ui_url", uiURL), slog.String("error", err.Error()))
				}
			}
			return nil
		},
	})

	return nil
}

func buildAdminBaseURL(cfg *config.HealthConfig, boundAddr string) string {
	if cfg != nil && cfg.UnixSocket != "" {
		return healthurl.BuildUnixBaseURL(cfg.UnixSocket)
	}

	// boundAddr comes from net.Listener.Addr().String() (e.g. "127.0.0.1:8080" or "[::]:8080").
	host, port, err := net.SplitHostPort(boundAddr)
	if err != nil || port == "" {
		return ""
	}

	configuredHost := ""
	if cfg != nil {
		if h, _, err := net.SplitHostPort(cfg.ListenAddr); err == nil {
			configuredHost = h
		}
	}

	chosenHost := configuredHost
	if chosenHost == "" || isUnspecifiedHost(chosenHost) {
		chosenHost = host
	}
	if chosenHost == "" || isUnspecifiedHost(chosenHost) {
		chosenHost = "localhost"
	}

	return "http://" + net.JoinHostPort(chosenHost, port)
}

func buildStartupSummary(
	cfg *config.Config,
	baseURL string,
	probeState *mcpclient.ProbeState,
	oauthState *oauth.DiscoveryState,
	detection codexplugin.Detection,
) startupSummary {
	summary := startupSummary{
		HealthURL:              baseURL,
		UIURL:                  baseURL + "/ui",
		MetricsURL:             baseURL + "/metrics",
		CodexDetected:          detection.Detected,
		CodexHome:              detection.CodexHome,
		CodexPluginInstalled:   detection.PluginInstalled,
		CodexPluginDir:         detection.PluginDir,
		CodexPluginInstallHint: detection.InstallHint,
	}
	if cfg == nil {
		return summary
	}

	summary.HealthURLFile = cfg.Health.URLFile
	summary.ConfigSource = startupConfigSource(cfg.Runtime)
	summary.ProfileName = cfg.Runtime.ProfileName
	summary.ProfilePath = cfg.Runtime.ProfilePath
	summary.TunnelID = cfg.ControlPlane.TunnelID.String()
	summary.MCPTargetKind, summary.MCPTargetValue = startupMCPTarget(cfg)
	summary.FirstFailingDependency = startupFirstFailingDependency(cfg, probeState, oauthState)
	return summary
}

func startupConfigSource(runtimeCfg config.RuntimeConfig) string {
	switch {
	case runtimeCfg.ProfileFile && runtimeCfg.ProfilePath != "":
		return "profile-file:" + runtimeCfg.ProfilePath
	case runtimeCfg.ProfileName != "":
		return "profile:" + runtimeCfg.ProfileName
	case runtimeCfg.ProfilePath != "":
		return runtimeCfg.ProfilePath
	case runtimeCfg.ConfigFile != "":
		return runtimeCfg.ConfigFile
	default:
		return "flags/environment"
	}
}

func startupMCPTarget(cfg *config.Config) (string, string) {
	if cfg == nil {
		return "", ""
	}
	if binding := cfg.MCP.MainChannelBinding(); binding != nil {
		if binding.ServerURL != nil {
			return string(binding.TransportKind), binding.ServerURL.String()
		}
		if binding.Command != "" {
			return string(binding.TransportKind), binding.Command
		}
	}
	if cfg.MCP.ServerURL != nil {
		return string(cfg.MCP.TransportKind), cfg.MCP.ServerURL.String()
	}
	if cfg.MCP.Command != "" {
		return string(cfg.MCP.TransportKind), cfg.MCP.Command
	}
	return "", ""
}

func startupFirstFailingDependency(cfg *config.Config, probeState *mcpclient.ProbeState, oauthState *oauth.DiscoveryState) string {
	if probeState != nil && probeState.IsDone() {
		if _, err, ok := probeState.Wait(10 * time.Millisecond); ok && err != nil {
			return "mcp_probe: " + err.Error()
		}
	}
	if cfg != nil && cfg.MCP.TransportKind == config.MCPTransportHTTPStreamable {
		if oauthState != nil && oauthState.IsDone() {
			if _, _, _, err, ok := oauthState.Wait(10 * time.Millisecond); ok && err != nil {
				return "oauth_metadata: " + err.Error()
			}
		}
	}
	return ""
}

func isUnspecifiedHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
