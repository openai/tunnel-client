package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/controlplane/admin"
)

const defaultAdminRequestTimeout = 30 * time.Second

func NewTunnelsCommand(lookupEnv func(string) (string, bool), stdout io.Writer, stderr io.Writer) *cobra.Command {
	if lookupEnv == nil {
		lookupEnv = func(key string) (string, bool) { return "", false }
	}

	tunnelsCmd := &cobra.Command{
		Use:           "tunnels",
		Short:         "Manage tunnels via the control-plane admin API",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	tunnelsCmd.SetOut(stdout)
	tunnelsCmd.SetErr(stderr)

	tunnelsCmd.AddCommand(
		newTunnelCreateCmd(lookupEnv),
		newTunnelGetCmd(lookupEnv),
		newTunnelListCmd(lookupEnv),
		newTunnelUpdateCmd(lookupEnv),
		newTunnelDeleteCmd(lookupEnv),
	)

	return tunnelsCmd
}

func newTunnelCreateCmd(lookupEnv func(string) (string, bool)) *cobra.Command {
	var name, description string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a tunnel",
		Long:  "Create a tunnel with organization/workspace attachments. At least one org or workspace ID is required.",
		Example: strings.TrimSpace(`
  # Create with org + workspace scope
  tunnel-client tunnels create \
    --name "My Tunnel" \
    --description "Routes to prod MCP" \
    --organization-id org_123 \
    --workspace-id ws_456

  # Create scoped only to a workspace
  tunnel-client tunnels create \
    --name "Workspace Only" \
    --description "WS-only tunnel" \
    --workspace-id ws_456
`),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" {
				return errors.New("name is required (set --name)")
			}
			if strings.TrimSpace(description) == "" {
				return errors.New("description is required (set --description)")
			}

			client, cfg, err := adminClientFromCmd(cmd, lookupEnv)
			if err != nil {
				return err
			}
			if len(cfg.OrganizationIDs) == 0 && len(cfg.WorkspaceIDs) == 0 {
				return errors.New("at least one of --organization-id or --workspace-id is required")
			}

			req := admin.TunnelCreateRequest{
				Name:            name,
				Description:     description,
				WorkspaceIDs:    cfg.WorkspaceIDs,
				OrganizationIDs: cfg.OrganizationIDs,
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), defaultAdminRequestTimeout)
			defer cancel()

			t, err := client.CreateTunnel(ctx, req)
			if err != nil {
				return err
			}
			return printTunnel(cmd, t)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Name for the tunnel (required)")
	cmd.Flags().StringVar(&description, "description", "", "Description for the tunnel (required)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("description")
	requireOrgsOrWorkspaces(cmd)

	return cmd
}

func newTunnelGetCmd(lookupEnv func(string) (string, bool)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <tunnel_id>",
		Short: "Fetch a tunnel by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := adminClientFromCmd(cmd, lookupEnv)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), defaultAdminRequestTimeout)
			defer cancel()

			t, err := client.GetTunnel(ctx, args[0])
			if err != nil {
				return err
			}
			return printTunnel(cmd, t)
		},
	}
	return cmd
}

func newTunnelListCmd(lookupEnv func(string) (string, bool)) *cobra.Command {
	var tenantID string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tunnels filtered by organization or workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := adminClientFromCmd(cmd, lookupEnv)
			if err != nil {
				return err
			}

			// Enforce exactly one filter to mirror API expectations and keep scope explicit.
			filterCount := 0
			if len(cfg.OrganizationIDs) > 0 {
				filterCount++
			}
			if len(cfg.WorkspaceIDs) > 0 {
				filterCount++
			}
			if tenantID != "" {
				filterCount++
			}
			if filterCount != 1 {
				return errors.New("provide exactly one of --organization-id, --workspace-id, or --tenant-id")
			}

			orgID := first(cfg.OrganizationIDs)
			wsID := first(cfg.WorkspaceIDs)

			ctx, cancel := context.WithTimeout(cmd.Context(), defaultAdminRequestTimeout)
			defer cancel()

			resp, err := client.ListTunnels(ctx, orgID, wsID, tenantID)
			if err != nil {
				return err
			}
			return printTunnelList(cmd, resp)
		},
	}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Tenant identifier to filter tunnels by (optional)")
	requireOrgsOrWorkspaces(cmd)
	return cmd
}

func newTunnelUpdateCmd(lookupEnv func(string) (string, bool)) *cobra.Command {
	var name, description string
	cmd := &cobra.Command{
		Use:   "update <tunnel_id>",
		Short: "Update a tunnel",
		Long: "Update a tunnel; fields you set replace existing values.\n" +
			"Organization/workspace lists are PUT-like replacements; omit the flags to keep existing edges.",
		Example: strings.TrimSpace(`
  # Rename a tunnel and keep existing org/workspace edges
  tunnel-client tunnels update tunnel_abc --name "New Name"

  # Replace org/workspace scopes explicitly (PUT-like)
  tunnel-client tunnels update tunnel_abc \
    --organization-id org_123 \
    --workspace-id ws_456 \
    --description "New description"
`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := adminClientFromCmd(cmd, lookupEnv)
			if err != nil {
				return err
			}

			updateReq := admin.TunnelUpdateRequest{}
			if cmd.Flags().Changed("name") {
				updateReq.Name = strPtr(strings.TrimSpace(name))
			}
			if cmd.Flags().Changed("description") {
				updateReq.Description = strPtr(description)
			}
			if cmd.Flags().Changed("organization-id") {
				orgs := make([]string, 0, len(cfg.OrganizationIDs))
				orgs = append(orgs, cfg.OrganizationIDs...)
				updateReq.OrganizationIDs = &orgs
			}
			if cmd.Flags().Changed("workspace-id") {
				wss := make([]string, 0, len(cfg.WorkspaceIDs))
				wss = append(wss, cfg.WorkspaceIDs...)
				updateReq.WorkspaceIDs = &wss
			}

			if updateReq.Name == nil && updateReq.Description == nil && updateReq.OrganizationIDs == nil && updateReq.WorkspaceIDs == nil {
				return errors.New("provide at least one field to update")
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), defaultAdminRequestTimeout)
			defer cancel()

			t, err := client.UpdateTunnel(ctx, args[0], updateReq)
			if err != nil {
				return err
			}
			return printTunnel(cmd, t)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "New name (optional; omit to keep current)")
	cmd.Flags().StringVar(&description, "description", "", "New description (optional; omit to keep current)")
	requireOrgsOrWorkspaces(cmd)
	return cmd
}

func newTunnelDeleteCmd(lookupEnv func(string) (string, bool)) *cobra.Command {
	var confirm bool
	cmd := &cobra.Command{
		Use:   "delete <tunnel_id>",
		Short: "Delete a tunnel (requires --confirm)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirm {
				return errors.New("refusing to delete without --confirm")
			}

			client, _, err := adminClientFromCmd(cmd, lookupEnv)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), defaultAdminRequestTimeout)
			defer cancel()

			t, err := client.DeleteTunnel(ctx, args[0])
			if err != nil {
				return err
			}
			return printTunnel(cmd, t)
		},
	}
	cmd.Flags().BoolVar(&confirm, "confirm", false, "Required to delete a tunnel")
	return cmd
}

func adminClientFromCmd(cmd *cobra.Command, lookupEnv func(string) (string, bool)) (*admin.AdminTunnelClient, *config.AdminConfig, error) {
	fs := mergedFlagSet(cmd)
	cfg, err := config.LoadAdminConfig(fs, lookupEnv)
	if err != nil {
		return nil, nil, err
	}
	client, err := admin.NewAdminTunnelClient(cfg)
	if err != nil {
		return nil, nil, err
	}
	return client, cfg, nil
}

func mergedFlagSet(cmd *cobra.Command) *pflag.FlagSet {
	fs := pflag.NewFlagSet(cmd.Name(), pflag.ContinueOnError)
	fs.AddFlagSet(cmd.Flags())
	fs.AddFlagSet(cmd.InheritedFlags())
	return fs
}

func printTunnel(cmd *cobra.Command, t *admin.Tunnel) error {
	if t == nil {
		return nil
	}
	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(t)
	}

	builder := strings.Builder{}
	builder.WriteString(fmt.Sprintf("Tunnel %s\n", t.ID))
	builder.WriteString(fmt.Sprintf("  Name: %s\n", t.Name))
	builder.WriteString(fmt.Sprintf("  Description: %s\n", t.Description))
	if t.Creator != "" {
		builder.WriteString(fmt.Sprintf("  Creator: %s\n", t.Creator))
	}
	builder.WriteString(fmt.Sprintf("  Organizations: %s\n", strings.Join(t.OrganizationIDs, ", ")))
	builder.WriteString(fmt.Sprintf("  Workspaces: %s\n", strings.Join(t.WorkspaceIDs, ", ")))
	if len(t.TenantIDs) > 0 {
		builder.WriteString(fmt.Sprintf("  Tenants: %s\n", strings.Join(t.TenantIDs, ", ")))
	}

	_, err := fmt.Fprint(cmd.OutOrStdout(), builder.String())
	return err
}

func printTunnelList(cmd *cobra.Command, list *admin.TunnelListResponse) error {
	if list == nil {
		return nil
	}
	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(list)
	}

	for _, t := range list.Tunnels {
		if err := printTunnel(cmd, &t); err != nil {
			return err
		}
	}
	return nil
}

func first(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return items[0]
}

func strPtr(s string) *string {
	return &s
}

func requireOrgsOrWorkspaces(cmd *cobra.Command) {
	cmd.Flags().StringSlice("organization-id", nil, "Organization identifier(s) used for scope and tunnel attachment (repeatable)")
	cmd.Flags().StringSlice("workspace-id", nil, "Workspace identifier(s) used for scope and tunnel attachment (repeatable)")
}
