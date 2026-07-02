package main

import (
	"strings"

	assistantkb "github.com/openai/tunnel-client/docs"
	pluginsbundle "github.com/openai/tunnel-client/plugins"
)

func buildCodexAssistantKnowledgeItem(prompt string) map[string]any {
	parts := []string{
		strings.TrimSpace(assistantkb.BuildPromptContext(prompt)),
		strings.TrimSpace(pluginsbundle.BuildTunnelMCPPromptContext(prompt)),
	}
	text := strings.Join(compactKnowledgeParts(parts), "\n\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return map[string]any{
		"type": "message",
		"role": "developer",
		"content": []map[string]any{
			{
				"type": "input_text",
				"text": text,
			},
		},
	}
}

func compactKnowledgeParts(parts []string) []string {
	compacted := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		compacted = append(compacted, part)
	}
	return compacted
}
