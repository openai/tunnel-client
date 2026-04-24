package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestRootCommandIncludesTunnels(t *testing.T) {
	t.Parallel()

	root := NewAdminCommand(func(string) (string, bool) { return "", false }, io.Discard, io.Discard)

	cmd, _, err := root.Find([]string{"tunnels"})
	require.NoError(t, err)
	require.Equal(t, "tunnels", cmd.Name())
}

func TestDeleteRequiresConfirm(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"tunnel_123","name":"test"}`))
	}))
	t.Cleanup(server.Close)

	root := NewAdminCommand(func(key string) (string, bool) {
		if key == "OPENAI_ADMIN_KEY" {
			return "admin", true
		}
		return "", false
	}, io.Discard, io.Discard)

	root.SetArgs([]string{
		"tunnels", "delete", "tunnel_123",
		"--control-plane.base-url", server.URL,
	})

	err := root.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to delete")
}

func TestDeleteAcceptsOptionalScopeFlags(t *testing.T) {
	t.Parallel()

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"tunnel_123","name":"test"}`))
	}))
	t.Cleanup(server.Close)

	root := NewAdminCommand(func(key string) (string, bool) {
		if key == "OPENAI_ADMIN_KEY" {
			return "admin", true
		}
		return "", false
	}, io.Discard, io.Discard)

	root.SetArgs([]string{
		"tunnels", "delete", "tunnel_123",
		"--control-plane.base-url", server.URL,
		"--organization-id", "org-1",
		"--workspace-id", "ws-1",
		"--confirm",
	})

	require.NoError(t, root.Execute())
	require.Equal(t, "/v1/tunnels/tunnel_123", gotPath)
}

func TestListRequiresSingleScope(t *testing.T) {
	t.Parallel()

	root := NewAdminCommand(func(key string) (string, bool) {
		if key == "OPENAI_ADMIN_KEY" {
			return "admin", true
		}
		return "", false
	}, io.Discard, io.Discard)

	root.SetArgs([]string{
		"tunnels", "list",
		"--organization-id", "org-1",
		"--workspace-id", "ws-1",
	})

	err := root.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly one")
}

func TestUpdateRequiresFields(t *testing.T) {
	t.Parallel()

	var received struct {
		OrgIDs []string `json:"organization_ids"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"tunnel_123","name":"test"}`))
	}))
	t.Cleanup(server.Close)

	root := NewAdminCommand(func(key string) (string, bool) {
		if key == "OPENAI_ADMIN_KEY" {
			return "admin", true
		}
		return "", false
	}, io.Discard, io.Discard)

	root.SetArgs([]string{
		"tunnels", "update", "tunnel_123",
		"--organization-id", "org-1",
		"--control-plane.base-url", server.URL,
	})

	err := root.Execute()
	require.NoError(t, err)
	require.Equal(t, []string{"org-1"}, received.OrgIDs)
}

func TestTunnelsHelpUsesSubcommandHelp(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	root := NewAdminCommand(func(key string) (string, bool) {
		if key == "OPENAI_ADMIN_KEY" {
			return "admin", true
		}
		return "", false
	}, &out, io.Discard)

	root.SetArgs([]string{"tunnels", "--help"})
	err := root.Execute()
	require.NoError(t, err)

	help := out.String()
	require.Contains(t, help, "create")
	require.Contains(t, help, "runtime control-plane key")
}

func TestTunnelSubcommandExamplesUseAdminPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "create",
			args: []string{"tunnels", "create", "--help"},
		},
		{
			name: "update",
			args: []string{"tunnels", "update", "--help"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			root := NewAdminCommand(func(key string) (string, bool) {
				if key == "OPENAI_ADMIN_KEY" {
					return "admin", true
				}
				return "", false
			}, &out, io.Discard)

			root.SetArgs(tc.args)
			err := root.Execute()
			require.NoError(t, err)

			help := out.String()
			require.Contains(t, help, "tunnel-client admin tunnels "+tc.name)
			require.NotContains(t, help, "tunnel-client tunnels "+tc.name)
			require.True(t, strings.Contains(help, "Examples:") || strings.Contains(help, "Example:"))
		})
	}
}

func TestCreateHelpExplainsReadyDelay(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	root := NewAdminCommand(func(string) (string, bool) { return "", false }, &out, io.Discard)

	root.SetArgs([]string{"tunnels", "create", "--help"})
	require.NoError(t, root.Execute())

	help := out.String()
	require.Contains(t, help, "wait 25-30 seconds")
	require.Contains(t, help, "active and ready")
}

func TestCreatePrintsReadyDelayNoteForTextOutput(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/tunnels", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"tunnel_123","name":"created tunnel","description":"created description","organization_ids":["org-1"],"workspace_ids":["ws-1"]}`))
	}))
	t.Cleanup(server.Close)

	root := NewAdminCommand(func(key string) (string, bool) {
		if key == "OPENAI_ADMIN_KEY" {
			return "admin", true
		}
		return "", false
	}, &out, io.Discard)

	root.SetArgs([]string{
		"tunnels", "create",
		"--control-plane.base-url", server.URL,
		"--name", "created tunnel",
		"--description", "created description",
		"--organization-id", "org-1",
		"--workspace-id", "ws-1",
	})

	require.NoError(t, root.Execute())
	require.Contains(t, out.String(), "Tunnel tunnel_123")
	require.Contains(t, out.String(), tunnelCreateReadyDelayNote)
}

func TestCreateOmitsReadyDelayNoteForJSONOutput(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/tunnels", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("X-Request-Id", "req_create_json_123")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"tunnel_123","name":"created tunnel","description":"created description","organization_ids":["org-1"],"workspace_ids":["ws-1"]}`))
	}))
	t.Cleanup(server.Close)

	root := NewAdminCommand(func(key string) (string, bool) {
		if key == "OPENAI_ADMIN_KEY" {
			return "admin", true
		}
		return "", false
	}, &out, io.Discard)

	root.SetArgs([]string{
		"tunnels", "create",
		"--control-plane.base-url", server.URL,
		"--name", "created tunnel",
		"--description", "created description",
		"--organization-id", "org-1",
		"--json",
	})

	require.NoError(t, root.Execute())
	require.NotContains(t, out.String(), tunnelCreateReadyDelayNote)
	require.Contains(t, out.String(), `"id": "tunnel_123"`)
	require.Contains(t, out.String(), `"request_id": "req_create_json_123"`)
}

func TestCreateRejectsDuplicateScope(t *testing.T) {
	t.Parallel()

	root := NewAdminCommand(func(key string) (string, bool) {
		if key == "OPENAI_ADMIN_KEY" {
			return "admin", true
		}
		return "", false
	}, io.Discard, io.Discard)

	root.SetArgs([]string{
		"tunnels", "create",
		"--name", "dup",
		"--description", "dup test",
		"--organization-id", "org-1",
		"--organization-id", "org-1",
	})

	err := root.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate organization-id")
}

func TestGetFallsBackToRuntimeKeyWhenAdminKeyIsMissing(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/tunnels/tunnel_123", r.URL.Path)
		require.Equal(t, "Bearer runtime-key", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"tunnel_123","name":"runtime tunnel","description":"metadata"}`))
	}))
	t.Cleanup(server.Close)

	root := NewAdminCommand(func(key string) (string, bool) {
		if key == "CONTROL_PLANE_API_KEY" {
			return "runtime-key", true
		}
		return "", false
	}, io.Discard, io.Discard)

	root.SetArgs([]string{
		"tunnels", "get", "tunnel_123",
		"--control-plane.base-url", server.URL,
	})

	require.NoError(t, root.Execute())
}

func TestListStillRequiresAdminKey(t *testing.T) {
	t.Parallel()

	root := NewAdminCommand(func(key string) (string, bool) {
		if key == "CONTROL_PLANE_API_KEY" {
			return "runtime-key", true
		}
		return "", false
	}, io.Discard, io.Discard)

	root.SetArgs([]string{
		"tunnels", "list",
		"--organization-id", "org-1",
	})

	err := root.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "OPENAI_ADMIN_KEY")
}

func TestGetHelpExplainsRuntimeVsAdminKeySplit(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	root := NewAdminCommand(func(string) (string, bool) { return "", false }, &out, io.Discard)

	root.SetArgs([]string{"tunnels", "get", "--help"})
	require.NoError(t, root.Execute())

	help := out.String()
	require.Contains(t, help, "runtime control-plane key")
	require.Contains(t, help, "CONTROL_PLANE_API_KEY")
	require.Contains(t, help, "OPENAI_ADMIN_KEY")
}

func TestCreateRequiresScope(t *testing.T) {
	t.Parallel()

	root := NewAdminCommand(func(key string) (string, bool) {
		if key == "OPENAI_ADMIN_KEY" {
			return "admin", true
		}
		return "", false
	}, io.Discard, io.Discard)

	root.SetArgs([]string{
		"tunnels", "create",
		"--name", "no-scope",
		"--description", "missing scope",
	})

	err := root.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least one of --organization-id or --workspace-id is required")
}

// Prevent cobra from calling os.Exit during tests.
func init() {
	cobra.MousetrapHelpText = ""
}
