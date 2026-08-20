// Package runtimecli owns the command surface shared by the narrow runtime
// binaries. It deliberately depends only on runtime-safe packages so flavor
// entrypoints can stay thin without importing the full client command tree.
package runtimecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/version"
)

// LookupEnv is the environment lookup contract used by runtime configuration.
type LookupEnv func(string) (string, bool)

// Spec describes the small flavor-specific portion of one runtime command.
type Spec struct {
	Use      string
	Short    string
	RunShort string
	Flavor   runtimeconfig.Flavor
	Run      func(*cobra.Command, LookupEnv) error
}

// NewRootCommand constructs the deliberately narrow command tree shared by
// runtime and runtime-cloudflared. The supplied callback owns only flavor
// configuration and app construction.
func NewRootCommand(spec Spec, lookupEnv LookupEnv, stdout, stderr io.Writer) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           spec.Use,
		Short:         spec.Short,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	rootCmd.CompletionOptions.DisableDefaultCmd = true
	rootCmd.Version = Version(string(spec.Flavor))
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	disableImplicitHelpCommand(rootCmd)

	runCmd := &cobra.Command{
		Use:   "run",
		Short: spec.RunShort,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if spec.Run == nil {
				return errors.New("runtime command callback is nil")
			}
			return spec.Run(cmd, lookupEnv)
		},
	}
	runtimeconfig.RegisterFlags(runCmd.Flags(), spec.Flavor)
	runCmd.SetUsageFunc(func(cmd *cobra.Command) error {
		runtimeconfig.WriteUsage(runCmd.Flags(), cmd.OutOrStdout())
		return nil
	})
	runCmd.SetHelpFunc(func(cmd *cobra.Command, _ []string) {
		runtimeconfig.WriteUsage(runCmd.Flags(), cmd.OutOrStdout())
	})
	rootCmd.AddCommand(runCmd)
	return rootCmd
}

// RunFXApp starts one runtime Fx app, waits for cancellation, Fx shutdown, or
// an optional companion failure, and then stops the app with its configured
// timeout.
func RunFXApp(ctx context.Context, app *fx.App, label string, failureCh <-chan error) error {
	if app == nil {
		return fmt.Errorf("%s app is nil", label)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	startCtx, startCancel := context.WithTimeout(ctx, app.StartTimeout())
	defer startCancel()
	if err := app.Start(startCtx); err != nil {
		return fmt.Errorf("start %s: %w", label, err)
	}
	var runErr error
	select {
	case <-ctx.Done():
	case shutdown := <-app.Wait():
		if shutdown.ExitCode != 0 {
			runErr = fmt.Errorf("%s shutdown requested with exit code %d", label, shutdown.ExitCode)
		}
	case err := <-failureCh:
		if err != nil {
			runErr = fmt.Errorf("cloudflared supervision failed: %w", err)
		}
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), app.StopTimeout())
	defer stopCancel()
	stopErr := app.Stop(stopCtx)
	if stopErr != nil {
		stopErr = fmt.Errorf("stop %s: %w", label, stopErr)
	}
	return errors.Join(runErr, stopErr)
}

// Version renders static build identity for one runtime flavor without
// starting subprocesses or consulting a checkout.
func Version(flavor string) string {
	metadata := version.CurrentBuildMetadata()
	if metadata.Flavor != flavor {
		return strings.Join([]string{
			"invalid runtime build metadata:",
			"linked-flavor=" + metadata.Flavor,
			"expected=" + flavor,
		}, " ")
	}
	parts := []string{metadata.SemanticVersion}
	if metadata.GitSHA != "" {
		parts = append(parts, "git sha: "+metadata.GitSHA)
	}
	if metadata.GoVersion != "" {
		parts = append(parts, "go: "+metadata.GoVersion)
	}
	if metadata.BuildFlags != "" {
		parts = append(parts, "build flags: "+metadata.BuildFlags)
	}
	parts = append(parts, "flavor="+metadata.Flavor)
	return strings.Join(parts, " ")
}

// Cobra adds a visible help subcommand whenever a command has children.
// Runtime artifacts intentionally expose only run; --help remains available
// as the flag-based help surface.
func disableImplicitHelpCommand(rootCmd *cobra.Command) {
	rootCmd.SetHelpCommand(&cobra.Command{
		Use:    "__runtime_help_disabled",
		Hidden: true,
	})
}
