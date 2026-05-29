package config

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

type fileConfigValues struct {
	Path        string
	ProfileName string
	ProfilePath string
	ProfileDir  string
	ProfileFile bool
	Raw         []byte
	Env         map[string]string
}

type fileConfig struct {
	ConfigVersion *int                   `yaml:"config_version"`
	CABundle      *string                `yaml:"ca_bundle"`
	HTTPProxy     *string                `yaml:"http_proxy"`
	ControlPlane  fileControlPlaneConfig `yaml:"control_plane"`
	Log           fileLogConfig          `yaml:"log"`
	Health        fileHealthConfig       `yaml:"health"`
	AdminUI       fileAdminUIConfig      `yaml:"admin_ui"`
	Process       fileProcessConfig      `yaml:"process"`
	MCP           fileMCPConfig          `yaml:"mcp"`
	Harpoon       fileHarpoonConfig      `yaml:"harpoon"`
	Proxy         fileProxyConfig        `yaml:"proxy"`
}

type fileControlPlaneConfig struct {
	BaseURL               *string           `yaml:"base_url"`
	URLPath               *string           `yaml:"url_path"`
	TunnelID              *string           `yaml:"tunnel_id"`
	APIKey                *string           `yaml:"api_key"`
	ClientCert            *string           `yaml:"client_cert"`
	ClientKey             *string           `yaml:"client_key"`
	HTTPProxy             *string           `yaml:"http_proxy"`
	MaxInFlightRequests   *int              `yaml:"max_inflight_requests"`
	PollTimeout           *string           `yaml:"poll_timeout"`
	PollDeadlineGuardrail *string           `yaml:"poll_deadline_guardrail"`
	ExtraHeaders          map[string]string `yaml:"extra_headers"`
}

type fileLogConfig struct {
	Level         *string `yaml:"level"`
	Format        *string `yaml:"format"`
	File          *string `yaml:"file"`
	HTTPRawUnsafe *bool   `yaml:"http_raw_unsafe"`
}

type fileHealthConfig struct {
	ListenAddr *string `yaml:"listen_addr"`
	UnixSocket *string `yaml:"unix_socket"`
	URLFile    *string `yaml:"url_file"`
}

type fileAdminUIConfig struct {
	AllowRemote     *bool `yaml:"allow_remote"`
	OpenBrowser     *bool `yaml:"open_browser"`
	LogBufferEvents *int  `yaml:"log_buffer_events"`
}

type fileProcessConfig struct {
	PIDFile *string `yaml:"pid_file"`
}

type fileMCPConfig struct {
	ServerURLs            []fileMCPServerURL `yaml:"server_urls"`
	Commands              []fileMCPCommand   `yaml:"commands"`
	HTTPProxy             *string            `yaml:"http_proxy"`
	ClientCert            *string            `yaml:"client_cert"`
	ClientKey             *string            `yaml:"client_key"`
	ExtraHeaders          map[string]string  `yaml:"extra_headers"`
	DiscoveryExtraHeaders map[string]string  `yaml:"discovery_extra_headers"`
	ConnectionMaxTTL      *string            `yaml:"connection_max_ttl"`
	MaxConcurrentRequests *int               `yaml:"max_concurrent_requests"`
}

type fileMCPServerURL struct {
	Channel    *string `yaml:"channel"`
	URL        string  `yaml:"url"`
	UnixSocket *string `yaml:"unix_socket"`
	HTTPProxy  *string `yaml:"http_proxy"`
	ClientCert *string `yaml:"client_cert"`
	ClientKey  *string `yaml:"client_key"`
}

type fileMCPCommand struct {
	Channel *string `yaml:"channel"`
	Command string  `yaml:"command"`
}

type fileHarpoonConfig struct {
	Targets               []fileHarpoonTarget `yaml:"targets"`
	AllowPlaintextHTTP    *bool               `yaml:"allow_plaintext_http"`
	MaxResponseBytes      *int                `yaml:"max_response_bytes"`
	MaxRedirects          *int                `yaml:"max_redirects"`
	HTTPProxy             *string             `yaml:"http_proxy"`
	AdditionalTransports  []string            `yaml:"additional_transports"`
	CapturePayloads       *bool               `yaml:"capture_payloads"`
	HostsIncludeSuffix    []string            `yaml:"hosts_include_suffix"`
	HostsIncludeRegex     []string            `yaml:"hosts_include_regex"`
	HostsIncludeLoopback  *bool               `yaml:"hosts_include_loopback"`
	HostsIncludePrivateIP *bool               `yaml:"hosts_include_private"`
}

type fileHarpoonTarget struct {
	Label       string  `yaml:"label"`
	URL         string  `yaml:"url"`
	UnixSocket  *string `yaml:"unix_socket"`
	Description *string `yaml:"description"`
}

type fileProxyConfig struct {
	CheckInterval *string `yaml:"check_interval"`
}

func loadFileConfigValues(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (*fileConfigValues, error) {
	source, err := ResolveConfigSource(fs, lookupEnv)
	if err != nil {
		return nil, err
	}
	if source.Path == "" {
		return nil, nil
	}

	data, err := os.ReadFile(source.Path)
	if err != nil {
		return nil, fmt.Errorf("read config file %s: %w", source.Path, err)
	}

	cfg, err := parseFileConfig(source.Path, data)
	if err != nil {
		return nil, err
	}

	env, err := cfg.toEnv(lookupEnv)
	if err != nil {
		return nil, fmt.Errorf("parse config file %s: %w", source.Path, err)
	}
	return &fileConfigValues{
		Path:        source.Path,
		ProfileName: source.ProfileName,
		ProfilePath: source.ProfilePath,
		ProfileDir:  source.ProfileDir,
		ProfileFile: source.ProfileFile,
		Raw:         bytes.Clone(data),
		Env:         env,
	}, nil
}

func parseFileConfig(path string, data []byte) (fileConfig, error) {
	var cfg fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return fileConfig{}, fmt.Errorf("parse config file %s: %w", path, err)
	}
	if cfg.ConfigVersion != nil && *cfg.ConfigVersion != 1 {
		return fileConfig{}, fmt.Errorf("parse config file %s: unsupported config_version %d", path, *cfg.ConfigVersion)
	}
	return cfg, nil
}

func lookupEnvWithFileValues(lookupEnv func(string) (string, bool), values *fileConfigValues) func(string) (string, bool) {
	if values == nil || len(values.Env) == 0 {
		return lookupEnv
	}
	return func(key string) (string, bool) {
		if val, ok := lookupEnv(key); ok {
			return val, true
		}
		val, ok := values.Env[key]
		return val, ok
	}
}

func (c fileConfig) toEnv(lookupEnv func(string) (string, bool)) (map[string]string, error) {
	env := make(map[string]string)

	setString(env, "CA_BUNDLE", c.CABundle)
	setString(env, "TUNNEL_CLIENT_HTTP_PROXY", c.HTTPProxy)

	if err := setResolvedString(env, "CONTROL_PLANE_BASE_URL", "control_plane.base_url", c.ControlPlane.BaseURL, lookupEnv); err != nil {
		return nil, err
	}
	if err := setResolvedString(env, "CONTROL_PLANE_URL_PATH", "control_plane.url_path", c.ControlPlane.URLPath, lookupEnv); err != nil {
		return nil, err
	}
	setString(env, "CONTROL_PLANE_TUNNEL_ID", c.ControlPlane.TunnelID)
	setString(env, "CONTROL_PLANE_CLIENT_CERT", c.ControlPlane.ClientCert)
	setString(env, "CONTROL_PLANE_CLIENT_KEY", c.ControlPlane.ClientKey)
	setString(env, "CONTROL_PLANE_HTTP_PROXY", c.ControlPlane.HTTPProxy)
	setInt(env, "CONTROL_PLANE_MAX_INFLIGHT_REQUESTS", c.ControlPlane.MaxInFlightRequests)
	setString(env, "CONTROL_PLANE_POLL_TIMEOUT", c.ControlPlane.PollTimeout)
	setString(env, "CONTROL_PLANE_POLL_DEADLINE_GUARDRAIL", c.ControlPlane.PollDeadlineGuardrail)
	if c.ControlPlane.APIKey != nil {
		apiKey, err := resolveConfigSecretReference("control_plane.api_key", *c.ControlPlane.APIKey, lookupEnv)
		if err != nil {
			return nil, err
		}
		env["CONTROL_PLANE_API_KEY"] = apiKey
	}
	if len(c.ControlPlane.ExtraHeaders) > 0 {
		env["CONTROL_PLANE_EXTRA_HEADERS"] = joinHeaderMap(c.ControlPlane.ExtraHeaders)
	}

	setString(env, "LOG_LEVEL", c.Log.Level)
	setString(env, "LOG_FORMAT", c.Log.Format)
	setString(env, "LOG_FILE", c.Log.File)
	setBool(env, "LOG_HTTP_RAW_UNSAFE", c.Log.HTTPRawUnsafe)

	if err := setResolvedString(env, "HEALTH_LISTEN_ADDR", "health.listen_addr", c.Health.ListenAddr, lookupEnv); err != nil {
		return nil, err
	}
	if err := setResolvedString(env, "HEALTH_UNIX_SOCKET", "health.unix_socket", c.Health.UnixSocket, lookupEnv); err != nil {
		return nil, err
	}
	setString(env, "HEALTH_URL_FILE", c.Health.URLFile)
	setBool(env, "ALLOW_REMOTE_UI", c.AdminUI.AllowRemote)
	setBool(env, "OPEN_WEB_UI", c.AdminUI.OpenBrowser)
	setInt(env, "ADMIN_UI_LOG_BUFFER_EVENTS", c.AdminUI.LogBufferEvents)
	setString(env, "PID_FILE", c.Process.PIDFile)

	mcpServerEntries, err := formatResolvedMCPServerURLEntries(c.MCP.ServerURLs, lookupEnv)
	if err != nil {
		return nil, err
	}
	if len(mcpServerEntries) > 0 {
		env["MCP_SERVER_URL"] = strings.Join(mcpServerEntries, "\n")
	}
	mcpCommandEntries, err := formatResolvedMCPCommandEntries(c.MCP.Commands, lookupEnv)
	if err != nil {
		return nil, err
	}
	if len(mcpCommandEntries) > 0 {
		env["MCP_COMMAND"] = strings.Join(mcpCommandEntries, "\n")
	}
	setString(env, "MCP_HTTP_PROXY", c.MCP.HTTPProxy)
	setString(env, "MCP_CLIENT_CERT", c.MCP.ClientCert)
	setString(env, "MCP_CLIENT_KEY", c.MCP.ClientKey)
	if len(c.MCP.ExtraHeaders) > 0 {
		env["MCP_EXTRA_HEADERS"] = joinHeaderMap(c.MCP.ExtraHeaders)
	}
	if len(c.MCP.DiscoveryExtraHeaders) > 0 {
		env["MCP_DISCOVERY_EXTRA_HEADERS"] = joinHeaderMap(c.MCP.DiscoveryExtraHeaders)
	}
	setString(env, "MCP_CONNECTION_MAX_TTL", c.MCP.ConnectionMaxTTL)
	setInt(env, "MCP_MAX_CONCURRENT_REQUESTS", c.MCP.MaxConcurrentRequests)

	harpoonTargets, err := formatResolvedHarpoonTargets(c.Harpoon.Targets, lookupEnv)
	if err != nil {
		return nil, err
	}
	if len(harpoonTargets) > 0 {
		env["HARPOON_TARGETS"] = strings.Join(harpoonTargets, ";")
	}
	setBool(env, "HARPOON_ALLOW_PLAINTEXT_HTTP", c.Harpoon.AllowPlaintextHTTP)
	setInt(env, "HARPOON_MAX_RESPONSE_BYTES", c.Harpoon.MaxResponseBytes)
	setInt(env, "HARPOON_MAX_REDIRECTS", c.Harpoon.MaxRedirects)
	setString(env, "HARPOON_HTTP_PROXY", c.Harpoon.HTTPProxy)
	if len(c.Harpoon.AdditionalTransports) > 0 {
		env["HARPOON_ADDITIONAL_TRANSPORTS"] = strings.Join(c.Harpoon.AdditionalTransports, ";")
	}
	setBool(env, "HARPOON_CAPTURE_PAYLOADS", c.Harpoon.CapturePayloads)
	if len(c.Harpoon.HostsIncludeSuffix) > 0 {
		env["HARPOON_HOSTS_INCLUDE_SUFFIX"] = strings.Join(c.Harpoon.HostsIncludeSuffix, ";")
	}
	if len(c.Harpoon.HostsIncludeRegex) > 0 {
		env["HARPOON_HOSTS_INCLUDE_REGEX"] = strings.Join(c.Harpoon.HostsIncludeRegex, ";")
	}
	setBool(env, "HARPOON_HOSTS_INCLUDE_LOOPBACK", c.Harpoon.HostsIncludeLoopback)
	setBool(env, "HARPOON_HOSTS_INCLUDE_PRIVATE", c.Harpoon.HostsIncludePrivateIP)

	setString(env, "PROXY_CHECK_INTERVAL", c.Proxy.CheckInterval)

	return env, nil
}

func setString(env map[string]string, key string, value *string) {
	if value != nil {
		env[key] = *value
	}
}

func setResolvedString(env map[string]string, key string, source string, value *string, lookupEnv func(string) (string, bool)) error {
	if value == nil {
		return nil
	}
	resolved, err := resolveConfigValueReference(source, *value, lookupEnv)
	if err != nil {
		return err
	}
	env[key] = resolved
	return nil
}

func setBool(env map[string]string, key string, value *bool) {
	if value != nil {
		env[key] = strconv.FormatBool(*value)
	}
}

func setInt(env map[string]string, key string, value *int) {
	if value != nil {
		env[key] = strconv.Itoa(*value)
	}
}

func resolveConfigSecretReference(source string, raw string, lookupEnv func(string) (string, bool)) (string, error) {
	return resolveConfigValueReference(source, raw, lookupEnv)
}

func resolveConfigValueReference(source string, raw string, lookupEnv func(string) (string, bool)) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%s cannot be empty", source)
	}
	lower := strings.ToLower(raw)

	switch {
	case strings.HasPrefix(lower, "env:"):
		name := strings.TrimSpace(raw[len("env:"):])
		if name == "" {
			return "", fmt.Errorf("invalid %s reference %q: environment variable name is required", source, raw)
		}
		value, ok := lookupEnv(name)
		if !ok {
			return "", fmt.Errorf("invalid %s reference %q: environment variable %q is not set", source, raw, name)
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return "", fmt.Errorf("invalid %s reference %q: environment variable %q is empty", source, raw, name)
		}
		return value, nil
	case strings.HasPrefix(lower, "file:"):
		path := strings.TrimSpace(raw[len("file:"):])
		if path == "" {
			return "", fmt.Errorf("invalid %s reference %q: file path is required", source, raw)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("invalid %s reference %q: read file: %w", source, raw, err)
		}
		value := strings.TrimSpace(string(data))
		if value == "" {
			return "", fmt.Errorf("invalid %s reference %q: file is empty", source, raw)
		}
		return value, nil
	default:
		return raw, nil
	}
}

func joinHeaderMap(headers map[string]string) string {
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, key+": "+headers[key])
	}
	return strings.Join(entries, ";")
}

func formatMCPServerURLEntries(entries []fileMCPServerURL) ([]string, error) {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.URL) == "" {
			return nil, fmt.Errorf("mcp.server_urls entry requires url")
		}
		parts := []string{}
		if entry.Channel != nil && *entry.Channel != "" {
			parts = append(parts, "channel="+*entry.Channel)
		}
		parts = append(parts, "url="+entry.URL)
		if entry.UnixSocket != nil && *entry.UnixSocket != "" {
			parts = append(parts, "unix-socket="+*entry.UnixSocket)
		}
		if entry.HTTPProxy != nil && *entry.HTTPProxy != "" {
			parts = append(parts, "http-proxy="+*entry.HTTPProxy)
		}
		if entry.ClientCert != nil && *entry.ClientCert != "" {
			parts = append(parts, "client-cert="+*entry.ClientCert)
		}
		if entry.ClientKey != nil && *entry.ClientKey != "" {
			parts = append(parts, "client-key="+*entry.ClientKey)
		}
		out = append(out, strings.Join(parts, ","))
	}
	return out, nil
}

func formatResolvedMCPServerURLEntries(entries []fileMCPServerURL, lookupEnv func(string) (string, bool)) ([]string, error) {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.URL) == "" {
			return nil, fmt.Errorf("mcp.server_urls entry requires url")
		}
		urlValue, err := resolveConfigValueReference("mcp.server_urls.url", entry.URL, lookupEnv)
		if err != nil {
			return nil, err
		}
		parts := []string{}
		if entry.Channel != nil && *entry.Channel != "" {
			parts = append(parts, "channel="+*entry.Channel)
		}
		parts = append(parts, "url="+urlValue)
		if entry.UnixSocket != nil && *entry.UnixSocket != "" {
			socketPath, err := resolvePathReference("mcp.server_urls.unix_socket", *entry.UnixSocket, lookupEnv)
			if err != nil {
				return nil, err
			}
			parts = append(parts, "unix-socket="+socketPath)
		}
		if entry.HTTPProxy != nil && *entry.HTTPProxy != "" {
			parts = append(parts, "http-proxy="+*entry.HTTPProxy)
		}
		if entry.ClientCert != nil && *entry.ClientCert != "" {
			parts = append(parts, "client-cert="+*entry.ClientCert)
		}
		if entry.ClientKey != nil && *entry.ClientKey != "" {
			parts = append(parts, "client-key="+*entry.ClientKey)
		}
		out = append(out, strings.Join(parts, ","))
	}
	return out, nil
}

func formatMCPCommandEntries(entries []fileMCPCommand) ([]string, error) {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.Command) == "" {
			return nil, fmt.Errorf("mcp.commands entry requires command")
		}
		parts := []string{}
		if entry.Channel != nil && *entry.Channel != "" {
			parts = append(parts, "channel="+*entry.Channel)
		}
		parts = append(parts, "command="+entry.Command)
		out = append(out, strings.Join(parts, ","))
	}
	return out, nil
}

func formatResolvedMCPCommandEntries(entries []fileMCPCommand, lookupEnv func(string) (string, bool)) ([]string, error) {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.Command) == "" {
			return nil, fmt.Errorf("mcp.commands entry requires command")
		}
		commandValue, err := resolveConfigValueReference("mcp.commands.command", entry.Command, lookupEnv)
		if err != nil {
			return nil, err
		}
		parts := []string{}
		if entry.Channel != nil && *entry.Channel != "" {
			parts = append(parts, "channel="+*entry.Channel)
		}
		parts = append(parts, "command="+commandValue)
		out = append(out, strings.Join(parts, ","))
	}
	return out, nil
}

func formatResolvedHarpoonTargets(targets []fileHarpoonTarget, lookupEnv func(string) (string, bool)) ([]string, error) {
	out := make([]string, 0, len(targets))
	for _, target := range targets {
		if strings.TrimSpace(target.Label) == "" {
			return nil, fmt.Errorf("harpoon.targets entry requires label")
		}
		if strings.TrimSpace(target.URL) == "" {
			return nil, fmt.Errorf("harpoon.targets entry %q requires url", target.Label)
		}
		targetURL, err := resolveConfigValueReference("harpoon.targets.url", target.URL, lookupEnv)
		if err != nil {
			return nil, err
		}
		parts := []string{"label=" + target.Label, "url=" + targetURL}
		if target.UnixSocket != nil {
			socketPath, err := resolvePathReference("harpoon.targets.unix_socket", *target.UnixSocket, lookupEnv)
			if err != nil {
				return nil, err
			}
			parts = append(parts, "unix-socket="+socketPath)
		}
		if target.Description != nil && *target.Description != "" {
			parts = append(parts, "desc="+*target.Description)
		}
		out = append(out, strings.Join(parts, ","))
	}
	return out, nil
}
