package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"go.openai.org/api/tunnel-client/pkg/app"
	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/controlplane"
	"go.openai.org/api/tunnel-client/pkg/oauth"
	"go.openai.org/api/tunnel-client/pkg/version"
)

type runEmbeddedMCPStubOptions struct {
	Enabled       bool
	ListenAddr    string
	ServerName    string
	ServerVersion string
}

type tunnelEventLogger struct {
	*fxevent.SlogLogger
	logger        *slog.Logger
	cfg           *config.ControlPlaneConfig
	metadataState *controlplane.MetadataState
	mcpConfig     *config.MCPConfig
}

func newTunnelEventLogger(logger *slog.Logger, cfg *config.ControlPlaneConfig, metadataState *controlplane.MetadataState, mcpConfig *config.MCPConfig) fxevent.Logger {
	return &tunnelEventLogger{
		SlogLogger:    &fxevent.SlogLogger{Logger: logger},
		logger:        logger,
		cfg:           cfg,
		metadataState: metadataState,
		mcpConfig:     mcpConfig,
	}
}

func (l *tunnelEventLogger) LogEvent(event fxevent.Event) {
	if started, ok := event.(*fxevent.Started); ok && started.Err == nil {
		tunnelURL := l.cfg.BaseURL.JoinPath("v1", "tunnel", l.cfg.TunnelID.String()).String()
		metaName := "tunnel meta hasn't fetched"
		metaDescription := "tunnel meta hasn't fetched"
		if l.metadataState != nil {
			metadata, err, ok := l.metadataState.Wait(2 * time.Second)
			if ok && err == nil && metadata != nil {
				metaName = metadata.Name
				metaDescription = metadata.Description
			}
		}
		oauthDiscoveryURLs := make([]string, 0)
		if l.mcpConfig != nil {
			priority := 1
			for _, metadataURL := range oauth.BuildResourceMetadataURLs(l.mcpConfig.ServerURL) {
				if metadataURL == nil {
					continue
				}
				oauthDiscoveryURLs = append(oauthDiscoveryURLs, fmt.Sprintf("%d:%s", priority, metadataURL.String()))
				priority++
			}
		}
		l.logger.Info("🟢 tunnel-client started",
			slog.String("tunnel_id", l.cfg.TunnelID.String()),
			slog.String("tunnel_url", tunnelURL),
			slog.String("name", metaName),
			slog.String("description", metaDescription),
			slog.Any("oauth_discovery_urls", oauthDiscoveryURLs),
			slog.String("version", version.Version),
		)
	} else {
		l.SlogLogger.LogEvent(event)
	}
}

func newRunCommand(lookupEnv func(string) (string, bool)) *cobra.Command {
	embeddedStub := runEmbeddedMCPStubOptions{
		ListenAddr:    defaultDevMCPStubListenAddr,
		ServerName:    defaultDevMCPStubName,
		ServerVersion: defaultDevMCPStubVersion,
	}
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run the tunnel client poller",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTunnel(cmd, lookupEnv, embeddedStub)
		},
	}
	config.RegisterFlags(runCmd.Flags())
	runCmd.Flags().BoolVar(&embeddedStub.Enabled, "embedded-mcp-stub", false, "Start the embedded demo MCP + OAuth stub and bind the main channel to it for this run")
	runCmd.Flags().StringVar(&embeddedStub.ListenAddr, "embedded-mcp-listen-addr", defaultDevMCPStubListenAddr, "Listen address for the embedded demo MCP stub used by --embedded-mcp-stub")
	runCmd.Flags().StringVar(&embeddedStub.ServerName, "embedded-mcp-server-name", defaultDevMCPStubName, "Server name advertised by the embedded demo MCP stub")
	runCmd.Flags().StringVar(&embeddedStub.ServerVersion, "embedded-mcp-server-version", defaultDevMCPStubVersion, "Server version advertised by the embedded demo MCP stub")

	runCmd.SetUsageFunc(func(cmd *cobra.Command) error {
		config.WriteUsage(runCmd.Flags(), cmd.OutOrStdout())
		return nil
	})
	runCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		config.WriteUsage(runCmd.Flags(), cmd.OutOrStdout())
	})
	return runCmd
}

func runTunnel(cmd *cobra.Command, lookupEnv func(string) (string, bool), embeddedStub runEmbeddedMCPStubOptions) error {
	stub, err := configureRunEmbeddedMCPStub(cmd, embeddedStub)
	if err != nil {
		return err
	}
	if stub != nil {
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = stub.Shutdown(shutdownCtx)
		}()
	}

	cfg, err := config.LoadFromFlagSet(cmd.Flags(), lookupEnv)
	if err != nil {
		if needsFirstUseGuidance(err) {
			return fmt.Errorf("configure tunnel-client: %w; %s", err, firstUseGuidance(err))
		}
		return fmt.Errorf("configure tunnel-client: %w", err)
	}

	fxApp := app.New(cfg,
		fx.Provide(func() io.Writer { return cmd.OutOrStdout() }),
		fx.WithLogger(func(logger *slog.Logger, cfg *config.ControlPlaneConfig, metadataState *controlplane.MetadataState, mcpConfig *config.MCPConfig) fxevent.Logger {
			return newTunnelEventLogger(logger, cfg, metadataState, mcpConfig)
		}),
	)
	fxApp.Run()
	return nil
}

func configureRunEmbeddedMCPStub(cmd *cobra.Command, opts runEmbeddedMCPStubOptions) (*devMCPStubInstance, error) {
	if !opts.Enabled {
		return nil, nil
	}
	if explicitMainTargetFlagChanged(cmd, "mcp.command", "mcp-command") {
		return nil, fmt.Errorf("--embedded-mcp-stub cannot be combined with --mcp.command; use one main MCP target path")
	}
	if explicitMainTargetFlagChanged(cmd, "mcp.server-url", "mcp-server-url") {
		return nil, fmt.Errorf("--embedded-mcp-stub cannot be combined with --mcp.server-url; use one main MCP target path")
	}
	stub, err := startDevMCPStub(devMCPStubOptions{
		ListenAddr:    opts.ListenAddr,
		ServerName:    opts.ServerName,
		ServerVersion: opts.ServerVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("start embedded MCP stub: %w", err)
	}
	if err := cmd.Flags().Set("mcp.server-url", fmt.Sprintf("channel=main,url=%s", stub.MCPURL())); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = stub.Shutdown(shutdownCtx)
		return nil, fmt.Errorf("configure embedded MCP stub: %w", err)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Embedded MCP stub enabled.\n")
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  MCP URL: %s\n", stub.MCPURL())
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Protected resource metadata: %s\n", stub.ProtectedResourceMetadataURL())
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Authorization server metadata: %s\n", stub.AuthorizationServerMetadataURL())
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  These are the embedded demo MCP/OAuth endpoints. tunnel-client health/ui URLs are separate and will be logged after startup.\n")
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Try in ChatGPT after the tunnel connects: server_info, echo(\"hello from tunnel-client\"), uppercase(\"openai tunnel\")\n")
	return stub, nil
}

func explicitMainTargetFlagChanged(cmd *cobra.Command, names ...string) bool {
	if cmd == nil {
		return false
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		flag := cmd.Flags().Lookup(name)
		if flag != nil && flag.Changed {
			return true
		}
	}
	return false
}

func needsFirstUseGuidance(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "tunnel ID is required") ||
		strings.Contains(message, "main channel is required") ||
		strings.Contains(message, "control plane API key is required")
}

func firstUseGuidance(err error) string {
	message := err.Error()
	if strings.Contains(message, "tunnel ID is required") {
		return "create `CONTROL_PLANE_API_KEY` in Runtime API keys for `tunnel-client doctor` and `tunnel-client run`; if you still need a tunnel id, create `OPENAI_ADMIN_KEY` only for `tunnel-client admin tunnels create|list ...`, then run `tunnel-client admin tunnels create --help` or `tunnel-client admin tunnels list --help`; then run `tunnel-client init --sample sample_mcp_stdio_local --profile local-stdio --tunnel-id tunnel_... --mcp-command \"python /path/to/server.py\"`, `tunnel-client doctor --profile local-stdio --explain`, and `tunnel-client run --profile local-stdio`; for the full first-use flow run `tunnel-client help quickstart`"
	}
	if strings.Contains(message, "main channel is required") {
		return "for a local MCP server, run `tunnel-client init --sample sample_mcp_stdio_local --profile local-stdio --tunnel-id tunnel_... --mcp-command \"python /path/to/server.py\"`, then `tunnel-client doctor --profile local-stdio --explain`, then `tunnel-client run --profile local-stdio`; for the shortest demo path run `tunnel-client run --embedded-mcp-stub --control-plane.tunnel-id tunnel_... --health.listen-addr 127.0.0.1:0 --health.url-file /tmp/tunnel-client-health.url`; for the full first-use flow run `tunnel-client help quickstart`"
	}
	return "create `CONTROL_PLANE_API_KEY` in Runtime API keys for `tunnel-client run`, keep `OPENAI_ADMIN_KEY` separate for `tunnel-client admin ...`, then follow `tunnel-client init`, `tunnel-client doctor --profile <name> --explain`, and `tunnel-client run --profile <name>`; for first-time setup run `tunnel-client help quickstart`"
}
