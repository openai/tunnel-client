package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/pflag"
)

// AdminConfig captures the options required for tunnel management API calls.
//
// The admin CLI surface is intentionally narrower than the runtime client
// configuration: it only needs a base URL, an admin API key, and the scoped
// organization/workspace headers required by the control plane.
type AdminConfig struct {
	BaseURL         *url.URL
	AdminKey        string
	OrganizationIDs []string
	WorkspaceIDs    []string
}

// RegisterAdminFlags attaches admin/tunnel-management flags to the provided flag set.
func RegisterAdminFlags(fs *pflag.FlagSet) {
	fs.String("control-plane.base-url", defaultControlPlaneBaseURL, "Tunnel control-plane base URL (env.CONTROL_PLANE_BASE_URL)")
	fs.String("admin-key", "", "Admin API key for tunnel management (env.OPENAI_ADMIN_KEY)")
	fs.Bool("json", false, "Output JSON instead of text")
}

// LoadAdminConfig builds an AdminConfig from the provided flag set and environment.
//
// It enforces that an admin API key is present and that at least one of
// organization_id or workspace_id is supplied to scope the request context.
func LoadAdminConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (*AdminConfig, error) {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}

	baseURLRaw := firstSet(
		getValue(fs, "control-plane.base-url"),
		envOrDefault(lookupEnv, "CONTROL_PLANE_BASE_URL", defaultControlPlaneBaseURL),
	)
	baseURL, err := parseURL(baseURLRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid control-plane.base-url: %w", err)
	}

	adminKeyFlag := getValue(fs, "admin-key")
	adminKey, err := resolveAdminKey(adminKeyFlag, lookupEnv)
	if err != nil {
		return nil, err
	}

	orgIDs, err := stringSliceValue(fs, "organization-id")
	if err != nil {
		return nil, err
	}
	workspaceIDs, err := stringSliceValue(fs, "workspace-id")
	if err != nil {
		return nil, err
	}
	if err := ensureUnique("organization-id", orgIDs); err != nil {
		return nil, err
	}
	if err := ensureUnique("workspace-id", workspaceIDs); err != nil {
		return nil, err
	}

	return &AdminConfig{
		BaseURL:         baseURL,
		AdminKey:        adminKey,
		OrganizationIDs: orgIDs,
		WorkspaceIDs:    workspaceIDs,
	}, nil
}

func resolveAdminKey(flagValue string, lookupEnv func(string) (string, bool)) (string, error) {
	if flagValue != "" {
		key, err := dereferenceKey(flagValue, lookupEnv)
		if err == nil {
			return key, nil
		}
		if errors.Is(err, errUnrecognizedKeyFormat) {
			return "", errors.New("admin-key must use env: or file: prefixes")
		}
		return "", err
	}

	if val, ok := lookupEnv("OPENAI_ADMIN_KEY"); ok {
		if strings.TrimSpace(val) == "" {
			return "", errors.New("OPENAI_ADMIN_KEY must be non-empty")
		}
		return strings.TrimSpace(val), nil
	}

	return "", errors.New("admin key is required; set --admin-key or OPENAI_ADMIN_KEY")
}

var errUnrecognizedKeyFormat = errors.New("unrecognized key format")

// dereferenceKey resolves env:/file: prefixes used to avoid passing secrets directly.
func dereferenceKey(raw string, lookupEnv func(string) (string, bool)) (string, error) {
	const (
		envPrefix  = "env:"
		filePrefix = "file:"
	)

	switch {
	case strings.HasPrefix(raw, envPrefix):
		envVar := strings.TrimPrefix(raw, envPrefix)
		if envVar == "" {
			return "", fmt.Errorf("invalid admin-key: environment variable name is required after env:")
		}
		if val, ok := lookupEnv(envVar); ok {
			if strings.TrimSpace(val) == "" {
				return "", fmt.Errorf("environment variable %s referenced by --admin-key is empty", envVar)
			}
			return strings.TrimSpace(val), nil
		}
		return "", fmt.Errorf("environment variable %s referenced by --admin-key is not set", envVar)
	case strings.HasPrefix(raw, filePrefix):
		path := strings.TrimPrefix(raw, filePrefix)
		if path == "" {
			return "", fmt.Errorf("invalid admin-key: file path is required after file:")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read admin key file %s: %w", path, err)
		}
		key := strings.TrimSpace(string(data))
		if key == "" {
			return "", fmt.Errorf("file %s referenced by --admin-key is empty", path)
		}
		return key, nil
	default:
		return "", errUnrecognizedKeyFormat
	}
}

func stringSliceValue(fs *pflag.FlagSet, name string) ([]string, error) {
	if fs == nil {
		return nil, nil
	}
	flag := fs.Lookup(name)
	if flag == nil {
		return nil, nil
	}
	values, err := fs.GetStringSlice(name)
	if err != nil {
		return nil, fmt.Errorf("parse --%s: %w", name, err)
	}

	cleaned := make([]string, 0, len(values))
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	return cleaned, nil
}

func ensureUnique(flagName string, values []string) error {
	seen := make(map[string]bool, len(values))
	dups := make([]string, 0)
	for _, v := range values {
		if seen[v] {
			dups = append(dups, v)
			continue
		}
		seen[v] = true
	}
	if len(dups) > 0 {
		return fmt.Errorf("duplicate %s values: %s", flagName, strings.Join(dups, ", "))
	}
	return nil
}
