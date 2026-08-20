package codexplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openai/tunnel-client/pkg/codexplugin/session"
	pluginstate "github.com/openai/tunnel-client/pkg/codexplugin/state"
	"github.com/openai/tunnel-client/pkg/config"
	adminapi "github.com/openai/tunnel-client/pkg/controlplane/admin"
	"github.com/openai/tunnel-client/pkg/healthurl"
)

const (
	defaultAdminProfileName    = "default"
	defaultAdminKeyRef         = "env:OPENAI_ADMIN_KEY"
	defaultRuntimeAPIKeyRef    = "env:CONTROL_PLANE_API_KEY"
	defaultControlPlaneBaseURL = "https://api.openai.com"
)

type Manager struct {
	lookupEnv func(string) (string, bool)
	runtime   session.Runtime
}

type PayloadError struct {
	Code    int
	Payload map[string]any
}

func (e *PayloadError) Error() string {
	return "payload error"
}

type AdminProfileResult struct {
	Name                string `json:"name"`
	ControlPlaneBaseURL string `json:"control_plane_base_url"`
	ControlPlaneURLPath string `json:"control_plane_url_path,omitempty"`
	AdminKey            string `json:"admin_key"`
	UpdatedAt           string `json:"updated_at,omitempty"`
	Path                string `json:"path"`
	Active              bool   `json:"active"`
}

type CreateOptions struct {
	Alias               string
	Name                string
	Description         string
	AdminProfileName    string
	AdminKeyRef         string
	ControlPlaneBaseURL string
	ControlPlaneURLPath string
	OrganizationIDs     []string
	WorkspaceIDs        []string
}

type ConnectOptions struct {
	CreateOptions
	TunnelID      string
	ProfileName   string
	ProfileDir    string
	MCPServerURL  string
	MCPCommand    string
	RuntimeAPIKey string
	TunnelBin     string
}

type ListOptions struct {
	AdminProfileName    string
	AdminKeyRef         string
	ControlPlaneBaseURL string
	ControlPlaneURLPath string
	OrganizationIDs     []string
	WorkspaceIDs        []string
	TenantID            string
}

type AliasOptions struct {
	Alias               string
	AdminProfileName    string
	AdminKeyRef         string
	ControlPlaneBaseURL string
	ControlPlaneURLPath string
}

type CleanupOptions struct {
	Apply bool
}

type RepairAction struct {
	ID      string `json:"id"`
	Command string `json:"command"`
	Reason  string `json:"reason"`
}

type effectiveAdminProfile struct {
	Name                string
	ControlPlaneBaseURL string
	ControlPlaneURLPath string
	AdminKey            string
	Path                string
}

func NewManager(lookupEnv func(string) (string, bool), runtime session.Runtime) *Manager {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	if runtime.Run == nil || runtime.Start == nil {
		runtime = session.DefaultRuntime()
	}
	return &Manager{lookupEnv: lookupEnv, runtime: runtime}
}

func (m *Manager) ListAdminProfiles() (map[string]any, error) {
	root := pluginstate.ResolveRoot(m.lookupEnv)
	file, err := pluginstate.LoadAdminProfiles(root)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(file.Profiles))
	for name := range file.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]AdminProfileResult, 0, len(names))
	for _, name := range names {
		profile := file.Profiles[name]
		entries = append(entries, AdminProfileResult{
			Name:                profile.Name,
			ControlPlaneBaseURL: profile.ControlPlaneBaseURL,
			ControlPlaneURLPath: profile.ControlPlaneURLPath,
			AdminKey:            profile.AdminKey,
			UpdatedAt:           profile.UpdatedAt,
			Path:                pluginstate.AdminProfilesPath(root),
			Active:              file.ActiveProfile == name,
		})
	}
	return map[string]any{
		"profiles":       entries,
		"active_profile": file.ActiveProfile,
		"path":           pluginstate.AdminProfilesPath(root),
		"state_root":     root.Path,
	}, nil
}

func (m *Manager) SetAdminProfile(name, baseURL, urlPath, adminKey string, activate bool) (map[string]any, error) {
	root := pluginstate.ResolveRoot(m.lookupEnv)
	if err := pluginstate.EnsureDirs(root); err != nil {
		return nil, err
	}
	normalizedName, err := pluginstate.NormalizeAlias(name)
	if err != nil {
		return nil, err
	}
	file, err := pluginstate.LoadAdminProfiles(root)
	if err != nil {
		return nil, err
	}
	existing := file.Profiles[normalizedName]
	resolvedBaseURL := firstNonEmpty(strings.TrimSpace(baseURL), existing.ControlPlaneBaseURL, envValue(m.lookupEnv, "CONTROL_PLANE_BASE_URL"), defaultControlPlaneBaseURL)
	resolvedURLPath := firstNonEmpty(strings.TrimSpace(urlPath), existing.ControlPlaneURLPath, envValue(m.lookupEnv, "CONTROL_PLANE_URL_PATH"))
	resolvedAdminKey := firstNonEmpty(strings.TrimSpace(adminKey), existing.AdminKey, defaultAdminKeyRef)
	if err := pluginstate.ValidateSecretReference(resolvedAdminKey, "admin profile "+normalizedName+" admin_key"); err != nil {
		return nil, err
	}
	file.Profiles[normalizedName] = pluginstate.AdminProfile{
		Name:                normalizedName,
		ControlPlaneBaseURL: resolvedBaseURL,
		ControlPlaneURLPath: resolvedURLPath,
		AdminKey:            resolvedAdminKey,
		UpdatedAt:           pluginstate.UTCNow(),
	}
	if activate || file.ActiveProfile == "" {
		file.ActiveProfile = normalizedName
	}
	if err := pluginstate.SaveAdminProfiles(root, file); err != nil {
		return nil, err
	}
	return map[string]any{
		"profile": AdminProfileResult{
			Name:                normalizedName,
			ControlPlaneBaseURL: resolvedBaseURL,
			ControlPlaneURLPath: resolvedURLPath,
			AdminKey:            resolvedAdminKey,
			UpdatedAt:           file.Profiles[normalizedName].UpdatedAt,
			Path:                pluginstate.AdminProfilesPath(root),
			Active:              file.ActiveProfile == normalizedName,
		},
		"active_profile": file.ActiveProfile,
		"path":           pluginstate.AdminProfilesPath(root),
		"state_root":     root.Path,
	}, nil
}

func (m *Manager) ActivateAdminProfile(name string) (map[string]any, error) {
	root := pluginstate.ResolveRoot(m.lookupEnv)
	file, err := pluginstate.LoadAdminProfiles(root)
	if err != nil {
		return nil, err
	}
	normalizedName, err := pluginstate.NormalizeAlias(name)
	if err != nil {
		return nil, err
	}
	profile, ok := file.Profiles[normalizedName]
	if !ok {
		return nil, fmt.Errorf("admin profile %s is not known", normalizedName)
	}
	file.ActiveProfile = normalizedName
	if err := pluginstate.SaveAdminProfiles(root, file); err != nil {
		return nil, err
	}
	return map[string]any{
		"profile": AdminProfileResult{
			Name:                profile.Name,
			ControlPlaneBaseURL: profile.ControlPlaneBaseURL,
			ControlPlaneURLPath: profile.ControlPlaneURLPath,
			AdminKey:            profile.AdminKey,
			UpdatedAt:           profile.UpdatedAt,
			Path:                pluginstate.AdminProfilesPath(root),
			Active:              true,
		},
		"active_profile": file.ActiveProfile,
		"path":           pluginstate.AdminProfilesPath(root),
		"state_root":     root.Path,
	}, nil
}

func (m *Manager) DeleteAdminProfile(name string) (map[string]any, error) {
	root := pluginstate.ResolveRoot(m.lookupEnv)
	file, err := pluginstate.LoadAdminProfiles(root)
	if err != nil {
		return nil, err
	}
	normalizedName, err := pluginstate.NormalizeAlias(name)
	if err != nil {
		return nil, err
	}
	if _, ok := file.Profiles[normalizedName]; !ok {
		return nil, fmt.Errorf("admin profile %s is not known", normalizedName)
	}
	aliases, err := pluginstate.LoadAliases(root)
	if err != nil {
		return nil, err
	}
	for _, record := range aliases {
		if record.AdminProfile == normalizedName {
			return nil, fmt.Errorf("admin profile %s is still referenced by alias %s", normalizedName, record.Alias)
		}
	}
	processes, err := pluginstate.LoadProcesses(root)
	if err != nil {
		return nil, err
	}
	for _, record := range processes {
		if record.AdminProfile == normalizedName && record.Alias != "" && record.Mode != "stopped" {
			return nil, fmt.Errorf("admin profile %s is still referenced by active runtime %s", normalizedName, record.Alias)
		}
	}
	delete(file.Profiles, normalizedName)
	if file.ActiveProfile == normalizedName {
		file.ActiveProfile = firstProfileName(file.Profiles)
	}
	if err := pluginstate.SaveAdminProfiles(root, file); err != nil {
		return nil, err
	}
	return map[string]any{
		"deleted_profile": normalizedName,
		"active_profile":  file.ActiveProfile,
		"path":            pluginstate.AdminProfilesPath(root),
		"state_root":      root.Path,
	}, nil
}

func (m *Manager) Create(opts CreateOptions) (map[string]any, error) {
	alias, err := pluginstate.NormalizeAlias(opts.Alias)
	if err != nil {
		return nil, err
	}
	if err := validateCreateOrConnectScope("create", opts.OrganizationIDs, opts.WorkspaceIDs); err != nil {
		return nil, err
	}
	root := pluginstate.ResolveRoot(m.lookupEnv)
	if err := pluginstate.EnsureDirs(root); err != nil {
		return nil, err
	}
	aliases, err := pluginstate.LoadAliases(root)
	if err != nil {
		return nil, err
	}
	previous := aliases[alias]
	adminProfile, err := m.resolveAdminProfile(root, opts.AdminProfileName, opts.AdminKeyRef, opts.ControlPlaneBaseURL, opts.ControlPlaneURLPath, previous.AdminProfile)
	if err != nil {
		return nil, err
	}
	tunnel, err := m.resolveTunnel(root, alias, opts.Name, opts.Description, opts.OrganizationIDs, opts.WorkspaceIDs, adminProfile, true)
	if err != nil {
		return nil, err
	}
	aliases[alias] = pluginstate.AliasRecordFromTunnel(
		alias,
		tunnel.ID,
		tunnel.Name,
		tunnel.Description,
		tunnel.OrganizationIDs,
		tunnel.WorkspaceIDs,
		tunnel.TenantIDs,
		adminProfile.Name,
		"",
		"",
		"",
		"",
		"",
	)
	if err := pluginstate.SaveAliases(root, aliases); err != nil {
		return nil, err
	}
	_ = pluginstate.AppendHistory(root, "create", alias, tunnel.ID, fmt.Sprintf("name=%s admin_profile=%s", tunnel.Name, adminProfile.Name))
	return map[string]any{
		"alias":              alias,
		"tunnel":             tunnelToMap(*tunnel),
		"admin_profile":      adminProfile.Name,
		"admin_profile_path": adminProfile.Path,
		"state_root":         root.Path,
	}, nil
}

func (m *Manager) Connect(opts ConnectOptions) (map[string]any, error) {
	alias, err := pluginstate.NormalizeAlias(opts.Alias)
	if err != nil {
		return nil, err
	}
	if err := validateCreateOrConnectScope("connect", opts.OrganizationIDs, opts.WorkspaceIDs); err != nil {
		return nil, err
	}
	root := pluginstate.ResolveRoot(m.lookupEnv)
	if err := pluginstate.EnsureDirs(root); err != nil {
		return nil, err
	}
	profileName, err := session.NormalizeProfileName(opts.ProfileName, alias)
	if err != nil {
		return nil, err
	}
	profileDir, err := session.ResolveProfileDir(opts.ProfileDir, m.lookupEnv)
	if err != nil {
		return nil, err
	}
	target, err := targetFromOptions(opts.MCPServerURL, opts.MCPCommand)
	if err != nil {
		return nil, err
	}
	runtimeAPIKey := firstNonEmpty(strings.TrimSpace(opts.RuntimeAPIKey), defaultRuntimeAPIKeyRef)
	if err := pluginstate.ValidateSecretReference(runtimeAPIKey, "runtime api_key"); err != nil {
		return nil, err
	}
	aliases, err := pluginstate.LoadAliases(root)
	if err != nil {
		return nil, err
	}
	previous := aliases[alias]
	adminProfile, err := m.resolveAdminProfile(root, opts.AdminProfileName, opts.AdminKeyRef, opts.ControlPlaneBaseURL, opts.ControlPlaneURLPath, previous.AdminProfile)
	if err != nil {
		return nil, err
	}

	var tunnel *adminapi.Tunnel
	remoteErrorText := ""
	if strings.TrimSpace(opts.TunnelID) != "" {
		if err := config.ValidateTunnelID(strings.TrimSpace(opts.TunnelID)); err != nil {
			return nil, err
		}
		tunnel, err = m.remoteGet(strings.TrimSpace(opts.TunnelID), adminProfile, runtimeAPIKey)
		if err != nil {
			tunnel = providedTunnel(alias, opts)
			remoteErrorText = err.Error()
		}
	} else {
		tunnel, err = m.resolveTunnel(root, alias, opts.Name, opts.Description, opts.OrganizationIDs, opts.WorkspaceIDs, adminProfile, true)
		if err != nil {
			var remoteErr *remoteError
			if errors.As(err, &remoteErr) && previous.TunnelID != "" && isNotFound(err) {
				tunnel = localTunnelFromAlias(previous, alias)
				remoteErrorText = err.Error()
			} else {
				return nil, err
			}
		}
	}

	tunnelID := tunnel.ID
	replaceExistingRuntime := previous.TunnelID != "" && previous.TunnelID != tunnelID
	configPath, err := session.WriteRuntimeProfile(
		alias,
		profileName,
		tunnelID,
		adminProfile.ControlPlaneBaseURL,
		adminProfile.ControlPlaneURLPath,
		runtimeAPIKey,
		target,
		profileDir,
		root,
		m.lookupEnv,
	)
	if err != nil {
		return nil, err
	}
	healthURLFile := session.ProfileHealthURLFile(alias, root)
	aliases[alias] = pluginstate.AliasRecordFromTunnel(
		alias,
		tunnel.ID,
		tunnel.Name,
		defaultDescription(alias, opts.Description, tunnel.Description),
		tunnel.OrganizationIDs,
		tunnel.WorkspaceIDs,
		tunnel.TenantIDs,
		adminProfile.Name,
		configPath,
		profileName,
		profileDir,
		configPath,
		healthURLFile,
	)
	if err := pluginstate.SaveAliases(root, aliases); err != nil {
		return nil, err
	}

	processes, err := pluginstate.LoadProcesses(root)
	if err != nil {
		return nil, err
	}
	existingProcess := processes[alias]
	if replaceExistingRuntime && existingProcess.Alias != "" {
		_ = pluginstate.AppendHistory(root, "stale-process", alias, existingProcess.TunnelID, fmt.Sprintf("replacing with tunnel_id=%s", tunnelID))
	}
	launchEnv := runtimeLaunchEnvOverrides(runtimeAPIKey, m.lookupEnv)
	launch, err := session.StartOrReuse(
		m.runtime,
		alias,
		profileName,
		profileDir,
		opts.TunnelBin,
		root,
		launchEnv,
		existingProcess.PID,
		replaceExistingRuntime,
	)
	if err != nil {
		return nil, err
	}
	processes[alias] = pluginstate.ProcessRecord{
		Alias:         alias,
		TunnelID:      tunnelID,
		AdminProfile:  adminProfile.Name,
		ConfigPath:    configPath,
		ProfileName:   profileName,
		ProfileDir:    profileDir,
		ProfilePath:   configPath,
		HealthURLFile: healthURLFile,
		TargetKind:    target.Kind,
		TargetValue:   target.Value,
		Command:       launch.Command,
		StartedAt:     pluginstate.UTCNow(),
		Mode:          launch.Mode,
		SessionName:   launch.SessionName,
		PID:           launch.PID,
		LogPath:       launch.LogPath,
	}
	if err := pluginstate.SaveProcesses(root, processes); err != nil {
		return nil, err
	}
	_ = pluginstate.AppendHistory(root, "connect", alias, tunnelID, fmt.Sprintf("mode=%s session=%s pid=%d started=%t healthy=%t ready=%t", launch.Mode, valueOrDash(launch.SessionName), launch.PID, launch.Started, launch.Healthy, launch.Ready))
	record := aliases[alias]
	process := processes[alias]
	payload := m.connectPayload(root, alias, *tunnel, adminProfile, record, process, launch, remoteErrorText)
	if !launch.Healthy {
		return payload, &PayloadError{Code: 2, Payload: payload}
	}
	return payload, nil
}

func (m *Manager) ListRuntimes(opts ListOptions) (map[string]any, error) {
	root := pluginstate.ResolveRoot(m.lookupEnv)
	if err := pluginstate.EnsureDirs(root); err != nil {
		return nil, err
	}
	adminProfile, err := m.resolveAdminProfile(root, opts.AdminProfileName, opts.AdminKeyRef, opts.ControlPlaneBaseURL, opts.ControlPlaneURLPath, "")
	if err != nil {
		return nil, err
	}
	aliases, err := pluginstate.LoadAliases(root)
	if err != nil {
		return nil, err
	}
	localAliases := make([]map[string]any, 0, len(aliases))
	names := make([]string, 0, len(aliases))
	for name := range aliases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		localAliases = append(localAliases, aliasToMap(aliases[name]))
	}
	payload := map[string]any{
		"aliases":            localAliases,
		"admin_profile":      adminProfile.Name,
		"admin_profile_path": adminProfile.Path,
		"state_root":         root.Path,
	}
	if hasRemoteScope(opts.OrganizationIDs, opts.WorkspaceIDs, opts.TenantID) {
		remote, err := m.remoteList(opts, adminProfile)
		if err != nil {
			return nil, err
		}
		byTunnelID := map[string]string{}
		byAdmin := map[string]string{}
		for _, record := range aliases {
			byTunnelID[record.TunnelID] = record.Alias
			byAdmin[record.TunnelID] = record.AdminProfile
		}
		merged := make([]map[string]any, 0, len(remote))
		for _, tunnel := range remote {
			item := tunnelToMap(tunnel)
			item["local_alias"] = byTunnelID[tunnel.ID]
			item["local_admin_profile"] = byAdmin[tunnel.ID]
			merged = append(merged, item)
		}
		payload["remote_tunnels"] = merged
	}
	return payload, nil
}

func (m *Manager) CleanupInventory(opts CleanupOptions) (map[string]any, error) {
	root := pluginstate.ResolveRoot(m.lookupEnv)
	if err := pluginstate.EnsureDirs(root); err != nil {
		return nil, err
	}
	aliases, err := pluginstate.LoadAliases(root)
	if err != nil {
		return nil, err
	}
	processes, err := pluginstate.LoadProcesses(root)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(aliases))
	for name := range aliases {
		names = append(names, name)
	}
	for name := range processes {
		if _, ok := aliases[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	seen := map[string]bool{}
	entries := make([]map[string]any, 0, len(names))
	removed := []string{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		record := aliases[name]
		process := processes[name]
		local := m.localRuntimeDetails(root, name, record, process)
		classification := inventoryClassification(local, record, process)
		entry := map[string]any{
			"alias":           name,
			"tunnel_id":       firstNonEmpty(record.TunnelID, process.TunnelID),
			"classification":  classification,
			"profile":         local["profile"],
			"runtime_state":   local["runtime_state"],
			"live_runtime":    local["live_admin_ui"],
			"cleanup_safe":    classification == "stale_alias",
			"cleanup_command": "tunnel-client runtimes cleanup --apply",
		}
		entries = append(entries, entry)
		if opts.Apply && classification == "stale_alias" {
			delete(aliases, name)
			delete(processes, name)
			removed = append(removed, name)
			_ = pluginstate.AppendHistory(root, "cleanup", name, firstNonEmpty(record.TunnelID, process.TunnelID), "removed stale local alias/process metadata")
		}
	}
	if opts.Apply {
		if err := pluginstate.SaveAliases(root, aliases); err != nil {
			return nil, err
		}
		if err := pluginstate.SaveProcesses(root, processes); err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"state_root": root.Path,
		"apply":      opts.Apply,
		"entries":    entries,
		"removed":    removed,
		"next_steps": []string{"Review entries with classification=stale_alias, then run `tunnel-client runtimes cleanup --apply` to remove only stale local alias/process metadata."},
	}, nil
}

func (m *Manager) Status(opts AliasOptions) (map[string]any, error) {
	root := pluginstate.ResolveRoot(m.lookupEnv)
	alias, err := pluginstate.NormalizeAlias(opts.Alias)
	if err != nil {
		return nil, err
	}
	aliases, err := pluginstate.LoadAliases(root)
	if err != nil {
		return nil, err
	}
	processes, err := pluginstate.LoadProcesses(root)
	if err != nil {
		return nil, err
	}
	record, ok := aliases[alias]
	if !ok {
		return nil, fmt.Errorf("alias %s is not known; run create or connect first", alias)
	}
	adminProfile, err := m.resolveAdminProfile(root, opts.AdminProfileName, opts.AdminKeyRef, opts.ControlPlaneBaseURL, opts.ControlPlaneURLPath, record.AdminProfile)
	if err != nil {
		return nil, err
	}
	process := processes[alias]
	var remote *adminapi.Tunnel
	stale := false
	errorText := ""
	lookupAttempted := false
	lookupAuthKind := ""
	lookupAuthRef := ""
	lookupSkippedReason := ""
	keyRef, authKind := m.statusReadOnlyKeyRef(record, process, adminProfile)
	if keyRef != "" {
		if available, reason := secretReferenceAvailable(keyRef, m.lookupEnv); available {
			lookupAttempted = true
			lookupAuthKind = authKind
			lookupAuthRef = keyRef
			remote, err = m.remoteGet(record.TunnelID, adminProfile, keyRef)
			if err != nil {
				errorText = err.Error()
				stale = isNotFound(err)
			}
		} else {
			lookupSkippedReason = reason
		}
	} else {
		lookupSkippedReason = m.statusRemoteLookupSkippedReason(record, process, adminProfile)
	}
	payload := m.statusPayload(root, alias, record, process, adminProfile, remote, stale, errorText, lookupAttempted, lookupAuthKind, lookupAuthRef, lookupSkippedReason)
	if stale && process.Alias == "" {
		return payload, &PayloadError{Code: 2, Payload: payload}
	}
	return payload, nil
}

func (m *Manager) Stop(opts AliasOptions) (map[string]any, error) {
	root := pluginstate.ResolveRoot(m.lookupEnv)
	alias, err := pluginstate.NormalizeAlias(opts.Alias)
	if err != nil {
		return nil, err
	}
	aliases, err := pluginstate.LoadAliases(root)
	if err != nil {
		return nil, err
	}
	processes, err := pluginstate.LoadProcesses(root)
	if err != nil {
		return nil, err
	}
	record, ok := aliases[alias]
	if !ok {
		return nil, fmt.Errorf("alias %s is not known; run create or connect first", alias)
	}
	adminProfile, err := m.resolveAdminProfile(root, opts.AdminProfileName, opts.AdminKeyRef, opts.ControlPlaneBaseURL, opts.ControlPlaneURLPath, record.AdminProfile)
	if err != nil {
		return nil, err
	}
	process := processes[alias]
	alreadyStopped := false
	stopError := ""
	previousMode := process.Mode
	if process.Alias == "" || process.Mode == "stopped" {
		alreadyStopped = true
	} else if process.Mode == "tmux" {
		sessionName := firstNonEmpty(process.SessionName, session.TmuxSessionName(alias, root))
		running, _ := session.TmuxHasSessionName(m.runtime, sessionName)
		if running {
			result, err := session.StopTmux(m.runtime, sessionName)
			if err != nil {
				stopError = err.Error()
			} else if result.ReturnCode != 0 {
				stopError = strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout))
			}
		} else {
			alreadyStopped = true
		}
	} else if process.Mode == "process" && process.PID > 0 {
		if err := session.TerminateProcess(process.PID); err != nil {
			stopError = err.Error()
		} else if !session.WaitForProcessExit(process.PID) {
			stopError = fmt.Sprintf("process %d did not exit after SIGTERM", process.PID)
		}
	} else {
		alreadyStopped = true
	}
	session.ClearHealthURLFile(alias, root)
	if process.Alias != "" {
		process.Mode = "stopped"
		process.SessionName = ""
		process.PID = 0
		processes[alias] = process
		if err := pluginstate.SaveProcesses(root, processes); err != nil {
			return nil, err
		}
	}
	detail := fmt.Sprintf("previous_mode=%s already_stopped=%t", valueOrDash(previousMode), alreadyStopped)
	if stopError != "" {
		detail += " error=" + stopError
	}
	_ = pluginstate.AppendHistory(root, "stop", alias, record.TunnelID, detail)
	payload := m.statusPayload(root, alias, record, processes[alias], adminProfile, nil, false, stopError, false, "", "", "stop is a local-only operation")
	payload["already_stopped"] = alreadyStopped
	payload["stopped"] = stopError == ""
	payload["stop_error"] = stopError
	if stopError != "" {
		return payload, &PayloadError{Code: 2, Payload: payload}
	}
	return payload, nil
}

func (m *Manager) Remove(opts AliasOptions) (map[string]any, error) {
	root := pluginstate.ResolveRoot(m.lookupEnv)
	alias, err := pluginstate.NormalizeAlias(opts.Alias)
	if err != nil {
		return nil, err
	}
	aliases, err := pluginstate.LoadAliases(root)
	if err != nil {
		return nil, err
	}
	processes, err := pluginstate.LoadProcesses(root)
	if err != nil {
		return nil, err
	}
	record, aliasKnown := aliases[alias]
	process := processes[alias]
	if !aliasKnown && process.Alias == "" {
		return nil, fmt.Errorf("alias %s is not known", alias)
	}
	if process.Alias != "" && process.Mode != "" && process.Mode != "stopped" {
		return nil, fmt.Errorf("alias %s still has a managed runtime; run `tunnel-client runtimes stop %s` first", alias, alias)
	}
	delete(aliases, alias)
	if err := pluginstate.SaveAliases(root, aliases); err != nil {
		return nil, err
	}
	delete(processes, alias)
	if err := pluginstate.SaveProcesses(root, processes); err != nil {
		return nil, err
	}

	removedPaths := []string{}
	for _, pathValue := range uniquePaths(
		record.ConfigPath,
		record.ProfilePath,
		record.HealthURLFile,
		process.ConfigPath,
		process.ProfilePath,
		process.HealthURLFile,
		process.LogPath,
	) {
		if strings.TrimSpace(pathValue) == "" {
			continue
		}
		if err := os.Remove(pathValue); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove local state path %s: %w", pathValue, err)
		}
		removedPaths = append(removedPaths, pathValue)
	}

	tunnelID := firstNonEmpty(record.TunnelID, process.TunnelID)
	_ = pluginstate.AppendHistory(root, "rm", alias, tunnelID, fmt.Sprintf("removed_paths=%d", len(removedPaths)))
	return map[string]any{
		"alias":         alias,
		"removed":       true,
		"removed_paths": removedPaths,
		"state_root":    root.Path,
		"tunnel_id":     tunnelID,
	}, nil
}

type remoteError struct{ message string }

func (e *remoteError) Error() string { return e.message }

func (m *Manager) resolveAdminProfile(root pluginstate.Root, requestedName, adminKeyRef, baseURL, urlPath, defaultName string) (effectiveAdminProfile, error) {
	name := firstNonEmpty(requestedName, defaultName)
	if name == "" {
		if value, ok := m.lookupEnv("TUNNEL_MCP_ADMIN_PROFILE"); ok && strings.TrimSpace(value) != "" {
			name = strings.TrimSpace(value)
		} else {
			name = defaultAdminProfileName
		}
	}
	normalizedName, err := pluginstate.NormalizeAlias(name)
	if err != nil {
		return effectiveAdminProfile{}, err
	}
	file, err := pluginstate.LoadAdminProfiles(root)
	if err != nil {
		return effectiveAdminProfile{}, err
	}
	existing := file.Profiles[normalizedName]
	resolvedBaseURL := firstNonEmpty(strings.TrimSpace(baseURL), existing.ControlPlaneBaseURL, envValue(m.lookupEnv, "CONTROL_PLANE_BASE_URL"), defaultControlPlaneBaseURL)
	resolvedURLPath := firstNonEmpty(strings.TrimSpace(urlPath), existing.ControlPlaneURLPath, envValue(m.lookupEnv, "CONTROL_PLANE_URL_PATH"))
	resolvedAdminKey := firstNonEmpty(strings.TrimSpace(adminKeyRef), existing.AdminKey, defaultAdminKeyRef)
	if err := pluginstate.ValidateSecretReference(resolvedAdminKey, "admin profile "+normalizedName+" admin_key"); err != nil {
		return effectiveAdminProfile{}, err
	}
	if existing.Name == "" || existing.ControlPlaneBaseURL != resolvedBaseURL || existing.ControlPlaneURLPath != resolvedURLPath || existing.AdminKey != resolvedAdminKey {
		file.Profiles[normalizedName] = pluginstate.AdminProfile{
			Name:                normalizedName,
			ControlPlaneBaseURL: resolvedBaseURL,
			ControlPlaneURLPath: resolvedURLPath,
			AdminKey:            resolvedAdminKey,
			UpdatedAt:           pluginstate.UTCNow(),
		}
		file.ActiveProfile = normalizedName
		if err := pluginstate.SaveAdminProfiles(root, file); err != nil {
			return effectiveAdminProfile{}, err
		}
	}
	return effectiveAdminProfile{
		Name:                normalizedName,
		ControlPlaneBaseURL: resolvedBaseURL,
		ControlPlaneURLPath: resolvedURLPath,
		AdminKey:            resolvedAdminKey,
		Path:                pluginstate.AdminProfilesPath(root),
	}, nil
}

func (m *Manager) resolveTunnel(root pluginstate.Root, alias, requestedName, description string, organizationIDs, workspaceIDs []string, adminProfile effectiveAdminProfile, createIfMissing bool) (*adminapi.Tunnel, error) {
	aliases, err := pluginstate.LoadAliases(root)
	if err != nil {
		return nil, err
	}
	if existing, ok := aliases[alias]; ok && existing.TunnelID != "" {
		existingAdmin, err := m.resolveAdminProfile(root, "", "", "", "", existing.AdminProfile)
		if err != nil {
			return nil, err
		}
		tunnel, err := m.remoteGet(existing.TunnelID, existingAdmin, "")
		if err == nil {
			return tunnel, nil
		}
		if !isNotFound(err) {
			return nil, err
		}
		_ = pluginstate.AppendHistory(root, "stale-alias", alias, existing.TunnelID, err.Error())
		delete(aliases, alias)
		if err := pluginstate.SaveAliases(root, aliases); err != nil {
			return nil, err
		}
	}
	if hasRemoteScope(organizationIDs, workspaceIDs, "") {
		desiredName := firstNonEmpty(requestedName, alias)
		tunnels, err := m.remoteListForLookup(organizationIDs, workspaceIDs, "", adminProfile)
		if err == nil {
			for _, tunnel := range tunnels {
				if tunnel.Name == desiredName {
					return &tunnel, nil
				}
			}
		} else if !isNotFound(err) {
			return nil, err
		}
	}
	if !createIfMissing {
		return nil, fmt.Errorf("alias %s is not known", alias)
	}
	if len(organizationIDs) == 0 && len(workspaceIDs) == 0 {
		return nil, fmt.Errorf("creating a tunnel requires --organization-id or --workspace-id")
	}
	return m.remoteCreate(alias, requestedName, description, organizationIDs, workspaceIDs, adminProfile)
}

func (m *Manager) remoteGet(tunnelID string, adminProfile effectiveAdminProfile, keyRef string) (*adminapi.Tunnel, error) {
	client, err := m.newAdminClient(adminProfile, firstNonEmpty(keyRef, adminProfile.AdminKey))
	if err != nil {
		return nil, err
	}
	tunnel, err := client.GetTunnel(mustContext(), tunnelID)
	if err != nil {
		return nil, &remoteError{message: err.Error()}
	}
	return tunnel, nil
}

func (m *Manager) remoteCreate(alias, requestedName, description string, organizationIDs, workspaceIDs []string, adminProfile effectiveAdminProfile) (*adminapi.Tunnel, error) {
	client, err := m.newAdminClient(adminProfile, adminProfile.AdminKey)
	if err != nil {
		return nil, err
	}
	tunnel, err := client.CreateTunnel(mustContext(), adminapi.TunnelCreateRequest{
		Name:            firstNonEmpty(requestedName, alias),
		Description:     defaultDescription(alias, description, ""),
		OrganizationIDs: append([]string(nil), organizationIDs...),
		WorkspaceIDs:    append([]string(nil), workspaceIDs...),
	})
	if err != nil {
		return nil, &remoteError{message: err.Error()}
	}
	return tunnel, nil
}

func (m *Manager) remoteList(opts ListOptions, adminProfile effectiveAdminProfile) ([]adminapi.Tunnel, error) {
	return m.remoteListForLookup(opts.OrganizationIDs, opts.WorkspaceIDs, opts.TenantID, adminProfile)
}

func (m *Manager) remoteListForLookup(organizationIDs, workspaceIDs []string, tenantID string, adminProfile effectiveAdminProfile) ([]adminapi.Tunnel, error) {
	if err := validateListScope(organizationIDs, workspaceIDs, tenantID); err != nil {
		return nil, err
	}
	client, err := m.newAdminClient(adminProfile, adminProfile.AdminKey)
	if err != nil {
		return nil, err
	}
	orgID, wsID := "", ""
	if len(organizationIDs) > 0 {
		orgID = organizationIDs[0]
	}
	if len(workspaceIDs) > 0 {
		wsID = workspaceIDs[0]
	}
	resp, err := client.ListTunnels(mustContext(), orgID, wsID, tenantID)
	if err != nil {
		return nil, &remoteError{message: err.Error()}
	}
	return resp.Tunnels, nil
}

func (m *Manager) newAdminClient(adminProfile effectiveAdminProfile, keyRef string) (*adminapi.AdminTunnelClient, error) {
	key, err := resolveSecretReference(keyRef, m.lookupEnv)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(adminProfile.ControlPlaneBaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse control-plane base URL %s: %w", adminProfile.ControlPlaneBaseURL, err)
	}
	controlPlaneURLPath, err := config.NormalizeControlPlaneURLPath(adminProfile.ControlPlaneURLPath)
	if err != nil {
		return nil, err
	}
	return adminapi.NewAdminTunnelClient(&config.AdminConfig{
		BaseURL:  parsed,
		URLPath:  controlPlaneURLPath,
		AdminKey: key,
	})
}

func (m *Manager) connectPayload(root pluginstate.Root, alias string, tunnel adminapi.Tunnel, adminProfile effectiveAdminProfile, record pluginstate.AliasRecord, process pluginstate.ProcessRecord, launch session.LaunchResult, remoteErr string) map[string]any {
	local := m.localRuntimeDetails(root, alias, record, process)
	effectiveHealth := local["effective_health"].(map[string]any)
	payload := map[string]any{
		"alias":              alias,
		"tunnel":             tunnelToMap(tunnel),
		"admin_profile":      adminProfile.Name,
		"admin_profile_path": adminProfile.Path,
		"profile_name":       record.ProfileName,
		"profile_dir":        record.ProfileDir,
		"profile_path":       record.ProfilePath,
		"profile_exists":     local["profile"].(map[string]any)["exists"],
		"config_path":        record.ConfigPath,
		"health_url_file":    record.HealthURLFile,
		"health_url":         local["health"].(map[string]any)["url"],
		"ui_url":             local["health"].(map[string]any)["ui"],
		"runtime_state":      local["runtime_state"],
		"healthy":            effectiveHealth["healthz"].(map[string]any)["ok"],
		"ready":              effectiveHealth["readyz"].(map[string]any)["ok"],
		"launched":           launch.Launched,
		"mode":               launch.Mode,
		"command":            launch.Command,
		"session_name":       launch.SessionName,
		"log_path":           launch.LogPath,
		"launch_diagnostics": launchDiagnostics(launch),
		"started":            launch.Started,
		"running":            launch.Running,
		"already_running":    launch.AlreadyRunning,
		"remote_error":       remoteErr,
		"tmux":               local["tmux"],
		"process_running":    local["process_running"],
		"process":            processToMap(process),
		"local":              local,
		"repair_actions":     repairActions(alias, record, process, local, remoteErr),
		"next_steps":         []string{doctorCommand(record.ProfileName, record.ProfileDir, record.ConfigPath, true)},
	}
	if launch.PID > 0 {
		payload["pid"] = launch.PID
	}
	if launch.ExitCode != nil {
		payload["exit_code"] = *launch.ExitCode
	}
	return payload
}

func (m *Manager) statusPayload(root pluginstate.Root, alias string, record pluginstate.AliasRecord, process pluginstate.ProcessRecord, adminProfile effectiveAdminProfile, remote *adminapi.Tunnel, stale bool, errorText string, attempted bool, authKind string, authRef string, skippedReason string) map[string]any {
	local := m.localRuntimeDetails(root, alias, record, process)
	effectiveHealth := local["effective_health"].(map[string]any)
	actions := repairActions(alias, record, process, local, errorText)
	payload := map[string]any{
		"alias":                     alias,
		"tunnel_id":                 record.TunnelID,
		"admin_profile":             adminProfile.Name,
		"admin_profile_path":        adminProfile.Path,
		"remote":                    nil,
		"stale":                     stale,
		"error":                     errorText,
		"remote_error":              errorText,
		"repair_command":            repairCommand(alias, record, process),
		"repair_actions":            actions,
		"config_path":               record.ConfigPath,
		"profile_name":              record.ProfileName,
		"profile_dir":               record.ProfileDir,
		"profile_path":              record.ProfilePath,
		"profile_exists":            local["profile"].(map[string]any)["exists"],
		"health_url_file":           record.HealthURLFile,
		"health_url":                local["health"].(map[string]any)["url"],
		"ui_url":                    local["health"].(map[string]any)["ui"],
		"runtime_state":             local["runtime_state"],
		"healthy":                   effectiveHealth["healthz"].(map[string]any)["ok"],
		"ready":                     effectiveHealth["readyz"].(map[string]any)["ok"],
		"control_plane_poll_health": local["control_plane_poll_health"],
		"remote_lookup_attempted":   attempted,
		"remote_lookup_auth_kind":   authKind,
		"remote_lookup_auth_ref":    authRef,
		"remote_skipped_reason":     skippedReason,
		"tmux":                      local["tmux"],
		"process_running":           local["process_running"],
		"process":                   nil,
		"local":                     local,
		"next_steps":                nextStepCommands(actions, doctorCommand(record.ProfileName, record.ProfileDir, record.ConfigPath, true), "tunnel-client runtimes status "+alias),
	}
	if remote != nil {
		payload["remote"] = tunnelToMap(*remote)
	}
	if process.Alias != "" {
		payload["process"] = processToMap(process)
	}
	return payload
}

func (m *Manager) localRuntimeDetails(root pluginstate.Root, alias string, record pluginstate.AliasRecord, process pluginstate.ProcessRecord) map[string]any {
	healthURLFile := firstNonEmpty(process.HealthURLFile, record.HealthURLFile)
	profileName := firstNonEmpty(process.ProfileName, record.ProfileName)
	profileDir := firstNonEmpty(process.ProfileDir, record.ProfileDir)
	profilePath := firstNonEmpty(process.ProfilePath, record.ProfilePath)
	configPath := firstNonEmpty(process.ConfigPath, record.ConfigPath)
	logPath := process.LogPath

	health := pathDetails(healthURLFile)
	rawHealthURL := session.ReadHealthURL(healthURLFile)
	probe := session.ProbeHealthEndpoints(rawHealthURL)
	liveAdmin := m.findLiveAdminUI(root, firstNonEmpty(record.TunnelID, process.TunnelID), session.NormalizeHealthBaseURL(rawHealthURL))
	effectiveProbe := probe
	if !probe.Healthz.OK {
		if liveURL := stringValue(liveAdmin["base_url"]); liveURL != "" {
			effectiveProbe = session.ProbeHealthEndpoints(liveURL)
		}
	}
	health["raw_url"] = rawHealthURL
	health["base_url"] = probe.BaseURL
	health["url"] = probe.Healthz.URL
	if probe.BaseURL != "" {
		health["ui"] = strings.TrimRight(probe.BaseURL, "/") + "/ui"
	} else {
		health["ui"] = ""
	}
	health["healthz"] = endpointToMap(probe.Healthz)
	health["readyz"] = endpointToMap(probe.Readyz)
	effectiveHealth := map[string]any{
		"base_url": effectiveProbe.BaseURL,
		"url":      effectiveProbe.Healthz.URL,
		"ui":       uiURLFromBase(effectiveProbe.BaseURL),
		"healthz":  endpointToMap(effectiveProbe.Healthz),
		"readyz":   endpointToMap(effectiveProbe.Readyz),
	}

	profile := pathDetails(profilePath)
	profile["name"] = profileName
	profile["dir"] = profileDir
	profile["config_path"] = configPath

	log := pathDetails(logPath)
	log["tail"] = readLogTail(logPath, 20)

	tmuxSession := firstNonEmpty(process.SessionName, session.TmuxSessionName(alias, root))
	tmuxRunning, _ := session.TmuxHasSessionName(m.runtime, tmuxSession)
	processRunning := process.PID > 0 && session.PIDIsRunning(process.PID)
	runtimeRunning := tmuxRunning || processRunning
	reportedProcessRunning := processRunning
	if process.Mode == "tmux" && tmuxRunning {
		reportedProcessRunning = true
	}
	controlPlanePollHealth := controlPlanePollHealthFromLiveAdmin(liveAdmin)
	return map[string]any{
		"runtime_state": runtimeState(runtimeRunning || boolValue(liveAdmin["found"]), effectiveProbe),
		"issues": localIssues(
			process,
			tmuxRunning,
			processRunning,
			profile["exists"].(bool),
			health,
			log["exists"].(bool),
			liveAdmin,
			controlPlanePollHealth,
		),
		"profile":                   profile,
		"health":                    health,
		"effective_health":          effectiveHealth,
		"live_admin_ui":             liveAdmin,
		"control_plane_poll_health": controlPlanePollHealth,
		"log":                       log,
		"tmux": map[string]any{
			"session_name": tmuxSession,
			"running":      tmuxRunning,
		},
		"process_running": reportedProcessRunning,
	}
}

func (m *Manager) statusReadOnlyKeyRef(record pluginstate.AliasRecord, process pluginstate.ProcessRecord, adminProfile effectiveAdminProfile) (string, string) {
	for _, pathValue := range []string{process.ProfilePath, record.ProfilePath, process.ConfigPath, record.ConfigPath} {
		if keyRef := controlPlaneAPIKeyRefFromProfile(pathValue); keyRef != "" {
			if ok, _ := secretReferenceAvailable(keyRef, m.lookupEnv); ok {
				return keyRef, "runtime"
			}
		}
	}
	for _, keyRef := range []string{defaultRuntimeAPIKeyRef, "env:OPENAI_API_KEY"} {
		if ok, _ := secretReferenceAvailable(keyRef, m.lookupEnv); ok {
			return keyRef, "runtime"
		}
	}
	if ok, _ := secretReferenceAvailable(adminProfile.AdminKey, m.lookupEnv); ok {
		return adminProfile.AdminKey, "admin"
	}
	return "", ""
}

func (m *Manager) statusRemoteLookupSkippedReason(record pluginstate.AliasRecord, process pluginstate.ProcessRecord, adminProfile effectiveAdminProfile) string {
	reasons := []string{}
	seen := map[string]bool{}
	for _, keyRef := range []string{
		controlPlaneAPIKeyRefFromProfile(process.ProfilePath),
		controlPlaneAPIKeyRefFromProfile(record.ProfilePath),
		controlPlaneAPIKeyRefFromProfile(process.ConfigPath),
		controlPlaneAPIKeyRefFromProfile(record.ConfigPath),
		defaultRuntimeAPIKeyRef,
		"env:OPENAI_API_KEY",
		adminProfile.AdminKey,
	} {
		if keyRef == "" || seen[keyRef] {
			continue
		}
		seen[keyRef] = true
		if ok, reason := secretReferenceAvailable(keyRef, m.lookupEnv); ok {
			return ""
		} else if reason != "" {
			reasons = append(reasons, reason)
		}
	}
	if len(reasons) == 0 {
		return "no runtime or admin key reference is available for read-only lookup"
	}
	return reasons[len(reasons)-1]
}

func targetFromOptions(serverURL, command string) (session.Target, error) {
	if strings.TrimSpace(serverURL) != "" {
		parsed, err := url.Parse(strings.TrimSpace(serverURL))
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return session.Target{}, fmt.Errorf("--mcp-server-url must be an http or https URL")
		}
		if err := pluginstate.RejectInlineSecretMaterial(serverURL, "mcp server URL"); err != nil {
			return session.Target{}, err
		}
		return session.Target{Kind: "server_url", Value: strings.TrimSpace(serverURL)}, nil
	}
	if strings.TrimSpace(command) != "" {
		if err := pluginstate.RejectInlineSecretMaterial(command, "mcp command"); err != nil {
			return session.Target{}, err
		}
		return session.Target{Kind: "command", Value: command}, nil
	}
	return session.Target{}, fmt.Errorf("connect requires --mcp-server-url or --mcp-command")
}

func runtimeLaunchEnvOverrides(secretRef string, lookupEnv func(string) (string, bool)) map[string]string {
	if !strings.HasPrefix(secretRef, "env:") {
		return map[string]string{}
	}
	envName := strings.TrimPrefix(secretRef, "env:")
	value, err := resolveSecretReference(secretRef, lookupEnv)
	if err != nil {
		return map[string]string{}
	}
	return map[string]string{envName: value}
}

func resolveSecretReference(secretRef string, lookupEnv func(string) (string, bool)) (string, error) {
	value := strings.TrimSpace(secretRef)
	if err := pluginstate.ValidateSecretReference(value, "secret reference"); err != nil {
		return "", err
	}
	if strings.HasPrefix(value, "env:") {
		envName := strings.TrimPrefix(value, "env:")
		if raw, ok := lookupEnv(envName); ok && strings.TrimSpace(raw) != "" {
			return strings.TrimSpace(raw), nil
		}
		return "", fmt.Errorf("environment variable %s is not set", envName)
	}
	path := strings.TrimPrefix(value, "file:")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read secret file %s: %w", path, err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return "", fmt.Errorf("secret file %s is empty", path)
	}
	return trimmed, nil
}

func secretReferenceAvailable(secretRef string, lookupEnv func(string) (string, bool)) (bool, string) {
	value := strings.TrimSpace(secretRef)
	if value == "" {
		return false, "secret reference is empty"
	}
	if strings.HasPrefix(value, "env:") {
		envName := strings.TrimPrefix(value, "env:")
		if raw, ok := lookupEnv(envName); ok && strings.TrimSpace(raw) != "" {
			return true, ""
		}
		return false, fmt.Sprintf("environment variable %s is not set", envName)
	}
	path := strings.TrimPrefix(value, "file:")
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Sprintf("secret file %s does not exist", path)
	}
	if info.Size() == 0 {
		return false, fmt.Sprintf("secret file %s is empty", path)
	}
	return true, ""
}

func controlPlaneAPIKeyRefFromProfile(pathValue string) string {
	if strings.TrimSpace(pathValue) == "" {
		return ""
	}
	data, err := os.ReadFile(pathValue)
	if err != nil {
		return ""
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}
	controlPlane, ok := raw["control_plane"].(map[string]any)
	if !ok {
		return ""
	}
	apiKey, _ := controlPlane["api_key"].(string)
	if apiKey == "" {
		return ""
	}
	if err := pluginstate.ValidateSecretReference(apiKey, "runtime api_key"); err != nil {
		return ""
	}
	return apiKey
}

func repairCommand(alias string, record pluginstate.AliasRecord, process pluginstate.ProcessRecord) string {
	parts := []string{"tunnel-client", "runtimes", "connect", "--alias", alias}
	if record.AdminProfile != "" {
		parts = append(parts, "--admin-profile", record.AdminProfile)
	}
	if record.ProfileName != "" {
		parts = append(parts, "--profile", record.ProfileName)
	}
	profileDir := firstNonEmpty(process.ProfileDir, record.ProfileDir)
	if profileDir != "" {
		parts = append(parts, "--profile-dir", profileDir)
	}
	if len(record.OrganizationIDs) > 0 {
		parts = append(parts, "--organization-id", record.OrganizationIDs[0])
	} else if len(record.WorkspaceIDs) > 0 {
		parts = append(parts, "--workspace-id", record.WorkspaceIDs[0])
	} else if len(record.TenantIDs) > 0 {
		parts = append(parts, "--tenant-id", record.TenantIDs[0])
	}
	switch process.TargetKind {
	case "server_url":
		parts = append(parts, "--mcp-server-url", process.TargetValue)
	case "command":
		parts = append(parts, "--mcp-command", process.TargetValue)
	default:
		parts = append(parts, "<add --mcp-server-url or --mcp-command>")
	}
	return shellJoin(parts)
}

func doctorCommand(profileName, profileDir, configPath string, explain bool) string {
	parts := []string{"tunnel-client", "doctor"}
	if profileName != "" {
		parts = append(parts, "--profile", profileName)
		if profileDir != "" {
			parts = append(parts, "--profile-dir", profileDir)
		}
	} else if configPath != "" {
		parts = append(parts, "--config", configPath)
	}
	if explain {
		parts = append(parts, "--explain")
	}
	return strings.Join(parts, " ")
}

func launchDiagnostics(launch session.LaunchResult) map[string]any {
	diagnostics := map[string]any{}
	if launch.ExitCode != nil {
		diagnostics["exit_code"] = *launch.ExitCode
	}
	if strings.TrimSpace(launch.LogPath) != "" {
		diagnostics["log_path"] = launch.LogPath
	}
	if strings.TrimSpace(launch.LogTail) != "" {
		diagnostics["log_tail"] = launch.LogTail
	}
	if len(diagnostics) == 0 {
		return nil
	}
	return diagnostics
}

func shellJoin(parts []string) string {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, shellQuote(part))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if strings.Contains(value, "<") && strings.Contains(value, ">") {
		return value
	}
	if isShellSafe(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func isShellSafe(value string) bool {
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune("@%_+=:,./-", r):
		default:
			return false
		}
	}
	return true
}

func firstProfileName(profiles map[string]pluginstate.AdminProfile) string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func uniquePaths(values ...string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func tunnelToMap(tunnel adminapi.Tunnel) map[string]any {
	return map[string]any{
		"id":               tunnel.ID,
		"name":             tunnel.Name,
		"description":      tunnel.Description,
		"organization_ids": tunnel.OrganizationIDs,
		"workspace_ids":    tunnel.WorkspaceIDs,
		"tenant_ids":       tunnel.TenantIDs,
	}
}

func aliasToMap(record pluginstate.AliasRecord) map[string]any {
	return map[string]any{
		"alias":            record.Alias,
		"tunnel_id":        record.TunnelID,
		"name":             record.Name,
		"admin_profile":    record.AdminProfile,
		"description":      record.Description,
		"organization_ids": record.OrganizationIDs,
		"workspace_ids":    record.WorkspaceIDs,
		"tenant_ids":       record.TenantIDs,
		"config_path":      record.ConfigPath,
		"profile_name":     record.ProfileName,
		"profile_dir":      record.ProfileDir,
		"profile_path":     record.ProfilePath,
		"health_url_file":  record.HealthURLFile,
		"updated_at":       record.UpdatedAt,
	}
}

func processToMap(record pluginstate.ProcessRecord) map[string]any {
	payload := map[string]any{
		"alias":           record.Alias,
		"tunnel_id":       record.TunnelID,
		"admin_profile":   record.AdminProfile,
		"mode":            record.Mode,
		"session_name":    record.SessionName,
		"config_path":     record.ConfigPath,
		"profile_name":    record.ProfileName,
		"profile_dir":     record.ProfileDir,
		"profile_path":    record.ProfilePath,
		"health_url_file": record.HealthURLFile,
		"target_kind":     record.TargetKind,
		"target_value":    record.TargetValue,
		"command":         record.Command,
		"log_path":        record.LogPath,
		"started_at":      record.StartedAt,
	}
	if record.PID > 0 {
		payload["pid"] = record.PID
	}
	return payload
}

func endpointToMap(probe session.EndpointProbe) map[string]any {
	return map[string]any{
		"url":    probe.URL,
		"ok":     probe.OK,
		"status": probe.Status,
		"body":   probe.Body,
		"error":  probe.Error,
	}
}

func pathDetails(pathValue string) map[string]any {
	if strings.TrimSpace(pathValue) == "" {
		return map[string]any{"path": "", "exists": false, "size_bytes": 0}
	}
	info, err := os.Stat(pathValue)
	if err != nil {
		return map[string]any{"path": pathValue, "exists": false, "size_bytes": 0}
	}
	size := int64(0)
	if !info.IsDir() {
		size = info.Size()
	}
	return map[string]any{"path": pathValue, "exists": true, "size_bytes": size}
}

func readLogTail(pathValue string, maxLines int) string {
	if strings.TrimSpace(pathValue) == "" {
		return ""
	}
	data, err := os.ReadFile(pathValue)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

func runtimeState(runtimeRunning bool, probe session.HealthProbe) string {
	if !runtimeRunning {
		return "stopped"
	}
	if probe.Healthz.OK {
		if probe.Readyz.OK {
			return "ready"
		}
		return "healthy"
	}
	return "starting"
}

func (m *Manager) findLiveAdminUI(root pluginstate.Root, tunnelID string, staleBaseURL string) map[string]any {
	result := map[string]any{"found": false}
	healthDir := filepath.Join(root.Path, "health")
	entries, err := os.ReadDir(healthDir)
	if err != nil {
		return result
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".url") {
			continue
		}
		path := filepath.Join(healthDir, entry.Name())
		rawURL := session.ReadHealthURL(path)
		baseURL := session.NormalizeHealthBaseURL(rawURL)
		if baseURL == "" || baseURL == staleBaseURL {
			continue
		}
		status, statusErr := fetchAdminJSON(baseURL, "/api/status")
		if statusErr != nil {
			continue
		}
		statusTunnelID := stringValue(status["control_plane_tunnel_id"])
		if tunnelID != "" && statusTunnelID != tunnelID {
			continue
		}
		system, _ := fetchAdminJSON(baseURL, "/api/system")
		result = map[string]any{
			"found":                  true,
			"base_url":               baseURL,
			"ui_url":                 uiURLFromBase(baseURL),
			"source_health_url_file": path,
			"match_reason":           "control_plane_tunnel_id",
			"status":                 status,
			"system":                 system,
		}
		return result
	}
	return result
}

func fetchAdminJSON(baseURL string, path string) (map[string]any, error) {
	target, err := healthurl.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	client, err := target.HTTPClient(500 * time.Millisecond)
	if err != nil {
		return nil, err
	}
	resp, err := client.Get(target.RequestURL(path))
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s returned HTTP %d", target.URL(path), resp.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func controlPlanePollHealthFromLiveAdmin(liveAdmin map[string]any) map[string]any {
	system, _ := liveAdmin["system"].(map[string]any)
	if system == nil {
		return map[string]any{"state": "unknown", "reason": "no live admin UI system snapshot"}
	}
	raw, _ := system["proxy_health"].([]any)
	for _, item := range raw {
		summary, _ := item.(map[string]any)
		route, _ := summary["route"].(map[string]any)
		if stringValue(route["kind"]) != "control_plane" {
			continue
		}
		return map[string]any{
			"state":        firstNonEmpty(stringValue(summary["health_state"]), "unknown"),
			"route":        route,
			"last_check":   summary["last_check"],
			"last_success": summary["last_success"],
			"history":      summary["history"],
		}
	}
	status, _ := liveAdmin["status"].(map[string]any)
	if status != nil && status["control_plane_route"] != nil {
		return map[string]any{
			"state":  "unknown",
			"route":  status["control_plane_route"],
			"reason": "route is present but proxy health snapshot did not report reachability",
		}
	}
	return map[string]any{"state": "unknown", "reason": "control-plane route health is not available"}
}

func uiURLFromBase(baseURL string) string {
	if strings.TrimSpace(baseURL) == "" {
		return ""
	}
	return strings.TrimRight(baseURL, "/") + "/ui"
}

func localIssues(process pluginstate.ProcessRecord, tmuxRunning bool, processRunning bool, profileExists bool, health map[string]any, logExists bool, liveAdmin map[string]any, controlPlanePollHealth map[string]any) []string {
	issues := []string{}
	if process.Mode == "tmux" && process.SessionName != "" && !tmuxRunning {
		issues = append(issues, "recorded tmux session is not running")
	}
	if process.Mode == "process" && process.PID > 0 && !processRunning {
		issues = append(issues, "recorded process pid is not running")
	}
	if process.ProfilePath != "" && !profileExists {
		issues = append(issues, "recorded runtime profile is missing")
	}
	if process.HealthURLFile != "" && strings.TrimSpace(stringValue(health["raw_url"])) == "" {
		issues = append(issues, "health URL file has not been populated")
	}
	healthz := health["healthz"].(map[string]any)
	if process.Alias != "" && stringValue(health["url"]) != "" && !boolValue(healthz["ok"]) {
		issue := "health endpoint is not healthy at " + stringValue(health["url"])
		if status := intValue(healthz["status"]); status != 0 {
			issue += fmt.Sprintf(" (HTTP %d)", status)
		} else if err := stringValue(healthz["error"]); err != "" {
			issue += " (" + err + ")"
		}
		issues = append(issues, issue)
	}
	readyz := health["readyz"].(map[string]any)
	if process.Alias != "" && stringValue(readyz["url"]) != "" && !boolValue(readyz["ok"]) {
		issue := "ready endpoint is not ready at " + stringValue(readyz["url"])
		if status := intValue(readyz["status"]); status != 0 {
			issue += fmt.Sprintf(" (HTTP %d)", status)
		} else if err := stringValue(readyz["error"]); err != "" {
			issue += " (" + err + ")"
		} else if body := stringValue(readyz["body"]); body != "" {
			issue += " (" + body + ")"
		}
		issues = append(issues, issue)
	}
	if process.LogPath != "" && (!tmuxRunning && !processRunning) && logExists {
		issues = append(issues, "runtime log exists but no active runtime is running")
	}
	if boolValue(liveAdmin["found"]) && !boolValue(healthz["ok"]) {
		issues = append(issues, "recorded health URL looks stale; live admin UI was found at "+stringValue(liveAdmin["base_url"]))
	}
	if state := stringValue(controlPlanePollHealth["state"]); state != "" && state != "unknown" && state != "healthy" && state != "direct" {
		issues = append(issues, "control-plane poll route health is "+state)
	}
	return issues
}

func repairActions(alias string, record pluginstate.AliasRecord, process pluginstate.ProcessRecord, local map[string]any, remoteErr string) []RepairAction {
	actions := []RepairAction{}
	profile, _ := local["profile"].(map[string]any)
	health, _ := local["health"].(map[string]any)
	liveAdmin, _ := local["live_admin_ui"].(map[string]any)
	pollHealth, _ := local["control_plane_poll_health"].(map[string]any)
	if profile != nil && !boolValue(profile["exists"]) {
		actions = append(actions, RepairAction{
			ID:      "reconnect_missing_profile",
			Command: repairCommand(alias, record, process),
			Reason:  "the recorded runtime profile is missing, so reconnecting rewrites the profile and relaunches the managed runtime",
		})
	}
	if process.Alias == "" || stringValue(local["runtime_state"]) == "stopped" {
		actions = append(actions, RepairAction{
			ID:      "start_runtime",
			Command: repairCommand(alias, record, process),
			Reason:  "no managed runtime is currently running for this alias",
		})
	}
	if boolValue(liveAdmin["found"]) {
		if healthz, _ := health["healthz"].(map[string]any); healthz != nil && !boolValue(healthz["ok"]) {
			actions = append(actions, RepairAction{
				ID:      "refresh_stale_health_url",
				Command: "tunnel-client runtimes connect --alias " + alias,
				Reason:  "the recorded health URL is stale but a live admin UI for the same tunnel was found",
			})
		}
	}
	if state := stringValue(pollHealth["state"]); state != "" && state != "unknown" && state != "healthy" && state != "direct" {
		actions = append(actions, RepairAction{
			ID:      "repair_control_plane_proxy",
			Command: doctorCommand(firstNonEmpty(process.ProfileName, record.ProfileName), firstNonEmpty(process.ProfileDir, record.ProfileDir), firstNonEmpty(process.ConfigPath, record.ConfigPath), true),
			Reason:  "local /healthz and /readyz can be green while the control-plane poll route is unhealthy through the configured proxy",
		})
	}
	if strings.TrimSpace(remoteErr) != "" {
		actions = append(actions, RepairAction{
			ID:      "check_remote_tunnel",
			Command: "tunnel-client runtimes status " + alias + " --json",
			Reason:  "the remote tunnel lookup returned an error and should be rechecked with the current runtime/admin credentials",
		})
	}
	if len(actions) == 0 {
		actions = append(actions, RepairAction{
			ID:      "inspect",
			Command: "tunnel-client runtimes status " + alias + " --json",
			Reason:  "no immediate local repair was detected; rerun status for the freshest structured state",
		})
	}
	return actions
}

func nextStepCommands(actions []RepairAction, extras ...string) []string {
	out := make([]string, 0, len(actions)+len(extras))
	seen := map[string]bool{}
	for _, action := range actions {
		if action.Command != "" && !seen[action.Command] {
			out = append(out, action.Command)
			seen[action.Command] = true
		}
	}
	for _, extra := range extras {
		if strings.TrimSpace(extra) != "" && !seen[extra] {
			out = append(out, extra)
			seen[extra] = true
		}
	}
	return out
}

func inventoryClassification(local map[string]any, record pluginstate.AliasRecord, process pluginstate.ProcessRecord) string {
	if live, _ := local["live_admin_ui"].(map[string]any); boolValue(live["found"]) || boolValue(local["process_running"]) {
		return "live_runtime"
	}
	profile, _ := local["profile"].(map[string]any)
	if profile != nil && boolValue(profile["exists"]) {
		return "valid_profile"
	}
	if profile != nil && (stringValue(profile["path"]) != "" || stringValue(profile["name"]) != "") {
		return "missing_profile"
	}
	if record.Alias != "" || process.Alias != "" {
		return "stale_alias"
	}
	return "stale_alias"
}

func hasRemoteScope(organizationIDs, workspaceIDs []string, tenantID string) bool {
	return len(organizationIDs) > 0 || len(workspaceIDs) > 0 || strings.TrimSpace(tenantID) != ""
}

func validateCreateOrConnectScope(action string, organizationIDs, workspaceIDs []string) error {
	if len(organizationIDs) > 0 && len(workspaceIDs) > 0 {
		return fmt.Errorf("runtimes %s accepts exactly one remote scope family: --organization-id or --workspace-id", action)
	}
	return nil
}

func validateListScope(organizationIDs, workspaceIDs []string, tenantID string) error {
	filterCount := 0
	if len(organizationIDs) > 0 {
		filterCount++
	}
	if len(workspaceIDs) > 0 {
		filterCount++
	}
	if strings.TrimSpace(tenantID) != "" {
		filterCount++
	}
	if filterCount > 1 {
		return errors.New("runtimes list accepts exactly one remote scope family: --organization-id, --workspace-id, or --tenant-id")
	}
	if len(organizationIDs) > 1 {
		return errors.New("runtimes list accepts at most one --organization-id for remote listing")
	}
	if len(workspaceIDs) > 1 {
		return errors.New("runtimes list accepts at most one --workspace-id for remote listing")
	}
	return nil
}

func providedTunnel(alias string, opts ConnectOptions) *adminapi.Tunnel {
	return &adminapi.Tunnel{
		ID:              strings.TrimSpace(opts.TunnelID),
		Name:            firstNonEmpty(opts.Name, alias),
		Description:     defaultDescription(alias, opts.Description, ""),
		OrganizationIDs: append([]string(nil), opts.OrganizationIDs...),
		WorkspaceIDs:    append([]string(nil), opts.WorkspaceIDs...),
	}
}

func localTunnelFromAlias(record pluginstate.AliasRecord, alias string) *adminapi.Tunnel {
	return &adminapi.Tunnel{
		ID:              record.TunnelID,
		Name:            firstNonEmpty(record.Name, alias),
		Description:     defaultDescription(alias, record.Description, ""),
		OrganizationIDs: append([]string(nil), record.OrganizationIDs...),
		WorkspaceIDs:    append([]string(nil), record.WorkspaceIDs...),
		TenantIDs:       append([]string(nil), record.TenantIDs...),
	}
}

func isNotFound(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "404") || strings.Contains(message, "not found")
}

func mustContext() context.Context {
	return context.Background()
}

func defaultDescription(alias, preferred, fallback string) string {
	return firstNonEmpty(preferred, fallback, "MCP tunnel for "+alias)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func envValue(lookupEnv func(string) (string, bool), key string) string {
	if lookupEnv == nil {
		return ""
	}
	value, ok := lookupEnv(key)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func stringValue(value any) string {
	s, _ := value.(string)
	return s
}

func boolValue(value any) bool {
	b, _ := value.(bool)
	return b
}

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return 0
	}
}
