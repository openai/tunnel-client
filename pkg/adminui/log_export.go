package adminui

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
	"github.com/openai/tunnel-client/pkg/version"
)

const (
	defaultLogExportWindow = 30 * time.Minute
	maxLogExportWindow     = 24 * time.Hour
	metricsSnapshotFile    = "tunnel-client.metrics.prom"
	runtimeSnapshotFile    = "tunnel-client.runtime.yaml"
)

type logExportManifest struct {
	GeneratedAt       time.Time        `json:"generated_at"`
	WindowStart       time.Time        `json:"window_start"`
	WindowEnd         time.Time        `json:"window_end"`
	Window            string           `json:"window"`
	EventCount        int              `json:"event_count"`
	LogBufferCapacity int              `json:"log_buffer_capacity"`
	MetricsBytes      int              `json:"metrics_bytes"`
	Redacted          bool             `json:"redacted"`
	Files             []string         `json:"files"`
	Runtime           logExportRuntime `json:"runtime"`
}

type logExportRuntime struct {
	Argv            []string                     `json:"argv" yaml:"argv"`
	Environment     map[string]string            `json:"environment" yaml:"environment"`
	Client          logExportClientDetails       `json:"client" yaml:"client"`
	ActualConfig    *logExportConfigFileSnapshot `json:"actual_config,omitempty" yaml:"actual_config,omitempty"`
	EffectiveConfig any                          `json:"effective_config,omitempty" yaml:"effective_config,omitempty"`
}

type logExportClientDetails struct {
	ClientName      string `json:"client_name" yaml:"client_name"`
	SemanticVersion string `json:"semantic_version" yaml:"semantic_version"`
	Version         string `json:"version" yaml:"version"`
	UserAgent       string `json:"user_agent" yaml:"user_agent"`
}

type logExportConfigFileSnapshot struct {
	Path     string `json:"path" yaml:"path"`
	Contents any    `json:"contents,omitempty" yaml:"contents,omitempty"`
}

type metricsSnapshot struct {
	Filename string
	Body     []byte
}

type logExportAdminSnapshots struct {
	Status  statusResponse       `json:"status"`
	System  systemResponse       `json:"system"`
	OAuth   oauthStatusResponse  `json:"oauth"`
	Harpoon logExportHarpoonData `json:"harpoon"`
}

type logExportHarpoonData struct {
	Status  harpoonStatusResponse  `json:"status"`
	Targets harpoonTargetsResponse `json:"targets"`
	Calls   harpoonCallsResponse   `json:"calls"`
}

// RuntimeSnapshotProvider returns redacted runtime metadata for support log exports.
type RuntimeSnapshotProvider func() logExportRuntime

// MetricsSnapshotProvider returns a point-in-time Prometheus text snapshot for support log exports.
type MetricsSnapshotProvider func() (metricsSnapshot, error)

// AdminSnapshotProvider returns the current support-facing admin API snapshots for export bundles.
type AdminSnapshotProvider func() logExportAdminSnapshots

func NewRuntimeSnapshotProvider(cfg *config.Config) RuntimeSnapshotProvider {
	return func() logExportRuntime {
		runtime := collectLogExportRuntime(
			os.Args,
			os.Environ(),
			sensitiveRuntimeEnvReferencesFromConfig(cfg),
		)
		runtime.ActualConfig = buildConfigFileSnapshot(cfg)
		runtime.EffectiveConfig = buildEffectiveConfigSnapshot(cfg)
		return runtime
	}
}

func NewMetricsSnapshotProvider(exporter http.Handler) MetricsSnapshotProvider {
	return func() (metricsSnapshot, error) {
		if exporter == nil {
			return metricsSnapshot{}, nil
		}

		req, err := http.NewRequest(http.MethodGet, "/metrics", nil)
		if err != nil {
			return metricsSnapshot{}, fmt.Errorf("create metrics snapshot request: %w", err)
		}

		rec := &snapshotResponseWriter{header: make(http.Header)}
		exporter.ServeHTTP(rec, req)
		if rec.statusCode == 0 {
			rec.statusCode = http.StatusOK
		}
		if rec.statusCode != http.StatusOK {
			return metricsSnapshot{}, fmt.Errorf("capture metrics snapshot: unexpected status %d", rec.statusCode)
		}

		return metricsSnapshot{
			Filename: metricsSnapshotFile,
			Body:     bytes.Clone(rec.body.Bytes()),
		}, nil
	}
}

func handleLogsExport(
	buf *LogBuffer,
	runtime RuntimeSnapshotProvider,
	metrics MetricsSnapshotProvider,
	adminSnapshots AdminSnapshotProvider,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		window := parseExportWindow(r)
		now := time.Now().UTC()
		events := buf.Since(now.Add(-window), buf.Capacity())
		snapshot, err := callMetricsSnapshot(metrics)
		if err != nil {
			http.Error(w, "capture metrics snapshot", http.StatusInternalServerError)
			return
		}

		archive, err := buildLogsArchive(
			events,
			now,
			window,
			buf.Capacity(),
			callRuntimeSnapshot(runtime),
			snapshot,
			callAdminSnapshots(adminSnapshots),
		)
		if err != nil {
			http.Error(w, "build logs archive", http.StatusInternalServerError)
			return
		}

		filename := "tunnel-client-logs-" + now.Format("20060102T150405Z") + ".tar.gz"
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
		w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
		w.Header().Set("Cache-Control", "no-store, max-age=0")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(archive)
	}
}

func callRuntimeSnapshot(runtime RuntimeSnapshotProvider) logExportRuntime {
	var snapshot logExportRuntime
	if runtime == nil {
		return withClientDetails(snapshot)
	}
	snapshot = runtime()
	return withClientDetails(snapshot)
}

func callMetricsSnapshot(metrics MetricsSnapshotProvider) (metricsSnapshot, error) {
	if metrics == nil {
		return metricsSnapshot{}, nil
	}
	return metrics()
}

func callAdminSnapshots(adminSnapshots AdminSnapshotProvider) logExportAdminSnapshots {
	if adminSnapshots == nil {
		return logExportAdminSnapshots{}
	}
	return adminSnapshots()
}

func buildLogsArchive(
	events []LogEvent,
	now time.Time,
	window time.Duration,
	logBufferCapacity int,
	runtime logExportRuntime,
	snapshot metricsSnapshot,
	adminSnapshots logExportAdminSnapshots,
) ([]byte, error) {
	runtime = withClientDetails(runtime)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	writersClosed := false
	defer func() {
		if !writersClosed {
			_ = closeLogArchiveWriters(tw, gz)
		}
	}()

	files := []string{
		"manifest.json",
		"README.txt",
		runtimeSnapshotFile,
		"tunnel-client.logs.ndjson",
	}
	if snapshot.Filename != "" {
		files = append(files, snapshot.Filename)
	}
	files = append(files, "admin/status.json", "admin/system.json", "admin/oauth.json", "admin/harpoon.json")
	manifest := logExportManifest{
		GeneratedAt:       now,
		WindowStart:       now.Add(-window),
		WindowEnd:         now,
		Window:            window.String(),
		EventCount:        len(events),
		LogBufferCapacity: logBufferCapacity,
		MetricsBytes:      len(snapshot.Body),
		Redacted:          true,
		Files:             files,
		Runtime:           runtime,
	}

	if err := writeTarJSON(tw, "manifest.json", manifest); err != nil {
		return nil, err
	}
	if err := writeTarFile(tw, "README.txt", []byte("Tunnel-client log export.\n\nLogs are captured from the admin UI in-memory buffer and redacted before export.\nThe NDJSON file contains one redacted JSON log event per line.\nmanifest.json includes the archive index and redacted runtime metadata.\ntunnel-client.runtime.yaml includes redacted argv, relevant environment variables, the startup YAML config file when present, and the effective startup config.\nThe Prometheus snapshot is captured at export time from /metrics when available.\nadmin/status.json, admin/system.json, admin/oauth.json, and admin/harpoon.json mirror the current admin API snapshots at export time.\n")); err != nil {
		return nil, err
	}
	if err := writeTarYAML(tw, runtimeSnapshotFile, runtime); err != nil {
		return nil, err
	}

	var logs bytes.Buffer
	enc := json.NewEncoder(&logs)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			return nil, fmt.Errorf("encode log event: %w", err)
		}
	}
	if err := writeTarFile(tw, "tunnel-client.logs.ndjson", logs.Bytes()); err != nil {
		return nil, err
	}
	if snapshot.Filename != "" {
		if err := writeTarFile(tw, snapshot.Filename, snapshot.Body); err != nil {
			return nil, err
		}
	}
	if err := writeTarRedactedJSON(tw, "admin/status.json", adminSnapshots.Status); err != nil {
		return nil, err
	}
	if err := writeTarRedactedJSON(tw, "admin/system.json", adminSnapshots.System); err != nil {
		return nil, err
	}
	if err := writeTarRedactedJSON(tw, "admin/oauth.json", adminSnapshots.OAuth); err != nil {
		return nil, err
	}
	if err := writeTarRedactedJSON(tw, "admin/harpoon.json", adminSnapshots.Harpoon); err != nil {
		return nil, err
	}

	if err := closeLogArchiveWriters(tw, gz); err != nil {
		writersClosed = true
		return nil, err
	}
	writersClosed = true
	return buf.Bytes(), nil
}

func closeLogArchiveWriters(tarWriter, gzipWriter io.Closer) error {
	var closeErrors []error
	if err := tarWriter.Close(); err != nil {
		closeErrors = append(closeErrors, fmt.Errorf("close tar writer: %w", err))
	}
	if err := gzipWriter.Close(); err != nil {
		closeErrors = append(closeErrors, fmt.Errorf("close gzip writer: %w", err))
	}
	return errors.Join(closeErrors...)
}

func collectLogExportRuntime(argv []string, environ []string, extraSensitiveEnvKeys ...map[string]struct{}) logExportRuntime {
	env := splitEnvironment(environ)
	envKeys := relevantRuntimeEnvKeys(env)
	for key := range envReferencesFromArgs(argv) {
		envKeys[key] = struct{}{}
	}
	sensitiveEnvKeys := sensitiveRuntimeEnvReferencesFromArgs(argv)
	for _, keys := range extraSensitiveEnvKeys {
		for key := range keys {
			envKeys[key] = struct{}{}
			sensitiveEnvKeys[key] = struct{}{}
		}
	}

	outEnv := make(map[string]string, len(envKeys))
	for key := range envKeys {
		val, ok := env[key]
		if !ok {
			continue
		}
		if _, sensitive := sensitiveEnvKeys[key]; sensitive {
			outEnv[key] = "[REDACTED]"
			continue
		}
		outEnv[key] = redactRuntimeEnv(key, val)
	}

	return logExportRuntime{
		Argv:        redactArgv(argv),
		Environment: outEnv,
		Client:      currentClientDetails(),
	}
}

func withClientDetails(runtime logExportRuntime) logExportRuntime {
	if runtime.Client.ClientName == "" {
		runtime.Client = currentClientDetails()
	}
	return runtime
}

func currentClientDetails() logExportClientDetails {
	return logExportClientDetails{
		ClientName:      version.ClientName,
		SemanticVersion: version.SemanticVersion,
		Version:         version.Version,
		UserAgent:       version.UserAgent,
	}
}

func splitEnvironment(environ []string) map[string]string {
	out := make(map[string]string, len(environ))
	for _, entry := range environ {
		key, val, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		out[key] = val
	}
	return out
}

func relevantRuntimeEnvKeys(env map[string]string) map[string]struct{} {
	out := make(map[string]struct{})
	for key := range env {
		if isRelevantRuntimeEnvKey(key) {
			out[key] = struct{}{}
		}
	}
	return out
}

func isRelevantRuntimeEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	switch upper {
	case "ALLOW_REMOTE_UI",
		"CA_BUNDLE",
		"HEALTH_LISTEN_ADDR",
		"HEALTH_UNIX_SOCKET",
		"HEALTH_URL_FILE",
		"HTTP_PROXY",
		"HTTPS_PROXY",
		"NO_PROXY",
		"OPENAI_API_KEY",
		"OPEN_WEB_UI",
		"PID_FILE",
		"PROXY_CHECK_INTERVAL":
		return true
	}
	for _, prefix := range []string{
		"ADMIN_UI_",
		"CLOUDFLARED_",
		"CONTROL_PLANE_",
		"HARPOON_",
		"LOG_",
		"MCP_",
		"OPENAI_TUNNEL_",
		"TUNNEL_",
	} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

func envReferencesFromArgs(argv []string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, arg := range argv {
		for _, key := range envReferencesFromString(arg) {
			out[key] = struct{}{}
		}
	}
	return out
}

func sensitiveRuntimeEnvReferencesFromArgs(argv []string) map[string]struct{} {
	out := make(map[string]struct{})
	for i, arg := range argv {
		name, value, hasValue := splitLongFlag(arg)
		if normalizeRuntimeKey(name) != "cloudflared_token" {
			continue
		}
		if !hasValue && i+1 < len(argv) {
			value = argv[i+1]
		}
		for _, key := range envReferencesFromString(value) {
			out[key] = struct{}{}
		}
	}
	return out
}

func sensitiveRuntimeEnvReferencesFromConfig(cfg *config.Config) map[string]struct{} {
	out := make(map[string]struct{})
	if cfg == nil || len(cfg.Runtime.ConfigFileContents) == 0 {
		return out
	}

	var file struct {
		Cloudflared struct {
			Token *string `yaml:"token"`
		} `yaml:"cloudflared"`
	}
	if err := yaml.Unmarshal(cfg.Runtime.ConfigFileContents, &file); err != nil || file.Cloudflared.Token == nil {
		return out
	}
	for _, key := range envReferencesFromString(*file.Cloudflared.Token) {
		out[key] = struct{}{}
	}
	return out
}

func envReferencesFromString(s string) []string {
	var keys []string
	remaining := s
	for {
		idx := strings.Index(strings.ToLower(remaining), "env:")
		if idx < 0 {
			return keys
		}
		start := idx + len("env:")
		tail := remaining[start:]
		end := 0
		for end < len(tail) && isEnvNameByte(tail[end]) {
			end++
		}
		if end > 0 {
			keys = append(keys, tail[:end])
		}
		remaining = tail[end:]
	}
}

func isEnvNameByte(ch byte) bool {
	return (ch >= 'A' && ch <= 'Z') ||
		(ch >= 'a' && ch <= 'z') ||
		(ch >= '0' && ch <= '9') ||
		ch == '_'
}

func redactArgv(argv []string) []string {
	if len(argv) == 0 {
		return nil
	}
	out := make([]string, 0, len(argv))
	redactNextForKey := ""
	for _, arg := range argv {
		if redactNextForKey != "" {
			out = append(out, redactRuntimeArgValue(redactNextForKey, arg))
			redactNextForKey = ""
			continue
		}

		name, value, hasValue := splitLongFlag(arg)
		if name == "" {
			out = append(out, redactString(arg))
			continue
		}
		if hasValue {
			out = append(out, name+"="+redactRuntimeArgValue(name, value))
			continue
		}
		out = append(out, arg)
		if isSensitiveRuntimeKey(name) || isHeaderListKey(name) {
			redactNextForKey = name
		}
	}
	return out
}

func splitLongFlag(arg string) (name string, value string, hasValue bool) {
	if !strings.HasPrefix(arg, "--") {
		return "", "", false
	}
	if name, value, ok := strings.Cut(arg, "="); ok {
		return name, value, true
	}
	return arg, "", false
}

func redactRuntimeArgValue(key string, value string) string {
	if isHeaderListKey(key) {
		return redactHeaderListValue(value)
	}
	if isSensitiveRuntimeKey(key) && !isReferenceValue(value) {
		return "[REDACTED]"
	}
	return redactString(value)
}

func redactRuntimeEnv(key string, value string) string {
	if isHeaderListKey(key) {
		return redactHeaderListValue(value)
	}
	if isSensitiveRuntimeKey(key) {
		return "[REDACTED]"
	}
	return redactString(value)
}

func buildConfigFileSnapshot(cfg *config.Config) *logExportConfigFileSnapshot {
	if cfg == nil || cfg.Runtime.ConfigFile == "" || len(cfg.Runtime.ConfigFileContents) == 0 {
		return nil
	}

	var contents any
	dec := yaml.NewDecoder(bytes.NewReader(cfg.Runtime.ConfigFileContents))
	if err := dec.Decode(&contents); err != nil {
		return &logExportConfigFileSnapshot{
			Path: redactString(cfg.Runtime.ConfigFile),
			Contents: map[string]any{
				"parse_error": redactString(err.Error()),
			},
		}
	}

	return &logExportConfigFileSnapshot{
		Path:     redactString(cfg.Runtime.ConfigFile),
		Contents: redactConfigFileValue("", contents),
	}
}

func redactConfigFileValue(key string, v any) any {
	if isHeaderListKey(key) {
		return redactHeaderConfigValue(v)
	}
	if isSensitiveRuntimeKey(key) {
		if s, ok := v.(string); ok && isSafeReferenceValue(s) {
			return redactSnapshotString(s)
		}
		return "[REDACTED]"
	}

	switch t := v.(type) {
	case string:
		return redactSnapshotString(t)
	case []any:
		out := make([]any, 0, len(t))
		for _, item := range t {
			out = append(out, redactConfigFileValue("", item))
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, value := range t {
			out[k] = redactConfigFileValue(k, value)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, value := range t {
			keyString := fmt.Sprint(k)
			out[keyString] = redactConfigFileValue(keyString, value)
		}
		return out
	default:
		return v
	}
}

func redactHeaderConfigValue(v any) any {
	switch t := v.(type) {
	case string:
		return redactHeaderListValue(t)
	case []any:
		out := make([]any, 0, len(t))
		for _, item := range t {
			out = append(out, redactHeaderConfigValue(item))
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for key := range t {
			out[key] = "[REDACTED]"
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for key := range t {
			out[fmt.Sprint(key)] = "[REDACTED]"
		}
		return out
	default:
		return v
	}
}

func buildEffectiveConfigSnapshot(cfg *config.Config) map[string]any {
	if cfg == nil {
		return nil
	}

	out := map[string]any{
		"source": map[string]any{
			"config_file":  redactString(cfg.Runtime.ConfigFile),
			"profile":      redactString(cfg.Runtime.ProfileName),
			"profile_path": redactString(cfg.Runtime.ProfilePath),
			"profile_dir":  redactString(cfg.Runtime.ProfileDir),
		},
		"control_plane": map[string]any{
			"base_url":                urlForSnapshot(cfg.ControlPlane.BaseURL),
			"tunnel_id":               cfg.ControlPlane.TunnelID.String(),
			"api_key":                 redactedPresence(cfg.ControlPlane.APIKey),
			"client_certificate":      clientCertificateSnapshot(cfg.ControlPlane.ClientCertificate),
			"max_inflight_requests":   cfg.ControlPlane.MaxInFlightRequests,
			"poll_timeout":            cfg.ControlPlane.PollTimeout.String(),
			"poll_deadline_guardrail": durationForSnapshot(cfg.ControlPlane.PollDeadlineGuardrail),
			"extra_headers":           redactHeaderMap(cfg.ControlPlane.ExtraHeaders),
			"http_proxy":              urlForSnapshot(cfg.ControlPlane.HTTPProxy),
			"http_proxy_source":       string(cfg.ControlPlane.HTTPProxySource),
			"poll_backoff_min":        durationForSnapshot(cfg.ControlPlane.PollBackoffMin),
			"poll_backoff_max":        durationForSnapshot(cfg.ControlPlane.PollBackoffMax),
		},
		"log": map[string]any{
			"level":           cfg.Logging.Level.String(),
			"format":          cfg.Logging.Format.String(),
			"file":            redactString(cfg.Logging.File),
			"http_raw_unsafe": cfg.Logging.HTTPRawUnsafe,
		},
		"health": map[string]any{
			"listen_addr": cfg.Health.ListenAddr,
			"unix_socket": redactString(cfg.Health.UnixSocket),
			"url_file":    redactString(cfg.Health.URLFile),
		},
		"process": map[string]any{
			"pid_file": redactString(cfg.Process.PIDFile),
		},
		"cloudflared": map[string]any{
			"enabled":       cfg.Cloudflared.Enabled(),
			"token":         redactedPresence(cfg.Cloudflared.Token),
			"path":          redactString(cfg.Cloudflared.Path),
			"ready_timeout": durationForSnapshot(cfg.Cloudflared.ReadyTimeout),
		},
		"admin_ui": map[string]any{
			"allow_remote":      cfg.AdminUI.AllowRemote,
			"open_browser":      cfg.AdminUI.OpenBrowser,
			"log_buffer_events": cfg.AdminUI.LogBufferEvents,
		},
		"mcp": map[string]any{
			"server_url":              urlForSnapshot(cfg.MCP.ServerURL),
			"unix_socket_path":        redactString(cfg.MCP.UnixSocketPath),
			"command":                 redactString(cfg.MCP.Command),
			"command_args":            redactStringSlice(cfg.MCP.CommandArgs),
			"transport":               string(cfg.MCP.TransportKind),
			"client_certificate":      clientCertificateSnapshot(cfg.MCP.ClientCertificate),
			"connection_max_ttl":      cfg.MCP.ConnectionMaxTTL.String(),
			"max_concurrent_requests": cfg.MCP.MaxConcurrentRequests,
			"extra_headers":           redactHeaderMap(cfg.MCP.ExtraHeaders),
			"discovery_extra_headers": redactHeaderMap(cfg.MCP.DiscoveryExtraHeaders),
			"http_proxy":              urlForSnapshot(cfg.MCP.HTTPProxy),
			"http_proxy_source":       string(cfg.MCP.HTTPProxySource),
			"channel_bindings":        mcpBindingSnapshots(cfg.MCP.ChannelBindings),
		},
		"harpoon": map[string]any{
			"allow_plaintext_http":  cfg.Harpoon.AllowPlaintextHTTP,
			"max_response_bytes":    cfg.Harpoon.MaxResponseBytes,
			"max_redirects":         cfg.Harpoon.MaxRedirects,
			"additional_transports": harpoonTransportsSnapshot(cfg.Harpoon.AdditionalTransports),
			"targets":               harpoonTargetSnapshots(cfg.Harpoon.Targets),
			"capture_payloads":      cfg.Harpoon.CapturePayloads,
			"host_classifier": map[string]any{
				"include_suffix":   cfg.Harpoon.HostClassifier.IncludeSuffix,
				"include_regex":    cfg.Harpoon.HostClassifier.IncludeRegex,
				"include_loopback": cfg.Harpoon.HostClassifier.IncludeLoopback,
				"include_private":  cfg.Harpoon.HostClassifier.IncludePrivate,
			},
			"http_proxy":        urlForSnapshot(cfg.Harpoon.HTTPProxy),
			"http_proxy_source": string(cfg.Harpoon.HTTPProxySource),
		},
		"proxy": map[string]any{
			"check_interval": cfg.ProxyHealth.CheckInterval.String(),
		},
	}
	if cfg.TLS != nil {
		out["tls"] = map[string]any{
			"ca_bundle": redactString(cfg.TLS.Path),
		}
	}
	return out
}

func urlForSnapshot(u *url.URL) string {
	if u == nil {
		return ""
	}
	return redactSnapshotString(u.String())
}

func durationForSnapshot(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.String()
}

func redactedPresence(value string) string {
	if value == "" {
		return ""
	}
	return "[REDACTED]"
}

func redactHeaderMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key := range values {
		out[key] = "[REDACTED]"
	}
	return out
}

func redactStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, redactString(value))
	}
	return out
}

func clientCertificateSnapshot(cert *tlsconfig.ClientCertificate) any {
	if cert == nil {
		return nil
	}
	return map[string]any{
		"cert_path": redactString(cert.CertPath),
		"key_path":  redactedPresence(cert.KeyPath),
	}
}

func mcpBindingSnapshots(bindings []config.MCPChannelBinding) []map[string]any {
	if len(bindings) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(bindings))
	for _, binding := range bindings {
		snapshot := map[string]any{
			"channel":            binding.Channel.String(),
			"transport":          string(binding.TransportKind),
			"server_url":         urlForSnapshot(binding.ServerURL),
			"unix_socket_path":   redactString(binding.UnixSocketPath),
			"command":            redactString(binding.Command),
			"command_args":       redactStringSlice(binding.CommandArgs),
			"client_certificate": clientCertificateSnapshot(binding.ClientCertificate),
			"http_proxy":         urlForSnapshot(binding.HTTPProxy),
			"http_proxy_source":  string(binding.HTTPProxySource),
		}
		out = append(out, snapshot)
	}
	return out
}

func harpoonTransportsSnapshot(transports []config.HarpoonTransportKind) []string {
	if len(transports) == 0 {
		return nil
	}
	out := make([]string, 0, len(transports))
	for _, transport := range transports {
		out = append(out, string(transport))
	}
	return out
}

func harpoonTargetSnapshots(targets []config.HarpoonTarget) []map[string]string {
	if len(targets) == 0 {
		return nil
	}
	out := make([]map[string]string, 0, len(targets))
	for _, target := range targets {
		out = append(out, map[string]string{
			"label":            target.Label,
			"description":      redactString(target.Description),
			"base_url":         urlForSnapshot(target.BaseURL),
			"unix_socket_path": redactString(target.UnixSocketPath),
		})
	}
	return out
}

func isReferenceValue(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "env:") || strings.HasPrefix(lower, "file:")
}

func isSafeReferenceValue(value string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "env:")
}

func isHeaderListKey(key string) bool {
	normalized := normalizeRuntimeKey(key)
	return strings.HasSuffix(normalized, "extra_headers") || strings.HasSuffix(normalized, "extra_header")
}

func normalizeRuntimeKey(key string) string {
	return strings.ToLower(strings.NewReplacer("-", "_", ".", "_").Replace(strings.TrimLeft(key, "-")))
}

func redactHeaderListValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return value
	}
	separator := ", "
	if strings.Contains(value, ";") {
		separator = "; "
	}
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, _, ok := strings.Cut(part, ":")
		if !ok || strings.TrimSpace(key) == "" {
			out = append(out, redactString(part))
			continue
		}
		out = append(out, strings.TrimSpace(key)+": [REDACTED]")
	}
	if len(out) == 0 {
		return redactString(value)
	}
	return strings.Join(out, separator)
}

func isSensitiveRuntimeKey(key string) bool {
	normalized := normalizeRuntimeKey(key)
	if isSensitiveAttrKey(normalized) {
		return true
	}
	for _, token := range strings.Split(normalized, "_") {
		switch token {
		case "key", "token", "secret", "password", "cookie", "authorization":
			return true
		}
	}
	return strings.HasSuffix(normalized, "_api_key") || strings.HasSuffix(normalized, "_private_key")
}

type snapshotResponseWriter struct {
	header     http.Header
	body       bytes.Buffer
	statusCode int
}

func (w *snapshotResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *snapshotResponseWriter) Write(p []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	return w.body.Write(p)
}

func (w *snapshotResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}

func writeTarJSON(tw *tar.Writer, name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", name, err)
	}
	data = append(data, '\n')
	return writeTarFile(tw, name, data)
}

func writeTarYAML(tw *tar.Writer, name string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", name, err)
	}
	return writeTarFile(tw, name, data)
}

func writeTarRedactedJSON(tw *tar.Writer, name string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s for redaction: %w", name, err)
	}

	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("decode %s for redaction: %w", name, err)
	}

	return writeTarJSON(tw, name, redactSnapshotJSONValue(payload))
}

func redactSnapshotJSONValue(v any) any {
	switch t := v.(type) {
	case string:
		return redactSnapshotString(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			if isSensitiveAttrKey(k) {
				out[k] = "[REDACTED]"
				continue
			}
			out[k] = redactSnapshotJSONValue(vv)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, vv := range t {
			out = append(out, redactSnapshotJSONValue(vv))
		}
		return out
	default:
		return v
	}
}

func redactSnapshotString(s string) string {
	parsed, err := url.Parse(s)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return redactString(s)
	}

	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = redactURLPathSecrets(parsed.Path)
	parsed.RawPath = ""
	return strings.ReplaceAll(parsed.String(), "%5BREDACTED%5D", "[REDACTED]")
}

func redactURLPathSecrets(path string) string {
	if path == "" {
		return path
	}
	segments := strings.Split(path, "/")
	redactNext := false
	for i, segment := range segments {
		if segment == "" {
			continue
		}
		if redactNext {
			segments[i] = "[REDACTED]"
			redactNext = false
			continue
		}

		redacted := redactString(segment)
		if redacted != segment || isLikelySecretPathSegment(segment) {
			segments[i] = "[REDACTED]"
			continue
		}
		if isSensitivePathKeySegment(segment) {
			redactNext = true
		}
	}
	return strings.Join(segments, "/")
}

func isSensitivePathKeySegment(segment string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "_", ".", "_").Replace(segment))
	switch normalized {
	case "api_key", "apikey", "access_token", "refresh_token", "id_token", "client_secret",
		"code", "password", "secret", "token", "authorization", "cookie", "key":
		return true
	default:
		return false
	}
}

func isLikelySecretPathSegment(segment string) bool {
	normalized := strings.ToLower(segment)
	return strings.Contains(normalized, "secret") ||
		strings.HasPrefix(normalized, "sk-") ||
		strings.Count(segment, ".") >= 2 && len(segment) >= 40
}

func writeTarFile(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    int64(len(data)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("write tar file %s: %w", name, err)
	}
	return nil
}

func parseExportWindow(r *http.Request) time.Duration {
	if r == nil || r.URL == nil {
		return defaultLogExportWindow
	}
	raw := r.URL.Query().Get("minutes")
	if raw == "" {
		return defaultLogExportWindow
	}
	minutes, err := strconv.Atoi(raw)
	if err != nil || minutes <= 0 {
		return defaultLogExportWindow
	}
	window := time.Duration(minutes) * time.Minute
	if window > maxLogExportWindow {
		return maxLogExportWindow
	}
	return window
}
