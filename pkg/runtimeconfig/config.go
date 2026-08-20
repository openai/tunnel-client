package runtimeconfig

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/openai/tunnel-client/pkg/tlsconfig"
	"github.com/openai/tunnel-client/pkg/types"
)

// LogFormat enumerates the supported logging formats.
type LogFormat int

const (
	LogFormatUnset LogFormat = iota
	LogFormatStructText
	LogFormatJSON
)

// MCPTransportKind describes the available MCP transport types.
type MCPTransportKind string

const (
	MCPTransportHTTPStreamable MCPTransportKind = "http-streamable"
	MCPTransportStdio          MCPTransportKind = "stdio"
	MCPTransportInMemory       MCPTransportKind = "in-memory"
)

// HarpoonTransportKind enumerates supported harpoon transports.
type HarpoonTransportKind string

const (
	HarpoonTransportHTTPStreamable HarpoonTransportKind = "http-streamable"
)

const (
	defaultControlPlaneBaseURL                         = "https://api.openai.com"
	defaultControlPlaneMTLSBaseURL                     = "https://mtls.api.openai.com"
	defaultControlPlaneMaxInFlight                     = 20
	maxControlPlaneMaxInFlight                         = 10000
	defaultControlPlanePollTimeout                     = 30 * time.Second
	defaultControlPlanePollDeadlineGuardrail           = 5000 * time.Millisecond
	maxControlPlanePollDeadlineGuardrail               = time.Minute
	maxControlPlanePollDeadline                        = 10 * time.Minute
	defaultLogLevel                                    = "info"
	defaultLogFormat                         LogFormat = LogFormatUnset
	defaultHealthListenAddr                            = "127.0.0.1:8080"
	defaultCloudflaredReadyTimeout                     = 30 * time.Second
	defaultMCPConnectionMaxTTL                         = 10 * time.Minute
	defaultMCPMaxConcurrentRequests                    = 10
	DefaultHarpoonMaxResponseBytes                     = 100 * 1024
	DefaultHarpoonMaxRedirects                         = 5
)

// These full-client extension defaults live beside the canonical profile
// loader because runtime flavors must recognize their harmless legacy values
// without importing pkg/config.
const (
	DefaultAdminUILogBufferEvents = 2000
	DefaultProxyCheckInterval     = 60 * time.Second
)

// DefaultControlPlaneBaseURL is the canonical production default shared by
// runtime and full-client administrative commands.
const DefaultControlPlaneBaseURL = defaultControlPlaneBaseURL

const _ = uint(maxControlPlaneMaxInFlight - defaultControlPlaneMaxInFlight)
const _ = uint(defaultControlPlanePollTimeout - 1)
const _ = uint(maxControlPlanePollDeadline - defaultControlPlanePollTimeout - defaultControlPlanePollDeadlineGuardrail)
const _ = uint(defaultControlPlanePollDeadlineGuardrail - 1)
const _ = uint(maxControlPlanePollDeadlineGuardrail - defaultControlPlanePollDeadlineGuardrail - 1)

var harpoonLabelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

var (
	errMissingControlPlaneAPIKey   = errors.New("control plane API key is required; set --control-plane.api-key (env:/file:) or CONTROL_PLANE_API_KEY or OPENAI_API_KEY")
	errMalformedControlPlaneAPIKey = errors.New("control plane API key is malformed")
	controlPlaneAPIKeyPattern      = regexp.MustCompile("^[0-9A-Za-z_-]+$")
	tunnelIDPattern                = regexp.MustCompile(`^tunnel_[a-z0-9]{32}$`)
	logFormatToString              = map[LogFormat]string{
		LogFormatStructText: "struct-text",
		LogFormatJSON:       "json",
	}
)

// ValidateControlPlaneAPIKey verifies that key can be safely used as an HTTP
// bearer token. It deliberately returns redacted errors: callers must not
// expose any key-derived details while reporting configuration failures.
func ValidateControlPlaneAPIKey(key string) error {
	if key == "" {
		return errMissingControlPlaneAPIKey
	}
	if strings.TrimSpace(key) != key {
		return errMalformedControlPlaneAPIKey
	}
	if !controlPlaneAPIKeyPattern.MatchString(key) {
		return errMalformedControlPlaneAPIKey
	}
	return nil
}

type flagAlias struct {
	Canonical string
	Alias     string
	Kind      string
}

// Flavor selects the deliberately narrow runtime configuration surface.
//
// The flavor is part of the compile-time artifact boundary: the normal runtime
// never registers or accepts cloudflared configuration, while the cloudflared
// runtime accepts only the approved companion settings in addition to the
// shared runtime surface.
type Flavor string

const (
	// FlavorFull lets the full client reuse the same production runtime
	// loader while layering its UI/debug-only settings in pkg/config.
	FlavorFull               Flavor = "full"
	FlavorRuntime            Flavor = "runtime"
	FlavorRuntimeCloudflared Flavor = "runtime-cloudflared"
)

// LoadContext exposes the effective lookup and selected source produced by the
// shared loader so full-client-only adapters can apply their own settings
// without reimplementing shared precedence or profile parsing.
type LoadContext struct {
	LookupEnv func(string) (string, bool)
	Source    ConfigSource
	RawConfig []byte
}

var commonFlagAliases = []flagAlias{
	{Canonical: "control-plane.base-url", Alias: "control-plane-base-url", Kind: "string"},
	{Canonical: "control-plane.url-path", Alias: "control-plane-url-path", Kind: "string"},
	{Canonical: "control-plane.tunnel-id", Alias: "control-plane-tunnel-id", Kind: "string"},
	{Canonical: "control-plane.organization-id", Alias: "control-plane-organization-id", Kind: "string"},
	{Canonical: "control-plane.api-key", Alias: "control-plane-api-key", Kind: "string"},
	{Canonical: "control-plane.client-cert", Alias: "control-plane-client-cert", Kind: "string"},
	{Canonical: "control-plane.client-key", Alias: "control-plane-client-key", Kind: "string"},
	{Canonical: "mcp.server-url", Alias: "mcp-server-url", Kind: "stringArray"},
	{Canonical: "mcp.command", Alias: "mcp-command", Kind: "stringArray"},
	{Canonical: "mcp.extra-headers", Alias: "mcp-extra-headers", Kind: "stringArray"},
	{Canonical: "mcp.discovery-extra-headers", Alias: "mcp-discovery-extra-headers", Kind: "stringArray"},
	{Canonical: "health.listen-addr", Alias: "health-listen-addr", Kind: "string"},
	{Canonical: "health.unix-socket", Alias: "health-unix-socket", Kind: "string"},
	{Canonical: "health.url-file", Alias: "health-url-file", Kind: "string"},
}

// Config captures the runtime values required to start the tunnel client.
type Config struct {
	ControlPlane ControlPlaneConfig
	Logging      LoggingConfig
	Health       HealthConfig
	Process      ProcessConfig
	MCP          MCPConfig
	Harpoon      HarpoonConfig
	TLS          *tlsconfig.Bundle
	Runtime      RuntimeConfig
}

// RuntimeConfig captures startup metadata that is useful for diagnostics but
// does not affect runtime behavior.
type RuntimeConfig struct {
	ConfigFile         string
	ConfigFileContents []byte
	ProfileName        string
	ProfilePath        string
	ProfileDir         string
	ProfileFile        bool
}

// ControlPlaneConfig defines how the client reaches the tunnel control plane.
type ControlPlaneConfig struct {
	BaseURL               *url.URL
	UnixSocketPath        string
	URLPath               string
	TunnelID              types.TunnelID
	OrganizationID        string
	APIKey                string
	MaxInFlightRequests   int
	PollTimeout           time.Duration
	PollDeadlineGuardrail time.Duration
	// PollChannels is an explicit, sorted allowlist of channels to drain. When
	// PollChannelsConfigured is false, polling remains wire-compatible with
	// older clients and sends no channel query parameters.
	PollChannels           []types.Channel
	PollChannelsConfigured bool
	// PollBackoffMin/PollBackoffMax allow overriding the poller's retry window.
	// Zero values fall back to the internal defaults.
	PollBackoffMin    time.Duration
	PollBackoffMax    time.Duration
	ClientCertificate *tlsconfig.ClientCertificate
	ExtraHeaders      map[string]string
	// MCPServerInfoHeader returns metadata generated from effective MCP channel
	// bindings and sent as a protected control-plane header. It is not
	// operator-configurable.
	MCPServerInfoHeader func() (string, error)
	HTTPProxy           *url.URL
	HTTPProxySource     ProxySource
}

// LoggingConfig defines logging behavior for the client.
type LoggingConfig struct {
	Level         slog.Level
	Format        LogFormat
	File          string
	HTTPRawUnsafe bool
}

// HealthConfig defines the health server behavior.
type HealthConfig struct {
	ListenAddr string
	UnixSocket string
	URLFile    string
}

// ProcessConfig defines process-level runtime settings.
type ProcessConfig struct {
	PIDFile string
}

// CloudflaredSettings defines the optional bundled Cloudflare Tunnel companion
// process. A configured token or explicit managed mode enables supervision;
// otherwise the normal tunnel-client runtime is unchanged.
type CloudflaredSettings struct {
	// Token is the pre-provisioned remotely managed Cloudflare Tunnel token.
	// It is kept in memory only and is passed to cloudflared through its
	// TUNNEL_TOKEN environment variable, never argv.
	Token string
	// Managed fetches the remotely managed Cloudflare Tunnel runtime token from
	// tunnel-service during startup. A configured Token takes precedence so
	// existing pre-provisioned deployments remain unchanged.
	Managed bool
	// Path overrides sibling-binary discovery for source builds and tests.
	Path string
	// ReadyTimeout bounds startup while waiting for cloudflared /ready.
	ReadyTimeout time.Duration
}

// Enabled reports whether the bundled cloudflared companion should run.
func (c CloudflaredSettings) Enabled() bool {
	return strings.TrimSpace(c.Token) != "" || c.Managed
}

// CloudflaredConfig composes the shared runtime configuration with the only
// extra settings accepted by the runtime-cloudflared artifact.
type CloudflaredConfig struct {
	Runtime     Config
	Cloudflared CloudflaredSettings
}

// MCPConfig captures configuration for the Model Context Protocol integration.
//
// The legacy top-level ServerURL/Command fields mirror the main channel so older
// call sites can keep reading cfg.MCP.ServerURL while the dispatcher routes from
// ChannelBindings. New connector/channel behavior should be modeled as an
// MCPChannelBinding first, then projected to the legacy fields only for
// compatibility.
type MCPConfig struct {
	ServerURL         *url.URL
	UnixSocketPath    string
	Command           string
	CommandArgs       []string
	TransportKind     MCPTransportKind
	ClientCertificate *tlsconfig.ClientCertificate
	ChannelBindings   []MCPChannelBinding
	// AllowNoMain is set only for an explicit poll allowlist that excludes main.
	// It keeps zero-value MCPConfig compatibility for older callers.
	AllowNoMain bool
	// StartupWaitTimeout enables an opt-in startup gate for the main
	// HTTP-streamable MCP listener. Zero preserves legacy behavior.
	StartupWaitTimeout    time.Duration
	ConnectionMaxTTL      time.Duration
	MaxConcurrentRequests int
	ExtraHeaders          map[string]string
	DiscoveryExtraHeaders map[string]string
	HTTPProxy             *url.URL
	HTTPProxySource       ProxySource
}

// PollTimeoutOrDefault returns the configured requested service wait or its runtime default.
func (c ControlPlaneConfig) PollTimeoutOrDefault() time.Duration {
	if c.PollTimeout <= 0 {
		return defaultControlPlanePollTimeout
	}
	return c.PollTimeout
}

// PollDeadlineGuardrailOrDefault returns the configured client deadline guardrail or its runtime default.
func (c ControlPlaneConfig) PollDeadlineGuardrailOrDefault() time.Duration {
	if c.PollDeadlineGuardrail <= 0 {
		return defaultControlPlanePollDeadlineGuardrail
	}
	return c.PollDeadlineGuardrail
}

// PollDeadlineTimeoutOrDefault returns the client HTTP/context deadline for one poll cycle.
func (c ControlPlaneConfig) PollDeadlineTimeoutOrDefault() time.Duration {
	pollTimeout := c.PollTimeoutOrDefault()
	pollDeadlineGuardrail := c.PollDeadlineGuardrailOrDefault()
	if pollDeadlineGuardrail >= maxControlPlanePollDeadline || pollTimeout > maxControlPlanePollDeadline-pollDeadlineGuardrail {
		return maxControlPlanePollDeadline
	}
	return pollTimeout + pollDeadlineGuardrail
}

// MCPChannelBinding maps one tunnel-service channel to one MCP transport.
//
// Exactly one binding may exist per channel. The reserved harpoon channel is
// supplied by the embedded Harpoon server, not by user MCP config. Streamable
// HTTP bindings may carry proxy and mTLS settings; stdio bindings deliberately
// ignore HTTP-only settings because they communicate over child-process
// stdin/stdout rather than a network socket.
type MCPChannelBinding struct {
	Channel           types.Channel
	TransportKind     MCPTransportKind
	ServerURL         *url.URL
	UnixSocketPath    string
	Command           string
	CommandArgs       []string
	ClientCertificate *tlsconfig.ClientCertificate
	HTTPProxy         *url.URL
	HTTPProxySource   ProxySource
}

// ChannelBindingFor returns the configured binding for the provided channel.
func (c *MCPConfig) ChannelBindingFor(channel types.Channel) *MCPChannelBinding {
	if c == nil {
		return nil
	}
	canonical := channel.Canonical()
	for i := range c.ChannelBindings {
		if c.ChannelBindings[i].Channel.Canonical() == canonical {
			return &c.ChannelBindings[i]
		}
	}
	return nil
}

// MainChannelBinding returns the binding for the main channel, if configured.
func (c *MCPConfig) MainChannelBinding() *MCPChannelBinding {
	return c.ChannelBindingFor(types.DefaultChannel)
}

// HarpoonConfig captures configuration for the embedded harpoon MCP server.
type HarpoonConfig struct {
	AllowPlaintextHTTP   bool
	MaxResponseBytes     int
	MaxRedirects         int
	AdditionalTransports []HarpoonTransportKind
	Targets              []HarpoonTarget
	HostClassifier       HarpoonHostClassifierConfig
	HTTPProxy            *url.URL
	HTTPProxySource      ProxySource
}

// HarpoonHostClassifierConfig controls which hosts are treated as private.
type HarpoonHostClassifierConfig struct {
	IncludeSuffix   []string
	IncludeRegex    []string
	IncludeLoopback bool
	IncludePrivate  bool
}

// HarpoonTarget describes a configured harpoon target.
type HarpoonTarget struct {
	Label          string
	Description    string
	BaseURL        *url.URL
	UnixSocketPath string
}

// AdditionalTransportEnabled reports whether a transport is enabled.
func (h HarpoonConfig) AdditionalTransportEnabled(kind HarpoonTransportKind) bool {
	for _, transport := range h.AdditionalTransports {
		if transport == kind {
			return true
		}
	}
	return false
}

// Load builds a runtime configuration by combining CLI flag arguments with
// environment variables. Flags take precedence over environment variables,
// environment variables over YAML/profile values, and defaults apply last.
func Load(args []string, flavor Flavor, lookupEnv func(string) (string, bool)) (*Config, error) {
	fs := pflag.NewFlagSet("tunnel-client", pflag.ContinueOnError)
	RegisterFlags(fs, flavor)
	fs.Usage = func() {
		WriteUsage(fs, fs.Output())
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if flavor == FlavorFull {
		cfg, _, _, err := LoadFullFromFlagSet(fs, lookupEnv)
		return cfg, err
	}
	if flavor == FlavorRuntimeCloudflared {
		cfg, err := LoadCloudflaredFromFlagSet(fs, lookupEnv)
		if err != nil {
			return nil, err
		}
		return &cfg.Runtime, nil
	}
	return LoadFromFlagSet(fs, lookupEnv)
}

// WriteUsage prints the runtime CLI usage text for the provided flag set.
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
		name = "tunnel-client-runtime"
	}
	_, _ = fmt.Fprintf(fs.Output(), "Usage of %s:\n", name)
	fs.PrintDefaults()
	_, _ = fmt.Fprintln(fs.Output(), "\nEnvironment variables:")
	_, _ = fmt.Fprintln(fs.Output(), "  CONTROL_PLANE_API_KEY\tAPI key used to authenticate to the tunnel control plane (required; preferred)")
	_, _ = fmt.Fprintln(fs.Output(), "  OPENAI_API_KEY\tAPI key env var used when CONTROL_PLANE_API_KEY unset")
	_, _ = fmt.Fprintln(fs.Output(), "  CONTROL_PLANE_TUNNEL_ID\tIdentifier for this client/tunnel (required)")
	_, _ = fmt.Fprintln(fs.Output(), "  MCP_SERVER_URL or MCP_COMMAND\tMain MCP target (required unless poll channels exclude main)")
}

// RegisterFlags attaches only the approved customer runtime flags to fs.
// The normal runtime intentionally omits cloudflared flags; the cloudflared
// flavor adds only the approved companion settings.
func RegisterFlags(fs *pflag.FlagSet, flavor Flavor) {
	if fs == nil {
		return
	}
	registerTLSFlags(fs)
	fs.String("config", "", "Path to YAML config file (env.TUNNEL_CLIENT_CONFIG). Precedence: flags > environment > YAML > defaults")
	fs.String("profile", "", "Profile name to load from the profile directory (env.TUNNEL_CLIENT_PROFILE)")
	fs.String("profile-file", "", "Path to a specific profile YAML file (env.TUNNEL_CLIENT_PROFILE_FILE)")
	fs.String("profile-dir", "", "Directory containing profile YAML files (env.TUNNEL_CLIENT_PROFILE_DIR; default $XDG_CONFIG_HOME/tunnel-client or ~/.config/tunnel-client)")
	fs.String("control-plane.base-url", defaultControlPlaneBaseURL, "Tunnel control-plane base URL (env.CONTROL_PLANE_BASE_URL)")
	fs.String("control-plane.url-path", "", "Optional URL path appended to the control-plane base URL before tunnel-client adds its /v1/... routes (env.CONTROL_PLANE_URL_PATH)")
	fs.String("control-plane.tunnel-id", "", "Identifier for this client/tunnel (env.CONTROL_PLANE_TUNNEL_ID)")
	fs.String("control-plane.organization-id", "", "Organization ID to send as OpenAI-Organization on tunnel control-plane requests (env.CONTROL_PLANE_ORGANIZATION_ID)")
	fs.String("control-plane.api-key", "", "Reference to environment variable or file containing the control-plane API key (format env:VARNAME or file:/path/to/secret)")
	fs.String("control-plane.client-cert", "", "Path to PEM client certificate for control-plane mTLS (format <path|env:VAR|file:/path>) (env.CONTROL_PLANE_CLIENT_CERT)")
	fs.String("control-plane.client-key", "", "Path to PEM client private key for control-plane mTLS (format <path|env:VAR|file:/path>) (env.CONTROL_PLANE_CLIENT_KEY)")
	fs.String("control-plane.http-proxy", "", "Outbound HTTP proxy for the control plane (format <url|env:VAR>)")
	fs.Int("control-plane.max-inflight", defaultControlPlaneMaxInFlight, "Capacity of the local polled-command buffer; polling pauses while the buffer is full (env.CONTROL_PLANE_MAX_INFLIGHT_REQUESTS, max 10000)")
	fs.Duration("control-plane.poll-timeout", defaultControlPlanePollTimeout, "Long-poll timeout when fetching commands from the control plane (env.CONTROL_PLANE_POLL_TIMEOUT)")
	fs.Duration("control-plane.poll-deadline-guardrail", defaultControlPlanePollDeadlineGuardrail, "Extra time after the requested long-poll wait before the control-plane HTTP/context deadline (env.CONTROL_PLANE_POLL_DEADLINE_GUARDRAIL)")
	fs.StringArray("control-plane.poll-channel", nil, "Channel to drain from the control plane (repeatable; env.CONTROL_PLANE_POLL_CHANNELS)")
	fs.StringArray("control-plane.extra-headers", nil, "Additional HTTP headers to send to the tunnel control-plane (format 'Key: Value', repeatable; values accept env:VAR or file:/path) (env.CONTROL_PLANE_EXTRA_HEADERS)")
	fs.String("log.level", defaultLogLevel, "Log level (debug, info, warn) (env.LOG_LEVEL)")
	fs.String("log.format", defaultLogFormat.String(), "Log format (struct-text, json) (env.LOG_FORMAT)")
	fs.String("log.file", "", "Log file path; defaults to stdout when empty (env.LOG_FILE)")
	fs.Bool("log.http-raw-unsafe", false, "Log full raw HTTP requests and responses (including bodies/headers). WARNING: May include PII or sensitive data. Use only for debugging. (env.LOG_HTTP_RAW_UNSAFE)")
	fs.String("health.listen-addr", defaultHealthListenAddr, "Address the health HTTP server listens on (ip:port). Use :8080 to listen on all interfaces, or 127.0.0.1:0 to request a loopback ephemeral port from the OS. Ignored when health.unix-socket is set. (env.HEALTH_LISTEN_ADDR)")
	fs.String("health.unix-socket", "", "Unix socket path for the health HTTP server. When set, tunnel-client serves health over the socket instead of binding TCP. (env.HEALTH_UNIX_SOCKET)")
	fs.String("health.url-file", "", "File to write the health base URL to after startup (env.HEALTH_URL_FILE)")
	fs.String("pid.file", "", "File to write the tunnel-client process ID to (env.PID_FILE)")
	fs.String("http-proxy", "", "Global outbound HTTP proxy (applies to control-plane, MCP, and Harpoon) (format <url|env:VAR>)")
	fs.StringArray("mcp.server-url", nil, "Target MCP server URL (repeatable; format url=...,channel=...,unix-socket=...,http-proxy=...,client-cert=...,client-key=...) (env.MCP_SERVER_URL)")
	fs.StringArray("mcp.command", nil, "Command to launch an MCP server over stdio (repeatable; format command=...,channel=...) (env.MCP_COMMAND)")
	fs.String("mcp.http-proxy", "", "Outbound HTTP proxy for MCP (format <url|env:VAR>)")
	fs.String("mcp.client-cert", "", "Path to PEM client certificate for MCP mTLS (format <path|env:VAR>) (env.MCP_CLIENT_CERT)")
	fs.String("mcp.client-key", "", "Path to PEM client private key for MCP mTLS (format <path|env:VAR>) (env.MCP_CLIENT_KEY)")
	fs.StringArray("mcp.extra-headers", nil, "Static HTTP headers to send to the configured MCP server origin (format 'Key: Value', repeatable; values accept env:VAR or file:/path) (env.MCP_EXTRA_HEADERS)")
	fs.StringArray("mcp.discovery-extra-headers", nil, "Static HTTP headers to send to MCP discovery/probe requests for the configured MCP server origin (format 'Key: Value', repeatable; values accept env:VAR or file:/path) (env.MCP_DISCOVERY_EXTRA_HEADERS)")
	fs.Duration("mcp.startup-wait-timeout", 0, "Maximum opt-in startup wait for the main MCP HTTP listener before first poll (env.MCP_STARTUP_WAIT_TIMEOUT)")
	fs.Duration("mcp.connection-max-ttl", defaultMCPConnectionMaxTTL, "Maximum lifetime of MCP transport connections (env.MCP_CONNECTION_MAX_TTL)")
	fs.Int("mcp.max-concurrent-requests", defaultMCPMaxConcurrentRequests, "Maximum number of requests actively dispatched to the MCP server (env.MCP_MAX_CONCURRENT_REQUESTS)")
	fs.StringArray("harpoon.target", nil, "Harpoon target mapping (format 'label=...,url=...,unix-socket=...,desc=...') (env.HARPOON_TARGETS)")
	fs.Bool("harpoon.allow-plaintext-http", false, "Allow http:// harpoon targets and redirects (env.HARPOON_ALLOW_PLAINTEXT_HTTP)")
	fs.Int("harpoon.max-response-bytes", DefaultHarpoonMaxResponseBytes, "Maximum harpoon response size in bytes (env.HARPOON_MAX_RESPONSE_BYTES)")
	fs.Int("harpoon.max-redirects", DefaultHarpoonMaxRedirects, "Maximum number of harpoon redirects (env.HARPOON_MAX_REDIRECTS)")
	fs.String("harpoon.http-proxy", "", "Outbound HTTP proxy for Harpoon requests (format <url|env:VAR>)")
	fs.StringArray("harpoon.additional-transport", nil, "Additional harpoon transports (http-streamable) (env.HARPOON_ADDITIONAL_TRANSPORTS)")
	fs.StringArray("harpoon.hosts-include-suffix", nil, "Host suffixes treated as private for Harpoon auto-registration (repeatable) (env.HARPOON_HOSTS_INCLUDE_SUFFIX)")
	fs.StringArray("harpoon.hosts-include-regex", nil, "Host regex patterns treated as private for Harpoon auto-registration (repeatable) (env.HARPOON_HOSTS_INCLUDE_REGEX)")
	fs.Bool("harpoon.hosts-include-loopback", true, "Treat loopback hosts as private for Harpoon auto-registration (env.HARPOON_HOSTS_INCLUDE_LOOPBACK)")
	fs.Bool("harpoon.hosts-include-private", true, "Treat private IPs as private for Harpoon auto-registration (env.HARPOON_HOSTS_INCLUDE_PRIVATE)")
	if flavor == FlavorRuntimeCloudflared || flavor == FlavorFull {
		fs.String("cloudflared.token", "", "Reference to an environment variable or file containing a pre-provisioned Cloudflare Tunnel token (format env:VARNAME or file:/path; env.CLOUDFLARED_TUNNEL_TOKEN)")
		fs.Bool("cloudflared.managed", false, "Fetch the managed Cloudflare Tunnel runtime token from the control plane on startup (env.CLOUDFLARED_MANAGED)")
		fs.String("cloudflared.path", "", "Path to the bundled cloudflared executable; defaults to the executable beside tunnel-client (env.CLOUDFLARED_PATH)")
		fs.Duration("cloudflared.ready-timeout", defaultCloudflaredReadyTimeout, "Maximum time to wait for bundled cloudflared to report ready (env.CLOUDFLARED_READY_TIMEOUT)")
	}
	if f := fs.Lookup("log.file"); f != nil {
		f.DefValue = "stdout"
	}
	registerFlagAliases(fs)
}

// LoadFromFlagSet builds normal runtime configuration from parsed flags.
func LoadFromFlagSet(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (*Config, error) {
	cfg, _, _, err := loadRuntimeFromFlagSet(fs, lookupEnv, FlavorRuntime)
	return cfg, err
}

// LoadCloudflaredFromFlagSet builds runtime-cloudflared configuration from
// parsed flags, adding only the approved companion settings.
func LoadCloudflaredFromFlagSet(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (*CloudflaredConfig, error) {
	runtimeCfg, fileValues, effectiveLookup, err := loadRuntimeFromFlagSet(fs, lookupEnv, FlavorRuntimeCloudflared)
	if err != nil {
		return nil, err
	}
	var fileToken *string
	if fileValues != nil {
		fileToken = fileValues.CloudflaredToken
	}
	cloudflared, err := buildCloudflaredSettings(fs, effectiveLookup, fileToken)
	if err != nil {
		return nil, err
	}
	return &CloudflaredConfig{Runtime: *runtimeCfg, Cloudflared: cloudflared}, nil
}

// LoadFullFromFlagSet loads the shared production core for the full client and
// returns the approved companion settings plus the effective lookup used for
// full-only adapters. The returned Config never grows full-client-only fields.
func LoadFullFromFlagSet(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (*Config, CloudflaredSettings, LoadContext, error) {
	runtimeCfg, fileValues, effectiveLookup, err := loadRuntimeFromFlagSet(fs, lookupEnv, FlavorFull)
	if err != nil {
		return nil, CloudflaredSettings{}, LoadContext{}, err
	}
	var fileToken *string
	context := LoadContext{LookupEnv: effectiveLookup}
	if fileValues != nil {
		fileToken = fileValues.CloudflaredToken
		context.Source = ConfigSource{
			Path:        fileValues.Path,
			ProfileName: fileValues.ProfileName,
			ProfilePath: fileValues.ProfilePath,
			ProfileDir:  fileValues.ProfileDir,
			ProfileFile: fileValues.ProfileFile,
		}
		context.RawConfig = append([]byte(nil), fileValues.Raw...)
	}
	cloudflared, err := buildCloudflaredSettings(fs, effectiveLookup, fileToken)
	if err != nil {
		return nil, CloudflaredSettings{}, LoadContext{}, err
	}
	return runtimeCfg, cloudflared, context, nil
}

func loadRuntimeFromFlagSet(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), flavor Flavor) (*Config, *fileConfigValues, func(string) (string, bool), error) {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	allowCloudflared := flavor == FlavorRuntimeCloudflared || flavor == FlavorFull
	allowFullOnly := flavor == FlavorFull
	if !allowFullOnly {
		if err := rejectUnsupportedRuntimeSettings(fs, lookupEnv, allowCloudflared); err != nil {
			return nil, nil, lookupEnv, err
		}
	}
	applyFlagAliases(fs)

	fileValues, err := loadFileConfigValues(fs, lookupEnv, allowCloudflared, allowFullOnly)
	if err != nil {
		return nil, nil, lookupEnv, err
	}
	lookupEnv = lookupEnvWithFileValues(lookupEnv, fileValues)

	tlsBundle, err := buildTLSBundle(fs, lookupEnv)
	if err != nil {
		return nil, nil, lookupEnv, err
	}
	globalProxy, globalProxySource, _, err := resolveProxyFlag(fs, lookupEnv, "http-proxy")
	if err != nil {
		return nil, nil, lookupEnv, err
	}
	controlPlane, err := buildControlPlaneConfig(fs, lookupEnv, globalProxy, globalProxySource)
	if err != nil {
		return nil, nil, lookupEnv, err
	}
	mcp, err := buildMCPConfig(fs, lookupEnv, globalProxy, globalProxySource)
	if err != nil {
		return nil, nil, lookupEnv, err
	}
	logging, err := buildLoggingConfig(fs, lookupEnv)
	if err != nil {
		return nil, nil, lookupEnv, err
	}
	health := buildHealthConfig(fs, lookupEnv)
	process := buildProcessConfig(fs, lookupEnv)
	harpoon, err := buildHarpoonConfig(fs, lookupEnv, globalProxy, globalProxySource)
	if err != nil {
		return nil, nil, lookupEnv, err
	}
	if err := validateConfiguredPollChannels(controlPlane, mcp, harpoon); err != nil {
		return nil, nil, lookupEnv, err
	}
	mcp.AllowNoMain = controlPlane.PollChannelsConfigured && !containsPollChannel(controlPlane.PollChannels, types.DefaultChannel)

	cfg := &Config{
		ControlPlane: controlPlane,
		Logging:      logging,
		Health:       health,
		Process:      process,
		MCP:          mcp,
		Harpoon:      harpoon,
		TLS:          tlsBundle,
	}
	if fileValues != nil {
		cfg.Runtime.ConfigFile = fileValues.Path
		cfg.Runtime.ConfigFileContents = fileValues.Raw
		cfg.Runtime.ProfileName = fileValues.ProfileName
		cfg.Runtime.ProfilePath = fileValues.ProfilePath
		cfg.Runtime.ProfileDir = fileValues.ProfileDir
		cfg.Runtime.ProfileFile = fileValues.ProfileFile
	}
	return cfg, fileValues, lookupEnv, nil
}

var fullOnlyFlags = []string{
	"allow-remote-ui",
	"open-web-ui",
	"admin-ui.log-buffer-events",
	"proxy.check-interval",
	"harpoon.capture-payloads",
}

type fullOnlyEnvSetting struct {
	name      string
	isDefault func(string) (bool, error)
}

var fullOnlyEnv = []fullOnlyEnvSetting{
	{name: "ALLOW_REMOTE_UI", isDefault: isDisabledBoolValue},
	{name: "OPEN_WEB_UI", isDefault: isDisabledBoolValue},
	{name: "ADMIN_UI_LOG_BUFFER_EVENTS", isDefault: isDefaultAdminUILogBufferEvents},
	{name: "PROXY_CHECK_INTERVAL", isDefault: isDefaultProxyCheckInterval},
	{name: "HARPOON_CAPTURE_PAYLOADS", isDefault: isDisabledBoolValue},
}

var cloudflaredFlags = []string{
	"cloudflared.token",
	"cloudflared.managed",
	"cloudflared.path",
	"cloudflared.ready-timeout",
}

var cloudflaredEnv = []string{
	"CLOUDFLARED_TUNNEL_TOKEN",
	"CLOUDFLARED_MANAGED",
	"CLOUDFLARED_PATH",
	"CLOUDFLARED_READY_TIMEOUT",
}

func rejectUnsupportedRuntimeSettings(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), allowCloudflared bool) error {
	for _, name := range fullOnlyFlags {
		if flag := fs.Lookup(name); flag != nil && flag.Changed {
			return fmt.Errorf("runtime configuration rejects full-client-only flag --%s", name)
		}
	}
	for _, setting := range fullOnlyEnv {
		raw, ok := lookupEnv(setting.name)
		if !ok {
			continue
		}
		isDefault, err := setting.isDefault(raw)
		if err != nil {
			return fmt.Errorf("runtime configuration rejects invalid full-client-only environment variable %s: %w", setting.name, err)
		}
		if !isDefault {
			return fmt.Errorf("runtime configuration rejects non-default full-client-only environment variable %s", setting.name)
		}
	}
	if allowCloudflared {
		return nil
	}
	for _, name := range cloudflaredFlags {
		if flag := fs.Lookup(name); flag != nil && flag.Changed {
			return fmt.Errorf("runtime configuration rejects cloudflared flag --%s", name)
		}
	}
	for _, name := range cloudflaredEnv {
		if _, ok := lookupEnv(name); ok {
			return fmt.Errorf("runtime configuration rejects cloudflared environment variable %s", name)
		}
	}
	return nil
}

func isDisabledBoolValue(raw string) (bool, error) {
	if raw == "" {
		return true, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, err
	}
	return !value, nil
}

func isDefaultAdminUILogBufferEvents(raw string) (bool, error) {
	value, err := strconv.Atoi(raw)
	if err != nil {
		return false, err
	}
	return value == DefaultAdminUILogBufferEvents, nil
}

func isDefaultProxyCheckInterval(raw string) (bool, error) {
	value, err := ParseProxyCheckInterval(raw)
	if err != nil {
		return false, err
	}
	return value == DefaultProxyCheckInterval, nil
}

// ParseProxyCheckInterval is the canonical parser for the full-client proxy
// health extension and its runtime profile/environment compatibility gate.
func ParseProxyCheckInterval(raw string) (time.Duration, error) {
	return time.ParseDuration(raw)
}

// ValidateTunnelID verifies that the tunnel id matches the runtime contract.
func ValidateTunnelID(tunnelID string) error {
	tunnelID = strings.TrimSpace(tunnelID)
	if tunnelID == "" {
		return errors.New("tunnel ID is required; set --control-plane.tunnel-id or CONTROL_PLANE_TUNNEL_ID")
	}
	if escaped := url.PathEscape(tunnelID); escaped != tunnelID {
		return fmt.Errorf("invalid tunnel ID %q: must be safe for use as a URL path parameter", tunnelID)
	}
	if !tunnelIDPattern.MatchString(tunnelID) {
		return fmt.Errorf("invalid tunnel ID %q: must match tunnel_<32 lowercase letters or digits>", tunnelID)
	}
	return nil
}

func getValue(fs *pflag.FlagSet, name string) string {
	flag := fs.Lookup(name)
	if flag == nil {
		return ""
	}
	if !flag.Changed {
		return ""
	}
	return flag.Value.String()
}

func resolveProxyFlag(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), flagName string) (*url.URL, ProxySource, bool, error) {
	if fs == nil {
		return nil, "", false, nil
	}
	flag := fs.Lookup(flagName)
	if flag != nil && flag.Changed {
		raw := strings.TrimSpace(flag.Value.String())
		if raw == "" {
			return nil, "", true, fmt.Errorf("invalid %s proxy: value is required", flagName)
		}
		parsed, source, err := parseProxyReference(flagName, raw, lookupEnv)
		if err != nil {
			return nil, "", true, err
		}
		return parsed, source, true, nil
	}

	if envName := proxyFlagEnvName(flagName); envName != "" {
		if raw, ok := lookupEnv(envName); ok && raw != "" {
			parsed, source, err := parseProxyReference(flagName, raw, lookupEnv)
			if err != nil {
				return nil, "", true, fmt.Errorf("invalid %s: %w", envName, err)
			}
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "env:") {
				source = ProxySource(envName)
			}
			return parsed, source, true, nil
		}
	}
	return nil, "", false, nil
}

func proxyFlagEnvName(flagName string) string {
	switch flagName {
	case "http-proxy":
		return "TUNNEL_CLIENT_HTTP_PROXY"
	case "control-plane.http-proxy":
		return "CONTROL_PLANE_HTTP_PROXY"
	case "mcp.http-proxy":
		return "MCP_HTTP_PROXY"
	case "harpoon.http-proxy":
		return "HARPOON_HTTP_PROXY"
	default:
		return ""
	}
}

func resolveProxyWithFallback(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), flagName string, fallback *url.URL, fallbackSource ProxySource) (*url.URL, ProxySource, error) {
	parsed, source, set, err := resolveProxyFlag(fs, lookupEnv, flagName)
	if err != nil {
		return nil, "", err
	}
	if set {
		return parsed, source, nil
	}
	if fallback != nil {
		return fallback, fallbackSource, nil
	}
	return nil, ProxySourceNone, nil
}

func registerTLSFlags(fs *pflag.FlagSet) {
	if fs == nil {
		return
	}
	fs.String("ca-bundle", "", "Path to PEM CA bundle for outbound TLS trust (additive to system trust) (env.CA_BUNDLE)")
}

// RegisterTLSFlags exposes the shared CA-bundle flag for narrow adapters such
// as the full client's administrative command surface.
func RegisterTLSFlags(fs *pflag.FlagSet) { registerTLSFlags(fs) }

func buildTLSBundle(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (*tlsconfig.Bundle, error) {
	var path string
	if flag := fs.Lookup("ca-bundle"); flag != nil && flag.Changed {
		path = strings.TrimSpace(flag.Value.String())
	} else if envVal, ok := lookupEnv("CA_BUNDLE"); ok && envVal != "" {
		path = strings.TrimSpace(envVal)
	}
	if path == "" {
		return nil, nil
	}
	resolvedPath, err := resolvePathReference("ca-bundle", path, lookupEnv)
	if err != nil {
		return nil, err
	}
	bundle, err := tlsconfig.LoadBundle(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("invalid ca-bundle %q: %w", resolvedPath, err)
	}
	return bundle, nil
}

// BuildTLSBundle exposes the shared CA-bundle loader for narrow adapters.
func BuildTLSBundle(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (*tlsconfig.Bundle, error) {
	return buildTLSBundle(fs, lookupEnv)
}

func buildMCPClientCertificate(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (*tlsconfig.ClientCertificate, error) {
	return buildClientCertificate("mcp.client-cert", "mcp.client-key", "MCP_CLIENT_CERT", "MCP_CLIENT_KEY", "MCP", fs, lookupEnv)
}

func buildControlPlaneClientCertificate(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (*tlsconfig.ClientCertificate, error) {
	return buildClientCertificate("control-plane.client-cert", "control-plane.client-key", "CONTROL_PLANE_CLIENT_CERT", "CONTROL_PLANE_CLIENT_KEY", "control-plane", fs, lookupEnv)
}

func buildClientCertificate(certFlagName, keyFlagName, certEnvName, keyEnvName, errorLabel string, fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (*tlsconfig.ClientCertificate, error) {
	rawCertPath := firstSet(
		getValue(fs, certFlagName),
		envOrDefault(lookupEnv, certEnvName, ""),
	)
	rawKeyPath := firstSet(
		getValue(fs, keyFlagName),
		envOrDefault(lookupEnv, keyEnvName, ""),
	)

	certPath, err := resolvePathReference(certFlagName, rawCertPath, lookupEnv)
	if err != nil {
		return nil, err
	}
	keyPath, err := resolvePathReference(keyFlagName, rawKeyPath, lookupEnv)
	if err != nil {
		return nil, err
	}
	clientCert, err := tlsconfig.LoadClientCertificate(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("invalid %s client certificate configuration: %w", errorLabel, err)
	}
	return clientCert, nil
}

func resolvePathReference(source, raw string, lookupEnv func(string) (string, bool)) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "file:") {
		path := strings.TrimSpace(raw[len("file:"):])
		if path == "" {
			return "", fmt.Errorf("invalid %s reference %q: file path is required", source, raw)
		}
		return path, nil
	}
	if !strings.HasPrefix(lower, "env:") {
		return raw, nil
	}

	name := strings.TrimSpace(raw[len("env:"):])
	if name == "" {
		return "", fmt.Errorf("invalid %s reference %q: env name is required", source, raw)
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
}

func envOrDefault(lookupEnv func(string) (string, bool), key, fallback string) string {
	if val, ok := lookupEnv(key); ok && val != "" {
		return val
	}
	return fallback
}

func firstSet(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func getControlPlaneAPIKey(flagValue string, lookupEnv func(string) (string, bool)) (string, error) {
	if flagValue != "" {
		const (
			envPrefix  = "env:"
			filePrefix = "file:"
		)

		switch {
		case strings.HasPrefix(flagValue, envPrefix):
			envVar := strings.TrimPrefix(flagValue, envPrefix)
			if envVar == "" {
				return "", errors.New("invalid control-plane.api-key: environment variable name is required after env:")
			}
			if val, ok := lookupEnv(envVar); ok {
				if val == "" {
					return "", fmt.Errorf("environment variable %s referenced by --control-plane.api-key is empty", envVar)
				}
				return val, nil
			}
			return "", fmt.Errorf("environment variable %s referenced by --control-plane.api-key is not set", envVar)
		case strings.HasPrefix(flagValue, filePrefix):
			path := strings.TrimPrefix(flagValue, filePrefix)
			if path == "" {
				return "", errors.New("invalid control-plane.api-key: file path is required after file:")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("read control-plane api key file %s: %w", path, err)
			}
			key := strings.TrimSpace(string(data))
			if key == "" {
				return "", fmt.Errorf("file %s referenced by --control-plane.api-key is empty", path)
			}
			return key, nil
		default:
			return "", fmt.Errorf("invalid control-plane.api-key: value must be prefixed with %q or %q", envPrefix, filePrefix)
		}
	}

	if val, ok := lookupEnv("CONTROL_PLANE_API_KEY"); ok {
		if val == "" {
			return "", errMissingControlPlaneAPIKey
		}
		return val, nil
	}

	if val, ok := lookupEnv("OPENAI_API_KEY"); ok {
		if val == "" {
			return "", errMissingControlPlaneAPIKey
		}
		return val, nil
	}

	return "", errMissingControlPlaneAPIKey
}

func buildControlPlaneConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), globalProxy *url.URL, globalProxySource ProxySource) (ControlPlaneConfig, error) {
	clientCertificate, err := buildControlPlaneClientCertificate(fs, lookupEnv)
	if err != nil {
		return ControlPlaneConfig{}, err
	}
	baseURLRaw := controlPlaneBaseURLRaw(fs, lookupEnv, clientCertificate)
	baseURL, err := parseURL(baseURLRaw)
	if err != nil {
		return ControlPlaneConfig{}, fmt.Errorf("invalid control-plane.base-url: %w", err)
	}
	controlPlaneURLPath, err := NormalizeControlPlaneURLPath(controlPlaneURLPathRaw(fs, lookupEnv))
	if err != nil {
		return ControlPlaneConfig{}, err
	}

	var tunnelID string
	if flag := fs.Lookup("control-plane.tunnel-id"); flag != nil && flag.Changed {
		val := flag.Value.String()
		if val == "" {
			return ControlPlaneConfig{}, errors.New("tunnel ID is required; set --control-plane.tunnel-id or CONTROL_PLANE_TUNNEL_ID")
		}
		tunnelID = val
	}

	if tunnelID == "" {
		if envVal, ok := lookupEnv("CONTROL_PLANE_TUNNEL_ID"); ok {
			if envVal == "" {
				return ControlPlaneConfig{}, errors.New("tunnel ID is required; set --control-plane.tunnel-id or CONTROL_PLANE_TUNNEL_ID")
			}
			tunnelID = envVal
		}
	}

	if err := ValidateTunnelID(tunnelID); err != nil {
		return ControlPlaneConfig{}, err
	}

	organizationID, err := controlPlaneOrganizationID(fs, lookupEnv)
	if err != nil {
		return ControlPlaneConfig{}, err
	}

	maxInFlight := defaultControlPlaneMaxInFlight
	if flag := fs.Lookup("control-plane.max-inflight"); flag != nil && flag.Changed {
		val, err := strconv.Atoi(flag.Value.String())
		if err != nil {
			return ControlPlaneConfig{}, fmt.Errorf("invalid value for --control-plane.max-inflight: %w", err)
		}
		if val <= 0 {
			return ControlPlaneConfig{}, errors.New("control-plane.max-inflight must be greater than zero")
		}
		if val > maxControlPlaneMaxInFlight {
			return ControlPlaneConfig{}, fmt.Errorf("control-plane.max-inflight must be less than or equal to %d", maxControlPlaneMaxInFlight)
		}
		maxInFlight = val
	} else if envVal, ok := lookupEnv("CONTROL_PLANE_MAX_INFLIGHT_REQUESTS"); ok {
		if envVal == "" {
			return ControlPlaneConfig{}, errors.New("CONTROL_PLANE_MAX_INFLIGHT_REQUESTS must be greater than zero")
		}
		val, err := strconv.Atoi(envVal)
		if err != nil {
			return ControlPlaneConfig{}, fmt.Errorf("invalid CONTROL_PLANE_MAX_INFLIGHT_REQUESTS: %w", err)
		}
		if val <= 0 {
			return ControlPlaneConfig{}, errors.New("CONTROL_PLANE_MAX_INFLIGHT_REQUESTS must be greater than zero")
		}
		if val > maxControlPlaneMaxInFlight {
			return ControlPlaneConfig{}, fmt.Errorf("CONTROL_PLANE_MAX_INFLIGHT_REQUESTS must be less than or equal to %d", maxControlPlaneMaxInFlight)
		}
		maxInFlight = val
	}

	pollTimeout := defaultControlPlanePollTimeout
	if flag := fs.Lookup("control-plane.poll-timeout"); flag != nil && flag.Changed {
		val, err := fs.GetDuration("control-plane.poll-timeout")
		if err != nil {
			return ControlPlaneConfig{}, fmt.Errorf("invalid value for --control-plane.poll-timeout: %w", err)
		}
		if val <= 0 {
			return ControlPlaneConfig{}, errors.New("control-plane.poll-timeout must be greater than zero")
		}
		pollTimeout = val
	} else if envVal, ok := lookupEnv("CONTROL_PLANE_POLL_TIMEOUT"); ok && envVal != "" {
		val, err := time.ParseDuration(envVal)
		if err != nil {
			return ControlPlaneConfig{}, fmt.Errorf("invalid CONTROL_PLANE_POLL_TIMEOUT: %w", err)
		}
		if val <= 0 {
			return ControlPlaneConfig{}, errors.New("CONTROL_PLANE_POLL_TIMEOUT must be greater than zero")
		}
		pollTimeout = val
	}

	pollDeadlineGuardrail := defaultControlPlanePollDeadlineGuardrail
	if flag := fs.Lookup("control-plane.poll-deadline-guardrail"); flag != nil && flag.Changed {
		val, err := fs.GetDuration("control-plane.poll-deadline-guardrail")
		if err != nil {
			return ControlPlaneConfig{}, fmt.Errorf("invalid value for --control-plane.poll-deadline-guardrail: %w", err)
		}
		if val <= 0 {
			return ControlPlaneConfig{}, errors.New("control-plane.poll-deadline-guardrail must be greater than zero")
		}
		pollDeadlineGuardrail = val
	} else if envVal, ok := lookupEnv("CONTROL_PLANE_POLL_DEADLINE_GUARDRAIL"); ok && envVal != "" {
		val, err := time.ParseDuration(envVal)
		if err != nil {
			return ControlPlaneConfig{}, fmt.Errorf("invalid CONTROL_PLANE_POLL_DEADLINE_GUARDRAIL: %w", err)
		}
		if val <= 0 {
			return ControlPlaneConfig{}, errors.New("CONTROL_PLANE_POLL_DEADLINE_GUARDRAIL must be greater than zero")
		}
		pollDeadlineGuardrail = val
	}
	if err := validateControlPlanePollTiming(pollTimeout, pollDeadlineGuardrail); err != nil {
		return ControlPlaneConfig{}, err
	}
	pollChannels, pollChannelsConfigured, err := resolvePollChannels(fs, lookupEnv)
	if err != nil {
		return ControlPlaneConfig{}, err
	}

	var apiKeyFlagValue string
	if flag := fs.Lookup("control-plane.api-key"); flag != nil && flag.Changed {
		apiKeyFlagValue = flag.Value.String()
	}

	apiKey, err := getControlPlaneAPIKey(apiKeyFlagValue, lookupEnv)
	if err != nil {
		return ControlPlaneConfig{}, err
	}
	if err := ValidateControlPlaneAPIKey(apiKey); err != nil {
		return ControlPlaneConfig{}, err
	}

	httpProxy, httpProxySource, err := resolveProxyWithFallback(fs, lookupEnv, "control-plane.http-proxy", globalProxy, globalProxySource)
	if err != nil {
		return ControlPlaneConfig{}, err
	}

	extraHeaders, err := buildControlPlaneExtraHeaders(fs, lookupEnv)
	if err != nil {
		return ControlPlaneConfig{}, err
	}

	return ControlPlaneConfig{
		BaseURL:                baseURL,
		URLPath:                controlPlaneURLPath,
		TunnelID:               types.TunnelID(tunnelID),
		OrganizationID:         organizationID,
		APIKey:                 apiKey,
		MaxInFlightRequests:    maxInFlight,
		PollTimeout:            pollTimeout,
		PollDeadlineGuardrail:  pollDeadlineGuardrail,
		PollChannels:           pollChannels,
		PollChannelsConfigured: pollChannelsConfigured,
		ClientCertificate:      clientCertificate,
		ExtraHeaders:           extraHeaders,
		HTTPProxy:              httpProxy,
		HTTPProxySource:        httpProxySource,
	}, nil
}

func resolvePollChannels(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) ([]types.Channel, bool, error) {
	var raw []string
	if flag := fs.Lookup("control-plane.poll-channel"); flag != nil && flag.Changed {
		values, err := fs.GetStringArray("control-plane.poll-channel")
		if err != nil {
			return nil, true, fmt.Errorf("invalid value for --control-plane.poll-channel: %w", err)
		}
		raw = values
	} else if value, ok := lookupEnv("CONTROL_PLANE_POLL_CHANNELS"); ok {
		raw = strings.Split(value, ",")
	} else {
		return nil, false, nil
	}
	if len(raw) == 0 {
		return nil, true, errors.New("control-plane.poll-channel must contain at least one channel")
	}
	seen := make(map[types.Channel]struct{}, len(raw))
	channels := make([]types.Channel, 0, len(raw))
	for _, value := range raw {
		if value == "" || strings.TrimSpace(value) == "" {
			return nil, true, errors.New("control-plane.poll-channel contains an empty channel")
		}
		channel, err := types.NormalizeChannel(value)
		if err != nil {
			return nil, true, fmt.Errorf("invalid control-plane.poll-channel %q: %w", value, err)
		}
		if channel.String() != value {
			return nil, true, fmt.Errorf("control-plane.poll-channel %q is not canonical; use %q", value, channel)
		}
		if _, ok := seen[channel]; ok {
			return nil, true, fmt.Errorf("duplicate control-plane.poll-channel %q", channel)
		}
		seen[channel] = struct{}{}
		channels = append(channels, channel)
	}
	sort.Slice(channels, func(i, j int) bool { return channels[i] < channels[j] })
	return channels, true, nil
}

func validateConfiguredPollChannels(controlPlane ControlPlaneConfig, mcp MCPConfig, harpoon HarpoonConfig) error {
	if !controlPlane.PollChannelsConfigured {
		if mcp.MainChannelBinding() == nil {
			if len(mcp.ChannelBindings) > 0 {
				return errors.New("main channel is required; add channel=main to one --mcp.server-url or --mcp.command entry")
			}
			return errors.New("main channel is required; set --mcp.server-url or --mcp.command, or MCP_SERVER_URL or MCP_COMMAND")
		}
		return nil
	}
	for _, channel := range controlPlane.PollChannels {
		switch channel {
		case types.DefaultChannel:
			if mcp.MainChannelBinding() == nil {
				return errors.New("control-plane.poll-channel main has no local MCP handler")
			}
		case types.ChannelHarpoon:
			// Main can discover and register Harpoon targets through OAuth after
			// startup. A true Harpoon-only process has no such bootstrap path and
			// must fail closed unless it starts with a routable target.
			if len(harpoon.Targets) == 0 && !containsPollChannel(controlPlane.PollChannels, types.DefaultChannel) {
				return errors.New("control-plane.poll-channel harpoon has no routable target")
			}
		default:
			if mcp.ChannelBindingFor(channel) == nil {
				return fmt.Errorf("control-plane.poll-channel %q has no local handler", channel)
			}
		}
	}
	return nil
}

func containsPollChannel(channels []types.Channel, want types.Channel) bool {
	for _, channel := range channels {
		if channel == want {
			return true
		}
	}
	return false
}

func validateControlPlanePollTiming(pollTimeout, pollDeadlineGuardrail time.Duration) error {
	if pollDeadlineGuardrail >= maxControlPlanePollDeadlineGuardrail {
		return fmt.Errorf("control-plane.poll-deadline-guardrail must be less than %s", maxControlPlanePollDeadlineGuardrail)
	}
	if pollTimeout > maxControlPlanePollDeadline-pollDeadlineGuardrail {
		return fmt.Errorf("control-plane.poll-timeout plus control-plane.poll-deadline-guardrail must be less than or equal to %s", maxControlPlanePollDeadline)
	}
	return nil
}

func controlPlaneBaseURLRaw(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), clientCertificate *tlsconfig.ClientCertificate) string {
	baseURLRaw := firstSet(
		getValue(fs, "control-plane.base-url"),
		envOrDefault(lookupEnv, "CONTROL_PLANE_BASE_URL", defaultControlPlaneBaseURL),
	)
	if clientCertificate == nil {
		return baseURLRaw
	}
	if strings.TrimRight(strings.TrimSpace(baseURLRaw), "/") == defaultControlPlaneBaseURL {
		return defaultControlPlaneMTLSBaseURL
	}
	return baseURLRaw
}

func controlPlaneURLPathRaw(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) string {
	return firstSet(
		getValue(fs, "control-plane.url-path"),
		envOrDefault(lookupEnv, "CONTROL_PLANE_URL_PATH", ""),
	)
}

// ControlPlaneURLPathRaw exposes shared flag/environment precedence for
// administrative commands that do not need the full runtime loader.
func ControlPlaneURLPathRaw(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) string {
	return controlPlaneURLPathRaw(fs, lookupEnv)
}

func controlPlaneOrganizationID(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (string, error) {
	organizationID := strings.TrimSpace(firstSet(
		getValue(fs, "control-plane.organization-id"),
		envOrDefault(lookupEnv, "CONTROL_PLANE_ORGANIZATION_ID", ""),
	))
	if strings.ContainsAny(organizationID, "\r\n") {
		return "", errors.New("control-plane.organization-id cannot contain header line breaks")
	}
	return organizationID, nil
}

// NormalizeControlPlaneURLPath validates and normalizes the optional control-plane URL prefix.
func NormalizeControlPlaneURLPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid control-plane.url-path: %w", err)
	}
	if parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid control-plane.url-path: must be a URL path without scheme, host, query, or fragment")
	}
	if !strings.HasPrefix(parsed.Path, "/") {
		return "", errors.New("invalid control-plane.url-path: must start with '/'")
	}
	if parsed.Path == "/" {
		return "", nil
	}

	return parsed.Path, nil
}

// ResolveControlPlanePath resolves routePath from the control-plane host root plus urlPath.
func ResolveControlPlanePath(baseURL *url.URL, urlPath, routePath string) *url.URL {
	if baseURL == nil {
		return nil
	}

	segments := []string{"/"}
	if normalizedURLPath := strings.Trim(urlPath, "/"); normalizedURLPath != "" {
		segments = append(segments, normalizedURLPath)
	}
	segments = append(segments, strings.TrimPrefix(routePath, "/"))

	return baseURL.ResolveReference(&url.URL{Path: path.Join(segments...)})
}

// buildControlPlaneExtraHeaders resolves additional headers for the control-plane HTTP client.
//
// Values can be supplied either via repeated:
//
//	--control-plane.extra-headers "Key: Value"
//
// or via the CONTROL_PLANE_EXTRA_HEADERS environment variable containing a
// comma- or semicolon-separated list:
//
//	CONTROL_PLANE_EXTRA_HEADERS="extra-header: env:EXTRA_HEADER, debug: 1"
func buildControlPlaneExtraHeaders(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (map[string]string, error) {
	headers, err := buildExtraHeaders(fs, lookupEnv, "control-plane.extra-headers", "CONTROL_PLANE_EXTRA_HEADERS")
	if err != nil {
		return nil, err
	}
	if err := validateControlPlaneExtraHeaders("control-plane.extra-headers", headers); err != nil {
		return nil, err
	}
	return headers, nil
}

func validateControlPlaneExtraHeaders(source string, headers map[string]string) error {
	for key := range headers {
		if isReservedControlPlaneHeader(key) {
			return fmt.Errorf("%s %q cannot override control-plane authentication or client metadata headers", source, key)
		}
	}
	return nil
}

func isReservedControlPlaneHeader(key string) bool {
	switch httpHeaderKey := strings.ToLower(strings.TrimSpace(key)); httpHeaderKey {
	case "authorization", "accept", "user-agent", "x-tunnel-client-name", "x-tunnel-client-version", "x-tunnel-mcp-server-info":
		return true
	default:
		return false
	}
}

func buildExtraHeaders(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), flagName, envName string) (map[string]string, error) {
	var raw []string

	if flag := fs.Lookup(flagName); flag != nil && flag.Changed {
		values, err := fs.GetStringArray(flagName)
		if err != nil {
			return nil, fmt.Errorf("invalid value for --%s: %w", flagName, err)
		}
		raw = append(raw, values...)
	} else if envVal, ok := lookupEnv(envName); ok && envVal != "" {
		if strings.HasPrefix(envVal, encodedExtraHeaderMapPrefix) {
			return decodeExtraHeaderMap(flagName, envVal, lookupEnv)
		}
		raw = splitHeaderList(envVal)
	}

	if len(raw) == 0 {
		return nil, nil
	}

	return parseHeaderList(raw, lookupEnv, flagName)
}

func splitHeaderList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';'
	})

	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if trimmed := strings.TrimSpace(f); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func parseHeaderList(values []string, lookupEnv func(string) (string, bool), source string) (map[string]string, error) {
	headers := make(map[string]string, len(values))
	for _, v := range values {
		key, val, err := parseHeader(v)
		if err != nil {
			return nil, err
		}
		if key == "" {
			continue
		}
		resolvedVal, err := resolveHeaderValue(source+"."+key, val, lookupEnv)
		if err != nil {
			return nil, err
		}
		val = resolvedVal
		headers[key] = val
	}
	return NormalizeExtraHeaders(source, headers)
}

func parseHeader(raw string) (string, string, error) {
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return "", "", errors.New("invalid header: expected 'Key: Value'")
	}

	key := strings.TrimSpace(parts[0])
	value := strings.TrimSpace(parts[1])

	if key == "" {
		return "", "", errors.New("invalid header: key cannot be empty")
	}
	if value == "" {
		return "", "", fmt.Errorf("invalid header value for %q: value cannot be empty", key)
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", "", fmt.Errorf("invalid header value for %q: value cannot contain CR or LF", key)
	}

	return key, value, nil
}

func resolveHeaderValue(source string, raw string, lookupEnv func(string) (string, bool)) (string, error) {
	const (
		envPrefix  = "env:"
		filePrefix = "file:"
	)
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	var value string
	switch {
	case strings.HasPrefix(lower, envPrefix):
		name := strings.TrimSpace(raw[len(envPrefix):])
		if !envNamePattern.MatchString(name) {
			return "", fmt.Errorf("invalid %s reference %q: environment variable name is invalid", source, raw)
		}
		envValue, ok := lookupEnv(name)
		if !ok {
			return "", fmt.Errorf("invalid %s reference %q: environment variable %q is not set", source, raw, name)
		}
		value = strings.TrimSpace(envValue)
	case strings.HasPrefix(lower, filePrefix):
		path := strings.TrimSpace(raw[len(filePrefix):])
		if path == "" {
			return "", fmt.Errorf("invalid %s reference %q: file path is required", source, raw)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s header value file %s: %w", source, path, err)
		}
		value = trimOneTrailingLineEnding(string(data))
	default:
		value = raw
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("invalid %s reference %q: resolved value is empty", source, raw)
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("invalid %s reference %q: resolved value cannot contain CR or LF", source, raw)
	}
	return value, nil
}

func trimOneTrailingLineEnding(value string) string {
	switch {
	case strings.HasSuffix(value, "\r\n"):
		return strings.TrimSuffix(value, "\r\n")
	case strings.HasSuffix(value, "\n"):
		return strings.TrimSuffix(value, "\n")
	case strings.HasSuffix(value, "\r"):
		return strings.TrimSuffix(value, "\r")
	default:
		return value
	}
}

func registerFlagAliases(fs *pflag.FlagSet) {
	if fs == nil {
		return
	}
	for _, alias := range commonFlagAliases {
		switch alias.Kind {
		case "string":
			fs.String(alias.Alias, "", fmt.Sprintf("Alias of --%s", alias.Canonical))
		case "stringArray":
			fs.StringArray(alias.Alias, nil, fmt.Sprintf("Alias of --%s", alias.Canonical))
		case "bool":
			fs.Bool(alias.Alias, false, fmt.Sprintf("Alias of --%s", alias.Canonical))
		default:
			continue
		}
		_ = fs.MarkHidden(alias.Alias)
	}
}

func applyFlagAliases(fs *pflag.FlagSet) {
	if fs == nil {
		return
	}
	for _, alias := range commonFlagAliases {
		canonicalFlag := fs.Lookup(alias.Canonical)
		aliasFlag := fs.Lookup(alias.Alias)
		if canonicalFlag == nil || aliasFlag == nil || canonicalFlag.Changed || !aliasFlag.Changed {
			continue
		}
		switch alias.Kind {
		case "string":
			if err := canonicalFlag.Value.Set(aliasFlag.Value.String()); err == nil {
				canonicalFlag.Changed = true
			}
		case "stringArray":
			values, err := fs.GetStringArray(alias.Alias)
			if err != nil {
				continue
			}
			for _, value := range values {
				if err := canonicalFlag.Value.Set(value); err != nil {
					break
				}
			}
			canonicalFlag.Changed = true
		case "bool":
			if err := canonicalFlag.Value.Set(aliasFlag.Value.String()); err == nil {
				canonicalFlag.Changed = true
			}
		}
	}
}

func buildLoggingConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (LoggingConfig, error) {
	logLevelRaw := firstSet(
		getValue(fs, "log.level"),
		envOrDefault(lookupEnv, "LOG_LEVEL", defaultLogLevel),
	)
	logFile := firstSet(
		getValue(fs, "log.file"),
		envOrDefault(lookupEnv, "LOG_FILE", ""),
	)
	logFormatFlag := getValue(fs, "log.format")
	logFormatEnv, logFormatEnvSet := lookupEnv("LOG_FORMAT")
	logFormatExplicit := logFormatFlag != "" || (logFormatEnvSet && logFormatEnv != "")
	logFormatRaw := firstSet(
		logFormatFlag,
		func() string {
			if logFormatEnvSet && logFormatEnv != "" {
				return logFormatEnv
			}
			if logFile != "" {
				return LogFormatStructText.String()
			}
			return defaultLogFormat.String()
		}(),
	)
	logFormat, err := ParseLogFormat(logFormatRaw)
	if err != nil {
		return LoggingConfig{}, err
	}

	if !strings.EqualFold(logLevelRaw, defaultLogLevel) && logFormat == defaultLogFormat {
		return LoggingConfig{}, fmt.Errorf("log level requires 'struct-text' or 'json' log format")
	}

	if logFormat == LogFormatUnset && logFile != "" && !logFormatExplicit {
		return LoggingConfig{}, fmt.Errorf("invalid logging configuration: file is only supported for json or struct-text")
	}

	httpRawUnsafe, err := resolveHTTPRawUnsafe(fs, lookupEnv)
	if err != nil {
		return LoggingConfig{}, err
	}

	level := slog.LevelInfo
	if logLevelRaw != "" {
		if err := level.UnmarshalText([]byte(logLevelRaw)); err != nil {
			return LoggingConfig{}, fmt.Errorf("parse log level %q: %w", logLevelRaw, err)
		}
	}

	return LoggingConfig{
		Level:         level,
		Format:        logFormat,
		File:          logFile,
		HTTPRawUnsafe: httpRawUnsafe,
	}, nil
}

func buildHealthConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) HealthConfig {
	listenAddr := firstSet(
		getValue(fs, "health.listen-addr"),
		envOrDefault(lookupEnv, "HEALTH_LISTEN_ADDR", defaultHealthListenAddr),
	)
	urlFile := firstSet(
		getValue(fs, "health.url-file"),
		envOrDefault(lookupEnv, "HEALTH_URL_FILE", ""),
	)
	unixSocket := firstSet(
		getValue(fs, "health.unix-socket"),
		envOrDefault(lookupEnv, "HEALTH_UNIX_SOCKET", ""),
	)

	return HealthConfig{
		ListenAddr: listenAddr,
		UnixSocket: unixSocket,
		URLFile:    urlFile,
	}
}

func buildProcessConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) ProcessConfig {
	pidFile := firstSet(
		getValue(fs, "pid.file"),
		envOrDefault(lookupEnv, "PID_FILE", ""),
	)

	return ProcessConfig{PIDFile: pidFile}
}

func buildCloudflaredSettings(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), fileToken *string) (CloudflaredSettings, error) {
	var token string
	if raw := getValue(fs, "cloudflared.token"); raw != "" {
		resolved, err := resolveRequiredSecretReference("cloudflared.token", raw, lookupEnv)
		if err != nil {
			return CloudflaredSettings{}, err
		}
		token = resolved
	} else if envToken, ok := lookupEnv("CLOUDFLARED_TUNNEL_TOKEN"); ok {
		token = strings.TrimSpace(envToken)
		if token == "" {
			return CloudflaredSettings{}, errors.New("CLOUDFLARED_TUNNEL_TOKEN is set but empty")
		}
	} else if fileToken != nil {
		resolved, err := resolveRequiredSecretReference("cloudflared.token", *fileToken, lookupEnv)
		if err != nil {
			return CloudflaredSettings{}, err
		}
		token = resolved
	}

	managed, err := resolveCloudflaredManaged(fs, lookupEnv)
	if err != nil {
		return CloudflaredSettings{}, err
	}

	path := firstSet(
		getValue(fs, "cloudflared.path"),
		envOrDefault(lookupEnv, "CLOUDFLARED_PATH", ""),
	)
	readyTimeoutRaw := firstSet(
		getValue(fs, "cloudflared.ready-timeout"),
		envOrDefault(lookupEnv, "CLOUDFLARED_READY_TIMEOUT", defaultCloudflaredReadyTimeout.String()),
	)
	readyTimeout, err := time.ParseDuration(readyTimeoutRaw)
	if err != nil {
		return CloudflaredSettings{}, fmt.Errorf("invalid cloudflared.ready-timeout: %w", err)
	}
	if readyTimeout <= 0 {
		return CloudflaredSettings{}, errors.New("cloudflared.ready-timeout must be positive")
	}

	return CloudflaredSettings{
		Token:        token,
		Managed:      managed,
		Path:         strings.TrimSpace(path),
		ReadyTimeout: readyTimeout,
	}, nil
}

func resolveCloudflaredManaged(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (bool, error) {
	if flag := fs.Lookup("cloudflared.managed"); flag != nil && flag.Changed {
		val, err := fs.GetBool("cloudflared.managed")
		if err != nil {
			return false, fmt.Errorf("parse --cloudflared.managed: %w", err)
		}
		return val, nil
	}

	if envVal, ok := lookupEnv("CLOUDFLARED_MANAGED"); ok && envVal != "" {
		val, err := strconv.ParseBool(envVal)
		if err != nil {
			return false, fmt.Errorf("parse CLOUDFLARED_MANAGED: %w", err)
		}
		return val, nil
	}

	return false, nil
}

func resolveRequiredSecretReference(source, raw string, lookupEnv func(string) (string, bool)) (string, error) {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, "env:") && !strings.HasPrefix(lower, "file:") {
		return "", fmt.Errorf("invalid %s: value must be prefixed with %q or %q", source, "env:", "file:")
	}
	value, err := resolveConfigSecretReference(source, raw, lookupEnv)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("invalid %s: resolved value is empty", source)
	}
	return value, nil
}

func buildMCPConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), globalProxy *url.URL, globalProxySource ProxySource) (MCPConfig, error) {
	commandEntries, err := resolveMCPEntries(fs, lookupEnv, "mcp.command", "MCP_COMMAND")
	if err != nil {
		return MCPConfig{}, err
	}
	serverEntries, err := resolveMCPEntries(fs, lookupEnv, "mcp.server-url", "MCP_SERVER_URL")
	if err != nil {
		return MCPConfig{}, err
	}

	bindings, err := parseMCPChannelBindings(commandEntries, serverEntries, lookupEnv)
	if err != nil {
		return MCPConfig{}, err
	}

	defaultClientCertificate, err := buildMCPClientCertificate(fs, lookupEnv)
	if err != nil {
		return MCPConfig{}, err
	}

	ttlRaw := firstSet(
		getValue(fs, "mcp.connection-max-ttl"),
		envOrDefault(lookupEnv, "MCP_CONNECTION_MAX_TTL", defaultMCPConnectionMaxTTL.String()),
	)
	ttl, err := time.ParseDuration(ttlRaw)
	if err != nil {
		return MCPConfig{}, fmt.Errorf("invalid mcp.connection-max-ttl: %w", err)
	}
	if ttl <= 0 {
		return MCPConfig{}, errors.New("mcp.connection-max-ttl must be positive")
	}

	startupWaitRaw := firstSet(
		getValue(fs, "mcp.startup-wait-timeout"),
		envOrDefault(lookupEnv, "MCP_STARTUP_WAIT_TIMEOUT", "0s"),
	)
	startupWaitTimeout, err := time.ParseDuration(startupWaitRaw)
	if err != nil {
		return MCPConfig{}, fmt.Errorf("invalid mcp.startup-wait-timeout: %w", err)
	}
	if startupWaitTimeout < 0 {
		return MCPConfig{}, errors.New("mcp.startup-wait-timeout must not be negative")
	}

	maxConcurrent := defaultMCPMaxConcurrentRequests
	if flag := fs.Lookup("mcp.max-concurrent-requests"); flag != nil && flag.Changed {
		val, err := strconv.Atoi(flag.Value.String())
		if err != nil {
			return MCPConfig{}, fmt.Errorf("invalid value for --mcp.max-concurrent-requests: %w", err)
		}
		if val <= 0 {
			return MCPConfig{}, errors.New("mcp.max-concurrent-requests must be greater than zero")
		}
		maxConcurrent = val
	} else if envVal, ok := lookupEnv("MCP_MAX_CONCURRENT_REQUESTS"); ok && envVal != "" {
		val, err := strconv.Atoi(envVal)
		if err != nil {
			return MCPConfig{}, fmt.Errorf("invalid MCP_MAX_CONCURRENT_REQUESTS: %w", err)
		}
		if val <= 0 {
			return MCPConfig{}, errors.New("MCP_MAX_CONCURRENT_REQUESTS must be greater than zero")
		}
		maxConcurrent = val
	}

	mcpProxy, mcpProxySource, err := resolveProxyWithFallback(fs, lookupEnv, "mcp.http-proxy", globalProxy, globalProxySource)
	if err != nil {
		return MCPConfig{}, err
	}
	extraHeaders, err := buildExtraHeaders(fs, lookupEnv, "mcp.extra-headers", "MCP_EXTRA_HEADERS")
	if err != nil {
		return MCPConfig{}, err
	}
	discoveryExtraHeaders, err := buildExtraHeaders(fs, lookupEnv, "mcp.discovery-extra-headers", "MCP_DISCOVERY_EXTRA_HEADERS")
	if err != nil {
		return MCPConfig{}, err
	}

	boundHTTPTransportCount := 0
	for i := range bindings {
		if bindings[i].TransportKind != MCPTransportHTTPStreamable {
			if bindings[i].HTTPProxy != nil {
				return MCPConfig{}, fmt.Errorf("mcp config: http-proxy not supported for %s channel %q", bindings[i].TransportKind, bindings[i].Channel.Canonical())
			}
			if bindings[i].UnixSocketPath != "" {
				return MCPConfig{}, fmt.Errorf("mcp config: unix-socket not supported for %s channel %q", bindings[i].TransportKind, bindings[i].Channel.Canonical())
			}
			if bindings[i].ClientCertificate != nil {
				return MCPConfig{}, fmt.Errorf("mcp config: client certificates are not supported for %s channel %q", bindings[i].TransportKind, bindings[i].Channel.Canonical())
			}
			bindings[i].HTTPProxySource = ProxySourceIgnored
			continue
		}
		boundHTTPTransportCount++
		if bindings[i].ClientCertificate == nil {
			bindings[i].ClientCertificate = defaultClientCertificate
		}
		if bindings[i].UnixSocketPath != "" && bindings[i].HTTPProxy != nil {
			return MCPConfig{}, fmt.Errorf("mcp config: unix-socket cannot be combined with http-proxy for channel %q", bindings[i].Channel.Canonical())
		}
		if bindings[i].UnixSocketPath != "" {
			bindings[i].HTTPProxySource = ProxySourceIgnored
			continue
		}
		if bindings[i].HTTPProxy != nil {
			if bindings[i].HTTPProxySource == "" {
				bindings[i].HTTPProxySource = ProxySource("mcp.server-url")
			}
			continue
		}
		if mcpProxy != nil {
			bindings[i].HTTPProxy = mcpProxy
			bindings[i].HTTPProxySource = mcpProxySource
			continue
		}
		if globalProxy != nil {
			bindings[i].HTTPProxy = globalProxy
			bindings[i].HTTPProxySource = globalProxySource
			continue
		}
		bindings[i].HTTPProxySource = ProxySourceNone
	}
	if defaultClientCertificate != nil && boundHTTPTransportCount == 0 {
		return MCPConfig{}, errors.New("mcp.client-cert and mcp.client-key require at least one http-streamable mcp.server-url binding")
	}

	cfg := MCPConfig{
		ClientCertificate:     defaultClientCertificate,
		ChannelBindings:       bindings,
		StartupWaitTimeout:    startupWaitTimeout,
		ConnectionMaxTTL:      ttl,
		MaxConcurrentRequests: maxConcurrent,
		ExtraHeaders:          extraHeaders,
		DiscoveryExtraHeaders: discoveryExtraHeaders,
		HTTPProxy:             mcpProxy,
		HTTPProxySource:       mcpProxySource,
	}
	if mainBinding := cfg.MainChannelBinding(); mainBinding != nil {
		cfg.ServerURL = mainBinding.ServerURL
		cfg.UnixSocketPath = mainBinding.UnixSocketPath
		cfg.Command = mainBinding.Command
		cfg.CommandArgs = mainBinding.CommandArgs
		cfg.TransportKind = mainBinding.TransportKind
		cfg.ClientCertificate = mainBinding.ClientCertificate
	}
	return cfg, nil
}

func resolveMCPEntries(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), flagName, envKey string) ([]string, error) {
	if flag := fs.Lookup(flagName); flag != nil && flag.Changed {
		values, err := fs.GetStringArray(flagName)
		if err != nil {
			return nil, fmt.Errorf("invalid value for --%s: %w", flagName, err)
		}
		return values, nil
	}
	if envVal, ok := lookupEnv(envKey); ok && envVal != "" {
		return splitMCPEnvEntries(envVal), nil
	}
	return nil, nil
}

func splitMCPEnvEntries(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// parseMCPChannelBindings normalizes all configured MCP endpoints into the
// dispatcher routing table. It rejects duplicate channels across HTTP and stdio
// so a connector command has a single deterministic downstream target.
func parseMCPChannelBindings(commandEntries, serverEntries []string, lookupEnv func(string) (string, bool)) ([]MCPChannelBinding, error) {
	bindings := make([]MCPChannelBinding, 0, len(commandEntries)+len(serverEntries))
	seen := make(map[types.Channel]MCPChannelBinding, len(commandEntries)+len(serverEntries))

	addBinding := func(binding MCPChannelBinding, source string) error {
		canonical := binding.Channel.Canonical()
		if canonical == "" {
			return fmt.Errorf("mcp config: %s channel name is empty", source)
		}
		if canonical == types.ChannelHarpoon {
			return fmt.Errorf("mcp config: %s channel %q is reserved", source, canonical)
		}
		if existing, ok := seen[canonical]; ok {
			return fmt.Errorf(
				"mcp config: duplicate channel %q from %s (%s already configured)",
				canonical,
				source,
				existing.TransportKind,
			)
		}
		seen[canonical] = binding
		bindings = append(bindings, binding)
		return nil
	}

	for _, entry := range serverEntries {
		binding, err := parseMCPBindingEntry(entry, MCPTransportHTTPStreamable, lookupEnv)
		if err != nil {
			return nil, err
		}
		if err := addBinding(binding, "mcp.server-url"); err != nil {
			return nil, err
		}
	}
	for _, entry := range commandEntries {
		binding, err := parseMCPBindingEntry(entry, MCPTransportStdio, lookupEnv)
		if err != nil {
			return nil, err
		}
		if err := addBinding(binding, "mcp.command"); err != nil {
			return nil, err
		}
	}

	return bindings, nil
}

func parseMCPBindingEntry(entry string, kind MCPTransportKind, lookupEnv func(string) (string, bool)) (MCPChannelBinding, error) {
	if strings.TrimSpace(entry) == "" {
		return MCPChannelBinding{}, fmt.Errorf("mcp config: %s entry is empty", kind)
	}

	if !isQualifiedMCPEntry(entry) {
		channel, err := types.NormalizeChannel("")
		if err != nil {
			return MCPChannelBinding{}, err
		}
		return buildMCPBinding(channel, kind, entry)
	}

	if kind == MCPTransportStdio {
		return parseQualifiedStdioMCPBindingEntry(entry)
	}

	parts := strings.Split(entry, ",")
	values := make(map[string]string, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		kv := strings.SplitN(trimmed, "=", 2)
		if len(kv) != 2 {
			return MCPChannelBinding{}, fmt.Errorf("mcp config: invalid entry %q (expected key=value)", entry)
		}
		key := strings.TrimSpace(kv[0])
		value := strings.TrimSpace(kv[1])
		if key == "" || value == "" {
			return MCPChannelBinding{}, fmt.Errorf("mcp config: invalid entry %q (empty %s)", entry, key)
		}
		values[key] = value
	}

	allowedKeys := map[string]bool{
		"channel": true,
	}
	switch kind {
	case MCPTransportHTTPStreamable:
		allowedKeys["url"] = true
		allowedKeys["unix-socket"] = true
		allowedKeys["http-proxy"] = true
		allowedKeys["client-cert"] = true
		allowedKeys["client-key"] = true
	case MCPTransportStdio:
		allowedKeys["command"] = true
	}
	for key := range values {
		if !allowedKeys[key] {
			return MCPChannelBinding{}, fmt.Errorf("mcp config: unsupported key %q in entry %q", key, entry)
		}
	}

	channelName := values["channel"]
	channel, err := types.NormalizeChannel(channelName)
	if err != nil {
		return MCPChannelBinding{}, err
	}

	switch kind {
	case MCPTransportHTTPStreamable:
		rawURL, ok := values["url"]
		if !ok {
			return MCPChannelBinding{}, fmt.Errorf("mcp config: server-url entry %q missing url", entry)
		}
		binding, err := buildMCPBinding(channel, kind, rawURL)
		if err != nil {
			return MCPChannelBinding{}, err
		}
		if rawProxy, ok := values["http-proxy"]; ok {
			parsed, source, err := parseProxyReference("mcp.server-url", rawProxy, lookupEnv)
			if err != nil {
				return MCPChannelBinding{}, err
			}
			binding.HTTPProxy = parsed
			binding.HTTPProxySource = source
		}
		if rawUnixSocket, ok := values["unix-socket"]; ok {
			socketPath, err := resolvePathReference("mcp.server-url unix-socket", rawUnixSocket, lookupEnv)
			if err != nil {
				return MCPChannelBinding{}, err
			}
			binding.UnixSocketPath = socketPath
		}
		if rawClientCert, ok := values["client-cert"]; ok {
			certPath, err := resolvePathReference("mcp.server-url client-cert", rawClientCert, lookupEnv)
			if err != nil {
				return MCPChannelBinding{}, err
			}
			rawClientKey, ok := values["client-key"]
			if !ok {
				return MCPChannelBinding{}, fmt.Errorf("mcp config: server-url entry %q missing client-key", entry)
			}
			keyPath, err := resolvePathReference("mcp.server-url client-key", rawClientKey, lookupEnv)
			if err != nil {
				return MCPChannelBinding{}, err
			}
			clientCert, err := tlsconfig.LoadClientCertificate(certPath, keyPath)
			if err != nil {
				return MCPChannelBinding{}, fmt.Errorf("invalid mcp.server-url client certificate entry %q: %w", entry, err)
			}
			binding.ClientCertificate = clientCert
		} else if _, ok := values["client-key"]; ok {
			return MCPChannelBinding{}, fmt.Errorf("mcp config: server-url entry %q missing client-cert", entry)
		}
		return binding, nil
	case MCPTransportStdio:
		if _, ok := values["http-proxy"]; ok {
			return MCPChannelBinding{}, fmt.Errorf("mcp config: http-proxy is not supported for stdio entry %q", entry)
		}
		rawCommand, ok := values["command"]
		if !ok {
			return MCPChannelBinding{}, fmt.Errorf("mcp config: command entry %q missing command", entry)
		}
		return buildMCPBinding(channel, kind, rawCommand)
	default:
		return MCPChannelBinding{}, fmt.Errorf("mcp config: unsupported transport %q", kind)
	}
}

func parseQualifiedStdioMCPBindingEntry(entry string) (MCPChannelBinding, error) {
	trimmed := strings.TrimSpace(entry)
	channelName := ""
	rawCommand := ""

	switch {
	case strings.HasPrefix(trimmed, "channel="):
		comma := strings.Index(trimmed, ",")
		if comma < 0 {
			return MCPChannelBinding{}, fmt.Errorf("mcp config: command entry %q missing command", entry)
		}
		channelName = strings.TrimSpace(strings.TrimPrefix(trimmed[:comma], "channel="))
		rest := strings.TrimSpace(trimmed[comma+1:])
		if !strings.HasPrefix(rest, "command=") {
			return MCPChannelBinding{}, fmt.Errorf("mcp config: invalid entry %q (expected command=...)", entry)
		}
		rawCommand = strings.TrimSpace(strings.TrimPrefix(rest, "command="))
	case strings.HasPrefix(trimmed, "command="):
		rawCommand = strings.TrimSpace(strings.TrimPrefix(trimmed, "command="))
		if comma := strings.LastIndex(rawCommand, ",channel="); comma >= 0 {
			channelName = strings.TrimSpace(rawCommand[comma+len(",channel="):])
			rawCommand = strings.TrimSpace(rawCommand[:comma])
		}
	default:
		return MCPChannelBinding{}, fmt.Errorf("mcp config: invalid entry %q (expected channel=... or command=...)", entry)
	}

	if channelName == "" {
		channelName = "main"
	}
	if rawCommand == "" {
		return MCPChannelBinding{}, fmt.Errorf("mcp config: command entry %q missing command", entry)
	}
	if err := rejectUnsupportedQualifiedStdioSegments(rawCommand, entry); err != nil {
		return MCPChannelBinding{}, err
	}

	channel, err := types.NormalizeChannel(channelName)
	if err != nil {
		return MCPChannelBinding{}, err
	}
	return buildMCPBinding(channel, MCPTransportStdio, rawCommand)
}

func rejectUnsupportedQualifiedStdioSegments(rawCommand, entry string) error {
	for _, key := range []string{"http-proxy", "url", "unix-socket", "client-cert", "client-key"} {
		if strings.Contains(strings.ToLower(rawCommand), ","+key+"=") {
			return fmt.Errorf("mcp config: unsupported key %q in entry %q", key, entry)
		}
	}
	return nil
}

func buildMCPBinding(channel types.Channel, kind MCPTransportKind, rawValue string) (MCPChannelBinding, error) {
	binding := MCPChannelBinding{
		Channel:       channel,
		TransportKind: kind,
	}

	switch kind {
	case MCPTransportHTTPStreamable:
		parsed, err := parseURL(rawValue)
		if err != nil {
			return MCPChannelBinding{}, fmt.Errorf("invalid mcp.server-url: %w", err)
		}
		binding.ServerURL = parsed
	case MCPTransportStdio:
		parsed, err := parseCommandArgv(rawValue)
		if err != nil {
			return MCPChannelBinding{}, fmt.Errorf("invalid mcp.command: %w", err)
		}
		binding.Command = rawValue
		binding.CommandArgs = parsed
	default:
		return MCPChannelBinding{}, fmt.Errorf("unsupported mcp transport %q", kind)
	}

	return binding, nil
}

func isQualifiedMCPEntry(entry string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(entry))
	return strings.HasPrefix(trimmed, "url=") ||
		strings.HasPrefix(trimmed, "command=") ||
		strings.HasPrefix(trimmed, "channel=") ||
		strings.HasPrefix(trimmed, "unix-socket=") ||
		strings.HasPrefix(trimmed, "http-proxy=") ||
		strings.HasPrefix(trimmed, "client-cert=") ||
		strings.HasPrefix(trimmed, "client-key=")
}

func buildHarpoonConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), globalProxy *url.URL, globalProxySource ProxySource) (HarpoonConfig, error) {
	allowPlaintext, err := getBool(fs, lookupEnv, "harpoon.allow-plaintext-http", "HARPOON_ALLOW_PLAINTEXT_HTTP")
	if err != nil {
		return HarpoonConfig{}, err
	}
	maxResponseBytes, err := getInt(fs, lookupEnv, "harpoon.max-response-bytes", "HARPOON_MAX_RESPONSE_BYTES", DefaultHarpoonMaxResponseBytes)
	if err != nil {
		return HarpoonConfig{}, err
	}
	if maxResponseBytes <= 0 {
		return HarpoonConfig{}, errors.New("harpoon.max-response-bytes must be positive")
	}
	if maxResponseBytes > DefaultHarpoonMaxResponseBytes {
		return HarpoonConfig{}, fmt.Errorf("harpoon.max-response-bytes must be less than or equal to %d", DefaultHarpoonMaxResponseBytes)
	}
	maxRedirects, err := getInt(fs, lookupEnv, "harpoon.max-redirects", "HARPOON_MAX_REDIRECTS", DefaultHarpoonMaxRedirects)
	if err != nil {
		return HarpoonConfig{}, err
	}
	if maxRedirects < 0 {
		return HarpoonConfig{}, errors.New("harpoon.max-redirects must be non-negative")
	}
	if maxRedirects > DefaultHarpoonMaxRedirects {
		return HarpoonConfig{}, fmt.Errorf("harpoon.max-redirects must be less than or equal to %d", DefaultHarpoonMaxRedirects)
	}
	targets, err := buildHarpoonTargets(fs, lookupEnv, allowPlaintext)
	if err != nil {
		return HarpoonConfig{}, err
	}
	additional, err := buildHarpoonAdditionalTransports(fs, lookupEnv)
	if err != nil {
		return HarpoonConfig{}, err
	}
	hostsIncludeSuffix, err := buildHarpoonHostIncludeList(fs, lookupEnv, "harpoon.hosts-include-suffix", "HARPOON_HOSTS_INCLUDE_SUFFIX")
	if err != nil {
		return HarpoonConfig{}, err
	}
	hostsIncludeRegex, err := buildHarpoonHostIncludeList(fs, lookupEnv, "harpoon.hosts-include-regex", "HARPOON_HOSTS_INCLUDE_REGEX")
	if err != nil {
		return HarpoonConfig{}, err
	}
	hostsIncludeLoopback, err := getBoolWithDefault(fs, lookupEnv, "harpoon.hosts-include-loopback", "HARPOON_HOSTS_INCLUDE_LOOPBACK", true)
	if err != nil {
		return HarpoonConfig{}, err
	}
	hostsIncludePrivate, err := getBoolWithDefault(fs, lookupEnv, "harpoon.hosts-include-private", "HARPOON_HOSTS_INCLUDE_PRIVATE", true)
	if err != nil {
		return HarpoonConfig{}, err
	}
	if err := validateHarpoonHostRegexes(hostsIncludeRegex); err != nil {
		return HarpoonConfig{}, err
	}
	httpProxy, httpProxySource, err := resolveProxyWithFallback(fs, lookupEnv, "harpoon.http-proxy", globalProxy, globalProxySource)
	if err != nil {
		return HarpoonConfig{}, err
	}
	return HarpoonConfig{
		AllowPlaintextHTTP:   allowPlaintext,
		MaxResponseBytes:     maxResponseBytes,
		MaxRedirects:         maxRedirects,
		Targets:              targets,
		AdditionalTransports: additional,
		HostClassifier: HarpoonHostClassifierConfig{
			IncludeSuffix:   hostsIncludeSuffix,
			IncludeRegex:    hostsIncludeRegex,
			IncludeLoopback: hostsIncludeLoopback,
			IncludePrivate:  hostsIncludePrivate,
		},
		HTTPProxy:       httpProxy,
		HTTPProxySource: httpProxySource,
	}, nil
}

func buildHarpoonHostIncludeList(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), flagName, envName string) ([]string, error) {
	var raw []string
	if flag := fs.Lookup(flagName); flag != nil && flag.Changed {
		values, err := fs.GetStringArray(flagName)
		if err != nil {
			return nil, fmt.Errorf("invalid value for --%s: %w", flagName, err)
		}
		raw = append(raw, values...)
	} else if envVal, ok := lookupEnv(envName); ok && envVal != "" {
		raw = splitTargetList(envVal)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out, nil
}

func validateHarpoonHostRegexes(values []string) error {
	for _, raw := range values {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}
		if _, err := regexp.Compile("(?i:" + pattern + ")"); err != nil {
			return fmt.Errorf("invalid harpoon host regex %q: %w", raw, err)
		}
	}
	return nil
}

func buildHarpoonTargets(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), allowPlaintext bool) ([]HarpoonTarget, error) {
	var rawTargets []string
	if flag := fs.Lookup("harpoon.target"); flag != nil && flag.Changed {
		values, err := fs.GetStringArray("harpoon.target")
		if err != nil {
			return nil, fmt.Errorf("invalid value for --harpoon.target: %w", err)
		}
		rawTargets = append(rawTargets, values...)
	} else if envVal, ok := lookupEnv("HARPOON_TARGETS"); ok && envVal != "" {
		rawTargets = splitTargetList(envVal)
	}

	targets := make([]HarpoonTarget, 0, len(rawTargets))
	for _, raw := range rawTargets {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		target, err := parseHarpoonTarget(raw, allowPlaintext, lookupEnv)
		if err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func splitTargetList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ';' || r == '\n'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func parseHarpoonTarget(raw string, allowPlaintext bool, lookupEnv func(string) (string, bool)) (HarpoonTarget, error) {
	parts := strings.Split(raw, ",")
	values := make(map[string]string, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return HarpoonTarget{}, fmt.Errorf("invalid harpoon target %q: expected key=value pairs", raw)
		}
		key := strings.TrimSpace(strings.ToLower(kv[0]))
		val := strings.Trim(strings.TrimSpace(kv[1]), `"'`)
		if key == "" {
			return HarpoonTarget{}, fmt.Errorf("invalid harpoon target %q: empty key", raw)
		}
		values[key] = val
	}

	label := values["label"]
	urlRaw := values["url"]
	if label == "" || urlRaw == "" {
		return HarpoonTarget{}, fmt.Errorf("invalid harpoon target %q: label and url are required", raw)
	}
	if !harpoonLabelPattern.MatchString(label) {
		return HarpoonTarget{}, fmt.Errorf("invalid harpoon target %q: label must match %s", raw, harpoonLabelPattern.String())
	}
	parsed, err := parseURL(urlRaw)
	if err != nil {
		return HarpoonTarget{}, fmt.Errorf("invalid harpoon target url %q: %w", urlRaw, err)
	}
	if !allowPlaintext && !strings.EqualFold(parsed.Scheme, "https") {
		return HarpoonTarget{}, fmt.Errorf("invalid harpoon target url %q: https is required", urlRaw)
	}
	unixSocketPath, err := resolvePathReference("harpoon.target unix-socket", values["unix-socket"], lookupEnv)
	if err != nil {
		return HarpoonTarget{}, err
	}
	return HarpoonTarget{
		Label:          label,
		Description:    values["desc"],
		BaseURL:        parsed,
		UnixSocketPath: unixSocketPath,
	}, nil
}

func buildHarpoonAdditionalTransports(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) ([]HarpoonTransportKind, error) {
	var raw []string
	if flag := fs.Lookup("harpoon.additional-transport"); flag != nil && flag.Changed {
		values, err := fs.GetStringArray("harpoon.additional-transport")
		if err != nil {
			return nil, fmt.Errorf("invalid value for --harpoon.additional-transport: %w", err)
		}
		raw = append(raw, values...)
	} else if envVal, ok := lookupEnv("HARPOON_ADDITIONAL_TRANSPORTS"); ok && envVal != "" {
		raw = splitTargetList(envVal)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[HarpoonTransportKind]struct{})
	out := make([]HarpoonTransportKind, 0, len(raw))
	for _, entry := range raw {
		entry = strings.TrimSpace(strings.ToLower(entry))
		if entry == "" {
			continue
		}
		kind := HarpoonTransportKind(entry)
		switch kind {
		case HarpoonTransportHTTPStreamable:
		default:
			return nil, fmt.Errorf("unsupported harpoon transport %q", entry)
		}
		if _, ok := seen[kind]; ok {
			continue
		}
		seen[kind] = struct{}{}
		out = append(out, kind)
	}
	return out, nil
}

func getInt(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), flagName, envName string, defaultValue int) (int, error) {
	if flag := fs.Lookup(flagName); flag != nil && flag.Changed {
		val, err := strconv.Atoi(flag.Value.String())
		if err != nil {
			return 0, fmt.Errorf("invalid value for --%s: %w", flagName, err)
		}
		return val, nil
	}
	if envVal, ok := lookupEnv(envName); ok && envVal != "" {
		val, err := strconv.Atoi(envVal)
		if err != nil {
			return 0, fmt.Errorf("invalid %s: %w", envName, err)
		}
		return val, nil
	}
	return defaultValue, nil
}

func getBool(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), flagName, envName string) (bool, error) {
	if flag := fs.Lookup(flagName); flag != nil && flag.Changed {
		val, err := strconv.ParseBool(flag.Value.String())
		if err != nil {
			return false, fmt.Errorf("parse --%s: %w", flagName, err)
		}
		return val, nil
	}
	if envVal, ok := lookupEnv(envName); ok && envVal != "" {
		val, err := strconv.ParseBool(envVal)
		if err != nil {
			return false, fmt.Errorf("parse %s: %w", envName, err)
		}
		return val, nil
	}
	return false, nil
}

func getBoolWithDefault(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), flagName, envName string, defaultValue bool) (bool, error) {
	if flag := fs.Lookup(flagName); flag != nil && flag.Changed {
		val, err := strconv.ParseBool(flag.Value.String())
		if err != nil {
			return false, fmt.Errorf("parse --%s: %w", flagName, err)
		}
		return val, nil
	}
	if envVal, ok := lookupEnv(envName); ok && envVal != "" {
		val, err := strconv.ParseBool(envVal)
		if err != nil {
			return false, fmt.Errorf("parse %s: %w", envName, err)
		}
		return val, nil
	}
	return defaultValue, nil
}

func parseCommandArgv(raw string) ([]string, error) {
	input := strings.TrimSpace(raw)
	if input == "" {
		return nil, errors.New("command is empty")
	}
	var (
		args     []string
		builder  strings.Builder
		inSingle bool
		inDouble bool
		escaped  bool
	)

	for i := 0; i < len(input); i++ {
		ch := input[i]
		if escaped {
			builder.WriteByte(ch)
			escaped = false
			continue
		}
		if inSingle {
			if ch == '\'' {
				inSingle = false
				continue
			}
			builder.WriteByte(ch)
			continue
		}
		if inDouble {
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inDouble = false
			default:
				builder.WriteByte(ch)
			}
			continue
		}
		switch ch {
		case '\\':
			escaped = true
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case ' ', '\t', '\n', '\r':
			if builder.Len() > 0 {
				args = append(args, builder.String())
				builder.Reset()
			}
		default:
			builder.WriteByte(ch)
		}
	}

	if escaped {
		return nil, errors.New("unterminated escape sequence")
	}
	if inSingle || inDouble {
		return nil, errors.New("unterminated quoted string")
	}
	if builder.Len() > 0 {
		args = append(args, builder.String())
	}
	if len(args) == 0 {
		return nil, errors.New("command is empty")
	}
	return args, nil
}

// String implements fmt.Stringer.
func (f LogFormat) String() string {
	if s, ok := logFormatToString[f]; ok {
		return s
	}
	return ""
}

// ParseLogFormat converts the provided raw string into a LogFormat value.
func ParseLogFormat(raw string) (LogFormat, error) {
	if raw == "" {
		return LogFormatUnset, nil
	}
	normalized := strings.ToLower(raw)
	for format, name := range logFormatToString {
		if normalized == name {
			return format, nil
		}
	}
	return LogFormatUnset, fmt.Errorf("unsupported log format %q: supported formats are \"struct-text\" or \"json\"", raw)
}

func resolveHTTPRawUnsafe(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (bool, error) {
	if flag := fs.Lookup("log.http-raw-unsafe"); flag != nil && flag.Changed {
		val, err := strconv.ParseBool(flag.Value.String())
		if err != nil {
			return false, fmt.Errorf("parse --log.http-raw-unsafe: %w", err)
		}
		return val, nil
	}

	if envVal, ok := lookupEnv("LOG_HTTP_RAW_UNSAFE"); ok && envVal != "" {
		val, err := strconv.ParseBool(envVal)
		if err != nil {
			return false, fmt.Errorf("parse LOG_HTTP_RAW_UNSAFE: %w", err)
		}
		return val, nil
	}

	return false, nil
}

func parseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("must include scheme and host")
	}
	return parsed, nil
}

// ParseURL exposes the shared URL validator for full-client adapters.
func ParseURL(raw string) (*url.URL, error) { return parseURL(raw) }
