package admin

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/openai/tunnel-client/pkg/config"
)

// NewAdminCommand is the top-level admin command that hosts admin/tunnels subcommands.
// It owns admin-scoped flags (admin-key, base-url, json) so child commands stay lean.
func NewAdminCommand(lookupEnv func(string) (string, bool), stdout io.Writer, stderr io.Writer) *cobra.Command {
	if lookupEnv == nil {
		lookupEnv = func(key string) (string, bool) { return "", false }
	}
	cmd := &cobra.Command{
		Use:           "admin",
		Short:         "Administrative operations for the tunnel control plane",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	// Admin-level flags inherited by subcommands.
	config.RegisterAdminFlags(cmd.PersistentFlags())

	cmd.AddCommand(NewTunnelsCommand(lookupEnv, stdout, stderr))
	return cmd
}
