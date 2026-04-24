package adminui

import (
	"strings"
	"testing"
)

func TestCodexSandboxTypeNormalizesLegacyAliases(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"":                   "danger-full-access",
		"dangerFullAccess":   "danger-full-access",
		"workspaceWrite":     "workspace-write",
		"readOnly":           "read-only",
		"danger-full-access": "danger-full-access",
	}

	for input, want := range cases {
		if got := codexSandboxType(input); got != want {
			t.Fatalf("codexSandboxType(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBuildTextInputItemUsesDirectTextVariant(t *testing.T) {
	t.Parallel()

	item := buildTextInputItem("diagnose tunnel state")
	if got := item["type"]; got != "text" {
		t.Fatalf("type = %#v, want %q", got, "text")
	}
	if got := item["text"]; got != "diagnose tunnel state" {
		t.Fatalf("text = %#v, want %q", got, "diagnose tunnel state")
	}
	if _, ok := item["content"]; ok {
		t.Fatalf("content should be omitted for text variant")
	}
}

func TestBuildCodexKnowledgeItemUsesPackagedDocs(t *testing.T) {
	t.Parallel()

	item := buildCodexKnowledgeItem("How do I connect this tunnel to ChatGPT?")
	if item == nil {
		t.Fatal("expected knowledge item")
	}
	content, ok := item["content"].([]map[string]any)
	if !ok || len(content) == 0 {
		t.Fatalf("unexpected content payload: %#v", item["content"])
	}
	text, _ := content[0]["text"].(string)
	if text == "" {
		t.Fatalf("expected knowledge text, got %#v", content[0]["text"])
	}
	for _, snippet := range []string{
		"Packaged tunnel-client knowledge base injected from the binary.",
		"docs/onboarding.md",
		"docs/enterprise-customer-onboarding.md",
		"Connection: Tunnel",
	} {
		if !strings.Contains(text, snippet) {
			t.Fatalf("expected knowledge text to contain %q, got:\n%s", snippet, text)
		}
	}
}

func TestBuildCodexKnowledgeItemUsesBundledPluginReferences(t *testing.T) {
	t.Parallel()

	item := buildCodexKnowledgeItem("How do I install the codex plugin from the tunnel-client binary?")
	if item == nil {
		t.Fatal("expected knowledge item")
	}
	content, ok := item["content"].([]map[string]any)
	if !ok || len(content) == 0 {
		t.Fatalf("unexpected content payload: %#v", item["content"])
	}
	text, _ := content[0]["text"].(string)
	if text == "" {
		t.Fatalf("expected knowledge text, got %#v", content[0]["text"])
	}
	for _, snippet := range []string{
		"Curated tunnel-mcp plugin references injected from the binary.",
		"plugins/tunnel-mcp/skills/tunnel-mcp/references/setup-and-install.md",
		"tunnel-client codex plugin install",
	} {
		if !strings.Contains(text, snippet) {
			t.Fatalf("expected knowledge text to contain %q, got:\n%s", snippet, text)
		}
	}
}

func TestBuildCodexKnowledgeItemUsesBundledBinaryGuidance(t *testing.T) {
	t.Parallel()

	item := buildCodexKnowledgeItem("The plugin is installed but tunnel-client is missing. How do I install the binary?")
	if item == nil {
		t.Fatal("expected knowledge item")
	}
	content, ok := item["content"].([]map[string]any)
	if !ok || len(content) == 0 {
		t.Fatalf("unexpected content payload: %#v", item["content"])
	}
	text, _ := content[0]["text"].(string)
	if text == "" {
		t.Fatalf("expected knowledge text, got %#v", content[0]["text"])
	}
	for _, snippet := range []string{
		"Curated tunnel-mcp plugin references injected from the binary.",
		"plugins/tunnel-mcp/skills/tunnel-mcp/references/binary.md",
		"https://github.com/openai/tunnel-client/releases/latest",
		"https://github.com/openai/tunnel-client",
		"git clone https://github.com/openai/tunnel-client.git",
		"TUNNEL_CLIENT_BIN",
		"--tunnel-client-bin /path/to/tunnel-client",
	} {
		if !strings.Contains(text, snippet) {
			t.Fatalf("expected knowledge text to contain %q, got:\n%s", snippet, text)
		}
	}
}
