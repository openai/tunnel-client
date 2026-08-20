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

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane/admin"
)

const defaultAdminRequestTimeout = 30 * time.Second
const tunnelCreateReadyDelayNote = "Note: wait 25-30 seconds before expecting a newly created tunnel to be active and ready."

func NewTunnelsCommand(lookupEnv func(string) (string, bool), stdout io.Writer, stderr io.Writer) *cobra.Command {
	if lookupEnv == nil {
		lookupEnv = func(key string) (string, bool) { return "", false }
	}

	tunnelsCmd := &cobra.Command{
		Use:   "tunnels",
		Short: "Inspect or manage tunnels via the control-plane API",
		Long: strings.TrimSpace(`
Use tunnel inspection and CRUD commands for tunnel metadata.

- tunnel-client admin tunnels get <tunnel_id> is a read-only metadata lookup and can use
  either a runtime control-plane key (CONTROL_PLANE_API_KEY / OPENAI_API_KEY) or an admin key.
- tunnel-client admin tunnels list, create, update, and delete require a real admin key
  (OPENAI_ADMIN_KEY / --admin-key) plus explicit org/workspace/tenant scope as applicable.
`),
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
		Long: strings.TrimSpace(`
Create a tunnel with organization/workspace attachments.

This command requires a real admin API key. At least one org or workspace ID is required.
After create succeeds, wait 25-30 seconds before expecting the tunnel to be active and ready.
`),
		Example: strings.TrimSpace(`
  # Create with org + workspace scope. Then wait 25-30 seconds before using it.
  tunnel-client admin tunnels create \
    --name "My Tunnel" \
    --description "Routes to prod MCP" \
    --organization-id org_123 \
    --workspace-id ws_456

  # Create scoped only to a workspace. Then wait 25-30 seconds before using it.
  tunnel-client admin tunnels create \
    --name "Workspace Only" \
    --description "WS-only tunnel" \
    --workspace-id ws_456
`),
		RunE: wrapAdminJSONErrors(func(cmd *cobra.Command, args []string) error {
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
			if err := printTunnel(cmd, t); err != nil {
				return err
			}
			return printTunnelCreateReadyDelay(cmd)
		}),
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
		Long: strings.TrimSpace(`
Fetch a tunnel by id.

This read-only metadata lookup works with either:
- a runtime control-plane key from CONTROL_PLANE_API_KEY / OPENAI_API_KEY, or
- a real admin key from OPENAI_ADMIN_KEY / --admin-key.

tunnel-client itself uses this lookup during startup to fetch tunnel metadata such as
the operator-visible tunnel name and description.

When you need explicit admin CRUD scope for list/create/update/delete, prefer
the --json form here and reuse the returned organization_ids / workspace_ids
instead of guessing ids.
`),
		Example: strings.TrimSpace(`
  # Inspect a known tunnel with the runtime key used by tunnel-client itself
  export CONTROL_PLANE_API_KEY=...
  tunnel-client admin tunnels get tunnel_0123456789abcdef0123456789abcdef

  # Inspect the same tunnel with an explicit admin key
  export OPENAI_ADMIN_KEY=...
  tunnel-client admin tunnels get tunnel_0123456789abcdef0123456789abcdef

  # Reuse the live scope values for admin CRUD
  tunnel-client admin --json tunnels get tunnel_0123456789abcdef0123456789abcdef
`),
		Args: cobra.ExactArgs(1),
		RunE: wrapAdminJSONErrors(func(cmd *cobra.Command, args []string) error {
			client, _, err := readOnlyAdminClientFromCmd(cmd, lookupEnv)
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
		}),
	}
	return cmd
}

func newTunnelListCmd(lookupEnv func(string) (string, bool)) *cobra.Command {
	var tenantID string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tunnels filtered by organization or workspace",
		Long: strings.TrimSpace(`
List tunnels filtered by organization, workspace, or tenant.

This command requires a real admin API key and exactly one explicit scope filter.
`),
		RunE: wrapAdminJSONErrors(func(cmd *cobra.Command, args []string) error {
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
		}),
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
		Long: "Update a tunnel with a real admin API key; fields you set replace existing values.\n" +
			"Organization/workspace lists are PUT-like replacements; omit the flags to keep existing edges.",
		Example: strings.TrimSpace(`
  # Rename a tunnel and keep existing org/workspace edges
  tunnel-client admin tunnels update tunnel_abc --name "New Name"

  # Replace org/workspace scopes explicitly (PUT-like)
  tunnel-client admin tunnels update tunnel_abc \
    --organization-id org_123 \
    --workspace-id ws_456 \
    --description "New description"
`),
		Args: cobra.ExactArgs(1),
		RunE: wrapAdminJSONErrors(func(cmd *cobra.Command, args []string) error {
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
		}),
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
		Long: "Delete a tunnel with a real admin API key. This command also requires --confirm.\n" +
			"Optional org/workspace flags are accepted for symmetry with other admin subcommands,\n" +
			"but the current delete endpoint identifies the tunnel solely by id.",
		Args: cobra.ExactArgs(1),
		RunE: wrapAdminJSONErrors(func(cmd *cobra.Command, args []string) error {
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
		}),
	}
	cmd.Flags().BoolVar(&confirm, "confirm", false, "Required to delete a tunnel")
	requireOrgsOrWorkspaces(cmd)
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

func readOnlyAdminClientFromCmd(cmd *cobra.Command, lookupEnv func(string) (string, bool)) (*admin.AdminTunnelClient, *config.AdminConfig, error) {
	fs := mergedFlagSet(cmd)
	loadWithLookup := func(envLookup func(string) (string, bool)) (*admin.AdminTunnelClient, *config.AdminConfig, error) {
		cfg, err := config.LoadAdminConfig(fs, envLookup)
		if err != nil {
			return nil, nil, err
		}
		client, err := admin.NewAdminTunnelClient(cfg)
		if err != nil {
			return nil, nil, err
		}
		return client, cfg, nil
	}

	client, cfg, err := loadWithLookup(lookupEnv)
	if err == nil {
		return client, cfg, nil
	}
	if !readOnlyTunnelLookupCanFallback(fs, lookupEnv) {
		return nil, nil, err
	}

	fallbackLookup := func(key string) (string, bool) {
		if key == "OPENAI_ADMIN_KEY" {
			if value, ok := lookupEnv("CONTROL_PLANE_API_KEY"); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value), true
			}
			if value, ok := lookupEnv("OPENAI_API_KEY"); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value), true
			}
		}
		return lookupEnv(key)
	}
	client, cfg, fallbackErr := loadWithLookup(fallbackLookup)
	if fallbackErr == nil {
		return client, cfg, nil
	}
	if readOnlyTunnelLookupMissingKey(fallbackErr) {
		return nil, nil, errors.New("tunnel get requires a runtime or admin key; set CONTROL_PLANE_API_KEY, OPENAI_API_KEY, OPENAI_ADMIN_KEY, or --admin-key")
	}
	return nil, nil, fallbackErr
}

func readOnlyTunnelLookupCanFallback(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) bool {
	if lookupEnv == nil {
		return false
	}
	if value, ok := lookupEnv("OPENAI_ADMIN_KEY"); ok && strings.TrimSpace(value) != "" {
		return false
	}
	flagValue := ""
	if flag := fs.Lookup("admin-key"); flag != nil {
		flagValue = strings.TrimSpace(flag.Value.String())
	}
	switch flagValue {
	case "", "env:OPENAI_ADMIN_KEY":
		if value, ok := lookupEnv("CONTROL_PLANE_API_KEY"); ok && strings.TrimSpace(value) != "" {
			return true
		}
		if value, ok := lookupEnv("OPENAI_API_KEY"); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func readOnlyTunnelLookupMissingKey(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "admin key is required") ||
		strings.Contains(message, "OPENAI_ADMIN_KEY") ||
		strings.Contains(message, "environment variable OPENAI_ADMIN_KEY")
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
	fmt.Fprintf(&builder, "Tunnel %s\n", t.ID)
	fmt.Fprintf(&builder, "  Name: %s\n", t.Name)
	fmt.Fprintf(&builder, "  Description: %s\n", t.Description)
	if t.Creator != "" {
		fmt.Fprintf(&builder, "  Creator: %s\n", t.Creator)
	}
	fmt.Fprintf(&builder, "  Organizations: %s\n", strings.Join(t.OrganizationIDs, ", "))
	fmt.Fprintf(&builder, "  Workspaces: %s\n", strings.Join(t.WorkspaceIDs, ", "))
	if len(t.TenantIDs) > 0 {
		fmt.Fprintf(&builder, "  Tenants: %s\n", strings.Join(t.TenantIDs, ", "))
	}

	_, err := fmt.Fprint(cmd.OutOrStdout(), builder.String())
	return err
}

func printTunnelCreateReadyDelay(cmd *cobra.Command) error {
	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut {
		return nil
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), tunnelCreateReadyDelayNote)
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

func wrapAdminJSONErrors(run func(cmd *cobra.Command, args []string) error) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if err := run(cmd, args); err != nil {
			return maybeWriteAdminJSONError(cmd, err)
		}
		return nil
	}
}

func maybeWriteAdminJSONError(cmd *cobra.Command, err error) error {
	jsonOut, _ := cmd.Flags().GetBool("json")
	if !jsonOut {
		return err
	}

	payload := map[string]any{
		"error": map[string]any{
			"message": err.Error(),
		},
	}

	var requestErr *admin.RequestError
	if errors.As(err, &requestErr) {
		errorPayload := payload["error"].(map[string]any)
		errorPayload["method"] = requestErr.Method
		errorPayload["path"] = requestErr.Path
		errorPayload["status_code"] = requestErr.StatusCode
		if requestErr.RequestID != "" {
			errorPayload["request_id"] = requestErr.RequestID
		}
		if requestErr.Code != "" {
			errorPayload["code"] = requestErr.Code
		}
		if requestErr.ErrorType != "" {
			errorPayload["type"] = requestErr.ErrorType
		}
		if requestErr.Mitigation != "" {
			errorPayload["mitigation"] = requestErr.Mitigation
		}
	}

	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	if writeErr := enc.Encode(payload); writeErr != nil {
		return writeErr
	}
	return silentExitError{code: 1}
}

type silentExitError struct {
	code int
}

func (e silentExitError) Error() string {
	return ""
}

func (e silentExitError) ExitCode() int {
	if e.code == 0 {
		return 1
	}
	return e.code
}
