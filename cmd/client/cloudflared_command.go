package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/openai/tunnel-client/pkg/cloudflared"
)

func newCloudflaredCommand(stdout io.Writer, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cloudflared",
		Short: "Inspect the bundled Cloudflare Tunnel companion",
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	manifest := cloudflared.BundledManifest()
	cmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print bundled cloudflared pin and provenance",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(
				cmd.OutOrStdout(),
				"bundled cloudflared %s\nmodule: %s@%s\nrelease commit: %s\nsecurity patch owner: %s\n",
				manifest.Version,
				manifest.ModulePath,
				manifest.ModuleVersion,
				manifest.ReleaseCommit,
				manifest.SecurityPatchOwner,
			)
			return err
		},
	})
	cmd.AddCommand(newCloudflaredConfigCommand())
	return cmd
}

func newCloudflaredConfigCommand() *cobra.Command {
	cfg := cloudflared.DefaultStandaloneConfig("")
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Print a token-free production cloudflared config",
		Long:  "Print a production-ready cloudflared YAML config for operators who run cloudflared directly without tunnel-client. The output references a token file path but never reads or embeds the token.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rendered, err := cloudflared.RenderStandaloneConfig(cfg)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(rendered)
			return err
		},
	}
	cmd.Flags().StringVar(&cfg.TokenFile, "token-file", "", "Path cloudflared should read the remotely managed tunnel token from; the token value is never read or emitted")
	cmd.Flags().StringVar(&cfg.MetricsAddress, "metrics-address", cfg.MetricsAddress, "Cloudflared Prometheus and readiness listener address")
	cmd.Flags().StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "Cloudflared application log level (debug, info, warn, error, fatal)")
	cmd.Flags().StringVar(&cfg.TransportLogLevel, "transport-log-level", cfg.TransportLogLevel, "Cloudflared transport log level (debug, info, warn, error, fatal)")
	cmd.Flags().StringVar(&cfg.Protocol, "protocol", cfg.Protocol, "Cloudflared edge transport protocol (auto, http2, quic)")
	cmd.Flags().StringVar(&cfg.EdgeIPVersion, "edge-ip-version", cfg.EdgeIPVersion, "Cloudflared edge IP mode (auto, 4, 6)")
	cmd.Flags().IntVar(&cfg.Retries, "retries", cfg.Retries, "Maximum cloudflared connection/protocol retries")
	cmd.Flags().DurationVar(&cfg.GracePeriod, "grace-period", cfg.GracePeriod, "Graceful shutdown budget for in-flight requests")
	return cmd
}
