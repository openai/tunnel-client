package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	admincmd "github.com/openai/tunnel-client/cmd/client/admin"
	"github.com/openai/tunnel-client/pkg/version"
)

func newRootCommand(lookupEnv func(string) (string, bool), stdout io.Writer, stderr io.Writer) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "tunnel-client",
		Short:         "Tunnel client for the OpenAI MCP control plane",
		Long:          rootCommandLong(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)

	rootCmd.Version = tunnelClientVersion()
	rootCmd.SetVersionTemplate("{{.Version}}\n")

	rootCmd.AddCommand(newInitCommand(lookupEnv, stdout, stderr))
	rootCmd.AddCommand(newDoctorCommand(lookupEnv, stdout, stderr))
	rootCmd.AddCommand(newHealthCommand(stdout, stderr))
	rootCmd.AddCommand(newRunCommand(lookupEnv))
	rootCmd.AddCommand(newDevCommand(stdout, stderr))
	rootCmd.AddCommand(newCodexCommand(lookupEnv, stdout, stderr))
	rootCmd.AddCommand(newProfilesCommand(lookupEnv, stdout, stderr))
	rootCmd.AddCommand(newAdminProfilesCommand(lookupEnv, stdout, stderr))
	rootCmd.AddCommand(newRuntimesCommand(lookupEnv, stdout, stderr))
	rootCmd.AddCommand(newPluginCommand(lookupEnv, stdout, stderr))
	rootCmd.AddCommand(admincmd.NewAdminCommand(lookupEnv, stdout, stderr))
	rootCmd.SetHelpCommand(newHelpCommand(rootCmd, stdout, stderr))

	return rootCmd
}

func tunnelClientVersion() string {
	if version.GitSHA != "" {
		return fmt.Sprintf("%s (git sha: %s)", version.Version, version.GitSHA)
	}
	return version.Version
}
