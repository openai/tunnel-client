package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	admincmd "go.openai.org/api/tunnel-client/cmd/client/admin"
	"go.openai.org/api/tunnel-client/pkg/version"
)

func newRootCommand(lookupEnv func(string) (string, bool), stdout io.Writer, stderr io.Writer) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "tunnel-client",
		Short:         "Tunnel client for the OpenAI MCP control plane",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)

	rootCmd.Version = tunnelClientVersion()
	rootCmd.SetVersionTemplate("{{.Version}}\n")

	rootCmd.AddCommand(newRunCommand(lookupEnv))
	rootCmd.AddCommand(admincmd.NewAdminCommand(lookupEnv, stdout, stderr))

	return rootCmd
}

func tunnelClientVersion() string {
	if version.GitSHA != "" {
		return fmt.Sprintf("%s (git sha: %s)", version.Version, version.GitSHA)
	}
	return version.Version
}
