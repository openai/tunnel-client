package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/openai/tunnel-client/pkg/codexplugin"
)

func newPluginCommand(lookupEnv func(string) (string, bool), stdout io.Writer, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "plugin",
		Short:  "Compatibility alias for `tunnel-client codex plugin`",
		Hidden: true,
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.AddCommand(newPluginCodexAliasCommand(lookupEnv, stdout, stderr))
	return cmd
}

func newPluginCodexAliasCommand(lookupEnv func(string) (string, bool), stdout io.Writer, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "codex",
		Short:  "Compatibility alias for `tunnel-client codex plugin`",
		Hidden: true,
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.AddCommand(newCodexPluginInstallCommand(lookupEnv, stdout, stderr))
	cmd.AddCommand(newCodexPluginUninstallCommand(lookupEnv, stdout, stderr))
	cmd.AddCommand(newCodexPluginExportCommand(stdout, stderr))
	return cmd
}

func newCodexPluginCommand(lookupEnv func(string) (string, bool), stdout io.Writer, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Install, uninstall, or export the embedded Codex plugin bundle",
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.AddCommand(newCodexPluginInstallCommand(lookupEnv, stdout, stderr))
	cmd.AddCommand(newCodexPluginUninstallCommand(lookupEnv, stdout, stderr))
	cmd.AddCommand(newCodexPluginExportCommand(stdout, stderr))
	return cmd
}

func newCodexPluginInstallCommand(lookupEnv func(string) (string, bool), stdout io.Writer, stderr io.Writer) *cobra.Command {
	var codexHome string
	var marketplace string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the embedded Tunnel MCP Codex plugin into CODEX_HOME",
		RunE: func(cmd *cobra.Command, args []string) error {
			home := codexHome
			if home == "" {
				home = codexplugin.ResolveCodexHome(lookupEnv)
			}
			detection, err := codexplugin.InstallForMarketplace(home, marketplace, currentExecutablePath())
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Installed %s into %s\n", detection.PluginName, detection.PluginDir)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Updated Codex config %s\n", detection.ConfigPath)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Persisted binary hint %s\n", detection.PluginBinaryHintPath)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Next:")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), `  tunnel-client codex assistant "Summarize the current tunnel setup."`)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  tunnel-client help plugin")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  test -f %s\n", detection.PluginDir+"/.codex-plugin/plugin.json")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  test -f %s\n", detection.PluginBinaryHintPath)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  start a new Codex session if plugins were already loaded")
			return nil
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.Flags().StringVar(&codexHome, "codex-home", "", "Override CODEX_HOME for plugin installation")
	cmd.Flags().StringVar(&marketplace, "marketplace", "debug", "Codex plugin marketplace cache key to install into")
	return cmd
}

func newCodexPluginExportCommand(stdout io.Writer, stderr io.Writer) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the embedded Tunnel MCP Codex plugin bundle to disk",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(dir) == "" {
				return fmt.Errorf("--dir is required")
			}
			if err := codexplugin.Export(dir, currentExecutablePath()); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Exported embedded Codex plugin bundle to %s\n", dir)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Persisted binary hint %s\n", dir+"/.tunnel-client-bin")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Next:")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s/scripts/tunnel_mcp self-check\n", dir)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  macOS/Linux: cd %s && sh scripts/install_plugin.sh --tunnel-client-bin /path/to/tunnel-client\n", dir)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Windows PowerShell: Set-Location %s; powershell -NoProfile -ExecutionPolicy Bypass -File .\\scripts\\Install-Plugin.ps1 --tunnel-client-bin C:\\path\\to\\tunnel-client.exe\n", dir)
			return nil
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.Flags().StringVar(&dir, "dir", "", "Destination directory")
	return cmd
}

func newCodexPluginUninstallCommand(lookupEnv func(string) (string, bool), stdout io.Writer, stderr io.Writer) *cobra.Command {
	var codexHome string
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the embedded Tunnel MCP Codex plugin from CODEX_HOME",
		RunE: func(cmd *cobra.Command, args []string) error {
			home := codexHome
			if home == "" {
				home = codexplugin.ResolveCodexHome(lookupEnv)
			}
			result, err := codexplugin.Uninstall(home)
			if err != nil {
				return err
			}
			if !result.RemovedPluginDir && !result.RemovedConfigSection {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Tunnel MCP plugin is not installed in %s\n", home)
				return nil
			}
			if result.RemovedPluginDir {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Removed on-disk %s plugin bundle from %s\n", result.PluginName, result.PluginDir)
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "No on-disk %s plugin bundle found in %s\n", result.PluginName, result.PluginDir)
			}
			if result.RemovedConfigSection {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Removed on-disk Codex config section from %s\n", result.ConfigPath)
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "No matching on-disk Codex config section found in %s\n", result.ConfigPath)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Next:")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  tunnel-client codex plugin install")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  restart any existing Codex session if the plugin was already loaded")
			return nil
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.Flags().StringVar(&codexHome, "codex-home", "", "Override CODEX_HOME for plugin removal")
	return cmd
}

func currentExecutablePath() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	return path
}
