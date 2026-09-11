package codexplugin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/codexplugin/session"
	pluginstate "github.com/openai/tunnel-client/pkg/codexplugin/state"
	adminapi "github.com/openai/tunnel-client/pkg/controlplane/admin"
)

func TestRuntimePayloadHealthDetailsLinksAreAdditive(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "component-health"}[supported], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/healthz":
					_, _ = w.Write([]byte("live"))
				case "/readyz":
					_, _ = w.Write([]byte("ready"))
				case "/health/mcp":
					if supported {
						_, _ = w.Write([]byte(`{"schema_version":1,"component":"mcp"}`))
					} else {
						http.NotFound(w, r)
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			root := pluginstate.Root{Path: t.TempDir()}
			require.NoError(t, pluginstate.EnsureDirs(root))
			urlFile := filepath.Join(root.Path, "health", "fixture.url")
			require.NoError(t, os.WriteFile(urlFile, []byte(server.URL+"/healthz"), 0o600))
			manager := NewManager(testLookupEnv(map[string]string{"TUNNEL_CLIENT_STATE_DIR": root.Path, "HOME": t.TempDir()}), session.Runtime{})
			record := pluginstate.AliasRecord{Alias: "fixture", TunnelID: "tunnel_fixture", HealthURLFile: urlFile}
			status := manager.statusPayload(root, "fixture", record, pluginstate.ProcessRecord{}, effectiveAdminProfile{}, nil, false, "", false, "", "", "")
			connected := manager.connectPayload(root, "fixture", adminapi.Tunnel{}, effectiveAdminProfile{}, record, pluginstate.ProcessRecord{}, session.LaunchResult{}, "")
			for _, payload := range []map[string]any{status, connected} {
				require.Equal(t, server.URL+"/healthz", payload["health_url"])
				require.Equal(t, true, payload["healthy"])
				require.Equal(t, true, payload["ready"])
				if supported {
					require.Equal(t, server.URL+"/health?details=true", payload["health_details_url"])
					require.Equal(t, server.URL+"/health/mcp", payload["mcp_health_url"])
				} else {
					require.NotContains(t, payload, "health_details_url")
					require.NotContains(t, payload, "mcp_health_url")
				}
			}
		})
	}
}
