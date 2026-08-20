package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

var (
	aliasCharsPattern = regexp.MustCompile(`[^a-z0-9-]+`)
	secretPatterns    = []*regexp.Regexp{
		regexp.MustCompile(`sk-[A-Za-z0-9_-]{12,}`),
		regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{12,}`),
		regexp.MustCompile(`(?i)\bAuthorization\s*:`),
		regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|password|secret)=\S+`),
		regexp.MustCompile(`(?i)\b(OPENAI_ADMIN_KEY|OPENAI_API_KEY|CONTROL_PLANE_API_KEY)=`),
		regexp.MustCompile(`://[^/\s:@]+:[^/\s:@]+@`),
	}
	secretFlagPattern      = regexp.MustCompile(`(?i)^-{1,2}[a-z0-9_-]*(api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|password|secret)[a-z0-9_-]*$`)
	secretReferencePattern = regexp.MustCompile(`^(env:[A-Za-z_][A-Za-z0-9_]*|file:.+|\$[A-Za-z_][A-Za-z0-9_]*|\$\{[A-Za-z_][A-Za-z0-9_]*\})$`)
)

type Error struct {
	message string
}

func (e *Error) Error() string {
	return e.message
}

func NewError(message string) error {
	return &Error{message: message}
}

type AliasRecord struct {
	Alias           string   `json:"alias"`
	TunnelID        string   `json:"tunnel_id"`
	Name            string   `json:"name"`
	AdminProfile    string   `json:"admin_profile,omitempty"`
	Description     string   `json:"description,omitempty"`
	OrganizationIDs []string `json:"organization_ids,omitempty"`
	WorkspaceIDs    []string `json:"workspace_ids,omitempty"`
	TenantIDs       []string `json:"tenant_ids,omitempty"`
	ConfigPath      string   `json:"config_path,omitempty"`
	ProfileName     string   `json:"profile_name,omitempty"`
	ProfileDir      string   `json:"profile_dir,omitempty"`
	ProfilePath     string   `json:"profile_path,omitempty"`
	HealthURLFile   string   `json:"health_url_file,omitempty"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
}

type ProcessRecord struct {
	Alias         string `json:"alias"`
	TunnelID      string `json:"tunnel_id"`
	ConfigPath    string `json:"config_path"`
	HealthURLFile string `json:"health_url_file"`
	TargetKind    string `json:"target_kind"`
	TargetValue   string `json:"target_value"`
	Command       string `json:"command"`
	StartedAt     string `json:"started_at"`
	AdminProfile  string `json:"admin_profile,omitempty"`
	ProfileName   string `json:"profile_name,omitempty"`
	ProfileDir    string `json:"profile_dir,omitempty"`
	ProfilePath   string `json:"profile_path,omitempty"`
	Mode          string `json:"mode,omitempty"`
	SessionName   string `json:"session_name,omitempty"`
	PID           int    `json:"pid,omitempty"`
	LogPath       string `json:"log_path,omitempty"`
}

type AdminProfile struct {
	Name                string `json:"name"`
	ControlPlaneBaseURL string `json:"control_plane_base_url"`
	ControlPlaneURLPath string `json:"control_plane_url_path,omitempty"`
	AdminKey            string `json:"admin_key"`
	UpdatedAt           string `json:"updated_at,omitempty"`
}

type AdminProfilesFile struct {
	ActiveProfile string                  `json:"active_profile,omitempty"`
	Profiles      map[string]AdminProfile `json:"profiles"`
}

type Root struct {
	Path string
}

func UTCNow() string {
	return time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
}

func NormalizeAlias(alias string) (string, error) {
	lowered := strings.ToLower(strings.TrimSpace(alias))
	lowered = strings.ReplaceAll(lowered, "_", "-")
	lowered = aliasCharsPattern.ReplaceAllString(lowered, "-")
	lowered = regexp.MustCompile(`-{2,}`).ReplaceAllString(lowered, "-")
	lowered = strings.Trim(lowered, "-")
	if lowered == "" {
		return "", NewError("alias must contain at least one ASCII letter or number")
	}
	return lowered, nil
}

func ResolveRoot(lookupEnv func(string) (string, bool)) Root {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	if value, ok := lookupEnv("TUNNEL_CLIENT_STATE_DIR"); ok && strings.TrimSpace(value) != "" {
		return Root{Path: filepath.Clean(strings.TrimSpace(value))}
	}
	for _, candidate := range legacyRootCandidates(lookupEnv) {
		if rootExists(candidate) {
			return Root{Path: candidate}
		}
	}
	if value, ok := lookupEnv("XDG_STATE_HOME"); ok && strings.TrimSpace(value) != "" {
		return Root{Path: filepath.Join(filepath.Clean(strings.TrimSpace(value)), "tunnel-client")}
	}
	if value, ok := lookupEnv("HOME"); ok && strings.TrimSpace(value) != "" {
		return Root{Path: defaultStatePath(filepath.Clean(strings.TrimSpace(value)))}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return Root{Path: defaultStatePath(filepath.Clean(home))}
	}
	return Root{Path: filepath.Join(".", ".tunnel-client")}
}

func legacyRootCandidates(lookupEnv func(string) (string, bool)) []string {
	candidates := []string{}
	if value, ok := lookupEnv("CODEX_HOME"); ok && strings.TrimSpace(value) != "" {
		candidates = append(candidates, filepath.Join(filepath.Clean(strings.TrimSpace(value)), "tunnel-mcp"))
	}
	if value, ok := lookupEnv("HOME"); ok && strings.TrimSpace(value) != "" {
		home := filepath.Clean(strings.TrimSpace(value))
		candidates = append(candidates,
			filepath.Join(home, ".codex", "tunnel-mcp"),
			filepath.Join(home, ".tunnel-client"),
		)
	}
	return candidates
}

func defaultStatePath(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "tunnel-client")
	}
	return filepath.Join(home, ".local", "state", "tunnel-client")
}

func rootExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func EnsureDirs(root Root) error {
	for _, subdir := range []string{
		root.Path,
		filepath.Join(root.Path, "configs"),
		filepath.Join(root.Path, "health"),
		filepath.Join(root.Path, "logs"),
	} {
		if err := os.MkdirAll(subdir, 0o755); err != nil {
			return fmt.Errorf("create state directory %s: %w", subdir, err)
		}
	}
	return nil
}

func AliasesPath(root Root) string {
	return filepath.Join(root.Path, "aliases.yaml")
}

func ProcessesPath(root Root) string {
	return filepath.Join(root.Path, "processes.yaml")
}

func AdminProfilesPath(root Root) string {
	return filepath.Join(root.Path, "admin_profiles.yaml")
}

func HistoryPath(root Root) string {
	return filepath.Join(root.Path, "history.md")
}

func LoadAliases(root Root) (map[string]AliasRecord, error) {
	raw := map[string]AliasRecord{}
	if err := loadJSONMap(AliasesPath(root), &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func SaveAliases(root Root, records map[string]AliasRecord) error {
	sorted := make(map[string]AliasRecord, len(records))
	keys := make([]string, 0, len(records))
	for key := range records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sorted[key] = records[key]
	}
	return saveJSONMap(AliasesPath(root), sorted)
}

func LoadProcesses(root Root) (map[string]ProcessRecord, error) {
	raw := map[string]ProcessRecord{}
	if err := loadJSONMap(ProcessesPath(root), &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func SaveProcesses(root Root, records map[string]ProcessRecord) error {
	sorted := make(map[string]ProcessRecord, len(records))
	keys := make([]string, 0, len(records))
	for key := range records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sorted[key] = records[key]
	}
	return saveJSONMap(ProcessesPath(root), sorted)
}

func LoadAdminProfiles(root Root) (AdminProfilesFile, error) {
	if _, err := os.Stat(AdminProfilesPath(root)); err != nil {
		if os.IsNotExist(err) {
			return AdminProfilesFile{Profiles: map[string]AdminProfile{}}, nil
		}
		return AdminProfilesFile{}, fmt.Errorf("read state file %s: %w", AdminProfilesPath(root), err)
	}
	var raw map[string]json.RawMessage
	if err := loadJSONMap(AdminProfilesPath(root), &raw); err != nil {
		return AdminProfilesFile{}, err
	}
	file := AdminProfilesFile{Profiles: map[string]AdminProfile{}}
	if active, ok := raw["active_profile"]; ok {
		if err := json.Unmarshal(active, &file.ActiveProfile); err != nil {
			return AdminProfilesFile{}, &Error{message: fmt.Sprintf("state file %s must contain a string active_profile", AdminProfilesPath(root))}
		}
	}
	if profilesRaw, ok := raw["profiles"]; ok {
		if err := json.Unmarshal(profilesRaw, &file.Profiles); err != nil {
			return AdminProfilesFile{}, &Error{message: fmt.Sprintf("state file %s must contain a profiles object", AdminProfilesPath(root))}
		}
		return file, nil
	}
	for key, value := range raw {
		if key == "active_profile" {
			continue
		}
		var profile AdminProfile
		if err := json.Unmarshal(value, &profile); err != nil {
			return AdminProfilesFile{}, &Error{message: fmt.Sprintf("state file %s must contain a profiles object", AdminProfilesPath(root))}
		}
		file.Profiles[key] = profile
	}
	return file, nil
}

func SaveAdminProfiles(root Root, file AdminProfilesFile) error {
	if file.Profiles == nil {
		file.Profiles = map[string]AdminProfile{}
	}
	sorted := make(map[string]AdminProfile, len(file.Profiles))
	keys := make([]string, 0, len(file.Profiles))
	for key := range file.Profiles {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sorted[key] = file.Profiles[key]
	}
	return saveJSONMap(AdminProfilesPath(root), map[string]any{
		"active_profile": file.ActiveProfile,
		"profiles":       sorted,
	})
}

func AppendHistory(root Root, action, alias, tunnelID, detail string) error {
	if err := EnsureDirs(root); err != nil {
		return err
	}
	line := fmt.Sprintf("- %s action=%s alias=%s tunnel_id=%s", UTCNow(), action, alias, valueOrDash(tunnelID))
	if clean := strings.TrimSpace(strings.ReplaceAll(detail, "\n", " ")); clean != "" {
		line = line + " detail=" + clean
	}
	f, err := os.OpenFile(HistoryPath(root), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("append history %s: %w", HistoryPath(root), err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		_ = f.Close()
		return fmt.Errorf("append history %s: %w", HistoryPath(root), err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close history %s: %w", HistoryPath(root), err)
	}
	return nil
}

func RejectInlineSecretMaterial(value string, field string) error {
	for _, pattern := range secretPatterns {
		if pattern.MatchString(value) {
			return &Error{message: fmt.Sprintf("%s appears to contain inline secret material; use env or file references instead", field)}
		}
	}
	for _, pair := range commandTokenPairs(value) {
		token := pair[0]
		next := pair[1]
		if !secretFlagPattern.MatchString(token) || strings.Contains(token, "=") {
			continue
		}
		if next != "" && !strings.HasPrefix(next, "-") && !IsSecretReference(next) {
			return &Error{message: fmt.Sprintf("%s appears to contain inline secret material; use env or file references instead", field)}
		}
	}
	return nil
}

func ValidateSecretReference(value string, field string) error {
	if strings.HasPrefix(value, "env:") || strings.HasPrefix(value, "file:") {
		return nil
	}
	return NewError(fmt.Sprintf("%s must be an env:NAME or file:/path reference", field))
}

func IsSecretReference(value string) bool {
	return secretReferencePattern.MatchString(value)
}

func AliasRecordFromTunnel(
	alias string,
	tunnelID string,
	name string,
	description string,
	organizationIDs []string,
	workspaceIDs []string,
	tenantIDs []string,
	adminProfile string,
	configPath string,
	profileName string,
	profileDir string,
	profilePath string,
	healthURLFile string,
) AliasRecord {
	return AliasRecord{
		Alias:           alias,
		TunnelID:        tunnelID,
		Name:            name,
		AdminProfile:    adminProfile,
		Description:     description,
		OrganizationIDs: append([]string(nil), organizationIDs...),
		WorkspaceIDs:    append([]string(nil), workspaceIDs...),
		TenantIDs:       append([]string(nil), tenantIDs...),
		ConfigPath:      configPath,
		ProfileName:     profileName,
		ProfileDir:      profileDir,
		ProfilePath:     profilePath,
		HealthURLFile:   healthURLFile,
		UpdatedAt:       UTCNow(),
	}
}

func loadJSONMap(path string, dest any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read state file %s: %w", path, err)
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return &Error{message: fmt.Sprintf("state file %s is not valid JSON-compatible YAML; repair or remove it", path)}
	}
	return nil
}

func saveJSONMap(path string, data any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state directory for %s: %w", path, err)
	}
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state file %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(payload, '\n'), 0o600); err != nil {
		return fmt.Errorf("write state file %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace state file %s: %w", path, err)
	}
	return nil
}

func commandTokenPairs(value string) [][2]string {
	tokens := strings.Fields(value)
	pairs := make([][2]string, 0, len(tokens))
	for i, token := range tokens {
		next := ""
		if i+1 < len(tokens) {
			next = tokens[i+1]
		}
		pairs = append(pairs, [2]string{token, next})
	}
	return pairs
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
