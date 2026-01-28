package adminui

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"runtime"
	"time"

	"go.uber.org/fx"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/health"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
)

type startupParams struct {
	fx.In

	Lifecycle     fx.Lifecycle
	Logger        *slog.Logger
	HealthConfig  *config.HealthConfig
	AdminUIConfig *config.AdminUIConfig
	HealthService health.Service
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
			baseURL := buildAdminBaseURL(p.HealthConfig.ListenAddr, boundAddr)
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
			// Put the URL directly in the message so it pops in terminal output,
			// even when users don't expand structured fields.
			logger.InfoContext(ctx, "🌐 WEB UI: "+uiURL,
				slog.String("ui_url", uiURL),
				slog.String("health_url", baseURL),
				slog.String("metrics_url", baseURL+"/metrics"),
			)

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

func buildAdminBaseURL(listenAddr string, boundAddr string) string {
	// boundAddr comes from net.Listener.Addr().String() (e.g. "127.0.0.1:8080" or "[::]:8080").
	host, port, err := net.SplitHostPort(boundAddr)
	if err != nil || port == "" {
		return ""
	}

	configuredHost := ""
	if h, _, err := net.SplitHostPort(listenAddr); err == nil {
		configuredHost = h
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
