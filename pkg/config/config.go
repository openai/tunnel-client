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

const (
	defaultControlPlaneBaseURL                    = "https://api.openai.com"
	defaultControlPlaneMaxInFlight                = 20
	maxControlPlaneMaxInFlight                    = 10000
	defaultControlPlanePollTimeout                = 30 * time.Second
	defaultLogLevel                               = "info"
	defaultLogFormat                    LogFormat = LogFormatUnset
	defaultHealthListenAddr                       = ":8080"
	defaultMCPConnectionMaxTTL                    = 10 * time.Minute
	defaultMCPMaxConcurrentRequests               = 10
	wellKnownOAuthProtectedResourcePath           = "/.well-known/oauth-protected-resource"
)

const _ = uint(maxControlPlaneMaxInFlight - defaultControlPlaneMaxInFlight)

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
}

// AdminUIConfig defines runtime behavior for the embedded admin web UI.
type AdminUIConfig struct {
	// Enabled controls whether the embedded web UI is mounted on the admin/health server.
	Enabled bool
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
	ConnectionMaxTTL      time.Duration
	MaxConcurrentRequests int
	// OAuthResourceMetadataURLs lists candidate OAuth protected resource metadata
	// endpoints derived from ServerURL following RFC 9728 discovery rules.
	OAuthResourceMetadataURLs []*url.URL
}

// BootstrapOAuthResourceMetadataURLs populates OAuthResourceMetadataURLs using
// the configured ServerURL when the slice is empty.
func (c *MCPConfig) BootstrapOAuthResourceMetadataURLs() error {
	if c == nil {
		return errors.New("mcp config: nil receiver")
	}
	if len(c.OAuthResourceMetadataURLs) > 0 {
		return nil
	}
	if c.ServerURL == nil {
		return errors.New("mcp config: server URL is required to derive oauth metadata URLs")
	}
	c.OAuthResourceMetadataURLs = buildResourceMetadataURLs(c.ServerURL)
	return nil
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
	_, _ = fmt.Fprintln(fs.Output(), "  START_WEB_UI\tSet to true to enable the embedded web UI (optional)")
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
	fs.Bool("start-web-ui", false, "Start the embedded web UI on the health server (env.START_WEB_UI)")
	fs.String("pid.file", "", "File to write the tunnel-client process ID to (env.PID_FILE)")
	fs.String("mcp.server-url", "", "Target MCP server URL (env.MCP_SERVER_URL)")
	fs.Duration("mcp.connection-max-ttl", defaultMCPConnectionMaxTTL, "Maximum lifetime of MCP transport connections (env.MCP_CONNECTION_MAX_TTL)")
	fs.Int("mcp.max-concurrent-requests", defaultMCPMaxConcurrentRequests, "Maximum number of concurrent requests to the MCP server (env.MCP_MAX_CONCURRENT_REQUESTS)")

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

	cfg := &Config{
		ControlPlane: controlPlane,
		Logging:      logging,
		Health:       health,
		Process:      process,
		MCP:          mcp,
		AdminUI:      adminUI,
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
	enabled, err := resolveStartWebUI(fs, lookupEnv)
	if err != nil {
		return AdminUIConfig{}, err
	}
	return AdminUIConfig{Enabled: enabled}, nil
}

func resolveStartWebUI(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (bool, error) {
	if flag := fs.Lookup("start-web-ui"); flag != nil && flag.Changed {
		val, err := fs.GetBool("start-web-ui")
		if err != nil {
			return false, fmt.Errorf("parse --start-web-ui: %w", err)
		}
		return val, nil
	}

	if envVal, ok := lookupEnv("START_WEB_UI"); ok && envVal != "" {
		val, err := strconv.ParseBool(envVal)
		if err != nil {
			return false, fmt.Errorf("parse START_WEB_UI: %w", err)
		}
		return val, nil
	}

	return false, nil
}

func buildMCPConfig(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (MCPConfig, error) {
	serverURLRaw := firstSet(
		getValue(fs, "mcp.server-url"),
		envOrDefault(lookupEnv, "MCP_SERVER_URL", ""),
	)
	if serverURLRaw == "" {
		return MCPConfig{}, errors.New("MCP server URL is required; set --mcp.server-url or MCP_SERVER_URL")
	}
	serverURL, err := parseURL(serverURLRaw)
	if err != nil {
		return MCPConfig{}, fmt.Errorf("invalid mcp.server-url: %w", err)
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
		ServerURL:                 serverURL,
		ConnectionMaxTTL:          ttl,
		MaxConcurrentRequests:     maxConcurrent,
		OAuthResourceMetadataURLs: buildResourceMetadataURLs(serverURL),
	}, nil
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

// buildResourceMetadataURLs constructs the ordered list of candidate OAuth
// protected resource metadata endpoints derived from the MCP server URL. It
// follows RFC 9728 discovery rules by prioritizing the path-specific well-known
// URI, then the root well-known URI.
func buildResourceMetadataURLs(serverURL *url.URL) []*url.URL {
	base := &url.URL{
		Scheme: serverURL.Scheme,
		Host:   serverURL.Host,
		Path:   wellKnownOAuthProtectedResourcePath,
	}

	urls := make([]*url.URL, 0, 2)

	pathSuffix := strings.Trim(serverURL.EscapedPath(), "/")
	if pathSuffix != "" {
		withPath := *base
		withPath.Path = path.Join(base.Path, pathSuffix)
		urls = append(urls, &withPath)
	}

	urls = append(urls, base)

	return urls
}
