package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openai/tunnel-client/pkg/codexplugin/state"
	"github.com/openai/tunnel-client/pkg/healthurl"
)

const (
	launchSettleDuration     = 50 * time.Millisecond
	launchHealthTimeout      = 2 * time.Second
	launchHealthPollInterval = 50 * time.Millisecond
	healthProbeTimeout       = 500 * time.Millisecond
	terminateWaitDuration    = 1 * time.Second
)

var profileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Target struct {
	Kind  string
	Value string
}

type LaunchResult struct {
	Mode           string `json:"mode"`
	Command        string `json:"command"`
	Launched       bool   `json:"launched"`
	Started        bool   `json:"started"`
	Running        bool   `json:"running"`
	Healthy        bool   `json:"healthy"`
	Ready          bool   `json:"ready"`
	AlreadyRunning bool   `json:"already_running"`
	HealthURL      string `json:"health_url,omitempty"`
	SessionName    string `json:"session_name,omitempty"`
	PID            int    `json:"pid,omitempty"`
	LogPath        string `json:"log_path,omitempty"`
	ExitCode       *int   `json:"exit_code,omitempty"`
	LogTail        string `json:"log_tail,omitempty"`
}

type EndpointProbe struct {
	URL    string `json:"url,omitempty"`
	OK     bool   `json:"ok"`
	Status int    `json:"status,omitempty"`
	Body   string `json:"body,omitempty"`
	Error  string `json:"error,omitempty"`
}

type HealthProbe struct {
	BaseURL string        `json:"base_url,omitempty"`
	Healthz EndpointProbe `json:"healthz"`
	Readyz  EndpointProbe `json:"readyz"`
}

type RuntimeObservation struct {
	Running     bool        `json:"running"`
	HealthURL   string      `json:"health_url,omitempty"`
	Healthy     bool        `json:"healthy"`
	Ready       bool        `json:"ready"`
	HealthProbe HealthProbe `json:"health_probe"`
}

type CompletedProcess struct {
	ReturnCode int
	Stdout     string
	Stderr     string
}

type Runner func(args []string, env map[string]string) (CompletedProcess, error)
type RunnerWithInput func(args []string, env map[string]string, stdin string) (CompletedProcess, error)

type Process interface {
	PID() int
	Poll() *int
}

type Starter func(args []string, env map[string]string, logPath string) (Process, error)

type Runtime struct {
	Run      Runner
	RunInput RunnerWithInput
	Start    Starter
}

func DefaultRuntime() Runtime {
	return Runtime{
		Run:      runCommand,
		RunInput: runCommandWithInput,
		Start:    startProcess,
	}
}

func DefaultProfileDir(lookupEnv func(string) (string, bool)) (string, error) {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	if override, ok := lookupEnv("TUNNEL_CLIENT_PROFILE_DIR"); ok && strings.TrimSpace(override) != "" {
		return filepath.Clean(strings.TrimSpace(override)), nil
	}
	if xdg, ok := lookupEnv("XDG_CONFIG_HOME"); ok && strings.TrimSpace(xdg) != "" {
		return filepath.Join(filepath.Clean(strings.TrimSpace(xdg)), "tunnel-client"), nil
	}
	if home, ok := lookupEnv("HOME"); ok && strings.TrimSpace(home) != "" {
		return filepath.Join(filepath.Clean(strings.TrimSpace(home)), ".config", "tunnel-client"), nil
	}
	configHome, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve default profile directory: %w", err)
	}
	return filepath.Join(configHome, "tunnel-client"), nil
}

func ResolveProfileDir(profileDir string, lookupEnv func(string) (string, bool)) (string, error) {
	if trimmed := strings.TrimSpace(profileDir); trimmed != "" {
		return filepath.Clean(trimmed), nil
	}
	return DefaultProfileDir(lookupEnv)
}

func NormalizeProfileName(profileName string, alias string) (string, error) {
	name := strings.TrimSpace(profileName)
	if name == "" {
		var err error
		name, err = state.NormalizeAlias(alias)
		if err != nil {
			return "", err
		}
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", state.NewError("profile name must not contain path separators")
	}
	if !profileNamePattern.MatchString(name) {
		return "", state.NewError("profile name must use letters, numbers, '.', '_' or '-'")
	}
	return name, nil
}

func WriteRuntimeProfile(
	alias string,
	profileName string,
	tunnelID string,
	baseURL string,
	urlPath string,
	apiKey string,
	target Target,
	profileDir string,
	root state.Root,
	lookupEnv func(string) (string, bool),
) (string, error) {
	normalizedAlias, err := state.NormalizeAlias(alias)
	if err != nil {
		return "", err
	}
	normalizedProfile, err := NormalizeProfileName(profileName, normalizedAlias)
	if err != nil {
		return "", err
	}
	if err := state.RejectInlineSecretMaterial(target.Value, "mcp "+target.Kind); err != nil {
		return "", err
	}
	if err := state.EnsureDirs(root); err != nil {
		return "", err
	}
	configRoot, err := ResolveProfileDir(profileDir, lookupEnv)
	if err != nil {
		return "", err
	}
	configPath := filepath.Join(configRoot, normalizedProfile+".yaml")
	healthURLFile := ProfileHealthURLFile(normalizedAlias, root)
	payload := map[string]any{
		"config_version": 1,
		"control_plane": map[string]any{
			"base_url":  baseURL,
			"tunnel_id": tunnelID,
			"api_key":   apiKey,
		},
		"health": map[string]any{
			"listen_addr": "127.0.0.1:0",
			"url_file":    healthURLFile,
		},
		"admin_ui": map[string]any{
			"open_browser": false,
		},
		"log": map[string]any{
			"level":  "info",
			"format": "json",
		},
		"mcp": mcpConfig(target),
	}
	if strings.TrimSpace(urlPath) != "" {
		payload["control_plane"].(map[string]any)["url_path"] = strings.TrimSpace(urlPath)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return "", fmt.Errorf("create profile directory %s: %w", filepath.Dir(configPath), err)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal profile %s: %w", configPath, err)
	}
	if err := os.WriteFile(configPath, append(data, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("write profile %s: %w", configPath, err)
	}
	return configPath, nil
}

func ProfileHealthURLFile(alias string, root state.Root) string {
	return filepath.Join(root.Path, "health", mustNormalizeAlias(alias)+".url")
}

func TmuxSessionName(alias string, root state.Root) string {
	sum := sha256.Sum256([]byte(root.Path))
	return fmt.Sprintf("tunnel-mcp__%s__%x", mustNormalizeAlias(alias), sum[:4])
}

func TunnelClientArgs(tunnelClientBin string, profileName string, profileDir string) []string {
	return []string{
		tunnelClientBin,
		"run",
		"--profile-dir",
		profileDir,
		"--profile",
		profileName,
	}
}

func TunnelClientCommand(tunnelClientBin string, profileName string, profileDir string) string {
	parts := TunnelClientArgs(tunnelClientBin, profileName, profileDir)
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, shellQuote(part))
	}
	return strings.Join(quoted, " ")
}

func StartOrReuse(
	rt Runtime,
	alias string,
	profileName string,
	profileDir string,
	tunnelClientBin string,
	root state.Root,
	envOverrides map[string]string,
	existingPID int,
	replaceExisting bool,
) (LaunchResult, error) {
	sessionName := TmuxSessionName(alias, root)
	command := TunnelClientCommand(tunnelClientBin, profileName, profileDir)
	logPath := LogPath(alias, root)

	if available, _ := TmuxAvailable(rt); available {
		hasSession, _ := TmuxHasSessionName(rt, sessionName)
		if hasSession {
			if replaceExisting {
				if result, err := StopTmux(rt, sessionName); err != nil {
					return LaunchResult{}, err
				} else if result.ReturnCode != 0 {
					return LaunchResult{}, fmt.Errorf("tmux kill-session failed: %s", strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
				}
			} else {
				observation := WaitForRuntimeHealth(rt, alias, root, "tmux", existingPID, sessionName)
				return LaunchResult{
					Mode:           "tmux",
					Command:        command,
					Launched:       false,
					Started:        observation.Healthy,
					Running:        observation.Running,
					Healthy:        observation.Healthy,
					Ready:          observation.Ready,
					AlreadyRunning: true,
					HealthURL:      observation.HealthURL,
					SessionName:    sessionName,
					LogPath:        logPath,
					LogTail:        LogTail(logPath, 20),
				}, nil
			}
		}
		ClearHealthURLFile(alias, root)
		if result, err := StartTmux(rt, sessionName, tunnelClientBin, profileName, profileDir, envOverrides, logPath); err != nil {
			return LaunchResult{}, err
		} else if result.ReturnCode != 0 {
			return LaunchResult{}, fmt.Errorf("tmux launch failed: %s", strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
		}
		observation := WaitForRuntimeHealth(rt, alias, root, "tmux", 0, sessionName)
		return LaunchResult{
			Mode:           "tmux",
			Command:        command,
			Launched:       true,
			Started:        observation.Healthy,
			Running:        observation.Running,
			Healthy:        observation.Healthy,
			Ready:          observation.Ready,
			AlreadyRunning: false,
			HealthURL:      observation.HealthURL,
			SessionName:    sessionName,
			LogPath:        logPath,
			LogTail:        LogTail(logPath, 20),
		}, nil
	}

	if existingPID > 0 && PIDIsRunning(existingPID) {
		if replaceExisting {
			if err := TerminateProcess(existingPID); err != nil {
				return LaunchResult{}, err
			}
		} else {
			observation := WaitForRuntimeHealth(rt, alias, root, "process", existingPID, "")
			return LaunchResult{
				Mode:           "process",
				Command:        command,
				Launched:       false,
				Started:        observation.Healthy,
				Running:        observation.Running,
				Healthy:        observation.Healthy,
				Ready:          observation.Ready,
				AlreadyRunning: true,
				HealthURL:      observation.HealthURL,
				PID:            existingPID,
				LogPath:        logPath,
				LogTail:        LogTail(logPath, 20),
			}, nil
		}
	}

	ClearHealthURLFile(alias, root)
	process, err := rt.Start(TunnelClientArgs(tunnelClientBin, profileName, profileDir), childEnv(envOverrides), logPath)
	if err != nil {
		return LaunchResult{}, err
	}
	var exitCodePtr *int
	if exitCode := exitCodeAfterLaunch(process); exitCode != nil {
		exitCodePtr = exitCode
		return LaunchResult{
			Mode:           "process",
			Command:        command,
			Launched:       true,
			Started:        false,
			Running:        false,
			Healthy:        false,
			Ready:          false,
			AlreadyRunning: false,
			PID:            process.PID(),
			LogPath:        logPath,
			ExitCode:       exitCodePtr,
			LogTail:        LogTail(logPath, 20),
		}, nil
	}
	observation := WaitForRuntimeHealth(rt, alias, root, "process", process.PID(), "")
	return LaunchResult{
		Mode:           "process",
		Command:        command,
		Launched:       true,
		Started:        observation.Healthy,
		Running:        observation.Running,
		Healthy:        observation.Healthy,
		Ready:          observation.Ready,
		AlreadyRunning: false,
		HealthURL:      observation.HealthURL,
		PID:            process.PID(),
		LogPath:        logPath,
		LogTail:        LogTail(logPath, 20),
	}, nil
}

func TmuxAvailable(rt Runtime) (bool, error) {
	result, err := rt.Run([]string{"tmux", "-V"}, nil)
	if err != nil {
		var execErr *exec.Error
		if ok := AsExecError(err, &execErr); ok {
			return false, nil
		}
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return result.ReturnCode == 0, nil
}

func TmuxHasSessionName(rt Runtime, sessionName string) (bool, error) {
	result, err := rt.Run([]string{"tmux", "has-session", "-t", "=" + sessionName}, nil)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return result.ReturnCode == 0, nil
}

func StartTmux(rt Runtime, sessionName string, tunnelClientBin string, profileName string, profileDir string, env map[string]string, logPath string) (CompletedProcess, error) {
	if err := ensurePrivateLogFile(logPath); err != nil {
		return CompletedProcess{}, err
	}
	if len(env) > 0 && rt.RunInput != nil {
		if result, err := rt.Run([]string{"tmux", "new-session", "-d", "-s", sessionName}, nil); err != nil {
			return result, err
		} else if result.ReturnCode != 0 {
			return result, nil
		}
		cleanupSession := func() {
			_, _ = StopTmux(rt, sessionName)
		}
		paneID, err := tmuxFirstPaneID(rt, sessionName)
		if err != nil {
			cleanupSession()
			return CompletedProcess{}, err
		}
		result, err := rt.RunInput([]string{"tmux", "source-file", "-"}, nil, tmuxLaunchScript(sessionName, paneID, tunnelClientBin, profileName, profileDir, env, logPath))
		if err != nil {
			cleanupSession()
			return result, err
		}
		if result.ReturnCode != 0 {
			cleanupSession()
		}
		return result, nil
	}
	args := []string{"tmux", "new-session", "-d"}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "-e", key+"="+env[key])
	}
	args = append(args, "-s", sessionName, logShellCommand(TunnelClientCommand(tunnelClientBin, profileName, profileDir), logPath))
	return rt.Run(args, childEnv(env))
}

func tmuxLaunchScript(sessionName string, paneID string, tunnelClientBin string, profileName string, profileDir string, env map[string]string, logPath string) string {
	lines := make([]string, 0, len(env)+1)
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("set-environment -t =%s %s %s", shellQuote(sessionName), shellQuote(key), shellQuote(env[key])))
	}
	lines = append(lines, fmt.Sprintf("respawn-pane -k -t %s %s", shellQuote(paneID), shellQuote(logShellCommand(TunnelClientCommand(tunnelClientBin, profileName, profileDir), logPath))))
	return strings.Join(lines, "\n") + "\n"
}

func tmuxFirstPaneID(rt Runtime, sessionName string) (string, error) {
	result, err := rt.Run([]string{"tmux", "list-panes", "-t", "=" + sessionName, "-F", "#{pane_id}"}, nil)
	if err != nil {
		return "", err
	}
	if result.ReturnCode != 0 {
		return "", fmt.Errorf("tmux list-panes failed: %s", strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed, nil
		}
	}
	return "", fmt.Errorf("tmux list-panes returned no pane id for session %s", sessionName)
}

func StopTmux(rt Runtime, sessionName string) (CompletedProcess, error) {
	return rt.Run([]string{"tmux", "kill-session", "-t", "=" + sessionName}, nil)
}

func LogPath(alias string, root state.Root) string {
	return filepath.Join(root.Path, "logs", mustNormalizeAlias(alias)+".log")
}

func LogTail(pathValue string, maxLines int) string {
	if strings.TrimSpace(pathValue) == "" || maxLines <= 0 {
		return ""
	}
	data, err := os.ReadFile(pathValue)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

func ensurePrivateLogFile(pathValue string) error {
	if strings.TrimSpace(pathValue) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(pathValue), 0o755); err != nil {
		return fmt.Errorf("create log directory %s: %w", filepath.Dir(pathValue), err)
	}
	if info, err := os.Lstat(pathValue); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("log file %s must not be a symlink", pathValue)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("log file %s must be a regular file", pathValue)
		}
		if err := os.Chmod(pathValue, 0o600); err != nil {
			return fmt.Errorf("secure log file %s: %w", pathValue, err)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat log file %s: %w", pathValue, err)
	}
	logFile, err := os.OpenFile(pathValue, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create log file %s: %w", pathValue, err)
	}
	if err := logFile.Close(); err != nil {
		return fmt.Errorf("close log file %s: %w", pathValue, err)
	}
	return nil
}

func logShellCommand(command string, logPath string) string {
	if strings.TrimSpace(logPath) == "" {
		return command
	}
	return command + " >> " + shellQuote(logPath) + " 2>&1"
}

func WaitForRuntimeHealth(rt Runtime, alias string, root state.Root, mode string, pid int, sessionName string) RuntimeObservation {
	deadline := time.Now().Add(launchHealthTimeout)
	for {
		running := runtimeIsRunning(rt, alias, root, mode, pid, sessionName)
		rawHealthURL := ReadHealthURL(ProfileHealthURLFile(alias, root))
		probe := ProbeHealthEndpoints(rawHealthURL)
		observation := RuntimeObservation{
			Running:     running,
			HealthURL:   probe.Healthz.URL,
			Healthy:     probe.Healthz.OK,
			Ready:       probe.Readyz.OK,
			HealthProbe: probe,
		}
		if observation.Healthy || !running || time.Now().After(deadline) {
			return observation
		}
		time.Sleep(launchHealthPollInterval)
	}
}

func ReadHealthURL(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func ProbeHealthEndpoints(rawHealthURL string) HealthProbe {
	target, err := healthurl.Parse(rawHealthURL)
	if err != nil {
		return HealthProbe{}
	}
	return HealthProbe{
		BaseURL: target.BaseURL,
		Healthz: probeEndpoint(target, "/healthz"),
		Readyz:  probeEndpoint(target, "/readyz"),
	}
}

func NormalizeHealthBaseURL(rawHealthURL string) string {
	return healthurl.NormalizeBaseURL(rawHealthURL)
}

func ClearHealthURLFile(alias string, root state.Root) {
	_ = os.Remove(ProfileHealthURLFile(alias, root))
}

func WaitForProcessExit(pid int) bool {
	deadline := time.Now().Add(terminateWaitDuration)
	for time.Now().Before(deadline) {
		if !PIDIsRunning(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return !PIDIsRunning(pid)
}

func runCommand(args []string, env map[string]string) (CompletedProcess, error) {
	return runCommandWithInput(args, env, "")
}

func runCommandWithInput(args []string, env map[string]string, stdin string) (CompletedProcess, error) {
	cmd := exec.Command(args[0], args[1:]...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if env != nil {
		cmd.Env = envList(env)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := CompletedProcess{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if ok := AsExitError(err, &exitErr); ok {
		result.ReturnCode = exitErr.ExitCode()
		return result, nil
	}
	return result, err
}

type osProcess struct {
	cmd    *exec.Cmd
	waitCh chan int
}

func (p *osProcess) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *osProcess) Poll() *int {
	if p == nil {
		return nil
	}
	select {
	case code := <-p.waitCh:
		return &code
	default:
		return nil
	}
}

func startProcess(args []string, env map[string]string, logPath string) (Process, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("create log directory %s: %w", filepath.Dir(logPath), err)
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file %s: %w", logPath, err)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.Env = envList(env)
	configureDetachedProcess(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	waitCh := make(chan int, 1)
	go func() {
		err := cmd.Wait()
		_ = logFile.Close()
		if err == nil {
			waitCh <- 0
			return
		}
		var exitErr *exec.ExitError
		if ok := AsExitError(err, &exitErr); ok {
			waitCh <- exitErr.ExitCode()
			return
		}
		waitCh <- 1
	}()
	return &osProcess{cmd: cmd, waitCh: waitCh}, nil
}

func exitCodeAfterLaunch(process Process) *int {
	deadline := time.Now().Add(launchSettleDuration)
	for time.Now().Before(deadline) {
		if exitCode := process.Poll(); exitCode != nil {
			return exitCode
		}
		time.Sleep(10 * time.Millisecond)
	}
	return process.Poll()
}

func runtimeIsRunning(rt Runtime, alias string, root state.Root, mode string, pid int, sessionName string) bool {
	switch mode {
	case "tmux":
		name := sessionName
		if name == "" {
			name = TmuxSessionName(alias, root)
		}
		ok, err := TmuxHasSessionName(rt, name)
		return err == nil && ok
	case "process":
		return PIDIsRunning(pid)
	default:
		return false
	}
}

func probeEndpoint(target healthurl.Target, path string) EndpointProbe {
	ctx, cancel := context.WithTimeout(context.Background(), healthProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.RequestURL(path), nil)
	if err != nil {
		return EndpointProbe{URL: target.URL(path), Error: err.Error()}
	}
	client, err := target.HTTPClient(healthProbeTimeout)
	if err != nil {
		return EndpointProbe{URL: target.URL(path), Error: err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return EndpointProbe{URL: target.URL(path), Error: err.Error()}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err := resp.Body.Close(); err != nil {
		return EndpointProbe{URL: target.URL(path), Error: err.Error()}
	}
	return EndpointProbe{
		URL:    target.URL(path),
		OK:     resp.StatusCode >= 200 && resp.StatusCode < 300,
		Status: resp.StatusCode,
		Body:   strings.TrimSpace(string(body)),
	}
}

func mcpConfig(target Target) map[string]any {
	switch target.Kind {
	case "server_url":
		return map[string]any{
			"server_urls": []map[string]string{
				{
					"channel": "main",
					"url":     target.Value,
				},
			},
		}
	case "command":
		return map[string]any{
			"commands": []map[string]string{
				{
					"channel": "main",
					"command": target.Value,
				},
			},
		}
	default:
		return map[string]any{}
	}
}

func childEnv(overrides map[string]string) map[string]string {
	env := map[string]string{}
	for _, entry := range os.Environ() {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			env[parts[0]] = parts[1]
		}
	}
	for key, value := range overrides {
		env[key] = value
	}
	return env
}

func envList(overrides map[string]string) []string {
	env := childEnv(overrides)
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+env[key])
	}
	return result
}

func mustNormalizeAlias(alias string) string {
	value, err := state.NormalizeAlias(alias)
	if err != nil {
		return alias
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func AsExitError(err error, target **exec.ExitError) bool {
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		*target = exitErr
		return true
	}
	return false
}

func AsExecError(err error, target **exec.Error) bool {
	if err == nil {
		return false
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		*target = execErr
		return true
	}
	return false
}

func ParsePortFromHealthURL(raw string) int {
	hostPort := strings.TrimPrefix(NormalizeHealthBaseURL(raw), "http://")
	_, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return 0
	}
	value, _ := strconv.Atoi(port)
	return value
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"\\$`") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
