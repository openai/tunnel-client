package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/openai/tunnel-client/pkg/runtimeapp"
	"github.com/openai/tunnel-client/pkg/runtimecli"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

func newRootCommand(lookupEnv func(string) (string, bool), stdout, stderr io.Writer) *cobra.Command {
	return runtimecli.NewRootCommand(runtimecli.Spec{
		Use:      "tunnel-client-runtime",
		Short:    "Tunnel client customer runtime",
		RunShort: "Run the tunnel client poller",
		Flavor:   runtimeconfig.FlavorRuntime,
		Run: func(cmd *cobra.Command, lookupEnv runtimecli.LookupEnv) error {
			cfg, err := runtimeconfig.LoadFromFlagSet(cmd.Flags(), lookupEnv)
			if err != nil {
				return fmt.Errorf("configure tunnel-client runtime: %w", err)
			}
			app := runtimeapp.New(cfg,
				fx.Provide(func() io.Writer { return cmd.OutOrStdout() }),
				fx.WithLogger(func() fxevent.Logger { return fxevent.NopLogger }),
			)
			return runtimecli.RunFXApp(cmd.Context(), app, "tunnel-client runtime", nil)
		},
	}, lookupEnv, stdout, stderr)
}
