package config

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/spf13/pflag"

	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
	"github.com/openai/tunnel-client/pkg/version"
)

// Shared production types are owned by runtimeconfig. These aliases preserve
// the historical pkg/config API for full-client callers.
type LogFormat = runtimeconfig.LogFormat
type MCPTransportKind = runtimeconfig.MCPTransportKind
type HarpoonTransportKind = runtimeconfig.HarpoonTransportKind
type RuntimeConfig = runtimeconfig.RuntimeConfig
type ControlPlaneConfig = runtimeconfig.ControlPlaneConfig
type LoggingConfig = runtimeconfig.LoggingConfig
type HealthConfig = runtimeconfig.HealthConfig
type ProcessConfig = runtimeconfig.ProcessConfig
type MCPConfig = runtimeconfig.MCPConfig
type MCPChannelBinding = runtimeconfig.MCPChannelBinding
type HarpoonTarget = runtimeconfig.HarpoonTarget
type HarpoonHostClassifierConfig = runtimeconfig.HarpoonHostClassifierConfig
type ProxySource = runtimeconfig.ProxySource
type CloudflaredConfig = runtimeconfig.CloudflaredSettings

const (
	LogFormatUnset      = runtimeconfig.LogFormatUnset
	LogFormatStructText = runtimeconfig.LogFormatStructText
	LogFormatJSON       = runtimeconfig.LogFormatJSON
)

const (
	MCPTransportHTTPStreamable = runtimeconfig.MCPTransportHTTPStreamable
	MCPTransportStdio          = runtimeconfig.MCPTransportStdio
	MCPTransportInMemory       = runtimeconfig.MCPTransportInMemory
)

const (
	HarpoonTransportHTTPStreamable = runtimeconfig.HarpoonTransportHTTPStreamable
	DefaultHarpoonMaxResponseBytes = runtimeconfig.DefaultHarpoonMaxResponseBytes
	DefaultHarpoonMaxRedirects     = runtimeconfig.DefaultHarpoonMaxRedirects
)

const (
	defaultProxyCheckInterval     = runtimeconfig.DefaultProxyCheckInterval
	defaultAdminUILogBufferEvents = runtimeconfig.DefaultAdminUILogBufferEvents
	maxAdminUILogBufferEvents     = 100000
)

// Config is the full-client superset. Shared runtime fields stay source
// compatible here, but their values are loaded exactly once by runtimeconfig.
// The one explicit adapter below is covered by parity tests.
type Config struct {
	ControlPlane ControlPlaneConfig
	Logging      LoggingConfig
	Health       HealthConfig
	Process      ProcessConfig
	Cloudflared  CloudflaredConfig
	MCP          MCPConfig
	AdminUI      AdminUIConfig
	Harpoon      HarpoonConfig
	ProxyHealth  ProxyHealthConfig
	TLS          *tlsconfig.Bundle
	Runtime      RuntimeConfig
}

// AdminUIConfig contains full-client-only UI settings.
type AdminUIConfig struct {
	AllowRemote     bool
	OpenBrowser     bool
	LogBufferEvents int
}

// HarpoonConfig keeps the full-client-only payload-capture switch alongside
// runtime-owned shared Harpoon settings for source compatibility.
type HarpoonConfig struct {
	AllowPlaintextHTTP   bool
	MaxResponseBytes     int
	MaxRedirects         int
	AdditionalTransports []HarpoonTransportKind
	Targets              []HarpoonTarget
	CapturePayloads      bool
	HostClassifier       HarpoonHostClassifierConfig
	HTTPProxy            *url.URL
	HTTPProxySource      ProxySource
}

// ProxyHealthConfig contains the full-client-only proxy connectivity probe.
type ProxyHealthConfig struct {
	CheckInterval time.Duration
}

func (h HarpoonConfig) AdditionalTransportEnabled(kind HarpoonTransportKind) bool {
	for _, transport := range h.AdditionalTransports {
		if transport == kind {
			return true
		}
	}
	return false
}

// Load builds the full-client superset from the canonical runtime loader plus
// the small set of full-only extension settings.
func Load(args []string, lookupEnv func(string) (string, bool)) (*Config, error) {
	fs := pflag.NewFlagSet("tunnel-client", pflag.ContinueOnError)
	RegisterFlags(fs)
	fs.Usage = func() { WriteUsage(fs, fs.Output()) }
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return LoadFromFlagSet(fs, lookupEnv)
}

// RegisterFlags delegates every shared flag to runtimeconfig and adds only
// full-client extensions.
func RegisterFlags(fs *pflag.FlagSet) {
	runtimeconfig.RegisterFlags(fs, runtimeconfig.FlavorFull)
	fs.Bool("allow-remote-ui", false, "Allow remote access to the embedded web UI and log endpoints (env.ALLOW_REMOTE_UI)")
	fs.Bool("open-web-ui", false, "Open the embedded web UI in your default browser on startup (env.OPEN_WEB_UI)")
	fs.Int("admin-ui.log-buffer-events", defaultAdminUILogBufferEvents, "Number of recent log events to keep in memory for the embedded web UI and export archive (env.ADMIN_UI_LOG_BUFFER_EVENTS, max 100000)")
	fs.Duration("proxy.check-interval", defaultProxyCheckInterval, "Interval between proxy connectivity checks (env.PROXY_CHECK_INTERVAL)")
	fs.Bool("harpoon.capture-payloads", false, "Capture request/response payloads for the Harpoon admin UI (debug only). (env.HARPOON_CAPTURE_PAYLOADS)")
}

// LoadFromFlagSet loads shared production behavior once in runtimeconfig, then
// applies the full-client-only extension values against the same effective
// flag > environment > profile > default lookup.
func LoadFromFlagSet(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (*Config, error) {
	core, cloudflared, context, err := runtimeconfig.LoadFullFromFlagSet(fs, lookupEnv)
	if err != nil {
		return nil, err
	}
	effectiveLookup := context.LookupEnv
	if effectiveLookup == nil {
		effectiveLookup = os.LookupEnv
	}
	adminUI, err := buildAdminUIConfig(fs, effectiveLookup)
	if err != nil {
		return nil, err
	}
	proxyHealth, err := buildProxyHealthConfig(fs, effectiveLookup)
	if err != nil {
		return nil, err
	}
	capturePayloads, err := getBool(fs, effectiveLookup, "harpoon.capture-payloads", "HARPOON_CAPTURE_PAYLOADS")
	if err != nil {
		return nil, err
	}
	return fullConfigFromRuntime(core, cloudflared, adminUI, capturePayloads, proxyHealth), nil
}

func fullConfigFromRuntime(
	core *runtimeconfig.Config,
	cloudflared CloudflaredConfig,
	adminUI AdminUIConfig,
	capturePayloads bool,
	proxyHealth ProxyHealthConfig,
) *Config {
	return &Config{
		ControlPlane: core.ControlPlane,
		Logging:      core.Logging,
		Health:       core.Health,
		Process:      core.Process,
		Cloudflared:  cloudflared,
		MCP:          core.MCP,
		AdminUI:      adminUI,
		Harpoon:      fullHarpoonConfig(core.Harpoon, capturePayloads),
		ProxyHealth:  proxyHealth,
		TLS:          core.TLS,
		Runtime:      core.Runtime,
	}
}

func fullHarpoonConfig(shared runtimeconfig.HarpoonConfig, capturePayloads bool) HarpoonConfig {
	return HarpoonConfig{
		AllowPlaintextHTTP:   shared.AllowPlaintextHTTP,
		MaxResponseBytes:     shared.MaxResponseBytes,
		MaxRedirects:         shared.MaxRedirects,
		AdditionalTransports: shared.AdditionalTransports,
		Targets:              shared.Targets,
		CapturePayloads:      capturePayloads,
		HostClassifier:       shared.HostClassifier,
		HTTPProxy:            shared.HTTPProxy,
		HTTPProxySource:      shared.HTTPProxySource,
	}
}

func buildAdminUIConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (AdminUIConfig, error) {
	allowRemote, err := getBool(fs, lookupEnv, "allow-remote-ui", "ALLOW_REMOTE_UI")
	if err != nil {
		return AdminUIConfig{}, err
	}
	openBrowser, err := getBool(fs, lookupEnv, "open-web-ui", "OPEN_WEB_UI")
	if err != nil {
		return AdminUIConfig{}, err
	}
	logBufferEvents, err := getInt(fs, lookupEnv, "admin-ui.log-buffer-events", "ADMIN_UI_LOG_BUFFER_EVENTS", defaultAdminUILogBufferEvents)
	if err != nil {
		return AdminUIConfig{}, err
	}
	if logBufferEvents <= 0 {
		return AdminUIConfig{}, errors.New("admin-ui.log-buffer-events must be greater than zero")
	}
	if logBufferEvents > maxAdminUILogBufferEvents {
		return AdminUIConfig{}, fmt.Errorf("admin-ui.log-buffer-events must be <= %d", maxAdminUILogBufferEvents)
	}
	return AdminUIConfig{AllowRemote: allowRemote, OpenBrowser: openBrowser, LogBufferEvents: logBufferEvents}, nil
}

func buildProxyHealthConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (ProxyHealthConfig, error) {
	raw := firstSet(getValue(fs, "proxy.check-interval"), envOrDefault(lookupEnv, "PROXY_CHECK_INTERVAL", defaultProxyCheckInterval.String()))
	interval, err := runtimeconfig.ParseProxyCheckInterval(raw)
	if err != nil {
		return ProxyHealthConfig{}, fmt.Errorf("invalid proxy.check-interval: %w", err)
	}
	if interval <= 0 {
		return ProxyHealthConfig{}, errors.New("proxy.check-interval must be positive")
	}
	return ProxyHealthConfig{CheckInterval: interval}, nil
}

func getValue(fs *pflag.FlagSet, name string) string {
	if fs == nil {
		return ""
	}
	flag := fs.Lookup(name)
	if flag == nil || !flag.Changed {
		return ""
	}
	return flag.Value.String()
}

func envOrDefault(lookupEnv func(string) (string, bool), key, fallback string) string {
	if value, ok := lookupEnv(key); ok {
		return value
	}
	return fallback
}

func firstSet(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func getInt(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), flagName, envName string, defaultValue int) (int, error) {
	raw := firstSet(getValue(fs, flagName), envOrDefault(lookupEnv, envName, strconv.Itoa(defaultValue)))
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", flagName, err)
	}
	return value, nil
}

func getBool(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), flagName, envName string) (bool, error) {
	if flag := fs.Lookup(flagName); flag != nil && flag.Changed {
		value, err := fs.GetBool(flagName)
		if err != nil {
			return false, fmt.Errorf("parse --%s: %w", flagName, err)
		}
		return value, nil
	}
	if raw, ok := lookupEnv(envName); ok && raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return false, fmt.Errorf("parse %s: %w", envName, err)
		}
		return value, nil
	}
	return false, nil
}

// WriteUsage keeps the full-client presentation while shared flag help comes
// from the single runtimeconfig registry.
func WriteUsage(fs *pflag.FlagSet, w io.Writer) {
	if fs == nil {
		return
	}
	if w == nil {
		w = fs.Output()
	}
	previousOutput := fs.Output()
	fs.SetOutput(w)
	defer fs.SetOutput(previousOutput)

	name := fs.Name()
	if name == "" {
		name = "tunnel-client"
	}
	_, _ = fmt.Fprintf(fs.Output(), "%s version %s", name, version.Version)
	if version.GitSHA != "" {
		_, _ = fmt.Fprintf(fs.Output(), " (git sha: %s)", version.GitSHA)
	}
	_, _ = fmt.Fprintln(fs.Output())
	_, _ = fmt.Fprintf(fs.Output(), "Usage of %s:\n", name)
	fs.PrintDefaults()
	_, _ = fmt.Fprintln(fs.Output(), "\nAgent-first next steps:")
	_, _ = fmt.Fprintln(fs.Output(), "  tunnel-client help quickstart")
	_, _ = fmt.Fprintln(fs.Output(), "  health_url_file=\"$(mktemp \"${TMPDIR:-/tmp}/tunnel-client-health.XXXXXX.url\")\"; tunnel-client run --embedded-mcp-stub --control-plane.tunnel-id tunnel_... --health.listen-addr 127.0.0.1:0 --health.url-file \"$health_url_file\"")
	_, _ = fmt.Fprintln(fs.Output(), "  tunnel-client init --profile sample_mcp_with_dcr --tunnel-id tunnel_... --mcp-server-url http://127.0.0.1:3001/mcp")
	_, _ = fmt.Fprintln(fs.Output(), "  tunnel-client doctor --profile sample_mcp_with_dcr")
	_, _ = fmt.Fprintln(fs.Output(), "  tunnel-client profiles samples list")
	_, _ = fmt.Fprintln(fs.Output(), "  UI convention: http://<health.listen-addr>/ui")
	_, _ = fmt.Fprintln(fs.Output(), "\nEnvironment variables:")
	_, _ = fmt.Fprintln(fs.Output(), "  CONTROL_PLANE_API_KEY\tAPI key used to authenticate to the tunnel control plane (required; preferred)")
	_, _ = fmt.Fprintln(fs.Output(), "  OPENAI_API_KEY\tAPI key env var used when CONTROL_PLANE_API_KEY unset")
	_, _ = fmt.Fprintln(fs.Output(), "  CONTROL_PLANE_ORGANIZATION_ID\tOrganization ID sent as OpenAI-Organization on tunnel control-plane requests (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  CONTROL_PLANE_URL_PATH\tOptional URL path appended to CONTROL_PLANE_BASE_URL before tunnel-client adds its /v1/... routes")
	_, _ = fmt.Fprintln(fs.Output(), "  CONTROL_PLANE_CLIENT_CERT\tPath to PEM client certificate for control-plane mTLS (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  CONTROL_PLANE_CLIENT_KEY\tPath to PEM client private key for control-plane mTLS (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  CONTROL_PLANE_EXTRA_HEADERS\tStatic headers for tunnel control-plane requests; values accept env:VAR or file:/path (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  TUNNEL_CLIENT_CONFIG\tPath to YAML config file (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  TUNNEL_CLIENT_PROFILE\tProfile name to load from the profile directory (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  TUNNEL_CLIENT_PROFILE_FILE\tPath to a specific profile YAML file (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  TUNNEL_CLIENT_PROFILE_DIR\tProfile directory override (default: $XDG_CONFIG_HOME/tunnel-client or ~/.config/tunnel-client)")
	_, _ = fmt.Fprintln(fs.Output(), "  XDG_CONFIG_HOME\tBase directory for default tunnel-client profiles (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  HEALTH_LISTEN_ADDR\tHealth/admin listen address; use :0 to request an ephemeral port (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  HEALTH_UNIX_SOCKET\tHealth/admin Unix socket path; when set, tunnel-client does not bind TCP for health/admin (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  HEALTH_URL_FILE\tWrite the resolved health base URL after startup; recommended with HEALTH_LISTEN_ADDR=:0 or HEALTH_UNIX_SOCKET (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  ALLOW_REMOTE_UI\tSet to true to allow non-loopback access to the embedded web UI (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  OPEN_WEB_UI\tSet to true to open the embedded web UI in a browser on startup (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  ADMIN_UI_LOG_BUFFER_EVENTS\tRecent log-event capacity for the embedded web UI and export archive (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  CLOUDFLARED_TUNNEL_TOKEN\tPre-provisioned remotely managed Cloudflare Tunnel token; enables bundled cloudflared supervision (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  CLOUDFLARED_MANAGED\tFetch the managed Cloudflare Tunnel runtime token from the control plane on startup (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  CLOUDFLARED_PATH\tAdvanced override for the bundled cloudflared executable path (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  CLOUDFLARED_READY_TIMEOUT\tMaximum startup wait for bundled cloudflared readiness (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  CA_BUNDLE\tPath to a PEM CA bundle used for outbound TLS connections (additive to system trust) (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  MCP_EXTRA_HEADERS\tStatic headers for outbound MCP HTTP requests to the configured MCP server origin; values accept env:VAR or file:/path (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  MCP_DISCOVERY_EXTRA_HEADERS\tStatic headers for MCP discovery/probe requests to the configured MCP server origin; values accept env:VAR or file:/path (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  MCP_STARTUP_WAIT_TIMEOUT\tMaximum opt-in startup wait for the main MCP HTTP listener before first poll (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  MCP_CLIENT_CERT\tPath (or env:VAR) to PEM client certificate for MCP mTLS (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  MCP_CLIENT_KEY\tPath (or env:VAR) to PEM client private key for MCP mTLS (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  PROXY_CHECK_INTERVAL\tInterval between proxy connectivity checks (optional)")
}

func ValidateControlPlaneAPIKey(key string) error {
	return runtimeconfig.ValidateControlPlaneAPIKey(key)
}
func ValidateTunnelID(tunnelID string) error { return runtimeconfig.ValidateTunnelID(tunnelID) }
func NormalizeControlPlaneURLPath(raw string) (string, error) {
	return runtimeconfig.NormalizeControlPlaneURLPath(raw)
}
func ResolveControlPlanePath(baseURL *url.URL, urlPath, routePath string) *url.URL {
	return runtimeconfig.ResolveControlPlanePath(baseURL, urlPath, routePath)
}
func ParseLogFormat(raw string) (LogFormat, error) { return runtimeconfig.ParseLogFormat(raw) }
func NormalizeExtraHeaders(source string, headers map[string]string) (map[string]string, error) {
	return runtimeconfig.NormalizeExtraHeaders(source, headers)
}

const (
	ProxySourceNone        = runtimeconfig.ProxySourceNone
	ProxySourceEnvironment = runtimeconfig.ProxySourceEnvironment
	ProxySourceIgnored     = runtimeconfig.ProxySourceIgnored
)

func RedactProxyURL(proxyURL *url.URL) string { return runtimeconfig.RedactProxyURL(proxyURL) }
func EnvProxyConfigured(lookupEnv func(string) (string, bool)) bool {
	return runtimeconfig.EnvProxyConfigured(lookupEnv)
}
func ProxyLogFields(proxyURL *url.URL, source ProxySource) []any {
	return runtimeconfig.ProxyLogFields(proxyURL, source)
}
