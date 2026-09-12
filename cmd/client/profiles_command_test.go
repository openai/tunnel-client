package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
	require.NoError(t, os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'config_version: 3\\n' > \"$1\"\n"), 0o600))

	_, _, err := executeProfilesCommand(t, map[string]string{
		"HOME":   t.TempDir(),
		"EDITOR": "sh " + editor,
	}, "profiles", "--profile-dir", profileDir, "edit", "sample")

	require.Error(t, err)
	require.Contains(t, err.Error(), "profile did not validate")
	require.Contains(t, err.Error(), "unsupported config_version 3")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, after)
}

func TestProfilesRejectInvalidTemplateBeforeSaving(t *testing.T) {
	t.Parallel()
	const profile = `config_version: 2
harpoon:
  targets:
    - label: case
      template:
        version: 1
        origin: https://private.example.invalid
        method: GET
        path_template: /cases/{case_id}
        parameters:
          case_id:
            type: string
            required: true
            pattern: '[A-Za-z0-9_-]+'
            max_length: 64
        headers:
          Authorization: env:UNAVAILABLE_TEMPLATE_CREDENTIAL
`
	for _, tc := range []struct{ name, from, to, want string }{
		{"method", "method: GET", "method: POST", "method must be GET"},
		{"origin", "https://private.example.invalid", "http://private.example.invalid", "HTTPS"},
		{"path", "/cases/{case_id}", "/../{case_id}", "invalid literal segment"},
		{"parameter", "            pattern: '[A-Za-z0-9_-]+'\n", "", "pattern or enum"},
	} {
		for _, operation := range []string{"add", "edit"} {
			t.Run(tc.name+"/"+operation, func(t *testing.T) {
				temp := t.TempDir()
				profileDir := filepath.Join(temp, "profiles")
				require.NoError(t, os.Mkdir(profileDir, 0o700))
				path := filepath.Join(profileDir, "sample.yaml")
				contents := strings.Replace(profile, tc.from, tc.to, 1)
				env := map[string]string{"HOME": temp}
				args := []string{"profiles", "--profile-dir", profileDir, operation, "sample"}
				if operation == "add" {
					source := filepath.Join(temp, "source.yaml")
					require.NoError(t, os.WriteFile(source, []byte(contents), 0o600))
					args = append(args, "--from-file", source)
				} else {
					require.NoError(t, os.WriteFile(path, []byte(profile), 0o600))
					editor := filepath.Join(temp, "editor.sh")
					require.NoError(t, os.WriteFile(editor, []byte("#!/bin/sh\ncat > \"$1\" <<'PROFILE_EOF'\n"+contents+"PROFILE_EOF\n"), 0o600))
					env["EDITOR"] = "sh " + editor
				}
				_, _, err := executeProfilesCommand(t, env, args...)
				require.ErrorContains(t, err, tc.want)
				if operation == "add" {
					_, err = os.Stat(path)
					require.ErrorIs(t, err, os.ErrNotExist)
				} else {
					after, err := os.ReadFile(path)
					require.NoError(t, err)
					require.Equal(t, profile, string(after))
				}
			})
		}
	}
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

func TestProfilesRejectEscapingProfileSymlinks(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	for _, operation := range []string{"add", "force", "edit"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			profileDir := filepath.Join(dir, "profiles")
			require.NoError(t, os.Mkdir(profileDir, 0o700))
			outside := filepath.Join(dir, "outside.yaml")
			require.NoError(t, os.WriteFile(outside, []byte("unchanged"), 0o600))
			require.NoError(t, os.Symlink("../outside.yaml", filepath.Join(profileDir, "sample.yaml")))
			args := []string{"profiles", "--profile-dir", profileDir}
			if operation == "edit" {
				args = append(args, "edit", "sample")
			} else {
				args = append(args, "add", "sample", "--sample", "sample_mcp_with_dcr",
					"--tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
					"--mcp-server-url", "https://mcp.example/mcp")
				if operation == "force" {
					args = append(args, "--force")
				}
			}
			_, _, err := executeProfilesCommand(t, map[string]string{"HOME": dir}, args...)
			require.Error(t, err)
			contents, err := os.ReadFile(outside)
			require.NoError(t, err)
			require.Equal(t, "unchanged", string(contents))
		})
	}
}

func TestProfilesAddPreservesSelectedRootAndExternalSource(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	profileDir := filepath.Join(dir, "profile directory")
	require.NoError(t, os.Mkdir(profileDir, 0o700))
	selectedDir := profileDir
	if runtime.GOOS != "windows" {
		selectedDir = filepath.Join(dir, "selected")
		require.NoError(t, os.Symlink(profileDir, selectedDir))
	}
	cwd, err := os.Getwd()
	require.NoError(t, err)
	relativeDir, err := filepath.Rel(cwd, selectedDir)
	require.NoError(t, err)
	source := filepath.Join(dir, "source.yaml")
	data := sampleMCPWithDCRProfile("tunnel_0123456789abcdef0123456789abcdef", "https://mcp.example/mcp", "")
	require.NoError(t, os.WriteFile(source, data, 0o600))
	_, stderr, err := executeProfilesCommand(t, map[string]string{"HOME": dir},
		"profiles", "--profile-dir", relativeDir, "add", "sample", "--from-file", source)
	require.NoError(t, err, stderr)
	contents, err := os.ReadFile(filepath.Join(profileDir, "sample.yaml"))
	require.NoError(t, err)
	require.Equal(t, data, contents)
}

func TestProfilesEditHandlesEditorFileReplacement(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("test editor uses sh")
	}
	for _, escape := range []bool{false, true} {
		name := "regular replacement"
		if escape {
			name = "escaping symlink replacement"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			profileDir := filepath.Join(dir, "profile directory")
			require.NoError(t, os.Mkdir(profileDir, 0o700))
			profilePath := filepath.Join(profileDir, "sample.yaml")
			original := sampleMCPWithDCRProfile("tunnel_0123456789abcdef0123456789abcdef", "https://original.example/mcp", "")
			replacement := sampleMCPWithDCRProfile("tunnel_0123456789abcdef0123456789abcdef", "https://updated.example/mcp", "")
			require.NoError(t, os.WriteFile(profilePath, original, 0o600))
			source := filepath.Join(dir, "source.yaml")
			require.NoError(t, os.WriteFile(source, replacement, 0o600))
			script := "#!/bin/sh\ncp \"$1\" \"$2.replacement\"\nchmod 644 \"$2.replacement\"\nmv \"$2.replacement\" \"$2\"\n"
			if escape {
				script = "#!/bin/sh\nrm \"$2\"\nln -s \"$1\" \"$2\"\n"
			}
			editor := filepath.Join(dir, "editor.sh")
			require.NoError(t, os.WriteFile(editor, []byte(script), 0o600))
			_, stderr, err := executeProfilesCommand(t, map[string]string{
				"HOME":   dir,
				"EDITOR": "sh " + editor + " " + source,
			}, "profiles", "--profile-dir", profileDir, "edit", "sample")
			if escape {
				require.Error(t, err)
			} else {
				require.NoError(t, err, stderr)
			}
			contents, err := os.ReadFile(profilePath)
			require.NoError(t, err)
			if escape {
				require.Equal(t, original, contents)
			} else {
				require.Equal(t, replacement, contents)
			}
			info, err := os.Stat(profilePath)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			entries, err := os.ReadDir(profileDir)
			require.NoError(t, err)
			require.Len(t, entries, 1, "edited and staging files must be removed")
			contents, err = os.ReadFile(source)
			require.NoError(t, err)
			require.Equal(t, replacement, contents)
		})
	}
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
	require.Contains(t, output, "TUNNEL_CLIENT_PROFILE_FILE")
	require.Contains(t, output, "XDG_CONFIG_HOME")
	require.False(t, strings.Contains(output, "Commands:"))
}

func TestValidateProfileConfigChecksTemplatePolicyWithoutResolvingSecrets(t *testing.T) {
	// Reading this environment value would make header validation fail. The
	// nonexistent file likewise proves profile validation does not read secrets.
	t.Setenv("TEMPLATE_PROFILE_CREDENTIAL", "private\ninvalid-header-value")
	profile := `config_version: 2
harpoon:
  targets:
    - label: case
      template:
        version: 1
        origin: https://private.example.invalid
        method: GET
        path_template: /cases/{case_id}
        parameters:
          case_id:
            type: string
            required: true
            pattern: '[A-Za-z0-9_-]+'
            max_length: 64
        headers:
          Authorization: 'ENV: TEMPLATE_PROFILE_CREDENTIAL'
          X-Session: 'FILE: ` + filepath.Join(t.TempDir(), "missing-credential") + `'
        allowed_headers: [Accept]
`
	for _, tc := range []struct{ name, from, to, want string }{
		{"valid secret references", "", "", ""},
		{"wrong method", "method: GET", "method: POST", "method must be GET"},
		{"insecure origin", "https://private.example.invalid", "http://private.example.invalid", "HTTPS"},
		{"unsafe path", "/cases/{case_id}", "/../{case_id}", "invalid literal segment"},
		{"unconstrained parameter", "            pattern: '[A-Za-z0-9_-]+'\n", "", "pattern or enum"},
		{"unbounded parameter", "            max_length: 64\n", "", "length bounds"},
		{"undeclared parameter", "/cases/{case_id}", "/cases/{other_id}", "undeclared parameter"},
		{"redirects", "        allowed_headers:", "        follow_redirects: true\n        allowed_headers:", "redirects must be disabled"},
		{"caller credentials", "allowed_headers: [Accept]", "allowed_headers: [X-AuthToken]", "authentication headers must be fixed"},
		{"invalid secret reference", "TEMPLATE_PROFILE_CREDENTIAL", "NOT-AN-ENV-NAME", "environment variable name is invalid"},
		{"invalid fixed header", "          Authorization: 'ENV: TEMPLATE_PROFILE_CREDENTIAL'", "          Authorization: \"private\\tcredential\"", "invalid template header value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contents := profile
			if tc.from != "" {
				contents = strings.Replace(contents, tc.from, tc.to, 1)
			}
			path := filepath.Join(t.TempDir(), "profile.yaml")
			err := validateProfileConfig(path, []byte(contents))
			if tc.want == "" {
				require.NoError(t, err, "validation must not read referenced credentials")
				return
			}
			require.ErrorContains(t, err, tc.want)
			require.NotContains(t, err.Error(), "private.example.invalid")
			require.NotContains(t, err.Error(), "private\tcredential")
			require.NotContains(t, err.Error(), "invalid-header-value")
		})
	}
}
