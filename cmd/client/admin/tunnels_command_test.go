package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
