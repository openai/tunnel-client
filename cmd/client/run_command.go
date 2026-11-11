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

	"github.com/openai/tunnel-client/pkg/app"
	"github.com/openai/tunnel-client/pkg/cloudflared"
	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane"
	"github.com/openai/tunnel-client/pkg/oauth"
	"github.com/openai/tunnel-client/pkg/runtimecli"
	"github.com/openai/tunnel-client/pkg/version"
)

type runEmbeddedMCPStubOptions struct {
	Enabled       bool
	Stateless     bool
	ListenAddr    string
	UnixSocket    string
	ServerName    string
	ServerVersion string
}

// testOnlyLegacyHarpoonProtocol is set only by the dedicated integration-test
// binary. The normal tunnel-client binary leaves it false and has no runtime
// flag or environment variable for this compatibility fixture.
var testOnlyLegacyHarpoonProtocol = "false"

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
		if l.mcpConfig != nil && !l.mcpConfig.DisableOAuthDiscovery {
			priority := 1
			for _, metadataURL := range oauth.BuildResourceMetadataURLs(l.mcpConfig.ServerURL) {
				if metadataURL == nil {
					continue
				}
				oauthDiscoveryURLs = append(oauthDiscoveryURLs, fmt.Sprintf("%d:%s", priority, metadataURL.String()))
				priority++
			}
		}
		l.logger.Info("tunnel-client process started; readiness and control-plane polling are reported separately",
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
	runCmd.Flags().BoolVar(&embeddedStub.Enabled, "embedded-mcp-stub", false, "Bind the main channel to the embedded demo MCP + OAuth stub with legacy sessions and modern stateless requests; mutually exclusive with --embedded-stateless-mcp-stub")
	runCmd.Flags().BoolVar(&embeddedStub.Stateless, "embedded-stateless-mcp-stub", false, "Start the embedded demo MCP + OAuth stub with stateless request handling, including initialize, and bind the main channel to it; mutually exclusive with --embedded-mcp-stub")
	runCmd.Flags().StringVar(&embeddedStub.ListenAddr, "embedded-mcp-listen-addr", defaultDevMCPStubListenAddr, "Listen address for either embedded demo MCP stub mode")
	runCmd.Flags().StringVar(&embeddedStub.UnixSocket, "embedded-mcp-unix-socket", "", "Unix socket path for either embedded demo MCP stub mode; mutually exclusive with --embedded-mcp-listen-addr")
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

	cfg, err := loadRunConfig(cmd, lookupEnv, embeddedStub, stub)
	if err != nil {
		if needsFirstUseGuidance(err) {
			return fmt.Errorf("configure tunnel-client: %w; %s", err, firstUseGuidance(err))
		}
		return fmt.Errorf("configure tunnel-client: %w", err)
	}

	var cloudflaredSupervisor *cloudflared.Supervisor
	appOptions := []fx.Option{
		fx.Provide(func() io.Writer { return cmd.OutOrStdout() }),
		fx.Populate(&cloudflaredSupervisor),
		fx.WithLogger(func(logger *slog.Logger, cfg *config.ControlPlaneConfig, metadataState *controlplane.MetadataState, mcpConfig *config.MCPConfig) fxevent.Logger {
			return newTunnelEventLogger(logger, cfg, metadataState, mcpConfig)
		}),
	}
	if testOnlyLegacyHarpoonProtocol == "true" {
		appOptions = append(appOptions, fx.Provide(fx.Annotate(
			func() bool { return true },
			fx.ResultTags(`name:"legacy_harpoon_protocol_for_testing"`),
		)))
	}
	fxApp := app.New(cfg, appOptions...)
	var failureCh <-chan error
	if cloudflaredSupervisor != nil {
		failureCh = cloudflaredSupervisor.Failures()
	}
	return runtimecli.RunFXApp(cmd.Context(), fxApp, "tunnel-client", failureCh)
}

func loadRunConfig(cmd *cobra.Command, lookupEnv func(string) (string, bool), opts runEmbeddedMCPStubOptions, stub *devMCPStubInstance) (*config.Config, error) {
	var cfg *config.Config
	var err error
	if opts.Stateless && stub != nil {
		cfg, err = config.LoadFromFlagSetWithMainMCPServerURL(cmd.Flags(), lookupEnv, stub.MCPURL())
	} else {
		cfg, err = config.LoadFromFlagSet(cmd.Flags(), lookupEnv)
	}
	if err != nil {
		return nil, err
	}
	if stub != nil {
		main := cfg.MCP.MainChannelBinding()
		if stub.UnixSocket != "" {
			main.UnixSocketPath = stub.UnixSocket
			cfg.MCP.UnixSocketPath = stub.UnixSocket
			main.HTTPProxy = nil
			main.HTTPProxySource = config.ProxySourceIgnored
		}
		if opts.Stateless {
			// The process-owned stub must not inherit proxies for external MCP targets.
			main.HTTPProxy = nil
			main.HTTPProxySource = config.ProxySourceNone
			main.Stateless = true
		}
	}
	return cfg, nil
}

func configureRunEmbeddedMCPStub(cmd *cobra.Command, opts runEmbeddedMCPStubOptions) (*devMCPStubInstance, error) {
	if opts.Enabled && opts.Stateless {
		return nil, fmt.Errorf("--embedded-mcp-stub and --embedded-stateless-mcp-stub are mutually exclusive")
	}
	if !opts.Enabled && !opts.Stateless {
		return nil, nil
	}
	if strings.TrimSpace(opts.UnixSocket) != "" && cmd.Flags().Changed("embedded-mcp-listen-addr") {
		return nil, fmt.Errorf("--embedded-mcp-listen-addr and --embedded-mcp-unix-socket are mutually exclusive")
	}
	embeddedFlag := "embedded-mcp-stub"
	if opts.Stateless {
		embeddedFlag = "embedded-stateless-mcp-stub"
	}
	if targetFlag := explicitMainTargetFlag(cmd, "mcp.command", "mcp-command", "mcp.server-url", "mcp-server-url"); targetFlag != "" {
		return nil, fmt.Errorf("--%s cannot be combined with --%s; use one main MCP target path", embeddedFlag, targetFlag)
	}
	stub, err := startDevMCPStub(devMCPStubOptions{
		Stateless:     opts.Stateless,
		ListenAddr:    opts.ListenAddr,
		UnixSocket:    opts.UnixSocket,
		ServerName:    opts.ServerName,
		ServerVersion: opts.ServerVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("start embedded MCP stub: %w", err)
	}
	if !opts.Stateless {
		if err := cmd.Flags().Set("mcp.server-url", fmt.Sprintf("channel=main,url=%s", stub.MCPURL())); err != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = stub.Shutdown(shutdownCtx)
			return nil, fmt.Errorf("configure embedded MCP stub: %w", err)
		}
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Embedded MCP stub enabled.\n")
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  MCP URL: %s\n", stub.MCPURL())
	if stub.UnixSocket != "" {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  MCP Unix socket: %s\n", stub.UnixSocket)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Protected resource metadata: %s\n", stub.ProtectedResourceMetadataURL())
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Authorization server metadata: %s\n", stub.AuthorizationServerMetadataURL())
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  These are the embedded demo MCP/OAuth endpoints. tunnel-client health/ui URLs are separate and will be logged after startup.\n")
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Try in ChatGPT after the tunnel connects: server_info, echo(\"hello from tunnel-client\"), uppercase(\"openai tunnel\")\n")
	return stub, nil
}

func explicitMainTargetFlag(cmd *cobra.Command, names ...string) string {
	if cmd == nil {
		return ""
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		flag := cmd.Flags().Lookup(name)
		if flag != nil && flag.Changed {
			return name
		}
	}
	return ""
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
		return "run `tunnel-client admin tunnels create --help` or `tunnel-client admin tunnels list --help` to acquire a tunnel id, then materialize a profile with `tunnel-client init`; for the full first-use flow run `tunnel-client help quickstart`"
	}
	if strings.Contains(message, "main channel is required") {
		return "for the shortest demo path run `tunnel-client run --embedded-mcp-stub --control-plane.tunnel-id tunnel_... --health.listen-addr 127.0.0.1:0 --health.url-file /tmp/tunnel-client-health.url`; otherwise create a profile with `tunnel-client init` or run `tunnel-client help quickstart`"
	}
	return "for first-time setup run `tunnel-client help quickstart` or create a profile with `tunnel-client init`"
}
