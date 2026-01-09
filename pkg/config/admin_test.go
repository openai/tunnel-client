package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

func TestLoadAdminConfig_FlagsOverrideEnv(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	RegisterAdminFlags(fs)
	fs.StringSlice("organization-id", nil, "")
	fs.StringSlice("workspace-id", nil, "")

	args := []string{
		"--control-plane.base-url=https://flag.example",
		"--admin-key=env:FLAG_ADMIN_KEY",
		"--organization-id", "org-1",
		"--organization-id", " org-2 ",
		"--workspace-id", "ws-1",
		"--workspace-id", "   ",
	}
	require.NoError(t, fs.Parse(args))

	lookup := map[string]string{
		"CONTROL_PLANE_BASE_URL": "https://env.example",
		"OPENAI_ADMIN_KEY":       "env-key",
		"FLAG_ADMIN_KEY":         "flag-key",
	}

	cfg, err := LoadAdminConfig(fs, lookupEnvMap(lookup))
	require.NoError(t, err)

	require.Equal(t, "https://flag.example", cfg.BaseURL.String())
	require.Equal(t, "flag-key", cfg.AdminKey)
	require.Equal(t, []string{"org-1", "org-2"}, cfg.OrganizationIDs)
	require.Equal(t, []string{"ws-1"}, cfg.WorkspaceIDs)
}

func TestLoadAdminConfig_UsesEnvWhenFlagsUnset(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	RegisterAdminFlags(fs)
	fs.StringSlice("organization-id", nil, "")
	fs.StringSlice("workspace-id", nil, "")
	require.NoError(t, fs.Parse(nil))

	lookup := map[string]string{
		"CONTROL_PLANE_BASE_URL": "https://env.example",
		"OPENAI_ADMIN_KEY":       "env-key",
	}

	cfg, err := LoadAdminConfig(fs, lookupEnvMap(lookup))
	require.NoError(t, err)

	require.Equal(t, "https://env.example", cfg.BaseURL.String())
	require.Equal(t, "env-key", cfg.AdminKey)
	require.Empty(t, cfg.OrganizationIDs)
	require.Empty(t, cfg.WorkspaceIDs)
}

func TestLoadAdminConfig_DuplicateScopesError(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	RegisterAdminFlags(fs)
	fs.StringSlice("organization-id", nil, "")
	fs.StringSlice("workspace-id", nil, "")

	args := []string{
		"--admin-key=env:FLAG_ADMIN_KEY",
		"--organization-id", "org-1",
		"--organization-id", "org-1",
	}
	require.NoError(t, fs.Parse(args))

	_, err := LoadAdminConfig(fs, lookupEnvMap(map[string]string{"FLAG_ADMIN_KEY": "flag-key"}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate organization-id values: org-1")
}

func TestLoadAdminConfig_InvalidBaseURL(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	RegisterAdminFlags(fs)
	fs.StringSlice("organization-id", nil, "")
	fs.StringSlice("workspace-id", nil, "")
	require.NoError(t, fs.Parse([]string{"--admin-key=env:FLAG_ADMIN_KEY", "--control-plane.base-url", "://missing"}))

	_, err := LoadAdminConfig(fs, lookupEnvMap(map[string]string{"FLAG_ADMIN_KEY": "flag-key"}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid control-plane.base-url")
}

func TestResolveAdminKeyFlagPrefixes(t *testing.T) {
	lookup := map[string]string{"MY_ADMIN": "  spaced-key "}
	key, err := resolveAdminKey("env:MY_ADMIN", lookupEnvMap(lookup))
	require.NoError(t, err)
	require.Equal(t, "spaced-key", key)

	dir := t.TempDir()
	secretPath := filepath.Join(dir, "key.txt")
	require.NoError(t, os.WriteFile(secretPath, []byte("file-key\n"), 0600))

	key, err = resolveAdminKey("file:"+secretPath, nil)
	require.NoError(t, err)
	require.Equal(t, "file-key", key)
}

func TestResolveAdminKeyRejectsRawFlag(t *testing.T) {
	key, err := resolveAdminKey("   raw-key  ", nil)
	require.Error(t, err)
	require.Empty(t, key)
	require.Contains(t, err.Error(), "admin-key must use env: or file: prefixes")

	key, err = resolveAdminKey("bla:whatever", nil)
	require.Error(t, err)
	require.Empty(t, key)
	require.Contains(t, err.Error(), "admin-key must use env: or file: prefixes")
}

func TestResolveAdminKeyEnvEmptyAndMissing(t *testing.T) {
	key, err := resolveAdminKey("", lookupEnvMap(map[string]string{"OPENAI_ADMIN_KEY": "   "}))
	require.Error(t, err)
	require.Empty(t, key)
	require.Contains(t, err.Error(), "must be non-empty")

	key, err = resolveAdminKey("", lookupEnvMap(nil))
	require.Error(t, err)
	require.Empty(t, key)
	require.Contains(t, err.Error(), "admin key is required")
}

func TestDereferenceKeyErrors(t *testing.T) {
	_, err := dereferenceKey("env:", lookupEnvMap(nil))
	require.Error(t, err)
	require.Contains(t, err.Error(), "environment variable name is required")

	_, err = dereferenceKey("env:MISSING", lookupEnvMap(nil))
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not set")

	_, err = dereferenceKey("file:", lookupEnvMap(nil))
	require.Error(t, err)
	require.Contains(t, err.Error(), "file path is required")

	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	require.NoError(t, os.WriteFile(empty, []byte("   \n"), 0600))

	_, err = dereferenceKey("file:"+empty, lookupEnvMap(nil))
	require.Error(t, err)
	require.Contains(t, err.Error(), "is empty")
}

func TestStringSliceValue_TrimsAndSkipsEmpty(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.StringSlice("organization-id", nil, "")
	require.NoError(t, fs.Parse([]string{"--organization-id", " org-1 ", "--organization-id", "  ", "--organization-id", "org-2"}))

	values, err := stringSliceValue(fs, "organization-id")
	require.NoError(t, err)
	require.Equal(t, []string{"org-1", "org-2"}, values)
}

func TestEnsureUnique(t *testing.T) {
	require.NoError(t, ensureUnique("organization-id", []string{"a", "b"}))

	err := ensureUnique("organization-id", []string{"a", "a"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate organization-id values: a")
}

func lookupEnvMap(m map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		val, ok := m[key]
		return val, ok
	}
}
