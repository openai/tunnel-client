package config

import (
	"github.com/spf13/pflag"

	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

// Profile selection and validation are shared production behavior. Keep this
// package as a source-compatible forwarding surface for full-client callers.
type ConfigSource = runtimeconfig.ConfigSource

const (
	ProfileEnvName     = runtimeconfig.ProfileEnvName
	ProfileFileEnvName = runtimeconfig.ProfileFileEnvName
	ProfileDirEnvName  = runtimeconfig.ProfileDirEnvName
	ConfigEnvName      = runtimeconfig.ConfigEnvName
)

func ResolveConfigSource(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (ConfigSource, error) {
	return runtimeconfig.ResolveConfigSource(fs, lookupEnv)
}

func ResolveProfileDir(explicitDir string, lookupEnv func(string) (string, bool)) (string, error) {
	return runtimeconfig.ResolveProfileDir(explicitDir, lookupEnv)
}

func DefaultProfileDir(lookupEnv func(string) (string, bool)) (string, error) {
	return runtimeconfig.DefaultProfileDir(lookupEnv)
}

func ProfilePath(name string, explicitDir string, lookupEnv func(string) (string, bool)) (string, string, error) {
	return runtimeconfig.ProfilePath(name, explicitDir, lookupEnv)
}

func ValidateProfileName(name string) error { return runtimeconfig.ValidateProfileName(name) }
func ValidateProfileFile(path string) error { return runtimeconfig.ValidateFullProfileFile(path) }
func ValidateProfileBytes(path string, data []byte) error {
	return runtimeconfig.ValidateFullProfileBytes(path, data)
}
