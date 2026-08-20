package main

import (
	"embed"
	"fmt"
	"io"
	"path"
	"runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

//go:embed help_topics/*.txt
var embeddedHelpTopics embed.FS

const pluginBundleInstallCommandPlaceholder = "{{PLUGIN_BUNDLE_INSTALL_COMMAND}}"

func availableHelpTopics() []string {
	entries, err := embeddedHelpTopics.ReadDir("help_topics")
	if err != nil {
		return nil
	}
	topics := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		topics = append(topics, strings.TrimSuffix(entry.Name(), path.Ext(entry.Name())))
	}
	slices.Sort(topics)
	return topics
}

func rootCommandLong() string {
	lines := []string{
		"Tunnel client for the OpenAI MCP control plane.",
		"Use it to connect a local or private MCP server to the OpenAI control plane over an outbound tunnel.",
		"When it starts, it runs the long-lived tunnel daemon and exposes local operator surfaces at /healthz, /readyz, and /ui.",
		`Fastest Codex terminal path: tunnel-client codex assistant "Summarize what tunnel-client is doing in this checkout."`,
		"",
	}
	lines = append(lines, canonicalWebPropertyLines("Canonical setup URLs:")...)
	lines = append(lines, "", "Agent-first help topics:")
	for _, topic := range availableHelpTopics() {
		lines = append(lines, fmt.Sprintf("  tunnel-client help %s", topic))
	}
	return strings.Join(lines, "\n")
}

func newHelpCommand(root *cobra.Command, stdout io.Writer, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "help [topic|command]",
		Short: "Show help for an agent-first topic or subcommand",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return root.Help()
			}
			topic := strings.TrimSpace(args[0])
			if body, ok := loadHelpTopic(topic); ok {
				_, err := fmt.Fprint(cmd.OutOrStdout(), body)
				return err
			}
			target, _, err := root.Find(args)
			if err == nil && target != nil && target != cmd {
				return target.Help()
			}
			return fmt.Errorf("unknown help topic %q; available topics: %s", topic, strings.Join(availableHelpTopics(), ", "))
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd
}

func loadHelpTopic(name string) (string, bool) {
	if strings.TrimSpace(name) == "" {
		return "", false
	}
	data, err := embeddedHelpTopics.ReadFile(embeddedHelpTopicPath(name))
	if err != nil {
		return "", false
	}
	return renderHelpTopicForOS(string(data), runtime.GOOS), true
}

// embed.FS paths always use forward slashes, including on Windows.
func embeddedHelpTopicPath(name string) string {
	return path.Join("help_topics", name+".txt")
}

func renderHelpTopicForOS(body string, goos string) string {
	return strings.ReplaceAll(body, pluginBundleInstallCommandPlaceholder, pluginBundleInstallCommand(goos))
}

func pluginBundleInstallCommand(goos string) string {
	switch goos {
	case "windows":
		return `  Windows PowerShell: Set-Location C:\tmp\tunnel-plugin; powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\Install-Plugin.ps1 --tunnel-client-bin C:\path\to\tunnel-client.exe`
	default:
		return "  macOS/Linux: cd /tmp/tunnel-plugin && sh scripts/install_plugin.sh --tunnel-client-bin /path/to/tunnel-client"
	}
}
