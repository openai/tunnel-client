package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"go.openai.org/api/tunnel-client/pkg/config"
)

func newInitCommand(lookupEnv func(string) (string, bool), stdout io.Writer, stderr io.Writer) *cobra.Command {
	var (
		profileName         string
		sampleName          string
		profileDir          string
		force               bool
		tunnelID            string
		controlPlaneBaseURL string
		controlPlaneAPIKey  string
		mcpServerURL        string
		mcpCommand          string
		healthListenAddr    string
		openWebUI           bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a runnable first-use tunnel-client profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(tunnelID) == "" {
				return fmt.Errorf(
					"tunnel ID is required.\n%s\nUse `tunnel-client admin tunnels create --help` or `tunnel-client admin tunnels list --help` to acquire one, then rerun `tunnel-client init`; for the full first-use flow run `tunnel-client help quickstart`",
					strings.Join(canonicalWebPropertyLines("Canonical setup URLs:"), "\n"),
				)
			}
			name := strings.TrimSpace(profileName)
			if name == "" {
				name = "sample_mcp_with_dcr"
			}
			sample := strings.TrimSpace(sampleName)
			if sample == "" {
				sample = defaultInitSampleName(mcpServerURL, mcpCommand)
			}
			path, dir, err := config.ProfilePath(name, profileDir, lookupEnv)
			if err != nil {
				return err
			}
			data, err := generateProfileSample(sample, sampleProfileRequest{
				TunnelID:         tunnelID,
				BaseURL:          controlPlaneBaseURL,
				APIKeyRef:        controlPlaneAPIKey,
				HealthListenAddr: healthListenAddr,
				OpenBrowser:      openWebUI,
				MCPServerURL:     mcpServerURL,
				MCPCommand:       mcpCommand,
			})
			if err != nil {
				return err
			}
			if err := config.ValidateProfileBytes(path, data); err != nil {
				return err
			}
			if strings.TrimSpace(mcpCommand) != "" {
				if _, err := preflightStdioCommand(mcpCommand); err != nil {
					return fmt.Errorf("mcp-command preflight failed: %w", err)
				}
			}
			if err := writeProfileFile(path, dir, data, force); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Created profile %s at %s\n", name, path)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Sample: %s\n", sample)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Next:\n")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  create or verify `CONTROL_PLANE_API_KEY` in Runtime API keys before `tunnel-client run`\n")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  if you still need tunnel CRUD, create `OPENAI_ADMIN_KEY` separately for `tunnel-client admin tunnels ...`\n")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  tunnel-client doctor --profile %s --explain\n", name)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  tunnel-client run --profile %s\n", name)
			for _, line := range canonicalWebPropertyLines("Canonical setup URLs:") {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Connector runtime note:\n  %s\n", connectorSettingsRuntimeNote(fmt.Sprintf("tunnel-client run --profile %s", name)))
			return nil
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.Flags().StringVar(&profileName, "profile", "sample_mcp_with_dcr", "Profile name to create")
	cmd.Flags().StringVar(&sampleName, "sample", "", "Built-in sample to materialize (auto-selects sample_mcp_stdio_local for --mcp-command, otherwise sample_mcp_with_dcr)")
	cmd.Flags().StringVar(&profileDir, "profile-dir", "", "Profile directory override")
	cmd.Flags().BoolVar(&force, "force", false, "Replace an existing profile")
	cmd.Flags().StringVar(&tunnelID, "tunnel-id", "", "Tunnel ID to write into the generated profile")
	cmd.Flags().StringVar(&controlPlaneBaseURL, "control-plane-base-url", "https://api.openai.com", "Control-plane base URL to write into the generated profile")
	cmd.Flags().StringVar(&controlPlaneAPIKey, "control-plane-api-key-ref", "env:CONTROL_PLANE_API_KEY", "Secret reference for the runtime control-plane API key")
	cmd.Flags().StringVar(&mcpServerURL, "mcp-server-url", "", "MCP server URL for the generated profile")
	cmd.Flags().StringVar(&mcpCommand, "mcp-command", "", "MCP command for the generated profile")
	cmd.Flags().StringVar(&healthListenAddr, "health-listen-addr", defaultInitHealthListenAddr, "Health listener address to write into the generated profile. Use :0 to request an ephemeral port at runtime.")
	cmd.Flags().BoolVar(&openWebUI, "open-web-ui", false, "Set admin_ui.open_browser=true in the generated profile")
	return cmd
}

func defaultInitSampleName(mcpServerURL string, mcpCommand string) string {
	if strings.TrimSpace(mcpCommand) != "" && strings.TrimSpace(mcpServerURL) == "" {
		return "sample_mcp_stdio_local"
	}
	return "sample_mcp_with_dcr"
}
