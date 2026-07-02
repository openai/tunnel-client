package codexplugin

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	pluginsbundle "go.openai.org/api/tunnel-client/plugins"
)

const (
	defaultMarketplace = "debug"
	defaultVersion     = "local"
	binHintFilename    = ".tunnel-client-bin"
)

type installedVersionCandidate struct {
	version string
	dir     string
	modTime time.Time
}

type Detection struct {
	CodexHome             string
	ConfigPath            string
	PluginDir             string
	PluginKey             string
	PluginMarketplace     string
	PluginVersion         string
	Detected              bool
	PluginName            string
	PluginInstalled       bool
	PluginBinaryHint      string
	PluginBinaryHintPath  string
	PluginBinaryHintFound bool
	EnabledConfigKeys     []string
	Installations         []PluginInstallation
	StaleConfigEntries    []StalePluginConfigEntry
	InstallHint           string
}

type PluginInstallation struct {
	Key            string `json:"key"`
	Marketplace    string `json:"marketplace"`
	Version        string `json:"version"`
	Dir            string `json:"dir"`
	ManifestPath   string `json:"manifest_path"`
	Installed      bool   `json:"installed"`
	BinaryHintPath string `json:"binary_hint_path,omitempty"`
	BinaryHint     string `json:"binary_hint,omitempty"`
}

type StalePluginConfigEntry struct {
	Key         string `json:"key"`
	Marketplace string `json:"marketplace"`
	Reason      string `json:"reason"`
}

type UninstallResult struct {
	CodexHome            string
	ConfigPath           string
	PluginDir            string
	PluginName           string
	RemovedPluginDir     bool
	RemovedConfigSection bool
}

func Detect(lookupEnv func(string) (string, bool)) Detection {
	codexHome := ResolveCodexHome(lookupEnv)
	manifest, _ := pluginsbundle.TunnelMCPManifest()
	detection := Detection{
		CodexHome:         codexHome,
		ConfigPath:        filepath.Join(codexHome, "config.toml"),
		PluginDir:         PluginTargetDir(codexHome),
		PluginName:        manifest.Name,
		PluginKey:         fmt.Sprintf("%s@%s", manifest.Name, defaultMarketplace),
		PluginMarketplace: defaultMarketplace,
		PluginVersion:     defaultVersion,
		InstallHint:       "tunnel-client codex plugin install",
		Detected:          codexLooksInstalled(codexHome),
	}
	if !detection.Detected {
		if _, err := exec.LookPath("codex"); err == nil {
			detection.Detected = true
		}
	}
	detection.EnabledConfigKeys = enabledPluginConfigKeys(detection.ConfigPath, manifest.Name)
	detection.Installations = findPluginInstallations(codexHome, manifest.Name, detection.EnabledConfigKeys)
	for _, installation := range detection.Installations {
		if !installation.Installed {
			detection.StaleConfigEntries = append(detection.StaleConfigEntries, StalePluginConfigEntry{
				Key:         installation.Key,
				Marketplace: installation.Marketplace,
				Reason:      "enabled in Codex config but no plugin manifest exists in the marketplace cache",
			})
			continue
		}
		if !detection.PluginInstalled || installation.Marketplace != defaultMarketplace {
			detection.PluginInstalled = true
			detection.PluginDir = installation.Dir
			detection.PluginKey = installation.Key
			detection.PluginMarketplace = installation.Marketplace
			detection.PluginVersion = installation.Version
			detection.PluginBinaryHint = installation.BinaryHint
			detection.PluginBinaryHintPath = installation.BinaryHintPath
			detection.PluginBinaryHintFound = installation.BinaryHintPath != "" && fileExists(installation.BinaryHintPath)
		}
	}
	if !detection.PluginInstalled && len(detection.EnabledConfigKeys) == 0 {
		installation := pluginInstallationFor(codexHome, manifest.Name, defaultMarketplace)
		detection.PluginInstalled = installation.Installed
		detection.PluginDir = installation.Dir
		detection.PluginBinaryHint = installation.BinaryHint
		detection.PluginBinaryHintPath = installation.BinaryHintPath
		detection.PluginBinaryHintFound = installation.BinaryHintPath != "" && fileExists(installation.BinaryHintPath)
	}
	return detection
}

func ResolveCodexHome(lookupEnv func(string) (string, bool)) string {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	if value, ok := lookupEnv("CODEX_HOME"); ok && strings.TrimSpace(value) != "" {
		return filepath.Clean(strings.TrimSpace(value))
	}
	if value, ok := lookupEnv("HOME"); ok && strings.TrimSpace(value) != "" {
		return filepath.Join(filepath.Clean(strings.TrimSpace(value)), ".codex")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(filepath.Clean(home), ".codex")
	}
	return filepath.Join(".", ".codex")
}

func PluginTargetDir(codexHome string) string {
	manifest, err := pluginsbundle.TunnelMCPManifest()
	if err != nil {
		return filepath.Join(codexHome, "plugins", "cache", defaultMarketplace, "tunnel-mcp", defaultVersion)
	}
	return PluginTargetDirFor(codexHome, defaultMarketplace, manifest.Name, defaultVersion)
}

func PluginTargetDirFor(codexHome string, marketplace string, pluginName string, version string) string {
	return filepath.Join(codexHome, "plugins", "cache", marketplace, pluginName, version)
}

func Install(codexHome string, tunnelClientBinary string) (Detection, error) {
	return InstallForMarketplace(codexHome, defaultMarketplace, tunnelClientBinary)
}

func InstallForMarketplace(codexHome string, marketplace string, tunnelClientBinary string) (Detection, error) {
	manifest, err := pluginsbundle.TunnelMCPManifest()
	if err != nil {
		return Detection{}, err
	}
	marketplace = strings.TrimSpace(marketplace)
	if marketplace == "" {
		marketplace = defaultMarketplace
	}
	if err := validateCacheSegment(marketplace, "marketplace"); err != nil {
		return Detection{}, err
	}
	target := filepath.Join(codexHome, "plugins", "cache", marketplace, manifest.Name, defaultVersion)
	if err := os.RemoveAll(target); err != nil {
		return Detection{}, fmt.Errorf("remove existing plugin target %s: %w", target, err)
	}
	if err := pluginsbundle.TunnelMCPExportToDir(target); err != nil {
		return Detection{}, err
	}
	if err := writeBinaryHint(target, tunnelClientBinary); err != nil {
		return Detection{}, err
	}
	configPath := filepath.Join(codexHome, "config.toml")
	if err := updateConfig(configPath, manifest.Name, marketplace); err != nil {
		return Detection{}, err
	}
	detection := Detect(func(key string) (string, bool) {
		if key == "CODEX_HOME" {
			return codexHome, true
		}
		return "", false
	})
	if detection.PluginBinaryHint == "" {
		return Detection{}, fmt.Errorf("installed plugin %s is missing %s", target, binHintFilename)
	}
	return detection, nil
}

func validateCacheSegment(value string, field string) error {
	if value == "" || value == "." || value == ".." {
		return fmt.Errorf("%s is required", field)
	}
	if strings.HasPrefix(value, "-") || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s contains unsupported characters: %q", field, value)
	}
	if strings.ContainsAny(value, `/\"`) {
		return fmt.Errorf("%s contains unsupported characters: %q", field, value)
	}
	return nil
}

func Uninstall(codexHome string) (UninstallResult, error) {
	manifest, err := pluginsbundle.TunnelMCPManifest()
	if err != nil {
		return UninstallResult{}, err
	}
	target := filepath.Join(codexHome, "plugins", "cache", defaultMarketplace, manifest.Name, defaultVersion)
	removedPluginDir := false
	if _, statErr := os.Stat(target); statErr == nil {
		if err := os.RemoveAll(target); err != nil {
			return UninstallResult{}, fmt.Errorf("remove installed plugin target %s: %w", target, err)
		}
		removedPluginDir = true
	} else if !os.IsNotExist(statErr) {
		return UninstallResult{}, fmt.Errorf("stat installed plugin target %s: %w", target, statErr)
	}
	configPath := filepath.Join(codexHome, "config.toml")
	removedConfigSection, err := removeConfigSection(configPath, manifest.Name, defaultMarketplace)
	if err != nil {
		return UninstallResult{}, err
	}
	return UninstallResult{
		CodexHome:            codexHome,
		ConfigPath:           configPath,
		PluginDir:            target,
		PluginName:           manifest.Name,
		RemovedPluginDir:     removedPluginDir,
		RemovedConfigSection: removedConfigSection,
	}, nil
}

func Export(dir string, tunnelClientBinary string) error {
	if err := pluginsbundle.TunnelMCPExportToDir(dir); err != nil {
		return err
	}
	return writeBinaryHint(dir, tunnelClientBinary)
}

func codexLooksInstalled(codexHome string) bool {
	if codexHome == "" {
		return false
	}
	if fileExists(filepath.Join(codexHome, "config.toml")) {
		return true
	}
	return dirExists(codexHome)
}

func enabledPluginConfigKeys(configPath string, pluginName string) []string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	prefix := `plugins."` + pluginName + `@`
	out := []string{}
	for _, section := range splitSections(string(data)) {
		if !strings.HasPrefix(section.name, prefix) || !strings.HasSuffix(section.name, `"`) {
			continue
		}
		enabled := false
		for _, line := range section.lines[1:] {
			if strings.TrimSpace(line) == "enabled = true" {
				enabled = true
				break
			}
		}
		if enabled {
			key := strings.TrimPrefix(section.name, `plugins."`)
			key = strings.TrimSuffix(key, `"`)
			out = append(out, key)
		}
	}
	return out
}

func findPluginInstallations(codexHome string, pluginName string, configKeys []string) []PluginInstallation {
	if len(configKeys) == 0 {
		return nil
	}
	out := make([]PluginInstallation, 0, len(configKeys))
	for _, key := range configKeys {
		marketplace := marketplaceFromPluginKey(key, pluginName)
		if marketplace == "" {
			continue
		}
		out = append(out, pluginInstallationFor(codexHome, pluginName, marketplace))
	}
	return out
}

func pluginInstallationFor(codexHome string, pluginName string, marketplace string) PluginInstallation {
	version, dir := installedPluginVersionDir(codexHome, pluginName, marketplace)
	if dir == "" {
		version = defaultVersion
		dir = PluginTargetDirFor(codexHome, marketplace, pluginName, version)
	}
	manifestPath := filepath.Join(dir, ".codex-plugin", "plugin.json")
	hintPath := filepath.Join(dir, binHintFilename)
	hint := readBinaryHintFromPath(hintPath)
	return PluginInstallation{
		Key:            fmt.Sprintf("%s@%s", pluginName, marketplace),
		Marketplace:    marketplace,
		Version:        version,
		Dir:            dir,
		ManifestPath:   manifestPath,
		Installed:      fileExists(manifestPath),
		BinaryHintPath: hintPath,
		BinaryHint:     hint,
	}
}

func marketplaceFromPluginKey(key string, pluginName string) string {
	prefix := pluginName + "@"
	if !strings.HasPrefix(key, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(key, prefix))
}

func installedPluginVersionDir(codexHome string, pluginName string, marketplace string) (string, string) {
	base := filepath.Join(codexHome, "plugins", "cache", marketplace, pluginName)
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", ""
	}
	versions := []installedVersionCandidate{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		version := entry.Name()
		dir := filepath.Join(base, version)
		if fileExists(filepath.Join(dir, ".codex-plugin", "plugin.json")) {
			modTime := time.Time{}
			if info, infoErr := entry.Info(); infoErr == nil {
				modTime = info.ModTime()
			}
			versions = append(versions, installedVersionCandidate{
				version: version,
				dir:     dir,
				modTime: modTime,
			})
		}
	}
	if len(versions) == 0 {
		return "", ""
	}
	best := versions[0]
	for _, candidate := range versions[1:] {
		if compareInstalledPluginVersion(best, candidate) < 0 {
			best = candidate
		}
	}
	return best.version, best.dir
}

func compareInstalledPluginVersion(left installedVersionCandidate, right installedVersionCandidate) int {
	if semverCmp, ok := compareSemverLikeVersions(left.version, right.version); ok && semverCmp != 0 {
		return semverCmp
	}
	if !left.modTime.Equal(right.modTime) {
		if left.modTime.Before(right.modTime) {
			return -1
		}
		return 1
	}
	return strings.Compare(left.version, right.version)
}

func compareSemverLikeVersions(leftVersion string, rightVersion string) (int, bool) {
	left, ok := parseSemverLikeVersion(leftVersion)
	if !ok {
		return 0, false
	}
	right, ok := parseSemverLikeVersion(rightVersion)
	if !ok {
		return 0, false
	}
	return left.compare(right), true
}

type semverLikeVersion struct {
	major      int
	minor      int
	patch      int
	prerelease string
}

func parseSemverLikeVersion(raw string) (semverLikeVersion, bool) {
	value := strings.TrimSpace(strings.TrimPrefix(raw, "v"))
	if value == "" {
		return semverLikeVersion{}, false
	}
	buildless, _, _ := strings.Cut(value, "+")
	core := buildless
	prerelease := ""
	if beforePrerelease, suffix, found := strings.Cut(buildless, "-"); found {
		core = beforePrerelease
		prerelease = suffix
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semverLikeVersion{}, false
	}
	major, ok := parseSemverNumericPart(parts[0])
	if !ok {
		return semverLikeVersion{}, false
	}
	minor, ok := parseSemverNumericPart(parts[1])
	if !ok {
		return semverLikeVersion{}, false
	}
	patch, ok := parseSemverNumericPart(parts[2])
	if !ok {
		return semverLikeVersion{}, false
	}
	return semverLikeVersion{
		major:      major,
		minor:      minor,
		patch:      patch,
		prerelease: prerelease,
	}, true
}

func parseSemverNumericPart(raw string) (int, bool) {
	if raw == "" {
		return 0, false
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return value, true
}

func (version semverLikeVersion) compare(other semverLikeVersion) int {
	for _, cmp := range []int{
		compareInts(version.major, other.major),
		compareInts(version.minor, other.minor),
		compareInts(version.patch, other.patch),
	} {
		if cmp != 0 {
			return cmp
		}
	}
	switch {
	case version.prerelease == "" && other.prerelease == "":
		return 0
	case version.prerelease == "":
		return 1
	case other.prerelease == "":
		return -1
	default:
		return compareSemverPrerelease(version.prerelease, other.prerelease)
	}
}

func compareSemverPrerelease(left string, right string) int {
	leftParts := strings.Split(left, ".")
	rightParts := strings.Split(right, ".")
	limit := len(leftParts)
	if len(rightParts) < limit {
		limit = len(rightParts)
	}
	for i := 0; i < limit; i++ {
		if cmp := compareSemverPrereleaseIdentifier(leftParts[i], rightParts[i]); cmp != 0 {
			return cmp
		}
	}
	return compareInts(len(leftParts), len(rightParts))
}

func compareSemverPrereleaseIdentifier(left string, right string) int {
	leftNumber, leftIsNumber := parseSemverNumericPart(left)
	rightNumber, rightIsNumber := parseSemverNumericPart(right)
	switch {
	case leftIsNumber && rightIsNumber:
		return compareInts(leftNumber, rightNumber)
	case leftIsNumber:
		return -1
	case rightIsNumber:
		return 1
	default:
		return strings.Compare(left, right)
	}
}

func compareInts(left int, right int) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func updateConfig(configPath string, pluginName string, marketplace string) error {
	sections, err := loadConfigSections(configPath)
	if err != nil {
		return err
	}
	sectionName := pluginSectionName(pluginName, marketplace)
	filtered := removeSection(sections, sectionName)
	filtered = append(filtered, tomlSection{
		name:  sectionName,
		lines: []string{fmt.Sprintf("[%s]", sectionName), "enabled = true"},
	})
	return writeConfigSections(configPath, filtered)
}

type tomlSection struct {
	name  string
	lines []string
}

func splitSections(text string) []tomlSection {
	sections := []tomlSection{{}}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			sections = append(sections, tomlSection{
				name:  strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"),
				lines: []string{line},
			})
			continue
		}
		sections[len(sections)-1].lines = append(sections[len(sections)-1].lines, line)
	}
	return sections
}

func pluginSectionName(pluginName string, marketplace string) string {
	return fmt.Sprintf(`plugins."%s@%s"`, pluginName, marketplace)
}

func loadConfigSections(configPath string) ([]tomlSection, error) {
	existingText := ""
	if data, err := os.ReadFile(configPath); err == nil {
		existingText = string(data)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read Codex config %s: %w", configPath, err)
	}
	return splitSections(existingText), nil
}

func removeSection(sections []tomlSection, sectionName string) []tomlSection {
	filtered := make([]tomlSection, 0, len(sections))
	for _, section := range sections {
		if section.name == sectionName {
			continue
		}
		filtered = append(filtered, section)
	}
	return filtered
}

func writeConfigSections(configPath string, sections []tomlSection) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("create Codex config directory for %s: %w", configPath, err)
	}
	if err := os.WriteFile(configPath, []byte(renderSections(sections)), 0o644); err != nil {
		return fmt.Errorf("write Codex config %s: %w", configPath, err)
	}
	return nil
}

func removeConfigSection(configPath string, pluginName string, marketplace string) (bool, error) {
	sections, err := loadConfigSections(configPath)
	if err != nil {
		return false, err
	}
	sectionName := pluginSectionName(pluginName, marketplace)
	filtered := removeSection(sections, sectionName)
	if len(filtered) == len(sections) {
		return false, nil
	}
	if err := writeConfigSections(configPath, filtered); err != nil {
		return false, err
	}
	return true, nil
}

func renderSections(sections []tomlSection) string {
	rendered := make([]string, 0, len(sections))
	for _, section := range sections {
		chunk := strings.Trim(strings.Join(section.lines, "\n"), "\n")
		if chunk != "" {
			rendered = append(rendered, chunk)
		}
	}
	return strings.Join(rendered, "\n\n") + "\n"
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func ReadInstalledBinaryHint(codexHome string) string {
	if strings.TrimSpace(codexHome) == "" {
		return ""
	}
	return readBinaryHintFromPath(filepath.Join(PluginTargetDir(codexHome), binHintFilename))
}

func readBinaryHintFromPath(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	hint := strings.TrimSpace(string(data))
	normalized, err := NormalizeBinaryPath(hint)
	if err != nil {
		return hint
	}
	return normalized
}

func writeBinaryHint(dir string, tunnelClientBinary string) error {
	resolved, err := NormalizeBinaryPath(tunnelClientBinary)
	if err != nil {
		return err
	}
	if resolved == "" {
		return nil
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("stat tunnel-client binary %s: %w", resolved, err)
	}
	if info.IsDir() {
		return fmt.Errorf("tunnel-client binary path is a directory: %s", resolved)
	}
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("tunnel-client binary is not executable: %s", resolved)
	}
	hintPath := filepath.Join(dir, binHintFilename)
	if err := os.WriteFile(hintPath, []byte(resolved+"\n"), 0o644); err != nil {
		return fmt.Errorf("write tunnel-client binary hint %s: %w", hintPath, err)
	}
	return nil
}

func NormalizeBinaryPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", nil
	}
	absPath, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve tunnel-client binary %q: %w", trimmed, err)
	}
	resolved, err := filepath.EvalSymlinks(absPath)
	switch {
	case err == nil:
		return filepath.Clean(resolved), nil
	case errors.Is(err, fs.ErrNotExist):
		return filepath.Clean(absPath), nil
	default:
		return "", fmt.Errorf("resolve tunnel-client binary symlinks for %q: %w", absPath, err)
	}
}
