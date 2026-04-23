package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfilesAddSampleAndList(t *testing.T) {
	t.Parallel()

	profileDir := t.TempDir()
	stdout, stderr, err := executeProfilesCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "profiles", "--profile-dir", profileDir, "add", "sample_mcp_with_dcr",
		"--sample", "sample_mcp_with_dcr",
		"--tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp-server-url", "https://mcp.example/mcp",
	)

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "Added profile sample_mcp_with_dcr")
	path := filepath.Join(profileDir, "sample_mcp_with_dcr.yaml")
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(contents), `api_key: "env:CONTROL_PLANE_API_KEY"`)
	require.Contains(t, string(contents), `url: "https://mcp.example/mcp"`)

	stdout, stderr, err = executeProfilesCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "profiles", "--profile-dir", profileDir, "list", "--json")

	require.NoError(t, err, stderr)
	var entries []profileListEntry
	require.NoError(t, json.Unmarshal([]byte(stdout), &entries))
	require.Equal(t, []profileListEntry{{Name: "sample_mcp_with_dcr", Path: path}}, entries)
}

func TestProfilesAddEnterpriseProxySample(t *testing.T) {
	t.Parallel()

	profileDir := t.TempDir()
	stdout, stderr, err := executeProfilesCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "profiles", "--profile-dir", profileDir, "add", "corp-proxy",
		"--sample", "sample_mcp_enterprise_proxy",
		"--tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp-server-url", "https://mcp.internal.example.com/mcp",
	)

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "Added profile corp-proxy")
	path := filepath.Join(profileDir, "corp-proxy.yaml")
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(contents), `ca_bundle: "env:ENTERPRISE_CA_BUNDLE"`)
	require.Contains(t, string(contents), `http_proxy: "env:HTTPS_PROXY"`)
	require.Contains(t, string(contents), `OPENAI_ADMIN_KEY`)
}

func TestProfilesListJSONUsesEmptyArrayForMissingDir(t *testing.T) {
	t.Parallel()

	missingProfileDir := filepath.Join(t.TempDir(), "missing")
	stdout, stderr, err := executeProfilesCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "profiles", "--profile-dir", missingProfileDir, "list", "--json")

	require.NoError(t, err, stderr)
	require.JSONEq(t, "[]", stdout)
}

func TestProfilesAddRejectsExistingWithoutForce(t *testing.T) {
	t.Parallel()

	profileDir := t.TempDir()
	path := filepath.Join(profileDir, "sample.yaml")
	require.NoError(t, os.WriteFile(path, []byte("config_version: 1\n"), 0o600))

	_, stderr, err := executeProfilesCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "profiles", "--profile-dir", profileDir, "add", "sample",
		"--sample", "sample_mcp_with_dcr",
		"--tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp-command", "python server.py",
	)

	require.Error(t, err)
	require.Empty(t, stderr)
	require.Contains(t, err.Error(), "already exists")
}

func TestProfilesAddFromFileWithForce(t *testing.T) {
	t.Parallel()

	temp := t.TempDir()
	profileDir := filepath.Join(temp, "profiles")
	source := filepath.Join(temp, "source.yaml")
	require.NoError(t, os.WriteFile(source, []byte(`config_version: 1
control_plane:
  tunnel_id: tunnel_0123456789abcdef0123456789abcdef
  api_key: env:CONTROL_PLANE_API_KEY
mcp:
  commands:
    - channel: main
      command: python server.py
`), 0o600))
	require.NoError(t, os.MkdirAll(profileDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(profileDir, "sample.yaml"), []byte("old\n"), 0o600))

	stdout, stderr, err := executeProfilesCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "profiles", "--profile-dir", profileDir, "add", "sample", "--from-file", source, "--force")

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "Added profile sample")
	contents, err := os.ReadFile(filepath.Join(profileDir, "sample.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(contents), "python server.py")
}

func TestProfilesEditValidatesBeforeSaving(t *testing.T) {
	t.Parallel()

	temp := t.TempDir()
	profileDir := filepath.Join(temp, "profiles")
	require.NoError(t, os.MkdirAll(profileDir, 0o700))
	path := filepath.Join(profileDir, "sample.yaml")
	original := []byte(`config_version: 1
control_plane:
  tunnel_id: tunnel_0123456789abcdef0123456789abcdef
  api_key: env:CONTROL_PLANE_API_KEY
mcp:
  commands:
    - channel: main
      command: python server.py
`)
	require.NoError(t, os.WriteFile(path, original, 0o600))

	editor := filepath.Join(temp, "editor.sh")
	require.NoError(t, os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'config_version: 2\\n' > \"$1\"\n"), 0o600))

	_, _, err := executeProfilesCommand(t, map[string]string{
		"HOME":   t.TempDir(),
		"EDITOR": "sh " + editor,
	}, "profiles", "--profile-dir", profileDir, "edit", "sample")

	require.Error(t, err)
	require.Contains(t, err.Error(), "profile did not validate")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, after)
}

func TestProfilesEditCreatesMissingProfileFromSkeleton(t *testing.T) {
	t.Parallel()

	temp := t.TempDir()
	profileDir := filepath.Join(temp, "profiles")
	editor := filepath.Join(temp, "editor.sh")
	require.NoError(t, os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'config_version: 1\\ncontrol_plane:\\n  tunnel_id: tunnel_0123456789abcdef0123456789abcdef\\n  api_key: env:CONTROL_PLANE_API_KEY\\nmcp:\\n  server_urls:\\n    - channel: main\\n      url: https://mcp.example/mcp\\n' > \"$1\"\n"), 0o600))

	stdout, stderr, err := executeProfilesCommand(t, map[string]string{
		"HOME":   t.TempDir(),
		"EDITOR": "sh " + editor,
	}, "profiles", "--profile-dir", profileDir, "edit", "new_profile")

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "Saved profile new_profile")
	contents, err := os.ReadFile(filepath.Join(profileDir, "new_profile.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(contents), "https://mcp.example/mcp")
}

func executeProfilesCommand(t *testing.T, env map[string]string, args ...string) (string, string, error) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	root := newRootCommand(func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}, &stdout, &stderr)
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func TestRootCommandIncludesProfiles(t *testing.T) {
	t.Parallel()

	root := newRootCommand(func(string) (string, bool) { return "", false }, io.Discard, io.Discard)

	profiles, _, err := root.Find([]string{"profiles"})
	require.NoError(t, err)
	require.Equal(t, "profiles", profiles.Name())
	require.NotNil(t, profiles.Commands())
}

func TestRunHelpMentionsProfileEnvironment(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	root := newRootCommand(func(string) (string, bool) { return "", false }, &stdout, io.Discard)
	root.SetArgs([]string{"run", "--help"})

	require.NoError(t, root.Execute())
	output := stdout.String()
	require.Contains(t, output, "TUNNEL_CLIENT_PROFILE")
	require.Contains(t, output, "XDG_CONFIG_HOME")
	require.False(t, strings.Contains(output, "Commands:"))
}
