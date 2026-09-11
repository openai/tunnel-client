package runtimeconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

const confinedProfileYAML = `
control_plane:
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: yaml-control-key
mcp:
  server_urls:
    - url: https://mcp.example/mcp
`

func TestExplicitEmptyLogFileUsesInheritedOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "managed.yaml"), []byte(confinedProfileYAML+"\nlog:\n  format: json\n  file: /profile-log.jsonl\n"), 0o600))
	for _, flavor := range []Flavor{FlavorFull, FlavorRuntime, FlavorRuntimeCloudflared} {
		for _, envFile := range []string{"", "/environment-log.jsonl"} {
			t.Run(string(flavor)+envFile, func(t *testing.T) {
				args := []string{"--profile-dir", dir, "--profile", "managed"}
				values := map[string]string{}
				if envFile != "" {
					values["LOG_FILE"] = envFile
				}
				env := lookupEnvMap(values)
				cfg, err := Load(args, flavor, env)
				require.NoError(t, err)
				expectedFile := envFile
				if expectedFile == "" {
					expectedFile = "/profile-log.jsonl"
				}
				require.Equal(t, expectedFile, cfg.Logging.File)
				cfg, err = Load(append(args, "--log.file", ""), flavor, env)
				require.NoError(t, err)
				require.Empty(t, cfg.Logging.File, "explicit stdout must override both profile and environment paths")
				require.Equal(t, LogFormatJSON, cfg.Logging.Format)
			})
		}
	}
}

func TestLoadNamedProfileConfinesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	dir := t.TempDir()
	profiles := filepath.Join(dir, "profiles")
	require.NoError(t, os.Mkdir(profiles, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(profiles, "valid.yaml"), []byte(confinedProfileYAML), 0o600))
	outside := filepath.Join(dir, "outside.yaml")
	require.NoError(t, os.WriteFile(outside, []byte(confinedProfileYAML), 0o600))
	require.NoError(t, os.Symlink("valid.yaml", filepath.Join(profiles, "alias.yaml")))
	require.NoError(t, os.Symlink("../outside.yaml", filepath.Join(profiles, "escape.yaml")))
	require.NoError(t, os.Symlink(outside, filepath.Join(profiles, "absolute.yaml")))
	require.NoError(t, os.Mkdir(filepath.Join(profiles, "directory.yaml"), 0o700))
	selected := filepath.Join(dir, "selected")
	require.NoError(t, os.Symlink(profiles, selected))

	for _, tc := range []struct {
		profile string
		allowed bool
	}{
		{"valid", true},
		{"alias", true},
		{"escape", false},
		{"absolute", false},
		{"directory", false},
	} {
		for _, selection := range []string{"flag", "env"} {
			t.Run(tc.profile+"/"+selection, func(t *testing.T) {
				env := map[string]string{
					ProfileDirEnvName:       selected,
					ProfileEnvName:          tc.profile,
					"CONTROL_PLANE_API_KEY": "env-control-key",
				}
				var args []string
				if selection == "flag" {
					args = []string{"--profile", tc.profile}
					env[ProfileEnvName] = "ignored-env-profile"
				}
				cfg, err := Load(args, FlavorRuntime, lookupEnvMap(env))
				if !tc.allowed {
					require.ErrorContains(t, err, "read config file")
					require.Nil(t, cfg)
					return
				}
				require.NoError(t, err)
				require.Equal(t, filepath.Join(selected, tc.profile+".yaml"), cfg.Runtime.ConfigFile)
				require.Equal(t, "env-control-key", cfg.ControlPlane.APIKey)
			})
		}
	}
}

func TestLoadExplicitConfigPathsRemainUnrestricted(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.yaml")
	require.NoError(t, os.WriteFile(outside, []byte(confinedProfileYAML), 0o600))
	selected := outside
	if runtime.GOOS != "windows" {
		selected = filepath.Join(dir, "selected.yaml")
		require.NoError(t, os.Symlink(outside, selected))
	}
	for _, tc := range []struct {
		flag string
		env  string
	}{
		{"config", ConfigEnvName},
		{"profile-file", ProfileFileEnvName},
	} {
		for _, selection := range []string{"flag", "env"} {
			t.Run(tc.flag+"/"+selection, func(t *testing.T) {
				env := map[string]string{ProfileDirEnvName: filepath.Join(dir, "unrelated-missing-profile-directory")}
				var args []string
				if selection == "flag" {
					args = []string{"--" + tc.flag, selected}
					env[ProfileEnvName] = "ignored-env-profile"
				} else {
					env[tc.env] = selected
				}
				cfg, err := Load(args, FlavorRuntime, lookupEnvMap(env))
				require.NoError(t, err)
				require.Equal(t, selected, cfg.Runtime.ConfigFile)
				require.Equal(t, "yaml-control-key", cfg.ControlPlane.APIKey)
			})
		}
	}
}
