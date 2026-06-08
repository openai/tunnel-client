package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"go.openai.org/api/tunnel-client/pkg/codexplugin"
	"go.openai.org/api/tunnel-client/pkg/codexplugin/session"
)

type runtimesCommonFlags struct {
	adminProfileName    string
	adminKeyRef         string
	controlPlaneBaseURL string
	controlPlaneURLPath string
	jsonOutput          bool
}

func newRuntimesCommand(lookupEnv func(string) (string, bool), stdout io.Writer, stderr io.Writer) *cobra.Command {
	return newRuntimesCommandWithRuntime(lookupEnv, stdout, stderr, session.DefaultRuntime())
}

func newRuntimesCommandWithRuntime(lookupEnv func(string) (string, bool), stdout io.Writer, stderr io.Writer, runtime session.Runtime) *cobra.Command {
	manager := codexplugin.NewManager(lookupEnv, runtime)
	common := &runtimesCommonFlags{}

	cmd := &cobra.Command{
		Use:   "runtimes",
		Short: "Manage native tunnel-client runtimes",
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.PersistentFlags().StringVar(&common.adminProfileName, "admin-profile", "", "Admin profile name to use")
	cmd.PersistentFlags().StringVar(&common.adminKeyRef, "admin-key", "", "Admin key reference to store/use, using env:NAME or file:/path")
	cmd.PersistentFlags().StringVar(&common.controlPlaneBaseURL, "control-plane-base-url", "", "Control-plane base URL")
	cmd.PersistentFlags().StringVar(&common.controlPlaneURLPath, "control-plane-url-path", "", "Optional URL path appended to the control-plane base URL")
	cmd.PersistentFlags().BoolVar(&common.jsonOutput, "json", false, "Emit JSON output")

	var createName string
	var createDescription string
	var createOrgIDs []string
	var createWorkspaceIDs []string
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create or reuse a remote tunnel alias",
		RunE: func(cmd *cobra.Command, args []string) error {
			alias, err := cmd.Flags().GetString("alias")
			if err != nil {
				return err
			}
			payload, err := manager.Create(codexplugin.CreateOptions{
				Alias:               alias,
				Name:                createName,
				Description:         createDescription,
				AdminProfileName:    common.adminProfileName,
				AdminKeyRef:         common.adminKeyRef,
				ControlPlaneBaseURL: common.controlPlaneBaseURL,
				ControlPlaneURLPath: common.controlPlaneURLPath,
				OrganizationIDs:     createOrgIDs,
				WorkspaceIDs:        createWorkspaceIDs,
			})
			if err != nil {
				return err
			}
			if common.jsonOutput {
				return writeJSON(cmd.OutOrStdout(), payload)
			}
			tunnel := payload["tunnel"].(map[string]any)
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Created alias %s for tunnel %s\n", payload["alias"], tunnel["id"])
			return err
		},
	}
	createCmd.Flags().String("alias", "", "Alias to create")
	createCmd.Flags().StringVar(&createName, "name", "", "Remote tunnel name override")
	createCmd.Flags().StringVar(&createDescription, "description", "", "Remote tunnel description override")
	createCmd.Flags().StringSliceVar(&createOrgIDs, "organization-id", nil, "Organization scope for remote tunnel creation")
	createCmd.Flags().StringSliceVar(&createWorkspaceIDs, "workspace-id", nil, "Workspace scope for remote tunnel creation")
	_ = createCmd.MarkFlagRequired("alias")
	cmd.AddCommand(createCmd)

	var connectName string
	var connectDescription string
	var connectOrgIDs []string
	var connectWorkspaceIDs []string
	var tunnelID string
	var profileName string
	var profileDir string
	var mcpServerURL string
	var mcpCommand string
	var runtimeAPIKey string
	var tunnelClientBin string
	connectCmd := &cobra.Command{
		Use:   "connect",
		Short: "Create or reuse a tunnel alias and run a native profile locally",
		Long: "Create or reuse a tunnel alias and run a native profile through tunnel-client's managed local runtime supervision.\n\n" +
			"For a long-lived local runtime managed by Codex, use this command instead of nohup or disown. " +
			"After connect, run `tunnel-client runtimes status <alias>` before reporting success.",
		RunE: func(cmd *cobra.Command, args []string) error {
			alias, err := cmd.Flags().GetString("alias")
			if err != nil {
				return err
			}
			payload, runErr := manager.Connect(codexplugin.ConnectOptions{
				CreateOptions: codexplugin.CreateOptions{
					Alias:               alias,
					Name:                connectName,
					Description:         connectDescription,
					AdminProfileName:    common.adminProfileName,
					AdminKeyRef:         common.adminKeyRef,
					ControlPlaneBaseURL: common.controlPlaneBaseURL,
					ControlPlaneURLPath: common.controlPlaneURLPath,
					OrganizationIDs:     connectOrgIDs,
					WorkspaceIDs:        connectWorkspaceIDs,
				},
				TunnelID:      tunnelID,
				ProfileName:   profileName,
				ProfileDir:    profileDir,
				MCPServerURL:  mcpServerURL,
				MCPCommand:    mcpCommand,
				RuntimeAPIKey: runtimeAPIKey,
				TunnelBin:     chooseTunnelClientBin(tunnelClientBin),
			})
			if runErr != nil {
				return maybeWritePayloadError(cmd, common.jsonOutput, payload, runErr)
			}
			if common.jsonOutput {
				return writeJSON(cmd.OutOrStdout(), payload)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Connected %s using %s mode\n", payload["alias"], payload["mode"])
			return err
		},
	}
	connectCmd.Flags().String("alias", "", "Alias to connect")
	connectCmd.Flags().StringVar(&connectName, "name", "", "Remote tunnel name override")
	connectCmd.Flags().StringVar(&connectDescription, "description", "", "Remote tunnel description override")
	connectCmd.Flags().StringSliceVar(&connectOrgIDs, "organization-id", nil, "Organization scope for remote tunnel lookup/creation")
	connectCmd.Flags().StringSliceVar(&connectWorkspaceIDs, "workspace-id", nil, "Workspace scope for remote tunnel lookup/creation")
	connectCmd.Flags().StringVar(&tunnelID, "tunnel-id", "", "Attach to an existing tunnel id without admin CRUD")
	connectCmd.Flags().StringVar(&profileName, "profile", "", "Profile name to write and run")
	connectCmd.Flags().StringVar(&profileDir, "profile-dir", "", "Directory for generated native profiles")
	connectCmd.Flags().StringVar(&mcpServerURL, "mcp-server-url", "", "Remote MCP server URL")
	connectCmd.Flags().StringVar(&mcpCommand, "mcp-command", "", "Local stdio MCP command")
	connectCmd.Flags().StringVar(&runtimeAPIKey, "runtime-api-key", "", "Runtime key reference to store in generated config")
	connectCmd.Flags().StringVar(&tunnelClientBin, "tunnel-client-bin", "", "Override the tunnel-client binary path used for the launched runtime")
	_ = connectCmd.MarkFlagRequired("alias")
	cmd.AddCommand(connectCmd)

	var listOrgIDs []string
	var listWorkspaceIDs []string
	var listTenantID string
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List local aliases and optionally remote scoped tunnels",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateRuntimeListScope(listOrgIDs, listWorkspaceIDs, listTenantID); err != nil {
				return err
			}
			payload, err := manager.ListRuntimes(codexplugin.ListOptions{
				AdminProfileName:    common.adminProfileName,
				AdminKeyRef:         common.adminKeyRef,
				ControlPlaneBaseURL: common.controlPlaneBaseURL,
				ControlPlaneURLPath: common.controlPlaneURLPath,
				OrganizationIDs:     listOrgIDs,
				WorkspaceIDs:        listWorkspaceIDs,
				TenantID:            listTenantID,
			})
			if err != nil {
				return err
			}
			if common.jsonOutput {
				return writeJSON(cmd.OutOrStdout(), payload)
			}
			aliases, _ := payload["aliases"].([]map[string]any)
			if len(aliases) == 0 {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "No runtime aliases found in %s\n", payload["state_root"])
				return err
			}
			for _, alias := range aliases {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", alias["alias"], alias["tunnel_id"]); err != nil {
					return err
				}
			}
			return nil
		},
	}
	listCmd.Flags().StringSliceVar(&listOrgIDs, "organization-id", nil, "Organization scope for remote listing")
	listCmd.Flags().StringSliceVar(&listWorkspaceIDs, "workspace-id", nil, "Workspace scope for remote listing")
	listCmd.Flags().StringVar(&listTenantID, "tenant-id", "", "Tenant scope for remote listing")
	cmd.AddCommand(listCmd)

	var cleanupApply bool
	cleanupCmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Inspect and optionally remove stale local runtime alias metadata",
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := manager.CleanupInventory(codexplugin.CleanupOptions{Apply: cleanupApply})
			if err != nil {
				return err
			}
			if common.jsonOutput {
				return writeJSON(cmd.OutOrStdout(), payload)
			}
			entries, _ := payload["entries"].([]map[string]any)
			if len(entries) == 0 {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "No runtime aliases found in %s\n", payload["state_root"])
				return err
			}
			for _, entry := range entries {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", entry["alias"], entry["classification"], entry["tunnel_id"]); err != nil {
					return err
				}
			}
			if !cleanupApply {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "Dry run only. Re-run with --apply to remove entries classified as stale_alias.")
			}
			return err
		},
	}
	cleanupCmd.Flags().BoolVar(&cleanupApply, "apply", false, "Remove only entries classified as stale_alias")
	cmd.AddCommand(cleanupCmd)

	statusCmd := &cobra.Command{
		Use:   "status <alias>",
		Short: "Inspect a local alias and its runtime state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := manager.Status(codexplugin.AliasOptions{
				Alias:               args[0],
				AdminProfileName:    common.adminProfileName,
				AdminKeyRef:         common.adminKeyRef,
				ControlPlaneBaseURL: common.controlPlaneBaseURL,
				ControlPlaneURLPath: common.controlPlaneURLPath,
			})
			if err != nil {
				return maybeWritePayloadError(cmd, common.jsonOutput, payload, err)
			}
			if common.jsonOutput {
				return writeJSON(cmd.OutOrStdout(), payload)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", payload["alias"], payload["runtime_state"], payload["tunnel_id"])
			return err
		},
	}
	cmd.AddCommand(statusCmd)

	stopCmd := &cobra.Command{
		Use:     "stop <alias>",
		Aliases: []string{"disconnect"},
		Short:   "Stop the managed local runtime for an alias",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := manager.Stop(codexplugin.AliasOptions{
				Alias:               args[0],
				AdminProfileName:    common.adminProfileName,
				AdminKeyRef:         common.adminKeyRef,
				ControlPlaneBaseURL: common.controlPlaneBaseURL,
				ControlPlaneURLPath: common.controlPlaneURLPath,
			})
			if err != nil {
				return maybeWritePayloadError(cmd, common.jsonOutput, payload, err)
			}
			if common.jsonOutput {
				return writeJSON(cmd.OutOrStdout(), payload)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Stopped %s\n", args[0])
			return err
		},
	}
	cmd.AddCommand(stopCmd)

	removeCmd := &cobra.Command{
		Use:     "rm <alias>",
		Aliases: []string{"remove"},
		Short:   "Remove local runtime metadata without deleting the remote tunnel",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := manager.Remove(codexplugin.AliasOptions{Alias: args[0]})
			if err != nil {
				return err
			}
			if common.jsonOutput {
				return writeJSON(cmd.OutOrStdout(), payload)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Removed local runtime metadata for %s\n", args[0])
			return err
		},
	}
	cmd.AddCommand(removeCmd)

	return cmd
}

func validateRuntimeListScope(organizationIDs, workspaceIDs []string, tenantID string) error {
	filterCount := 0
	if len(organizationIDs) > 0 {
		filterCount++
	}
	if len(workspaceIDs) > 0 {
		filterCount++
	}
	if tenantID != "" {
		filterCount++
	}
	if filterCount > 1 {
		return errors.New("runtimes list accepts exactly one remote scope family: --organization-id, --workspace-id, or --tenant-id")
	}
	if len(organizationIDs) > 1 {
		return errors.New("runtimes list accepts at most one --organization-id for remote listing")
	}
	if len(workspaceIDs) > 1 {
		return errors.New("runtimes list accepts at most one --workspace-id for remote listing")
	}
	return nil
}

func maybeWritePayloadError(cmd *cobra.Command, jsonOutput bool, payload map[string]any, err error) error {
	var payloadErr *codexplugin.PayloadError
	if errors.As(err, &payloadErr) {
		if jsonOutput && payload != nil {
			if writeErr := writeJSON(cmd.OutOrStdout(), payload); writeErr != nil {
				return writeErr
			}
			return silentExitError{code: payloadErr.Code}
		}
		if payload != nil {
			if value, ok := payload["error"].(string); ok && value != "" {
				return fmt.Errorf("%s", value)
			}
			if value, ok := payload["remote_error"].(string); ok && value != "" {
				return fmt.Errorf("%s", value)
			}
		}
		return silentExitError{code: payloadErr.Code}
	}
	return err
}

func chooseTunnelClientBin(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if current := currentExecutablePath(); current != "" {
		return current
	}
	return "tunnel-client"
}
