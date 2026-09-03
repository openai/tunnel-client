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
	"sync"
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
	legacyTmuxSocketName     = "default"
)

var (
	profileNamePattern     = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
	tmuxSessionNamePattern = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._-]*$")
	tmuxPaneIDPattern      = regexp.MustCompile("^%[0-9]+$")
	envNamePattern         = regexp.MustCompile("^[A-Za-z_][A-Za-z0-9_]*$")
)

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
	TmuxSocket     string `json:"tmux_socket,omitempty"`
	PID            int    `json:"pid,omitempty"`
	PIDStartTime   string `json:"pid_start_time,omitempty"`
	PIDExecutable  string `json:"pid_executable,omitempty"`
	LogPath        string `json:"log_path,omitempty"`
	ExitCode       *int   `json:"exit_code,omitempty"`
	LogTail        string `json:"log_tail,omitempty"`
}

// ExistingRuntime describes the locally persisted supervisor state that a new
// connect attempt may reuse or migrate.
type ExistingRuntime struct {
	Mode          string
	SessionName   string
	TmuxSocket    string
	PID           int
	PIDStartTime  string
	PIDExecutable string
}

// ProcessIdentity is the stable OS identity of a managed process. A PID alone
// is not enough because the OS may reuse it after a runtime exits.
type ProcessIdentity struct {
	StartTime  string
	Executable string
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

// abortableProcess is the extra contract required only if stable identity
// capture fails after start. DefaultRuntime returns one; custom starters that
// launch real children should do the same so cleanup never falls back to an
// unverified PID.
type abortableProcess interface {
	Abort() error
}

// Starter is kept argv-shaped for compatibility with callers that provide a
// test or embedding runtime. DefaultRuntime validates that argv is the fixed
// tunnel-client re-exec shape before it starts a process.
type Starter func(args []string, env map[string]string, logPath string) (Process, error)
type ProcessInspector func(pid int) (ProcessIdentity, error)
type ProcessTerminator func(pid int) error
type ProcessExitWaiter func(pid int, identity ProcessIdentity) bool

type Runtime struct {
	Run            Runner
	RunInput       RunnerWithInput
	Start          Starter
	InspectProcess ProcessInspector
	Terminate      ProcessTerminator
	WaitForExit    ProcessExitWaiter
}

func DefaultRuntime() Runtime {
	return Runtime{
		Run:            runTmuxCommand,
		RunInput:       runTmuxCommandWithInput,
		Start:          startProcess,
		InspectProcess: CaptureProcessIdentity,
		Terminate:      TerminateProcess,
		WaitForExit:    WaitForProcessIdentityExit,
	}
}

func (rt Runtime) inspectProcess(pid int) (ProcessIdentity, error) {
	if rt.InspectProcess != nil {
		return rt.InspectProcess(pid)
	}
	return CaptureProcessIdentity(pid)
}

func (rt Runtime) terminateProcess(pid int) error {
	if rt.Terminate != nil {
		return rt.Terminate(pid)
	}
	return TerminateProcess(pid)
}

func (rt Runtime) waitForProcessExit(pid int, identity ProcessIdentity) bool {
	if rt.WaitForExit != nil {
		return rt.WaitForExit(pid, identity)
	}
	return WaitForProcessIdentityExit(pid, identity)
}

// WaitForOwnedProcessExit waits until pid no longer has the recorded identity.
// A reused PID counts as exited because it is no longer safe to signal.
func (rt Runtime) WaitForOwnedProcessExit(pid int, identity ProcessIdentity) bool {
	return rt.waitForProcessExit(pid, identity)
}

func existingProcessIdentity(existing ExistingRuntime) ProcessIdentity {
	return ProcessIdentity{StartTime: existing.PIDStartTime, Executable: existing.PIDExecutable}
}

func (identity ProcessIdentity) complete() bool {
	return strings.TrimSpace(identity.StartTime) != "" && strings.TrimSpace(identity.Executable) != ""
}

// ProcessIdentityMatches reports whether pid still names the exact recorded
// process. Missing identity is an error because treating a bare PID as owned
// can signal an unrelated process after PID reuse.
func (rt Runtime) ProcessIdentityMatches(pid int, expected ProcessIdentity) (bool, error) {
	if pid <= 0 || !PIDIsRunning(pid) {
		return false, nil
	}
	if !expected.complete() {
		return false, fmt.Errorf("recorded process %d has no stable identity; refusing to reuse or signal a bare PID", pid)
	}
	actual, err := rt.inspectProcess(pid)
	if err != nil {
		if !PIDIsRunning(pid) {
			return false, nil
		}
		return false, fmt.Errorf("inspect process %d identity: %w", pid, err)
	}
	return actual.StartTime == expected.StartTime && processExecutableEqual(actual.Executable, expected.Executable), nil
}

// TerminateOwnedProcess signals pid only after its stable identity matches the
// recorded runtime. It returns whether a matching live process was signaled.
func (rt Runtime) TerminateOwnedProcess(pid int, expected ProcessIdentity) (bool, error) {
	matches, err := rt.ProcessIdentityMatches(pid, expected)
	if err != nil || !matches {
		return false, err
	}
	if err := rt.terminateProcess(pid); err != nil {
		return false, err
	}
	return true, nil
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
			"file":   LogPath(normalizedAlias, root),
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

func tunnelClientRunArgs(profileName string, profileDir string) []string {
	return []string{
		"run",
		"--profile-dir",
		profileDir,
		"--profile",
		profileName,
	}
}

// TunnelClientArgs is retained for callers that render or inspect a launch
// command. StartOrReuse never trusts tunnelClientBin for process execution.
func TunnelClientArgs(tunnelClientBin string, profileName string, profileDir string) []string {
	return append([]string{tunnelClientBin}, tunnelClientRunArgs(profileName, profileDir)...)
}

// TunnelClientCommand is retained for callers that render or inspect a launch
// command. StartOrReuse reports the current executable's command instead.
func TunnelClientCommand(tunnelClientBin string, profileName string, profileDir string) string {
	parts := TunnelClientArgs(tunnelClientBin, profileName, profileDir)
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, shellQuote(part))
	}
	return strings.Join(quoted, " ")
}

func currentTunnelClientInvocation(profileName string, profileDir string) ([]string, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve current tunnel-client executable: %w", err)
	}
	return append([]string{executable}, tunnelClientRunArgs(profileName, profileDir)...), nil
}

func currentTunnelClientCommand(profileName string, profileDir string) (string, error) {
	parts, err := currentTunnelClientInvocation(profileName, profileDir)
	if err != nil {
		return "", err
	}
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, shellQuote(part))
	}
	return strings.Join(quoted, " "), nil
}

func validateTunnelClientBin(tunnelClientBin string) error {
	tunnelClientBin = strings.TrimSpace(tunnelClientBin)
	if tunnelClientBin == "" {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current tunnel-client executable: %w", err)
	}
	currentInfo, err := os.Stat(executable)
	if err != nil {
		return fmt.Errorf("stat current tunnel-client executable %q: %w", executable, err)
	}
	requestedInfo, err := os.Stat(tunnelClientBin)
	if err != nil {
		return fmt.Errorf("stat requested tunnel-client executable %q: %w", tunnelClientBin, err)
	}
	if !os.SameFile(currentInfo, requestedInfo) {
		return fmt.Errorf("tunnel-client executable override must resolve to the current executable")
	}
	return nil
}

// StartOrReuse starts a process-supervised runtime for callers that do not have
// a persisted ProcessIdentity yet.
//
// Deprecated: callers that need to reuse or replace a live runtime must use
// StartOrReuseWithExistingRuntime and persist PIDStartTime and PIDExecutable.
// A live PID alone is intentionally rejected because it may have been reused
// by an unrelated process.
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
	if existingPID > 0 && PIDIsRunning(existingPID) {
		return LaunchResult{}, fmt.Errorf("cannot safely reuse live pid %d without a stable process identity; use StartOrReuseWithExistingRuntime", existingPID)
	}
	return StartOrReuseWithExistingRuntime(
		rt,
		alias,
		profileName,
		profileDir,
		tunnelClientBin,
		root,
		envOverrides,
		ExistingRuntime{PID: existingPID},
		replaceExisting,
	)
}

// StartOrReuseWithExistingRuntime starts or reuses a process-supervised runtime
// and migrates a recorded legacy tmux runtime when one exists.
func StartOrReuseWithExistingRuntime(
	rt Runtime,
	alias string,
	profileName string,
	profileDir string,
	tunnelClientBin string,
	root state.Root,
	envOverrides map[string]string,
	existing ExistingRuntime,
	replaceExisting bool,
) (LaunchResult, error) {
	if err := validateTunnelClientBin(tunnelClientBin); err != nil {
		return LaunchResult{}, err
	}
	command, err := currentTunnelClientCommand(profileName, profileDir)
	if err != nil {
		return LaunchResult{}, err
	}
	logPath := LogPath(alias, root)

	if existing.Mode == "tmux" {
		sessionName, err := OwnedTmuxSessionName(alias, root, existing.SessionName)
		if err != nil {
			return LaunchResult{}, err
		}
		tmuxSocket, hasSession, err := FindOwnedLegacyTmuxSession(rt, sessionName, existing.TmuxSocket)
		if err != nil {
			return LaunchResult{}, fmt.Errorf("inspect legacy tmux session: %w", err)
		}
		if hasSession {
			if result, err := StopTmuxOnResolvedSocket(rt, sessionName, tmuxSocket); err != nil {
				return LaunchResult{}, err
			} else if result.ReturnCode != 0 {
				return LaunchResult{}, fmt.Errorf("tmux kill-session failed: %s", strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
			}
		}
	}

	if existing.Mode != "tmux" && existing.PID > 0 && PIDIsRunning(existing.PID) {
		identity := existingProcessIdentity(existing)
		matches, err := rt.ProcessIdentityMatches(existing.PID, identity)
		if err != nil {
			return LaunchResult{}, err
		}
		if matches && replaceExisting {
			signaled, err := rt.TerminateOwnedProcess(existing.PID, identity)
			if err != nil {
				return LaunchResult{}, err
			}
			if signaled && !rt.waitForProcessExit(existing.PID, identity) {
				return LaunchResult{}, fmt.Errorf("process %d did not exit after SIGTERM", existing.PID)
			}
		} else if matches {
			observation := WaitForRuntimeHealthForProcess(rt, alias, root, existing.PID, identity)
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
				PID:            existing.PID,
				PIDStartTime:   identity.StartTime,
				PIDExecutable:  identity.Executable,
				LogPath:        logPath,
				LogTail:        LogTail(logPath, 20),
			}, nil
		}
	}

	ClearHealthURLFile(alias, root)
	args, err := currentTunnelClientInvocation(profileName, profileDir)
	if err != nil {
		return LaunchResult{}, err
	}
	process, err := rt.Start(args, childEnv(envOverrides), logPath)
	if err != nil {
		return LaunchResult{}, err
	}
	if exitCode := exitCodeAfterLaunch(process); exitCode != nil {
		return stoppedLaunchResult(command, logPath, process, ProcessIdentity{}, *exitCode), nil
	}
	identity, err := rt.inspectProcess(process.PID())
	if err != nil {
		// The child may have exited and been reaped between the settle check and
		// identity read. Preserve that ordinary launch-failure result instead of
		// reporting an identity error for a process that is already gone.
		if exitCode := exitCodeAfterLaunch(process); exitCode != nil {
			return stoppedLaunchResult(command, logPath, process, ProcessIdentity{}, *exitCode), nil
		}
		// The default starter retains an exact child handle, so it can abort
		// without signaling a bare PID. Do not persist or expose an unverified
		// PID: that would strand the runtime because later commands correctly
		// refuse to reuse or signal it.
		if cleanupErr := abortUnverifiedProcess(process); cleanupErr != nil {
			return LaunchResult{}, fmt.Errorf("capture launched process identity for pid %d: %w; safely abort launched process: %v", process.PID(), err, cleanupErr)
		}
		return LaunchResult{
			Mode:     "process",
			Command:  command,
			Launched: true,
			Running:  false,
			LogPath:  logPath,
			LogTail:  LogTail(logPath, 20),
		}, fmt.Errorf("capture launched process identity for pid %d; safely aborted launched process before state persistence: %w", process.PID(), err)
	}
	if exitCode := exitCodeAfterLaunch(process); exitCode != nil {
		return stoppedLaunchResult(command, logPath, process, identity, *exitCode), nil
	}
	observation := WaitForRuntimeHealthForProcess(rt, alias, root, process.PID(), identity)
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
		PIDStartTime:   identity.StartTime,
		PIDExecutable:  identity.Executable,
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
	return tmuxHasSessionNameAt(rt, sessionName, "", true)
}

// TmuxHasSessionNameAt checks an exact session on the recorded or recovered
// legacy socket. An empty socket recovers the ambient $TMUX socket when one is
// available, otherwise it selects tmux's default socket explicitly.
func TmuxHasSessionNameAt(rt Runtime, sessionName string, socketPath string) (bool, error) {
	socketPath = resolveLegacyTmuxSocket(socketPath)
	if socketPath != "" {
		if err := validateTmuxSocketPath(socketPath); err != nil {
			return false, err
		}
	}
	return tmuxHasSessionNameAt(rt, sessionName, socketPath, true)
}

// TmuxHasSessionNameStrict reports missing tmux as an error so callers do not
// assume a recorded legacy runtime is absent and launch a duplicate process.
func TmuxHasSessionNameStrict(rt Runtime, sessionName string) (bool, error) {
	return TmuxHasSessionNameStrictAt(rt, sessionName, "")
}

// TmuxHasSessionNameStrictAt inspects a recorded legacy runtime on its
// recorded or recovered tmux socket. An empty socket recovers the ambient
// $TMUX socket when one is available, otherwise it selects the default.
func TmuxHasSessionNameStrictAt(rt Runtime, sessionName string, socketPath string) (bool, error) {
	socketPath = resolveLegacyTmuxSocket(socketPath)
	if socketPath != "" {
		if err := validateTmuxSocketPath(socketPath); err != nil {
			return false, err
		}
	}
	return tmuxHasSessionNameAt(rt, sessionName, socketPath, false)
}

// FindOwnedLegacyTmuxSession inspects only the deterministic tunnel-client
// session name. Older state did not persist a socket, so first recover the
// ambient $TMUX socket and then check the default socket. If neither contains
// the session, fail closed: an older custom socket may still own it.
func FindOwnedLegacyTmuxSession(rt Runtime, sessionName string, recordedSocket string) (string, bool, error) {
	if socket := strings.TrimSpace(recordedSocket); socket != "" {
		running, err := TmuxHasSessionNameStrictAt(rt, sessionName, socket)
		return socket, running, err
	}
	candidates := []string{}
	if ambient := resolveLegacyTmuxSocket(""); ambient != "" {
		candidates = append(candidates, ambient)
	}
	candidates = append(candidates, "")
	for _, socket := range candidates {
		running, err := tmuxHasSessionNameAt(rt, sessionName, socket, false)
		if err != nil {
			return "", false, err
		}
		if running {
			return socket, true, nil
		}
	}
	return "", false, fmt.Errorf("recorded legacy tmux runtime has no socket provenance and no owned session was found on the ambient or default socket")
}

func tmuxHasSessionNameAt(rt Runtime, sessionName string, socketPath string, missingIsAbsent bool) (bool, error) {
	result, err := rt.Run(legacyTmuxArgs(socketPath, "has-session", "-t", "="+sessionName), nil)
	if err != nil {
		if missingIsAbsent && os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if result.ReturnCode == 0 {
		return true, nil
	}
	if missingIsAbsent || tmuxSessionIsKnownAbsent(result) {
		return false, nil
	}
	return false, fmt.Errorf("tmux has-session failed: %s", strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
}

func tmuxSessionIsKnownAbsent(result CompletedProcess) bool {
	if result.ReturnCode != 1 {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
	return strings.Contains(message, "can't find session:") ||
		strings.Contains(message, "no server running on ") ||
		(strings.Contains(message, "error connecting to ") && strings.Contains(message, "no such file or directory"))
}

func resolveLegacyTmuxSocket(recorded string) string {
	if socket := strings.TrimSpace(recorded); socket != "" {
		return socket
	}
	value := strings.TrimSpace(os.Getenv("TMUX"))
	if value == "" {
		return ""
	}
	socket, _, _ := strings.Cut(value, ",")
	if err := validateTmuxSocketPath(socket); err != nil {
		return ""
	}
	return socket
}

// OwnedTmuxSessionName returns the only legacy tmux session name that this
// state root and alias are allowed to inspect or stop.
func OwnedTmuxSessionName(alias string, root state.Root, recorded string) (string, error) {
	expected := TmuxSessionName(alias, root)
	if strings.TrimSpace(recorded) == "" {
		return expected, nil
	}
	if recorded != expected {
		return "", fmt.Errorf("recorded tmux session %q does not match tunnel-client-owned session %q", recorded, expected)
	}
	return expected, nil
}

func StartTmux(rt Runtime, sessionName string, tunnelClientBin string, profileName string, profileDir string, env map[string]string, logPath string) (CompletedProcess, error) {
	if err := validateTunnelClientBin(tunnelClientBin); err != nil {
		return CompletedProcess{}, err
	}
	if err := validateTmuxSessionName(sessionName); err != nil {
		return CompletedProcess{}, err
	}
	if err := ensurePrivateLogFile(logPath); err != nil {
		return CompletedProcess{}, err
	}
	if len(env) > 0 {
		if rt.RunInput == nil {
			return CompletedProcess{}, fmt.Errorf("tmux source-file runner is required when launch environment is set")
		}
		if result, err := rt.Run(legacyTmuxArgs("", "new-session", "-d", "-s", sessionName), nil); err != nil {
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
		script, err := tmuxEnvironmentScript(sessionName, env)
		if err != nil {
			cleanupSession()
			return CompletedProcess{}, err
		}
		result, err := rt.RunInput(legacyTmuxArgs("", "source-file", "-"), nil, script)
		if err != nil {
			cleanupSession()
			return result, err
		}
		if result.ReturnCode != 0 {
			cleanupSession()
			return result, nil
		}
		commandArgs, err := currentTunnelClientInvocation(profileName, profileDir)
		if err != nil {
			cleanupSession()
			return CompletedProcess{}, err
		}
		result, err = rt.Run(append(legacyTmuxArgs("", "respawn-pane", "-k", "-t", paneID), commandArgs...), nil)
		if err != nil {
			cleanupSession()
			return result, err
		}
		if result.ReturnCode != 0 {
			cleanupSession()
		}
		return result, nil
	}
	commandArgs, err := currentTunnelClientInvocation(profileName, profileDir)
	if err != nil {
		return CompletedProcess{}, err
	}
	// tmux directly execs a shell-command supplied as multiple arguments.
	// Keep the invocation split so profile values are never shell syntax.
	args := append(legacyTmuxArgs("", "new-session", "-d", "-s", sessionName), commandArgs...)
	return rt.Run(args, childEnv(env))
}

func tmuxEnvironmentScript(sessionName string, env map[string]string) (string, error) {
	if err := validateTmuxSessionName(sessionName); err != nil {
		return "", err
	}
	lines := make([]string, 0, len(env))
	keys := make([]string, 0, len(env))
	for key := range env {
		if !envNamePattern.MatchString(key) {
			return "", fmt.Errorf("tmux environment name %q must use letters, numbers or underscore", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("set-environment -t =%s %s %s", shellQuote(sessionName), shellQuote(key), shellQuote(env[key])))
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func tmuxFirstPaneID(rt Runtime, sessionName string) (string, error) {
	result, err := rt.Run(legacyTmuxArgs("", "list-panes", "-t", "="+sessionName, "-F", "#{pane_id}"), nil)
	if err != nil {
		return "", err
	}
	if result.ReturnCode != 0 {
		return "", fmt.Errorf("tmux list-panes failed: %s", strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			if !tmuxPaneIDPattern.MatchString(trimmed) {
				return "", fmt.Errorf("tmux list-panes returned invalid pane id %q", trimmed)
			}
			return trimmed, nil
		}
	}
	return "", fmt.Errorf("tmux list-panes returned no pane id for session %s", sessionName)
}

func StopTmux(rt Runtime, sessionName string) (CompletedProcess, error) {
	return stopTmuxOnResolvedSocket(rt, sessionName, "")
}

// StopTmuxAt stops an exact owned session on the recorded or recovered socket.
// An empty socket recovers the ambient $TMUX socket when one is available,
// otherwise it selects tmux's default socket explicitly.
func StopTmuxAt(rt Runtime, sessionName string, socketPath string) (CompletedProcess, error) {
	socketPath = resolveLegacyTmuxSocket(socketPath)
	if socketPath != "" {
		if err := validateTmuxSocketPath(socketPath); err != nil {
			return CompletedProcess{}, err
		}
	}
	return stopTmuxOnResolvedSocket(rt, sessionName, socketPath)
}

// StopTmuxOnResolvedSocket stops an exact session on a selector already
// returned by FindOwnedLegacyTmuxSession. Unlike StopTmuxAt, an empty selector
// means the explicit default socket even when $TMUX is set.
func StopTmuxOnResolvedSocket(rt Runtime, sessionName string, socketPath string) (CompletedProcess, error) {
	if socketPath != "" {
		if err := validateTmuxSocketPath(socketPath); err != nil {
			return CompletedProcess{}, err
		}
	}
	return stopTmuxOnResolvedSocket(rt, sessionName, socketPath)
}

func stopTmuxOnResolvedSocket(rt Runtime, sessionName string, socketPath string) (CompletedProcess, error) {
	return rt.Run(legacyTmuxArgs(socketPath, "kill-session", "-t", "="+sessionName), nil)
}

func legacyTmuxArgs(socketPath string, args ...string) []string {
	if strings.TrimSpace(socketPath) != "" {
		return append([]string{"tmux", "-S", socketPath}, args...)
	}
	return append([]string{"tmux", "-L", legacyTmuxSocketName}, args...)
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

// WaitForRuntimeHealth is the legacy best-effort observation helper. Process
// callers that need ownership checks should use StartOrReuseWithExistingRuntime,
// which passes the persisted identity into the private observation path.
func WaitForRuntimeHealth(rt Runtime, alias string, root state.Root, mode string, pid int, sessionName string) RuntimeObservation {
	return waitForRuntimeHealth(rt, alias, root, mode, pid, sessionName, ProcessIdentity{})
}

// WaitForRuntimeHealthForProcess verifies the process identity on every poll so
// a PID reused during startup is never reported as the managed runtime.
func WaitForRuntimeHealthForProcess(rt Runtime, alias string, root state.Root, pid int, identity ProcessIdentity) RuntimeObservation {
	return waitForRuntimeHealth(rt, alias, root, "process", pid, "", identity)
}

func waitForRuntimeHealth(rt Runtime, alias string, root state.Root, mode string, pid int, sessionName string, identity ProcessIdentity) RuntimeObservation {
	deadline := time.Now().Add(launchHealthTimeout)
	for {
		running := runtimeIsRunning(rt, alias, root, mode, pid, sessionName, identity)
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

func runTmuxCommand(args []string, env map[string]string) (CompletedProcess, error) {
	return runTmuxCommandWithInput(args, env, "")
}

func runTmuxCommandWithInput(args []string, env map[string]string, stdin string) (CompletedProcess, error) {
	tmuxArgs, err := validatedTmuxArgs(args)
	if err != nil {
		return CompletedProcess{}, err
	}
	cmd := exec.Command("tmux", tmuxArgs...)
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
	err = cmd.Run()
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

func validatedTmuxArgs(args []string) ([]string, error) {
	if len(args) == 0 || args[0] != "tmux" {
		return nil, fmt.Errorf("default runtime only supports tmux commands")
	}
	if len(args) < 2 {
		return nil, fmt.Errorf("default runtime only supports managed tmux commands")
	}
	if len(args) >= 4 && args[1] == "-S" {
		if err := validateTmuxSocketPath(args[2]); err != nil {
			return nil, err
		}
		switch args[3] {
		case "has-session":
			if len(args) == 6 && args[4] == "-t" {
				if err := validateTmuxTarget(args[5]); err != nil {
					return nil, err
				}
				return []string{"-S", args[2], "has-session", "-t", args[5]}, nil
			}
		case "kill-session":
			if len(args) == 6 && args[4] == "-t" {
				if err := validateTmuxTarget(args[5]); err != nil {
					return nil, err
				}
				return []string{"-S", args[2], "kill-session", "-t", args[5]}, nil
			}
		}
		return nil, fmt.Errorf("default runtime only supports managed tmux commands")
	}
	if len(args) >= 4 && args[1] == "-L" {
		if args[2] != legacyTmuxSocketName {
			return nil, fmt.Errorf("default runtime only supports the legacy tmux socket")
		}
		switch args[3] {
		case "has-session":
			if len(args) == 6 && args[4] == "-t" {
				if err := validateTmuxTarget(args[5]); err != nil {
					return nil, err
				}
				return []string{"-L", legacyTmuxSocketName, "has-session", "-t", args[5]}, nil
			}
		case "kill-session":
			if len(args) == 6 && args[4] == "-t" {
				if err := validateTmuxTarget(args[5]); err != nil {
					return nil, err
				}
				return []string{"-L", legacyTmuxSocketName, "kill-session", "-t", args[5]}, nil
			}
		case "new-session":
			if len(args) >= 7 && args[4] == "-d" && args[5] == "-s" {
				if err := validateTmuxSessionName(args[6]); err != nil {
					return nil, err
				}
				if len(args) == 7 {
					return []string{"-L", legacyTmuxSocketName, "new-session", "-d", "-s", args[6]}, nil
				}
				if len(args) == 13 {
					profileName, profileDir, err := fixedTunnelClientRunArgs(args[7:])
					if err != nil {
						return nil, err
					}
					invocation, err := currentTunnelClientInvocation(profileName, profileDir)
					if err != nil {
						return nil, err
					}
					return append([]string{"-L", legacyTmuxSocketName, "new-session", "-d", "-s", args[6]}, invocation...), nil
				}
			}
		case "list-panes":
			if len(args) == 8 && args[4] == "-t" && args[6] == "-F" && args[7] == "#{pane_id}" {
				if err := validateTmuxTarget(args[5]); err != nil {
					return nil, err
				}
				return []string{"-L", legacyTmuxSocketName, "list-panes", "-t", args[5], "-F", "#{pane_id}"}, nil
			}
		case "source-file":
			if len(args) == 5 && args[4] == "-" {
				return []string{"-L", legacyTmuxSocketName, "source-file", "-"}, nil
			}
		case "respawn-pane":
			if len(args) == 13 && args[4] == "-k" && args[5] == "-t" && tmuxPaneIDPattern.MatchString(args[6]) {
				profileName, profileDir, err := fixedTunnelClientRunArgs(args[7:])
				if err != nil {
					return nil, err
				}
				invocation, err := currentTunnelClientInvocation(profileName, profileDir)
				if err != nil {
					return nil, err
				}
				return append([]string{"-L", legacyTmuxSocketName, "respawn-pane", "-k", "-t", args[6]}, invocation...), nil
			}
		}
		return nil, fmt.Errorf("default runtime only supports managed tmux commands")
	}
	switch args[1] {
	case "-V":
		if len(args) == 2 {
			return []string{"-V"}, nil
		}
	case "has-session":
		if len(args) == 4 && args[2] == "-t" {
			if err := validateTmuxTarget(args[3]); err != nil {
				return nil, err
			}
			return []string{"has-session", "-t", args[3]}, nil
		}
	case "new-session":
		if len(args) >= 5 && args[2] == "-d" && args[3] == "-s" {
			if err := validateTmuxSessionName(args[4]); err != nil {
				return nil, err
			}
			if len(args) == 5 {
				return []string{"new-session", "-d", "-s", args[4]}, nil
			}
			if len(args) == 11 {
				profileName, profileDir, err := fixedTunnelClientRunArgs(args[5:])
				if err != nil {
					return nil, err
				}
				invocation, err := currentTunnelClientInvocation(profileName, profileDir)
				if err != nil {
					return nil, err
				}
				return append([]string{"new-session", "-d", "-s", args[4]}, invocation...), nil
			}
		}
	case "list-panes":
		if len(args) == 6 && args[2] == "-t" && args[4] == "-F" && args[5] == "#{pane_id}" {
			if err := validateTmuxTarget(args[3]); err != nil {
				return nil, err
			}
			return []string{"list-panes", "-t", args[3], "-F", "#{pane_id}"}, nil
		}
	case "kill-session":
		if len(args) == 4 && args[2] == "-t" {
			if err := validateTmuxTarget(args[3]); err != nil {
				return nil, err
			}
			return []string{"kill-session", "-t", args[3]}, nil
		}
	case "source-file":
		if len(args) == 3 && args[2] == "-" {
			return []string{"source-file", "-"}, nil
		}
	case "respawn-pane":
		if len(args) == 11 && args[2] == "-k" && args[3] == "-t" && tmuxPaneIDPattern.MatchString(args[4]) {
			profileName, profileDir, err := fixedTunnelClientRunArgs(args[5:])
			if err != nil {
				return nil, err
			}
			invocation, err := currentTunnelClientInvocation(profileName, profileDir)
			if err != nil {
				return nil, err
			}
			return append([]string{"respawn-pane", "-k", "-t", args[4]}, invocation...), nil
		}
	}
	return nil, fmt.Errorf("default runtime only supports managed tmux commands")
}

func validateTmuxTarget(target string) error {
	if !strings.HasPrefix(target, "=") {
		return fmt.Errorf("tmux target must use an exact session name")
	}
	return validateTmuxSessionName(strings.TrimPrefix(target, "="))
}

func validateTmuxSessionName(sessionName string) error {
	if !tmuxSessionNamePattern.MatchString(sessionName) {
		return fmt.Errorf("tmux session name must use letters, numbers, '.', '_' or '-'")
	}
	return nil
}

func validateTmuxSocketPath(socketPath string) error {
	if !filepath.IsAbs(socketPath) || strings.ContainsAny(socketPath, "\x00\r\n") {
		return fmt.Errorf("tmux socket path must be absolute")
	}
	return nil
}

type osProcess struct {
	cmd      *exec.Cmd
	done     chan struct{}
	mu       sync.Mutex
	exitCode *int
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
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.exitCode == nil {
		return nil
	}
	code := *p.exitCode
	return &code
}

// Abort terminates the exact os.Process returned by exec.Cmd.Start and waits
// for its reaper. os.Process tracks its released state, so this never turns a
// completed child into a bare-PID signal after PID reuse.
func (p *osProcess) Abort() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return fmt.Errorf("launched process handle is unavailable")
	}
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	<-p.done
	return nil
}

func (p *osProcess) finish(exitCode int) {
	p.mu.Lock()
	p.exitCode = &exitCode
	p.mu.Unlock()
	close(p.done)
}

func startProcess(args []string, env map[string]string, logPath string) (Process, error) {
	profileName, profileDir, err := fixedTunnelClientRunArgs(args)
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve current tunnel-client executable: %w", err)
	}
	if err := ensurePrivateLogFile(logPath); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file %s: %w", logPath, err)
	}
	cmd := exec.Command(executable, "run", "--profile-dir", profileDir, "--profile", profileName)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.Env = envList(env)
	configureDetachedProcess(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	process := &osProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		_ = logFile.Close()
		if err == nil {
			process.finish(0)
			return
		}
		var exitErr *exec.ExitError
		if ok := AsExitError(err, &exitErr); ok {
			process.finish(exitErr.ExitCode())
			return
		}
		process.finish(1)
	}()
	return process, nil
}

func abortUnverifiedProcess(process Process) error {
	abortable, ok := process.(abortableProcess)
	if !ok {
		return fmt.Errorf("starter did not return a safely abortable process handle")
	}
	return abortable.Abort()
}

func fixedTunnelClientRunArgs(args []string) (string, string, error) {
	if len(args) != 6 ||
		args[1] != "run" ||
		args[2] != "--profile-dir" ||
		args[4] != "--profile" {
		return "", "", fmt.Errorf("default runtime only supports the fixed tunnel-client run command")
	}
	if err := validateTunnelClientBin(args[0]); err != nil {
		return "", "", err
	}
	return args[5], args[3], nil
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

func stoppedLaunchResult(command string, logPath string, process Process, identity ProcessIdentity, exitCode int) LaunchResult {
	result := LaunchResult{
		Mode:           "process",
		Command:        command,
		Launched:       true,
		Started:        false,
		Running:        false,
		Healthy:        false,
		Ready:          false,
		AlreadyRunning: false,
		LogPath:        logPath,
		ExitCode:       &exitCode,
		LogTail:        LogTail(logPath, 20),
	}
	// An exited process does not need later signaling. Preserve its PID only
	// when we also captured the stable identity that makes that record safe.
	if identity.complete() {
		result.PID = process.PID()
		result.PIDStartTime = identity.StartTime
		result.PIDExecutable = identity.Executable
	}
	return result
}

func runtimeIsRunning(rt Runtime, alias string, root state.Root, mode string, pid int, sessionName string, identity ProcessIdentity) bool {
	switch mode {
	case "tmux":
		name := sessionName
		if name == "" {
			name = TmuxSessionName(alias, root)
		}
		ok, err := TmuxHasSessionName(rt, name)
		return err == nil && ok
	case "process":
		if identity.complete() {
			matches, err := rt.ProcessIdentityMatches(pid, identity)
			return err == nil && matches
		}
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
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
