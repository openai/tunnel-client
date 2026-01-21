package main

import (
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"go.openai.org/api/tunnel-client/pkg/app"
	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/controlplane"
	"go.openai.org/api/tunnel-client/pkg/version"
)

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
			for _, metadataURL := range l.mcpConfig.OAuthResourceMetadataURLs {
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
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run the tunnel client poller",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTunnel(cmd, lookupEnv)
		},
	}
	config.RegisterFlags(runCmd.PersistentFlags())

	runCmd.SetUsageFunc(func(cmd *cobra.Command) error {
		config.WriteUsage(runCmd.PersistentFlags(), cmd.OutOrStdout())
		return nil
	})
	runCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		config.WriteUsage(runCmd.PersistentFlags(), cmd.OutOrStdout())
	})
	return runCmd
}

func runTunnel(cmd *cobra.Command, lookupEnv func(string) (string, bool)) error {
	cfg, err := config.LoadFromFlagSet(cmd.Flags(), lookupEnv)
	if err != nil {
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
