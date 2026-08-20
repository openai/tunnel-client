package main

import (
	"fmt"
	"strings"
)

const (
	canonicalTunnelsManagementURL        = "https://platform.openai.com/settings/organization/tunnels"
	canonicalRuntimeAPIKeysURL           = "https://platform.openai.com/settings/organization/api-keys"
	canonicalAdminAPIKeysURL             = "https://platform.openai.com/settings/organization/admin-keys"
	canonicalChatGPTConnectorSettingsURL = "https://chatgpt.com/#settings/Connectors"
)

type canonicalWebProperty struct {
	CheckID string
	Label   string
	URL     string
}

var canonicalWebProperties = []canonicalWebProperty{
	{
		CheckID: "tunnels_management_url",
		Label:   "Tunnels management",
		URL:     canonicalTunnelsManagementURL,
	},
	{
		CheckID: "runtime_api_keys_url",
		Label:   "Runtime API keys",
		URL:     canonicalRuntimeAPIKeysURL,
	},
	{
		CheckID: "admin_api_keys_url",
		Label:   "Admin API keys",
		URL:     canonicalAdminAPIKeysURL,
	},
	{
		CheckID: "chatgpt_connector_settings_url",
		Label:   "ChatGPT connector settings",
		URL:     canonicalChatGPTConnectorSettingsURL,
	},
}

func canonicalWebPropertyLines(heading string) []string {
	lines := make([]string, 0, len(canonicalWebProperties)+1)
	if heading != "" {
		lines = append(lines, heading)
	}
	for _, property := range canonicalWebProperties {
		lines = append(lines, fmt.Sprintf("  %s: %s", property.Label, property.URL))
	}
	return lines
}

func connectorSettingsRuntimeNote(runCommand string) string {
	command := strings.TrimSpace(runCommand)
	if command == "" {
		command = "tunnel-client run"
	}
	return fmt.Sprintf(
		"Create or verify the connector in %s only while `%s` is running. Keep the daemon up for connector discovery and every MCP call from ChatGPT.",
		canonicalChatGPTConnectorSettingsURL,
		command,
	)
}
