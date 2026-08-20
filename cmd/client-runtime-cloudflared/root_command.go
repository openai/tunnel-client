package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	cloudflaredruntime "github.com/openai/tunnel-client/pkg/cloudflared/runtime"
	cloudflaredapp "github.com/openai/tunnel-client/pkg/runtimeapp/cloudflared"
	"github.com/openai/tunnel-client/pkg/runtimecli"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

func newRootCommand(lookupEnv func(string) (string, bool), stdout, stderr io.Writer) *cobra.Command {
	return runtimecli.NewRootCommand(runtimecli.Spec{
		Use:      "tunnel-client-runtime-cloudflared",
		Short:    "Tunnel client customer runtime with bundled cloudflared",
		RunShort: "Run the tunnel client poller with bundled cloudflared",
		Flavor:   runtimeconfig.FlavorRuntimeCloudflared,
		Run: func(cmd *cobra.Command, lookupEnv runtimecli.LookupEnv) error {
			cfg, err := runtimeconfig.LoadCloudflaredFromFlagSet(cmd.Flags(), lookupEnv)
			if err != nil {
				return fmt.Errorf("configure tunnel-client runtime-cloudflared: %w", err)
			}
			var supervisor *cloudflaredruntime.Supervisor
			app := cloudflaredapp.New(cfg,
				fx.Provide(func() io.Writer { return cmd.OutOrStdout() }),
				fx.Populate(&supervisor),
				fx.WithLogger(func() fxevent.Logger { return fxevent.NopLogger }),
			)
			var failureCh <-chan error
			if supervisor != nil {
				failureCh = supervisor.Failures()
			}
			return runtimecli.RunFXApp(cmd.Context(), app, "tunnel-client runtime-cloudflared", failureCh)
		},
	}, lookupEnv, stdout, stderr)
}
