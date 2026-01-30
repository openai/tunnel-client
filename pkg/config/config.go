package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"

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
	defaultControlPlaneBaseURL                = "https://api.openai.com"
	defaultControlPlaneMaxInFlight            = 20
	maxControlPlaneMaxInFlight                = 10000
	defaultControlPlanePollTimeout            = 30 * time.Second
	defaultLogLevel                           = "info"
	defaultLogFormat                LogFormat = LogFormatUnset
	defaultHealthListenAddr                   = ":8080"
	defaultMCPConnectionMaxTTL                = 10 * time.Minute
	defaultMCPMaxConcurrentRequests           = 10
	DefaultHarpoonMaxResponseBytes            = 100 * 1024
	DefaultHarpoonMaxRedirects                = 5
)

const _ = uint(maxControlPlaneMaxInFlight - defaultControlPlaneMaxInFlight)

var harpoonLabelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

var (
	errMissingControlPlaneAPIKey = errors.New("control plane API key is required; set --control-plane.api-key (env:/file:) or CONTROL_PLANE_API_KEY or OPENAI_API_KEY")
	tunnelIDPattern              = regexp.MustCompile(`^tunnel_[a-z0-9]{32}$`)
	logFormatToString            = map[LogFormat]string{
		LogFormatStructText: "struct-text",
		LogFormatJSON:       "json",
	}
)

// Config captures the runtime values required to start the tunnel client.
type Config struct {
	ControlPlane ControlPlaneConfig
	Logging      LoggingConfig
	Health       HealthConfig
	Process      ProcessConfig
	MCP          MCPConfig
	AdminUI      AdminUIConfig
	Harpoon      HarpoonConfig
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
}

// ControlPlaneConfig defines how the client reaches the tunnel control plane.
type ControlPlaneConfig struct {
	BaseURL             *url.URL
	TunnelID            types.TunnelID
	APIKey              string
	MaxInFlightRequests int
	PollTimeout         time.Duration
	// PollBackoffMin/PollBackoffMax allow overriding the poller's retry window.
	// Zero values fall back to the internal defaults.
	PollBackoffMin time.Duration
	PollBackoffMax time.Duration
	ExtraHeaders   map[string]string
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
	URLFile    string
}

// ProcessConfig defines process-level runtime settings.
type ProcessConfig struct {
	PIDFile string
}

// MCPConfig captures configuration for the Model Control Plane integration.
type MCPConfig struct {
	ServerURL             *url.URL
	Command               string
	CommandArgs           []string
	TransportKind         MCPTransportKind
	ConnectionMaxTTL      time.Duration
	MaxConcurrentRequests int
}

// HarpoonConfig captures configuration for the embedded harpoon MCP server.
type HarpoonConfig struct {
	AllowPlaintextHTTP   bool
	MaxResponseBytes     int
	MaxRedirects         int
	AdditionalTransports []HarpoonTransportKind
	Targets              []HarpoonTarget
	CapturePayloads      bool
}

// HarpoonTarget describes a configured harpoon target.
type HarpoonTarget struct {
	Label       string
	Description string
	BaseURL     *url.URL
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
	_, _ = fmt.Fprintln(fs.Output(), "\nEnvironment variables:")
	_, _ = fmt.Fprintln(fs.Output(), "  CONTROL_PLANE_API_KEY\tAPI key used to authenticate to the tunnel control plane (required; preferred)")
	_, _ = fmt.Fprintln(fs.Output(), "  OPENAI_API_KEY\tAPI key env var used when CONTROL_PLANE_API_KEY unset")
	_, _ = fmt.Fprintln(fs.Output(), "  ALLOW_REMOTE_UI\tSet to true to allow non-loopback access to the embedded web UI (optional)")
	_, _ = fmt.Fprintln(fs.Output(), "  OPEN_WEB_UI\tSet to true to open the embedded web UI in a browser on startup (optional)")
}

// RegisterFlags attaches all supported CLI flags to the provided flag set.
func RegisterFlags(fs *pflag.FlagSet) {
	fs.String("control-plane.base-url", defaultControlPlaneBaseURL, "Tunnel control-plane base URL (env.CONTROL_PLANE_BASE_URL)")
	fs.String("control-plane.tunnel-id", "", "Identifier for this client/tunnel (env.CONTROL_PLANE_TUNNEL_ID)")
	fs.String("control-plane.api-key", "", "Reference to environment variable or file containing the control-plane API key (format env:VARNAME or file:/path/to/secret)")
	fs.Int("control-plane.max-inflight", defaultControlPlaneMaxInFlight, "Maximum number of in-flight MCP requests before applying backpressure (env.CONTROL_PLANE_MAX_INFLIGHT_REQUESTS, max 10000)")
	fs.Duration("control-plane.poll-timeout", defaultControlPlanePollTimeout, "Long-poll timeout when fetching commands from the control plane (env.CONTROL_PLANE_POLL_TIMEOUT)")
	fs.StringArray("control-plane.extra-headers", nil, "Additional HTTP headers to send to the tunnel control-plane (format 'Key: Value', repeatable) (env.CONTROL_PLANE_EXTRA_HEADERS)")
	fs.String("log.level", defaultLogLevel, "Log level (debug, info, warn) (env.LOG_LEVEL)")
	fs.String("log.format", defaultLogFormat.String(), "Log format (struct-text, json) (env.LOG_FORMAT)")
	fs.String("log.file", "", "Log file path; defaults to stdout when empty (env.LOG_FILE)")
	fs.Bool("log.http-raw-unsafe", false, "Log full raw HTTP requests and responses (including bodies/headers). WARNING: May include PII or sensitive data. Use only for debugging. (env.LOG_HTTP_RAW_UNSAFE)")
	fs.String("health.listen-addr", defaultHealthListenAddr, "Address the health HTTP server listens on (ip:port) (env.HEALTH_LISTEN_ADDR)")
	fs.String("health.url-file", "", "File to write the health base URL to after startup (env.HEALTH_URL_FILE)")
	fs.Bool("allow-remote-ui", false, "Allow remote access to the embedded web UI and log endpoints (env.ALLOW_REMOTE_UI)")
	fs.Bool("open-web-ui", false, "Open the embedded web UI in your default browser on startup (env.OPEN_WEB_UI)")
	fs.String("pid.file", "", "File to write the tunnel-client process ID to (env.PID_FILE)")
	fs.String("mcp.server-url", "", "Target MCP server URL (env.MCP_SERVER_URL)")
	fs.String("mcp.command", "", "Command to launch an MCP server over stdio (env.MCP_COMMAND)")
	fs.Duration("mcp.connection-max-ttl", defaultMCPConnectionMaxTTL, "Maximum lifetime of MCP transport connections (env.MCP_CONNECTION_MAX_TTL)")
	fs.Int("mcp.max-concurrent-requests", defaultMCPMaxConcurrentRequests, "Maximum number of concurrent requests to the MCP server (env.MCP_MAX_CONCURRENT_REQUESTS)")
	fs.StringArray("harpoon-target", nil, "Harpoon target mapping (format 'label=...,url=...,desc=...') (env.HARPOON_TARGETS)")
	fs.Bool("harpoon-allow-plaintext-http", false, "Allow http:// harpoon targets and redirects (env.HARPOON_ALLOW_PLAINTEXT_HTTP)")
	fs.Int("harpoon-max-response-bytes", DefaultHarpoonMaxResponseBytes, "Maximum harpoon response size in bytes (env.HARPOON_MAX_RESPONSE_BYTES)")
	fs.Int("harpoon-max-redirects", DefaultHarpoonMaxRedirects, "Maximum number of harpoon redirects (env.HARPOON_MAX_REDIRECTS)")
	fs.StringArray("harpoon-additional-transport", nil, "Additional harpoon transports (http-streamable) (env.HARPOON_ADDITIONAL_TRANSPORTS)")
	fs.Bool("harpoon-capture-payloads", false, "Capture request/response payloads for the Harpoon admin UI (debug only). (env.HARPOON_CAPTURE_PAYLOADS)")

	if f := fs.Lookup("log.file"); f != nil {
		f.DefValue = "stdout"
	}
}

// LoadFromFlagSet builds a Config using the parsed values from the provided flag set.
//
// It respects the same precedence rules as Load(): flags override environment variables,
// which override defaults.
func LoadFromFlagSet(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (*Config, error) {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}

	mcp, err := buildMCPConfig(fs, lookupEnv)
	if err != nil {
		return nil, err
	}

	controlPlane, err := buildControlPlaneConfig(fs, lookupEnv)
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

	harpoon, err := buildHarpoonConfig(fs, lookupEnv)
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
	}

	return cfg, nil
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

func buildControlPlaneConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (ControlPlaneConfig, error) {
	baseURLRaw := firstSet(
		getValue(fs, "control-plane.base-url"),
		envOrDefault(lookupEnv, "CONTROL_PLANE_BASE_URL", defaultControlPlaneBaseURL),
	)
	baseURL, err := parseURL(baseURLRaw)
	if err != nil {
		return ControlPlaneConfig{}, fmt.Errorf("invalid control-plane.base-url: %w", err)
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

	if tunnelID == "" {
		return ControlPlaneConfig{}, errors.New("tunnel ID is required; set --control-plane.tunnel-id or CONTROL_PLANE_TUNNEL_ID")
	}
	if escaped := url.PathEscape(tunnelID); escaped != tunnelID {
		return ControlPlaneConfig{}, fmt.Errorf(
			"invalid tunnel ID %q: must be safe for use as a URL path parameter",
			tunnelID,
		)
	}
	if !tunnelIDPattern.MatchString(tunnelID) {
		return ControlPlaneConfig{}, fmt.Errorf("invalid tunnel ID %q: must match tunnel_<32 lowercase letters or digits>", tunnelID)
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

	var apiKeyFlagValue string
	if flag := fs.Lookup("control-plane.api-key"); flag != nil && flag.Changed {
		apiKeyFlagValue = flag.Value.String()
	}

	apiKey, err := getControlPlaneAPIKey(apiKeyFlagValue, lookupEnv)
	if err != nil {
		return ControlPlaneConfig{}, err
	}

	extraHeaders, err := buildControlPlaneExtraHeaders(fs, lookupEnv)
	if err != nil {
		return ControlPlaneConfig{}, err
	}

	return ControlPlaneConfig{
		BaseURL:             baseURL,
		TunnelID:            types.TunnelID(tunnelID),
		APIKey:              apiKey,
		MaxInFlightRequests: maxInFlight,
		PollTimeout:         pollTimeout,
		ExtraHeaders:        extraHeaders,
	}, nil
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
//	CONTROL_PLANE_EXTRA_HEADERS="extra-header: true, debug: 1"
func buildControlPlaneExtraHeaders(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (map[string]string, error) {
	var raw []string

	if flag := fs.Lookup("control-plane.extra-headers"); flag != nil && flag.Changed {
		values, err := fs.GetStringArray("control-plane.extra-headers")
		if err != nil {
			return nil, fmt.Errorf("invalid value for --control-plane.extra-headers: %w", err)
		}
		raw = append(raw, values...)
	} else if envVal, ok := lookupEnv("CONTROL_PLANE_EXTRA_HEADERS"); ok && envVal != "" {
		raw = splitHeaderList(envVal)
	}

	if len(raw) == 0 {
		return nil, nil
	}

	return parseHeaderList(raw)
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

func parseHeaderList(values []string) (map[string]string, error) {
	headers := make(map[string]string, len(values))
	for _, v := range values {
		key, val, err := parseHeader(v)
		if err != nil {
			return nil, err
		}
		if key == "" {
			continue
		}
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

	return key, value, nil
}

func buildLoggingConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (LoggingConfig, error) {
	logLevelRaw := firstSet(
		getValue(fs, "log.level"),
		envOrDefault(lookupEnv, "LOG_LEVEL", defaultLogLevel),
	)
	logFormatRaw := firstSet(
		getValue(fs, "log.format"),
		envOrDefault(lookupEnv, "LOG_FORMAT", defaultLogFormat.String()),
	)
	logFormat, err := ParseLogFormat(logFormatRaw)
	if err != nil {
		return LoggingConfig{}, err
	}
	logFile := firstSet(
		getValue(fs, "log.file"),
		envOrDefault(lookupEnv, "LOG_FILE", ""),
	)

	if !strings.EqualFold(logLevelRaw, defaultLogLevel) && logFormat == defaultLogFormat {
		return LoggingConfig{}, fmt.Errorf("log level requires 'struct-text' or 'json' log format")
	}

	if logFormat == LogFormatUnset && logFile != "" {
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

	return HealthConfig{
		ListenAddr: listenAddr,
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

func buildAdminUIConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (AdminUIConfig, error) {
	allowRemote, err := resolveAllowRemoteUI(fs, lookupEnv)
	if err != nil {
		return AdminUIConfig{}, err
	}

	openBrowser, err := resolveOpenWebUI(fs, lookupEnv)
	if err != nil {
		return AdminUIConfig{}, err
	}

	return AdminUIConfig{AllowRemote: allowRemote, OpenBrowser: openBrowser}, nil
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

func buildMCPConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (MCPConfig, error) {
	commandRaw := firstSet(
		getValue(fs, "mcp.command"),
		envOrDefault(lookupEnv, "MCP_COMMAND", ""),
	)
	serverURLRaw := firstSet(
		getValue(fs, "mcp.server-url"),
		envOrDefault(lookupEnv, "MCP_SERVER_URL", ""),
	)
	if commandRaw != "" && serverURLRaw != "" {
		return MCPConfig{}, errors.New("mcp.command and mcp.server-url are mutually exclusive; choose one")
	}

	transportKind := MCPTransportHTTPStreamable
	var (
		serverURL   *url.URL
		commandArgs []string
	)
	switch {
	case commandRaw != "":
		transportKind = MCPTransportStdio
		parsed, err := parseCommandArgv(commandRaw)
		if err != nil {
			return MCPConfig{}, fmt.Errorf("invalid mcp.command: %w", err)
		}
		commandArgs = parsed
	case serverURLRaw != "":
		parsed, err := parseURL(serverURLRaw)
		if err != nil {
			return MCPConfig{}, fmt.Errorf("invalid mcp.server-url: %w", err)
		}
		serverURL = parsed
	default:
		return MCPConfig{}, errors.New("MCP server URL or command is required; set --mcp.server-url, MCP_SERVER_URL, or --mcp.command")
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

	return MCPConfig{
		ServerURL:             serverURL,
		Command:               commandRaw,
		CommandArgs:           commandArgs,
		TransportKind:         transportKind,
		ConnectionMaxTTL:      ttl,
		MaxConcurrentRequests: maxConcurrent,
	}, nil
}

func buildHarpoonConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (HarpoonConfig, error) {
	allowPlaintext, err := getBool(fs, lookupEnv, "harpoon-allow-plaintext-http", "HARPOON_ALLOW_PLAINTEXT_HTTP")
	if err != nil {
		return HarpoonConfig{}, err
	}
	maxResponseBytes, err := getInt(fs, lookupEnv, "harpoon-max-response-bytes", "HARPOON_MAX_RESPONSE_BYTES", DefaultHarpoonMaxResponseBytes)
	if err != nil {
		return HarpoonConfig{}, err
	}
	if maxResponseBytes <= 0 {
		return HarpoonConfig{}, errors.New("harpoon-max-response-bytes must be positive")
	}
	if maxResponseBytes > DefaultHarpoonMaxResponseBytes {
		return HarpoonConfig{}, fmt.Errorf("harpoon-max-response-bytes must be less than or equal to %d", DefaultHarpoonMaxResponseBytes)
	}
	maxRedirects, err := getInt(fs, lookupEnv, "harpoon-max-redirects", "HARPOON_MAX_REDIRECTS", DefaultHarpoonMaxRedirects)
	if err != nil {
		return HarpoonConfig{}, err
	}
	if maxRedirects < 0 {
		return HarpoonConfig{}, errors.New("harpoon-max-redirects must be non-negative")
	}
	if maxRedirects > DefaultHarpoonMaxRedirects {
		return HarpoonConfig{}, fmt.Errorf("harpoon-max-redirects must be less than or equal to %d", DefaultHarpoonMaxRedirects)
	}
	targets, err := buildHarpoonTargets(fs, lookupEnv, allowPlaintext)
	if err != nil {
		return HarpoonConfig{}, err
	}
	additional, err := buildHarpoonAdditionalTransports(fs, lookupEnv)
	if err != nil {
		return HarpoonConfig{}, err
	}
	capturePayloads, err := getBool(fs, lookupEnv, "harpoon-capture-payloads", "HARPOON_CAPTURE_PAYLOADS")
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
	}, nil
}

func buildHarpoonTargets(fs *pflag.FlagSet, lookupEnv func(string) (string, bool), allowPlaintext bool) ([]HarpoonTarget, error) {
	var rawTargets []string
	if flag := fs.Lookup("harpoon-target"); flag != nil && flag.Changed {
		values, err := fs.GetStringArray("harpoon-target")
		if err != nil {
			return nil, fmt.Errorf("invalid value for --harpoon-target: %w", err)
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
		target, err := parseHarpoonTarget(raw, allowPlaintext)
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

func parseHarpoonTarget(raw string, allowPlaintext bool) (HarpoonTarget, error) {
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
	return HarpoonTarget{
		Label:       label,
		Description: values["desc"],
		BaseURL:     parsed,
	}, nil
}

func buildHarpoonAdditionalTransports(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) ([]HarpoonTransportKind, error) {
	var raw []string
	if flag := fs.Lookup("harpoon-additional-transport"); flag != nil && flag.Changed {
		values, err := fs.GetStringArray("harpoon-additional-transport")
		if err != nil {
			return nil, fmt.Errorf("invalid value for --harpoon-additional-transport: %w", err)
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
