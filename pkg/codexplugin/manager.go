package codexplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"go.openai.org/api/tunnel-client/pkg/codexplugin/session"
	pluginstate "go.openai.org/api/tunnel-client/pkg/codexplugin/state"
	"go.openai.org/api/tunnel-client/pkg/config"
	adminapi "go.openai.org/api/tunnel-client/pkg/controlplane/admin"
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
	OrganizationIDs     []string
	WorkspaceIDs        []string
	TenantID            string
}

type AliasOptions struct {
	Alias               string
	AdminProfileName    string
	AdminKeyRef         string
	ControlPlaneBaseURL string
}

type effectiveAdminProfile struct {
	Name                string
	ControlPlaneBaseURL string
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

func (m *Manager) SetAdminProfile(name, baseURL, adminKey string, activate bool) (map[string]any, error) {
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
	resolvedAdminKey := firstNonEmpty(strings.TrimSpace(adminKey), existing.AdminKey, defaultAdminKeyRef)
	if err := pluginstate.ValidateSecretReference(resolvedAdminKey, "admin profile "+normalizedName+" admin_key"); err != nil {
		return nil, err
	}
	file.Profiles[normalizedName] = pluginstate.AdminProfile{
		Name:                normalizedName,
		ControlPlaneBaseURL: resolvedBaseURL,
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
	adminProfile, err := m.resolveAdminProfile(root, opts.AdminProfileName, opts.AdminKeyRef, opts.ControlPlaneBaseURL, previous.AdminProfile)
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
	adminProfile, err := m.resolveAdminProfile(root, opts.AdminProfileName, opts.AdminKeyRef, opts.ControlPlaneBaseURL, previous.AdminProfile)
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
	adminProfile, err := m.resolveAdminProfile(root, opts.AdminProfileName, opts.AdminKeyRef, opts.ControlPlaneBaseURL, "")
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
	adminProfile, err := m.resolveAdminProfile(root, opts.AdminProfileName, opts.AdminKeyRef, opts.ControlPlaneBaseURL, record.AdminProfile)
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
	adminProfile, err := m.resolveAdminProfile(root, opts.AdminProfileName, opts.AdminKeyRef, opts.ControlPlaneBaseURL, record.AdminProfile)
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

func (m *Manager) resolveAdminProfile(root pluginstate.Root, requestedName, adminKeyRef, baseURL, defaultName string) (effectiveAdminProfile, error) {
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
	resolvedAdminKey := firstNonEmpty(strings.TrimSpace(adminKeyRef), existing.AdminKey, defaultAdminKeyRef)
	if err := pluginstate.ValidateSecretReference(resolvedAdminKey, "admin profile "+normalizedName+" admin_key"); err != nil {
		return effectiveAdminProfile{}, err
	}
	if existing.Name == "" || existing.ControlPlaneBaseURL != resolvedBaseURL || existing.AdminKey != resolvedAdminKey {
		file.Profiles[normalizedName] = pluginstate.AdminProfile{
			Name:                normalizedName,
			ControlPlaneBaseURL: resolvedBaseURL,
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
		existingAdmin, err := m.resolveAdminProfile(root, "", "", "", existing.AdminProfile)
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
	return adminapi.NewAdminTunnelClient(&config.AdminConfig{
		BaseURL:  parsed,
		AdminKey: key,
	})
}

func (m *Manager) connectPayload(root pluginstate.Root, alias string, tunnel adminapi.Tunnel, adminProfile effectiveAdminProfile, record pluginstate.AliasRecord, process pluginstate.ProcessRecord, launch session.LaunchResult, remoteErr string) map[string]any {
	local := m.localRuntimeDetails(root, alias, record, process)
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
		"healthy":            launch.Healthy,
		"ready":              launch.Ready,
		"launched":           launch.Launched,
		"mode":               launch.Mode,
		"command":            launch.Command,
		"session_name":       launch.SessionName,
		"log_path":           launch.LogPath,
		"started":            launch.Started,
		"running":            launch.Running,
		"already_running":    launch.AlreadyRunning,
		"remote_error":       remoteErr,
		"tmux":               local["tmux"],
		"process_running":    local["process_running"],
		"process":            processToMap(process),
		"local":              local,
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
	payload := map[string]any{
		"alias":                   alias,
		"tunnel_id":               record.TunnelID,
		"admin_profile":           adminProfile.Name,
		"admin_profile_path":      adminProfile.Path,
		"remote":                  nil,
		"stale":                   stale,
		"error":                   errorText,
		"remote_error":            errorText,
		"repair_command":          repairCommand(alias, record, process),
		"config_path":             record.ConfigPath,
		"profile_name":            record.ProfileName,
		"profile_dir":             record.ProfileDir,
		"profile_path":            record.ProfilePath,
		"profile_exists":          local["profile"].(map[string]any)["exists"],
		"health_url_file":         record.HealthURLFile,
		"health_url":              local["health"].(map[string]any)["url"],
		"ui_url":                  local["health"].(map[string]any)["ui"],
		"runtime_state":           local["runtime_state"],
		"healthy":                 local["health"].(map[string]any)["healthz"].(map[string]any)["ok"],
		"ready":                   local["health"].(map[string]any)["readyz"].(map[string]any)["ok"],
		"remote_lookup_attempted": attempted,
		"remote_lookup_auth_kind": authKind,
		"remote_lookup_auth_ref":  authRef,
		"remote_skipped_reason":   skippedReason,
		"tmux":                    local["tmux"],
		"process_running":         local["process_running"],
		"process":                 nil,
		"local":                   local,
		"next_steps":              []string{doctorCommand(record.ProfileName, record.ProfileDir, record.ConfigPath, true), repairCommand(alias, record, process), "tunnel-client runtimes status " + alias},
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
	return map[string]any{
		"runtime_state": runtimeState(runtimeRunning, probe),
		"issues": localIssues(
			process,
			tmuxRunning,
			processRunning,
			profile["exists"].(bool),
			health,
			log["exists"].(bool),
		),
		"profile": profile,
		"health":  health,
		"log":     log,
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
	return strings.Join(parts, " ")
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

func localIssues(process pluginstate.ProcessRecord, tmuxRunning bool, processRunning bool, profileExists bool, health map[string]any, logExists bool) []string {
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
	return issues
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
