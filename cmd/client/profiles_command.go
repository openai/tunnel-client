package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/openai/tunnel-client/pkg/config"
)

type profileListEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func newProfilesCommand(lookupEnv func(string) (string, bool), stdout io.Writer, stderr io.Writer) *cobra.Command {
	var profileDir string

	cmd := &cobra.Command{
		Use:   "profiles",
		Short: "Manage tunnel-client YAML profiles",
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.PersistentFlags().StringVar(&profileDir, "profile-dir", "", "Profile directory override")

	cmd.AddCommand(newProfilesListCommand(lookupEnv, &profileDir))
	cmd.AddCommand(newProfilesAddCommand(lookupEnv, &profileDir))
	cmd.AddCommand(newProfilesEditCommand(lookupEnv, &profileDir))
	cmd.AddCommand(newProfilesSamplesCommand())
	return cmd
}

func newProfilesListCommand(lookupEnv func(string) (string, bool), profileDir *string) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.ResolveProfileDir(*profileDir, lookupEnv)
			if err != nil {
				return err
			}
			entries, err := listProfiles(dir)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOutput {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}
			if len(entries) == 0 {
				_, err = fmt.Fprintf(out, "No profiles found in %s\n", dir)
				return err
			}
			for _, entry := range entries {
				if _, err := fmt.Fprintf(out, "%s\t%s\n", entry.Name, entry.Path); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit profile list as JSON")
	return cmd
}

func newProfilesAddCommand(lookupEnv func(string) (string, bool), profileDir *string) *cobra.Command {
	var fromFile string
	var force bool
	var sample string
	var tunnelID string
	var mcpServerURL string
	var mcpCommand string

	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a profile from a file or sample",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			path, dir, err := config.ProfilePath(name, *profileDir, lookupEnv)
			if err != nil {
				return err
			}
			data, err := profileDataFromAddFlags(fromFile, sample, tunnelID, mcpServerURL, mcpCommand)
			if err != nil {
				return err
			}
			if err := config.ValidateProfileBytes(path, data); err != nil {
				return err
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return fmt.Errorf("create profile directory %s: %w", dir, err)
			}
			flag := os.O_WRONLY | os.O_CREATE
			if force {
				flag |= os.O_TRUNC
			} else {
				flag |= os.O_EXCL
			}
			file, err := os.OpenFile(path, flag, 0o600)
			if err != nil {
				if os.IsExist(err) {
					return fmt.Errorf("profile %q already exists; pass --force to replace it", name)
				}
				return fmt.Errorf("write profile %s: %w", path, err)
			}
			if _, err := file.Write(data); err != nil {
				_ = file.Close()
				return fmt.Errorf("write profile %s: %w", path, err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("close profile %s: %w", path, err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Added profile %s at %s\n", name, path)
			return err
		},
	}
	cmd.Flags().StringVar(&fromFile, "from-file", "", "Copy profile YAML from this path")
	cmd.Flags().BoolVar(&force, "force", false, "Replace an existing profile")
	cmd.Flags().StringVar(&sample, "sample", "", "Generate a named sample profile")
	cmd.Flags().StringVar(&tunnelID, "tunnel-id", "", "Tunnel ID for generated sample profiles")
	cmd.Flags().StringVar(&mcpServerURL, "mcp-server-url", "", "MCP server URL for generated sample profiles")
	cmd.Flags().StringVar(&mcpCommand, "mcp-command", "", "MCP command for generated sample profiles")
	return cmd
}

func newProfilesSamplesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "samples",
		Short: "List or inspect built-in tunnel-client profile samples",
	}
	cmd.AddCommand(newProfilesSamplesListCommand())
	cmd.AddCommand(newProfilesSamplesShowCommand())
	return cmd
}

func newProfilesSamplesListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List built-in profile sample names",
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, name := range sortedSampleNames() {
				sample, ok := findProfileSample(name)
				if !ok {
					return fmt.Errorf("sample %q is registered but could not be loaded", name)
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", sample.Name, sample.Summary); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newProfilesSamplesShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show a built-in profile sample plus required inputs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sample, ok := findProfileSample(args[0])
			if !ok {
				return fmt.Errorf("unknown sample %q; run `tunnel-client profiles samples list`", args[0])
			}
			data, err := sample.Generate(sample.Example)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Sample: %s\n", sample.Name)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Summary: %s\n", sample.Summary)
			if sample.UseWhen != "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Use when: %s\n", sample.UseWhen)
			}
			if len(sample.RequiredFlags) > 0 {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Required:\n")
				for _, value := range sample.RequiredFlags {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", value)
				}
			}
			if len(sample.OptionalFlags) > 0 {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Optional:\n")
				for _, value := range sample.OptionalFlags {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", value)
				}
			}
			if len(sample.Caveats) > 0 {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Notes:\n")
				for _, value := range sample.Caveats {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", value)
				}
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n%s", data)
			return nil
		},
	}
}

func newProfilesEditCommand(lookupEnv func(string) (string, bool), profileDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "edit <name>",
		Short: "Edit a profile and validate it before saving",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			path, dir, err := config.ProfilePath(name, *profileDir, lookupEnv)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return fmt.Errorf("create profile directory %s: %w", dir, err)
			}

			contents, err := os.ReadFile(path)
			if err != nil {
				if !os.IsNotExist(err) {
					return fmt.Errorf("read profile %s: %w", path, err)
				}
				contents = sampleMCPWithDCRProfile("tunnel_00000000000000000000000000000000", "http://127.0.0.1:3001/mcp", "")
			}

			tmp, err := os.CreateTemp(dir, "."+name+".*.yaml")
			if err != nil {
				return fmt.Errorf("create temporary profile file in %s: %w", dir, err)
			}
			tmpPath := tmp.Name()
			defer func() {
				_ = os.Remove(tmpPath)
			}()
			if _, err := tmp.Write(contents); err != nil {
				_ = tmp.Close()
				return fmt.Errorf("write temporary profile %s: %w", tmpPath, err)
			}
			if err := tmp.Close(); err != nil {
				return fmt.Errorf("close temporary profile %s: %w", tmpPath, err)
			}
			if err := os.Chmod(tmpPath, 0o600); err != nil {
				return fmt.Errorf("chmod temporary profile %s: %w", tmpPath, err)
			}

			if err := runProfileEditor(tmpPath, lookupEnv); err != nil {
				return err
			}
			edited, err := os.ReadFile(tmpPath)
			if err != nil {
				return fmt.Errorf("read edited profile %s: %w", tmpPath, err)
			}
			if err := config.ValidateProfileBytes(path, edited); err != nil {
				return fmt.Errorf("profile did not validate; not saving %s: %w", path, err)
			}
			if err := os.Rename(tmpPath, path); err != nil {
				return fmt.Errorf("save profile %s: %w", path, err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Saved profile %s at %s\n", name, path)
			return err
		},
	}
}

func listProfiles(dir string) ([]profileListEntry, error) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []profileListEntry{}, nil
		}
		return nil, fmt.Errorf("read profile directory %s: %w", dir, err)
	}
	out := make([]profileListEntry, 0, len(dirEntries))
	for _, entry := range dirEntries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		if config.ValidateProfileName(name) != nil {
			continue
		}
		out = append(out, profileListEntry{
			Name: name,
			Path: filepath.Join(dir, entry.Name()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func profileDataFromAddFlags(fromFile, sample, tunnelID, mcpServerURL, mcpCommand string) ([]byte, error) {
	fromFile = strings.TrimSpace(fromFile)
	sample = strings.TrimSpace(sample)
	if (fromFile == "") == (sample == "") {
		return nil, fmt.Errorf("set exactly one of --from-file or --sample")
	}
	if fromFile != "" {
		data, err := os.ReadFile(fromFile)
		if err != nil {
			return nil, fmt.Errorf("read source profile %s: %w", fromFile, err)
		}
		return data, nil
	}
	if sample == "" {
		return nil, fmt.Errorf("sample name is required")
	}
	return generateProfileSample(sample, sampleProfileRequest{
		TunnelID:         tunnelID,
		BaseURL:          "https://api.openai.com",
		APIKeyRef:        "env:CONTROL_PLANE_API_KEY",
		HealthListenAddr: defaultInitHealthListenAddr,
		MCPServerURL:     mcpServerURL,
		MCPCommand:       mcpCommand,
	})
}

func sampleMCPWithDCRProfile(tunnelID, mcpServerURL, mcpCommand string) []byte {
	data, err := generateSampleMCPWithDCRProfile(sampleProfileRequest{
		TunnelID:         tunnelID,
		BaseURL:          "https://api.openai.com",
		APIKeyRef:        "env:CONTROL_PLANE_API_KEY",
		HealthListenAddr: defaultInitHealthListenAddr,
		MCPServerURL:     mcpServerURL,
		MCPCommand:       mcpCommand,
	})
	if err != nil {
		panic(err)
	}
	return data
}

func runProfileEditor(path string, lookupEnv func(string) (string, bool)) error {
	editor := ""
	if value, ok := lookupEnv("VISUAL"); ok {
		editor = strings.TrimSpace(value)
	}
	if editor == "" {
		if value, ok := lookupEnv("EDITOR"); ok {
			editor = strings.TrimSpace(value)
		}
	}
	if editor == "" {
		return fmt.Errorf("set VISUAL or EDITOR to edit profiles")
	}
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return fmt.Errorf("set VISUAL or EDITOR to edit profiles")
	}
	args := append(parts[1:], path)
	cmd := exec.Command(parts[0], args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run editor %q: %w", editor, err)
	}
	return nil
}
