package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeAlias(t *testing.T) {
	t.Parallel()

	value, err := NormalizeAlias(" Docs_MCP ")
	require.NoError(t, err)
	require.Equal(t, "docs-mcp", value)
}

func TestSaveAndLoadAdminProfilesPreservesCurrentShape(t *testing.T) {
	t.Parallel()

	root := Root{Path: t.TempDir()}
	err := SaveAdminProfiles(root, AdminProfilesFile{
		ActiveProfile: "sandbox",
		Profiles: map[string]AdminProfile{
			"sandbox": {
				Name:                "sandbox",
				ControlPlaneBaseURL: "https://api.openai.com",
				AdminKey:            "env:OPENAI_ADMIN_KEY",
			},
		},
	})
	require.NoError(t, err)

	data, err := os.ReadFile(AdminProfilesPath(root))
	require.NoError(t, err)
	require.Contains(t, string(data), `"active_profile": "sandbox"`)
	require.Contains(t, string(data), `"profiles": {`)

	loaded, err := LoadAdminProfiles(root)
	require.NoError(t, err)
	require.Equal(t, "sandbox", loaded.ActiveProfile)
	require.Equal(t, "env:OPENAI_ADMIN_KEY", loaded.Profiles["sandbox"].AdminKey)
}

func TestLoadAdminProfilesAcceptsLegacyFlatMap(t *testing.T) {
	t.Parallel()

	root := Root{Path: t.TempDir()}
	path := AdminProfilesPath(root)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("{\n  \"default\": {\n    \"name\": \"default\",\n    \"control_plane_base_url\": \"https://api.openai.com\",\n    \"admin_key\": \"env:OPENAI_ADMIN_KEY\"\n  }\n}\n"), 0o600))

	loaded, err := LoadAdminProfiles(root)
	require.NoError(t, err)
	require.Equal(t, "default", loaded.Profiles["default"].Name)
}

func TestAppendHistoryWritesMarkdownLog(t *testing.T) {
	t.Parallel()

	root := Root{Path: t.TempDir()}
	require.NoError(t, AppendHistory(root, "connect", "docs-mcp", "tunnel_123", "mode=tmux"))

	data, err := os.ReadFile(HistoryPath(root))
	require.NoError(t, err)
	require.Contains(t, string(data), "action=connect alias=docs-mcp tunnel_id=tunnel_123")
	require.Contains(t, string(data), "detail=mode=tmux")
}

func TestRejectInlineSecretMaterial(t *testing.T) {
	t.Parallel()

	err := RejectInlineSecretMaterial("--api-key super-secret", "mcp command")
	require.Error(t, err)
	require.Contains(t, err.Error(), "inline secret material")

	require.NoError(t, RejectInlineSecretMaterial("--api-key env:SAFE_KEY", "mcp command"))
}

func TestResolveRootPrefersExplicitStateDir(t *testing.T) {
	t.Parallel()

	root := ResolveRoot(func(key string) (string, bool) {
		switch key {
		case "TUNNEL_CLIENT_STATE_DIR":
			return "/tmp/tunnel-client-state", true
		case "HOME":
			return "/tmp/home", true
		default:
			return "", false
		}
	})

	require.Equal(t, "/tmp/tunnel-client-state", root.Path)
}

func TestResolveRootFallsBackToLegacyCodexHomeWhenPresent(t *testing.T) {
	t.Parallel()

	codexHome := t.TempDir()
	legacyRoot := filepath.Join(codexHome, "tunnel-mcp")
	require.NoError(t, os.MkdirAll(legacyRoot, 0o755))

	root := ResolveRoot(func(key string) (string, bool) {
		switch key {
		case "CODEX_HOME":
			return codexHome, true
		case "HOME":
			return t.TempDir(), true
		default:
			return "", false
		}
	})

	require.Equal(t, legacyRoot, root.Path)
}

func TestResolveRootPrefersLegacyCodexStateOverLegacyTunnelClientState(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	legacyCodexRoot := filepath.Join(home, ".codex", "tunnel-mcp")
	legacyTunnelClientRoot := filepath.Join(home, ".tunnel-client")
	require.NoError(t, os.MkdirAll(legacyCodexRoot, 0o755))
	require.NoError(t, os.MkdirAll(legacyTunnelClientRoot, 0o755))

	root := ResolveRoot(func(key string) (string, bool) {
		if key == "HOME" {
			return home, true
		}
		return "", false
	})

	require.Equal(t, legacyCodexRoot, root.Path)
}
