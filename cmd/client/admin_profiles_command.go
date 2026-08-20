package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/openai/tunnel-client/pkg/codexplugin"
	"github.com/openai/tunnel-client/pkg/codexplugin/session"
)

func newAdminProfilesCommand(lookupEnv func(string) (string, bool), stdout io.Writer, stderr io.Writer) *cobra.Command {
	return newAdminProfilesCommandWithRuntime(lookupEnv, stdout, stderr, session.DefaultRuntime())
}

func newAdminProfilesCommandWithRuntime(lookupEnv func(string) (string, bool), stdout io.Writer, stderr io.Writer, runtime session.Runtime) *cobra.Command {
	manager := codexplugin.NewManager(lookupEnv, runtime)
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "admin-profiles",
		Short: "Manage tunnel-client admin profiles used by runtimes commands",
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Emit JSON output")

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List saved admin profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := manager.ListAdminProfiles()
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd.OutOrStdout(), payload)
			}
			profiles, _ := payload["profiles"].([]codexplugin.AdminProfileResult)
			if len(profiles) == 0 {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "No admin profiles found in %s\n", payload["path"])
				return err
			}
			for _, profile := range profiles {
				active := ""
				if profile.Active {
					active = "\t(active)"
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s%s\n", profile.Name, profile.ControlPlaneBaseURL, active); err != nil {
					return err
				}
			}
			return nil
		},
	})

	var baseURL string
	var urlPath string
	var adminKey string
	var activate bool
	setCmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Create or update an admin profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := manager.SetAdminProfile(args[0], baseURL, urlPath, adminKey, activate)
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd.OutOrStdout(), payload)
			}
			profile := payload["profile"].(codexplugin.AdminProfileResult)
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Saved admin profile %s (%s)\n", profile.Name, profile.ControlPlaneBaseURL)
			return err
		},
	}
	setCmd.Flags().StringVar(&baseURL, "control-plane-base-url", "", "Control-plane base URL")
	setCmd.Flags().StringVar(&urlPath, "control-plane-url-path", "", "Optional URL path appended to the control-plane base URL")
	setCmd.Flags().StringVar(&adminKey, "admin-key", "", "Admin key reference to store, using env:NAME or file:/path")
	setCmd.Flags().BoolVar(&activate, "activate", true, "Mark the profile as active after saving")
	cmd.AddCommand(setCmd)

	activateCmd := &cobra.Command{
		Use:   "activate <name>",
		Short: "Mark an existing admin profile as active",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := manager.ActivateAdminProfile(args[0])
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd.OutOrStdout(), payload)
			}
			profile := payload["profile"].(codexplugin.AdminProfileResult)
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Activated admin profile %s\n", profile.Name)
			return err
		},
	}
	cmd.AddCommand(activateCmd)

	deleteCmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete an unused admin profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := manager.DeleteAdminProfile(args[0])
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd.OutOrStdout(), payload)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Deleted admin profile %s\n", args[0])
			return err
		},
	}
	cmd.AddCommand(deleteCmd)

	return cmd
}

func writeJSON(w io.Writer, payload any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}
