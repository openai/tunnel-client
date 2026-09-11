package codexplugin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/codexplugin/session"
	pluginstate "github.com/openai/tunnel-client/pkg/codexplugin/state"
)

func TestLocalRuntimeDetailsRejectsUnsafeManagedFilesWithoutProbing(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"outside", "parent-symlink", "leaf-symlink", "oversized", "directory"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			if runtime.GOOS == "windows" && strings.Contains(kind, "symlink") {
				t.Skip("symlink permissions vary on Windows")
			}
			root := pluginstate.Root{Path: t.TempDir()}
			require.NoError(t, pluginstate.EnsureDirs(root))
			var probes atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				probes.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			outside := t.TempDir()
			outsideHealth := filepath.Join(outside, "docs.url")
			outsideLog := filepath.Join(outside, "docs.log")
			require.NoError(t, os.WriteFile(outsideHealth, []byte(server.URL+"/healthz"), 0o600))
			require.NoError(t, os.WriteFile(outsideLog, []byte("outside-secret-log"), 0o600))
			healthPath := session.ProfileHealthURLFile("docs", root)
			logPath := session.LogPath("docs", root)
			switch kind {
			case "outside":
				healthPath, logPath = outsideHealth, outsideLog
			case "parent-symlink":
				for _, directory := range []string{"health", "logs"} {
					require.NoError(t, os.Remove(filepath.Join(root.Path, directory)))
					require.NoError(t, os.Symlink(outside, filepath.Join(root.Path, directory)))
				}
			case "leaf-symlink":
				require.NoError(t, os.Symlink(outsideHealth, healthPath))
				require.NoError(t, os.Symlink(outsideLog, logPath))
			case "oversized":
				require.NoError(t, os.WriteFile(healthPath, []byte(server.URL+"/healthz"+strings.Repeat(" ", 8193)), 0o600))
			case "directory":
				require.NoError(t, os.Mkdir(healthPath, 0o700))
				require.NoError(t, os.Mkdir(logPath, 0o700))
			}
			manager := NewManager(testLookupEnv(nil), session.Runtime{})
			local := manager.localRuntimeDetails(root, "docs", pluginstate.AliasRecord{Alias: "docs"}, pluginstate.ProcessRecord{
				Alias: "docs", Mode: "stopped", HealthURLFile: healthPath, LogPath: logPath,
			})
			require.Empty(t, local["health"].(map[string]any)["raw_url"])
			require.Empty(t, local["log"].(map[string]any)["tail"])
			require.Equal(t, false, local["log"].(map[string]any)["exists"])
			require.Equal(t, false, local["live_admin_ui"].(map[string]any)["found"])
			require.Zero(t, probes.Load())
			payload, err := json.Marshal(local)
			require.NoError(t, err)
			require.NotContains(t, string(payload), "outside-secret-log")
			require.NotContains(t, string(payload), server.URL)
		})
	}
}

func TestLocalRuntimeDetailsSupportsSelectedRootSymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	realRoot := t.TempDir()
	selectedRoot := filepath.Join(t.TempDir(), "selected state")
	require.NoError(t, os.Symlink(realRoot, selectedRoot))
	root := pluginstate.Root{Path: selectedRoot}
	require.NoError(t, pluginstate.EnsureDirs(root))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz", "/readyz":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	healthPath := session.ProfileHealthURLFile("docs", root)
	logPath := session.LogPath("docs", root)
	require.NoError(t, os.WriteFile(healthPath, []byte(server.URL+"/healthz\n"), 0o600))
	require.NoError(t, os.WriteFile(logPath, []byte("first\nsecond\n"), 0o600))
	profilePath := filepath.Join(t.TempDir(), "operator-profile.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("config_version: 1\n"), 0o600))
	manager := NewManager(testLookupEnv(nil), session.Runtime{})
	local := manager.localRuntimeDetails(root, "docs", pluginstate.AliasRecord{Alias: "docs", ProfilePath: profilePath}, pluginstate.ProcessRecord{
		Alias: "docs", Mode: "stopped", HealthURLFile: healthPath, LogPath: logPath,
	})
	require.Equal(t, server.URL+"/healthz", local["health"].(map[string]any)["raw_url"])
	require.Equal(t, true, local["health"].(map[string]any)["exists"])
	require.Equal(t, "first\nsecond", local["log"].(map[string]any)["tail"])
	require.Equal(t, true, local["profile"].(map[string]any)["exists"])
}

func TestRemoveRejectsPersistedOutsideManagedPaths(t *testing.T) {
	t.Parallel()
	for _, directory := range []string{"health", "logs"} {
		t.Run(directory, func(t *testing.T) {
			t.Parallel()
			root := pluginstate.Root{Path: t.TempDir()}
			require.NoError(t, pluginstate.EnsureDirs(root))
			sentinel := filepath.Join(t.TempDir(), "outside")
			require.NoError(t, os.WriteFile(sentinel, []byte("unchanged"), 0o600))
			process := pluginstate.ProcessRecord{Alias: "docs", Mode: "stopped"}
			if directory == "health" {
				process.HealthURLFile = sentinel
			} else {
				process.LogPath = sentinel
			}
			require.NoError(t, pluginstate.SaveAliases(root, map[string]pluginstate.AliasRecord{"docs": {Alias: "docs"}}))
			require.NoError(t, pluginstate.SaveProcesses(root, map[string]pluginstate.ProcessRecord{"docs": process}))
			manager := NewManager(testLookupEnv(map[string]string{"TUNNEL_CLIENT_STATE_DIR": root.Path}), session.Runtime{})
			_, err := manager.Remove(AliasOptions{Alias: "docs"})
			require.Error(t, err)
			aliases, err := pluginstate.LoadAliases(root)
			require.NoError(t, err)
			require.Equal(t, map[string]pluginstate.AliasRecord{"docs": {Alias: "docs"}}, aliases)
			processes, err := pluginstate.LoadProcesses(root)
			require.NoError(t, err)
			require.Equal(t, map[string]pluginstate.ProcessRecord{"docs": process}, processes)
			data, err := os.ReadFile(sentinel)
			require.NoError(t, err)
			require.Equal(t, "unchanged", string(data))
		})
	}
}
