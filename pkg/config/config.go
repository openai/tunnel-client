package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"go.openai.org/api/tunnel-client/pkg/tlsconfig"
	"go.openai.org/api/tunnel-client/pkg/types"
	"go.openai.org/api/tunnel-client/pkg/version"
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
	defaultProxyCheckInterval                          = 60 * time.Second
	defaultLogLevel                                    = "info"
	defaultLogFormat                         LogFormat = LogFormatUnset
	defaultHealthListenAddr                            = "127.0.0.1:8080"
	defaultAdminUILogBufferEvents                      = 2000
	maxAdminUILogBufferEvents                          = 100000
	defaultMCPConnectionMaxTTL                         = 10 * time.Minute
	defaultMCPMaxConcurrentRequests                    = 10
	DefaultHarpoonMaxResponseBytes                     = 100 * 1024
	DefaultHarpoonMaxRedirects                         = 5
)

const _ = uint(maxControlPlaneMaxInFlight - defaultControlPlaneMaxInFlight)
const _ = uint(defaultControlPlanePollTimeout - 1)
const _ = uint(maxControlPlanePollDeadline - defaultControlPlanePollTimeout - defaultControlPlanePollDeadlineGuardrail)
const _ = uint(defaultControlPlanePollDeadlineGuardrail - 1)
const _ = uint(maxControlPlanePollDeadlineGuardrail - defaultControlPlanePollDeadlineGuardrail - 1)

var harpoonLabelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

var (
	errMissingControlPlaneAPIKey = errors.New("control plane API key is required; set --control-plane.api-key (env:/file:) or CONTROL_PLANE_API_KEY or OPENAI_API_KEY")
	tunnelIDPattern              = regexp.MustCompile(`^tunnel_[a-z0-9]{32}$`)
	logFormatToString            = map[LogFormat]string{
		LogFormatStructText: "struct-text",
		LogFormatJSON:       "json",
	}
)

type flagAlias struct {
	Canonical string
	Alias     string
	Kind      string
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
	AdminUI      AdminUIConfig
	Harpoon      HarpoonConfig
	ProxyHealth  ProxyHealthConfig
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

// AdminUIConfig defines runtime behavior for the embedded admin web UI.
type AdminUIConfig struct {
	// AllowRemote controls whether the embedded web UI and log endpoints are
	// accessible from non-loopback clients.
	//
	// When false, the UI endpoints only respond to loopback requests (127.0.0.1/::1),
	// even if the health server is bound to 0.0.0.0/::.
	AllowRemote bool
	// OpenBrowser controls whether tunnel-client attempts to open the embedded UI
	// in the default browser on startup.
	OpenBrowser bool
	// LogBufferEvents controls how many recent log events the admin UI keeps in memory.
	LogBufferEvents int
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
	// PollBackoffMin/PollBackoffMax allow overriding the poller's retry window.
	// Zero values fall back to the internal defaults.
	PollBackoffMin    time.Duration
	PollBackoffMax    time.Duration
	ClientCertificate *tlsconfig.ClientCertificate
	ExtraHeaders      map[string]string
	HTTPProxy         *url.URL
	HTTPProxySource   ProxySource
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

// MCPConfig captures configuration for the Model Context Protocol integration.
//
// The legacy top-level ServerURL/Command fields mirror the main channel so older
// call sites can keep reading cfg.MCP.ServerURL while the dispatcher routes from
// ChannelBindings. New connector/channel behavior should be modeled as an
// MCPChannelBinding first, then projected to the legacy fields only for
// compatibility.
type MCPConfig struct {
	ServerURL             *url.URL
	UnixSocketPath        string
	Command               string
	CommandArgs           []string
	TransportKind         MCPTransportKind
	ClientCertificate     *tlsconfig.ClientCertificate
	ChannelBindings       []MCPChannelBinding
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
	CapturePayloads      bool
	HostClassifier       HarpoonHostClassifierConfig
	HTTPProxy            *url.URL
	HTTPProxySource      ProxySource
}

// ProxyHealthConfig controls proxy connectivity checks.
type ProxyHealthConfig struct {
	CheckInterval time.Duration
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

// Load builds a Config by combining CLI flag arguments with environment variables.
//
// Flags take precedence over environment variables. Environment variables take
// precedence over the built-in defaults.
func Load(args []string, lookupEnv func(string) (string, bool)) (*Config, error) {
	fs := pflag.NewFlagSet("tunnel-client", pflag.ContinueOnError)
	RegisterFlags(fs)
	fs.Usage = func() {
		WriteUsage(fs, fs.Output())
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return LoadFromFlagSet(fs, lookupEnv)
}

// WriteUsage prints the tunnel-client CLI usage text for the provided flag set.
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
	_, _ = fmt.Fprintln(fs.Output(), "  CA_BUNDLE\tPath to a PEM CA bundle used for outbound TLS connections (additive to system trust) (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  MCP_EXTRA_HEADERS\tStatic headers for outbound MCP HTTP requests to the configured MCP server origin; values accept env:VAR or file:/path (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  MCP_DISCOVERY_EXTRA_HEADERS\tStatic headers for MCP discovery/probe requests to the configured MCP server origin; values accept env:VAR or file:/path (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  MCP_CLIENT_CERT\tPath (or env:VAR) to PEM client certificate for MCP mTLS (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  MCP_CLIENT_KEY\tPath (or env:VAR) to PEM client private key for MCP mTLS (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  PROXY_CHECK_INTERVAL\tInterval between proxy connectivity checks (optional)")
}

// RegisterFlags attaches all supported CLI flags to the provided flag set.
func RegisterFlags(fs *pflag.FlagSet) {
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
	fs.StringArray("control-plane.extra-headers", nil, "Additional HTTP headers to send to the tunnel control-plane (format 'Key: Value', repeatable; values accept env:VAR or file:/path) (env.CONTROL_PLANE_EXTRA_HEADERS)")
	fs.String("log.level", defaultLogLevel, "Log level (debug, info, warn) (env.LOG_LEVEL)")
	fs.String("log.format", defaultLogFormat.String(), "Log format (struct-text, json) (env.LOG_FORMAT)")
	fs.String("log.file", "", "Log file path; defaults to stdout when empty (env.LOG_FILE)")
	fs.Bool("log.http-raw-unsafe", false, "Log full raw HTTP requests and responses (including bodies/headers). WARNING: May include PII or sensitive data. Use only for debugging. (env.LOG_HTTP_RAW_UNSAFE)")
	fs.String("health.listen-addr", defaultHealthListenAddr, "Address the health HTTP server listens on (ip:port). Use :8080 to listen on all interfaces, or 127.0.0.1:0 to request a loopback ephemeral port from the OS. Ignored when health.unix-socket is set. (env.HEALTH_LISTEN_ADDR)")
	fs.String("health.unix-socket", "", "Unix socket path for the health/admin HTTP server. When set, tunnel-client serves health/admin over the socket instead of binding TCP. (env.HEALTH_UNIX_SOCKET)")
	fs.String("health.url-file", "", "File to write the health base URL to after startup (env.HEALTH_URL_FILE)")
	fs.Bool("allow-remote-ui", false, "Allow remote access to the embedded web UI and log endpoints (env.ALLOW_REMOTE_UI)")
	fs.Bool("open-web-ui", false, "Open the embedded web UI in your default browser on startup (env.OPEN_WEB_UI)")
	fs.Int("admin-ui.log-buffer-events", defaultAdminUILogBufferEvents, "Number of recent log events to keep in memory for the embedded web UI and export archive (env.ADMIN_UI_LOG_BUFFER_EVENTS, max 100000)")
	fs.String("pid.file", "", "File to write the tunnel-client process ID to (env.PID_FILE)")
	fs.String("http-proxy", "", "Global outbound HTTP proxy (applies to control-plane, MCP, and Harpoon) (format <url|env:VAR>)")
	fs.Duration("proxy.check-interval", defaultProxyCheckInterval, "Interval between proxy connectivity checks (env.PROXY_CHECK_INTERVAL)")
	fs.StringArray("mcp.server-url", nil, "Target MCP server URL (repeatable; format url=...,channel=...,unix-socket=...,http-proxy=...,client-cert=...,client-key=...) (env.MCP_SERVER_URL)")
	fs.StringArray("mcp.command", nil, "Command to launch an MCP server over stdio (repeatable; format command=...,channel=...) (env.MCP_COMMAND)")
	fs.String("mcp.http-proxy", "", "Outbound HTTP proxy for MCP (format <url|env:VAR>)")
	fs.String("mcp.client-cert", "", "Path to PEM client certificate for MCP mTLS (format <path|env:VAR>) (env.MCP_CLIENT_CERT)")
	fs.String("mcp.client-key", "", "Path to PEM client private key for MCP mTLS (format <path|env:VAR>) (env.MCP_CLIENT_KEY)")
	fs.StringArray("mcp.extra-headers", nil, "Static HTTP headers to send to the configured MCP server origin (format 'Key: Value', repeatable; values accept env:VAR or file:/path) (env.MCP_EXTRA_HEADERS)")
	fs.StringArray("mcp.discovery-extra-headers", nil, "Static HTTP headers to send to MCP discovery/probe requests for the configured MCP server origin (format 'Key: Value', repeatable; values accept env:VAR or file:/path) (env.MCP_DISCOVERY_EXTRA_HEADERS)")
	fs.Duration("mcp.connection-max-ttl", defaultMCPConnectionMaxTTL, "Maximum lifetime of MCP transport connections (env.MCP_CONNECTION_MAX_TTL)")
	fs.Int("mcp.max-concurrent-requests", defaultMCPMaxConcurrentRequests, "Maximum number of requests actively dispatched to the MCP server (env.MCP_MAX_CONCURRENT_REQUESTS)")
	fs.StringArray("harpoon.target", nil, "Harpoon target mapping (format 'label=...,url=...,unix-socket=...,desc=...') (env.HARPOON_TARGETS)")
	fs.Bool("harpoon.allow-plaintext-http", false, "Allow http:// harpoon targets and redirects (env.HARPOON_ALLOW_PLAINTEXT_HTTP)")
	fs.Int("harpoon.max-response-bytes", DefaultHarpoonMaxResponseBytes, "Maximum harpoon response size in bytes (env.HARPOON_MAX_RESPONSE_BYTES)")
	fs.Int("harpoon.max-redirects", DefaultHarpoonMaxRedirects, "Maximum number of harpoon redirects (env.HARPOON_MAX_REDIRECTS)")
	fs.String("harpoon.http-proxy", "", "Outbound HTTP proxy for Harpoon requests (format <url|env:VAR>)")
	fs.StringArray("harpoon.additional-transport", nil, "Additional harpoon transports (http-streamable) (env.HARPOON_ADDITIONAL_TRANSPORTS)")
	fs.Bool("harpoon.capture-payloads", false, "Capture request/response payloads for the Harpoon admin UI (debug only). (env.HARPOON_CAPTURE_PAYLOADS)")
	fs.StringArray("harpoon.hosts-include-suffix", nil, "Host suffixes treated as private for Harpoon auto-registration (repeatable) (env.HARPOON_HOSTS_INCLUDE_SUFFIX)")
	fs.StringArray("harpoon.hosts-include-regex", nil, "Host regex patterns treated as private for Harpoon auto-registration (repeatable) (env.HARPOON_HOSTS_INCLUDE_REGEX)")
	fs.Bool("harpoon.hosts-include-loopback", true, "Treat loopback hosts as private for Harpoon auto-registration (env.HARPOON_HOSTS_INCLUDE_LOOPBACK)")
	fs.Bool("harpoon.hosts-include-private", true, "Treat private IPs as private for Harpoon auto-registration (env.HARPOON_HOSTS_INCLUDE_PRIVATE)")

	if f := fs.Lookup("log.file"); f != nil {
		f.DefValue = "stdout"
	}
	registerFlagAliases(fs)
}

// LoadFromFlagSet builds a Config using the parsed values from the provided flag set.
//
// It respects the same precedence rules as Load(): flags override environment variables,
// which override defaults.
func LoadFromFlagSet(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (*Config, error) {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	applyFlagAliases(fs)

	fileValues, err := loadFileConfigValues(fs, lookupEnv)
	if err != nil {
		return nil, err
	}
	lookupEnv = lookupEnvWithFileValues(lookupEnv, fileValues)

	tlsBundle, err := buildTLSBundle(fs, lookupEnv)
	if err != nil {
		return nil, err
	}

	globalProxy, globalProxySource, _, err := resolveProxyFlag(fs, lookupEnv, "http-proxy")
	if err != nil {
		return nil, err
	}

	controlPlane, err := buildControlPlaneConfig(fs, lookupEnv, globalProxy, globalProxySource)
	if err != nil {
		return nil, err
	}

	mcp, err := buildMCPConfig(fs, lookupEnv, globalProxy, globalProxySource)
	if err != nil {
		return nil, err
	}

	logging, err := buildLoggingConfig(fs, lookupEnv)
	if err != nil {
		return nil, err
	}

	health := buildHealthConfig(fs, lookupEnv)
	process := buildProcessConfig(fs, lookupEnv)

	adminUI, err := buildAdminUIConfig(fs, lookupEnv)
	if err != nil {
		return nil, err
	}

	harpoon, err := buildHarpoonConfig(fs, lookupEnv, globalProxy, globalProxySource)
	if err != nil {
		return nil, err
	}

	proxyHealth, err := buildProxyHealthConfig(fs, lookupEnv)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		ControlPlane: controlPlane,
		Logging:      logging,
		Health:       health,
		Process:      process,
		MCP:          mcp,
		AdminUI:      adminUI,
		Harpoon:      harpoon,
		ProxyHealth:  proxyHealth,
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

	return cfg, nil
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

	var apiKeyFlagValue string
	if flag := fs.Lookup("control-plane.api-key"); flag != nil && flag.Changed {
		apiKeyFlagValue = flag.Value.String()
	}

	apiKey, err := getControlPlaneAPIKey(apiKeyFlagValue, lookupEnv)
	if err != nil {
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
		BaseURL:               baseURL,
		URLPath:               controlPlaneURLPath,
		TunnelID:              types.TunnelID(tunnelID),
		OrganizationID:        organizationID,
		APIKey:                apiKey,
		MaxInFlightRequests:   maxInFlight,
		PollTimeout:           pollTimeout,
		PollDeadlineGuardrail: pollDeadlineGuardrail,
		ClientCertificate:     clientCertificate,
		ExtraHeaders:          extraHeaders,
		HTTPProxy:             httpProxy,
		HTTPProxySource:       httpProxySource,
	}, nil
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
	case "authorization", "accept", "user-agent", "x-tunnel-client-name", "x-tunnel-client-version":
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
	return headers, nil
}

func parseHeader(raw string) (string, string, error) {
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid header %q: expected 'Key: Value'", raw)
	}

	key := strings.TrimSpace(parts[0])
	value := strings.TrimSpace(parts[1])

	if key == "" {
		return "", "", fmt.Errorf("invalid header %q: key cannot be empty", raw)
	}
	if value == "" {
		return "", "", fmt.Errorf("invalid header %q: value cannot be empty", raw)
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", "", fmt.Errorf("invalid header %q: value cannot contain CR or LF", raw)
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

func buildProxyHealthConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (ProxyHealthConfig, error) {
	raw := firstSet(
		getValue(fs, "proxy.check-interval"),
		envOrDefault(lookupEnv, "PROXY_CHECK_INTERVAL", defaultProxyCheckInterval.String()),
	)
	interval, err := time.ParseDuration(raw)
	if err != nil {
		return ProxyHealthConfig{}, fmt.Errorf("invalid proxy.check-interval: %w", err)
	}
	if interval <= 0 {
		return ProxyHealthConfig{}, errors.New("proxy.check-interval must be positive")
	}
	return ProxyHealthConfig{CheckInterval: interval}, nil
}

func buildProcessConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) ProcessConfig {
	pidFile := firstSet(
		getValue(fs, "pid.file"),
		envOrDefault(lookupEnv, "PID_FILE", ""),
	)

	return ProcessConfig{PIDFile: pidFile}
}

func buildAdminUIConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (AdminUIConfig, error) {
	allowRemote, err := resolveAllowRemoteUI(fs, lookupEnv)
	if err != nil {
		return AdminUIConfig{}, err
	}

	openBrowser, err := resolveOpenWebUI(fs, lookupEnv)
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

func resolveOpenWebUI(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (bool, error) {
	if flag := fs.Lookup("open-web-ui"); flag != nil && flag.Changed {
		val, err := fs.GetBool("open-web-ui")
		if err != nil {
			return false, fmt.Errorf("parse --open-web-ui: %w", err)
		}
		return val, nil
	}

	if envVal, ok := lookupEnv("OPEN_WEB_UI"); ok && envVal != "" {
		val, err := strconv.ParseBool(envVal)
		if err != nil {
			return false, fmt.Errorf("parse OPEN_WEB_UI: %w", err)
		}
		return val, nil
	}

	return false, nil
}

func resolveAllowRemoteUI(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (bool, error) {
	if flag := fs.Lookup("allow-remote-ui"); flag != nil && flag.Changed {
		val, err := fs.GetBool("allow-remote-ui")
		if err != nil {
			return false, fmt.Errorf("parse --allow-remote-ui: %w", err)
		}
		return val, nil
	}

	if envVal, ok := lookupEnv("ALLOW_REMOTE_UI"); ok && envVal != "" {
		val, err := strconv.ParseBool(envVal)
		if err != nil {
			return false, fmt.Errorf("parse ALLOW_REMOTE_UI: %w", err)
		}
		return val, nil
	}

	return false, nil
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

	if len(bindings) == 0 {
		return nil, errors.New("main channel is required; set --mcp.server-url or --mcp.command, or MCP_SERVER_URL or MCP_COMMAND")
	}
	if _, ok := seen[types.DefaultChannel]; !ok {
		return nil, errors.New("main channel is required; add channel=main to one --mcp.server-url or --mcp.command entry")
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
	capturePayloads, err := getBool(fs, lookupEnv, "harpoon.capture-payloads", "HARPOON_CAPTURE_PAYLOADS")
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
		CapturePayloads:      capturePayloads,
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
