package version

import (
	_ "embed"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
)

const (
	// ClientName identifies this binary in structured control-plane metadata.
	ClientName              = "oai-tunnel-client"
	fallbackSemanticVersion = "0.0.1"

	// FlavorFull is the default build flavor for the existing complete client.
	FlavorFull = "full"
	// FlavorRuntime identifies the narrow runtime artifact without cloudflared.
	FlavorRuntime = "runtime"
	// FlavorRuntimeCloudflared identifies the narrow runtime artifact with the
	// approved bundled cloudflared supervisor.
	FlavorRuntimeCloudflared = "runtime-cloudflared"
)

var (
	// semanticVersion is intentionally a var (not const) so it can be overridden
	// at build time via -ldflags, allowing tagged releases to embed the
	// version string without changing source code for each release.
	semanticVersion      = fallbackSemanticVersion
	userAgentPrefix      = ClientName + "/"
	detectCheckoutGitSHA = detectGitSHAFromCheckout
)

//go:embed VERSION
var sourceSemanticVersion string

var (
	// GitSHA is populated at build time via ldflags or static Go build metadata.
	// Runtime artifacts never discover it from the checkout or start git.
	GitSHA = ""
	// GoVersion is populated at build time via ldflags. When a caller does not
	// inject it, the compiled runtime version is used without starting another
	// process.
	GoVersion = ""
	// BuildFlags is populated at build time via ldflags for release artifact
	// receipts, for example "-trimpath -buildvcs=false".
	BuildFlags = ""
	// Flavor is populated at build time via ldflags. Existing full-client
	// builds default to FlavorFull; runtime entrypoints link one of the narrow
	// runtime flavor constants.
	Flavor = FlavorFull
	// SemanticVersion exposes the release version without build metadata.
	SemanticVersion = semanticVersion
	// Version exposes the semver plus build metadata when available.
	Version = semanticVersion
	// UserAgent identifies the tunnel client in outbound HTTP requests.
	UserAgent = userAgentPrefix + semanticVersion
)

func init() {
	initVersion(debug.ReadBuildInfo)
}

type readBuildInfoFunc func() (*debug.BuildInfo, bool)

func initVersion(readBuildInfo readBuildInfoFunc) {
	flavor := effectiveFlavor()
	if GitSHA == "" {
		GitSHA = detectBuildGitSHAFrom(readBuildInfo)
	}
	if GitSHA == "" && flavor == FlavorFull {
		GitSHA = detectCheckoutGitSHA()
	}
	if GoVersion == "" {
		GoVersion = runtime.Version()
	}
	Flavor = flavor
	baseVersion := effectiveSemanticVersion()
	SemanticVersion = baseVersion
	Version = buildVersion(baseVersion, GitSHA)
	UserAgent = userAgentPrefix + Version
}

// BuildMetadata is the static identity linked into a tunnel-client artifact.
// Runtime entrypoints expose these values through their --version output.
type BuildMetadata struct {
	SemanticVersion string
	Version         string
	GitSHA          string
	GoVersion       string
	BuildFlags      string
	Flavor          string
}

// CurrentBuildMetadata returns the artifact identity without starting
// subprocesses.
func CurrentBuildMetadata() BuildMetadata {
	goVersion := strings.TrimSpace(GoVersion)
	if goVersion == "" {
		goVersion = runtime.Version()
	}
	return BuildMetadata{
		SemanticVersion: strings.TrimSpace(SemanticVersion),
		Version:         strings.TrimSpace(Version),
		GitSHA:          strings.TrimSpace(GitSHA),
		GoVersion:       goVersion,
		BuildFlags:      strings.TrimSpace(BuildFlags),
		Flavor:          effectiveFlavor(),
	}
}

func effectiveFlavor() string {
	flavor := strings.TrimSpace(Flavor)
	if flavor == "" {
		return FlavorFull
	}
	return flavor
}

func effectiveSemanticVersion() string {
	buildVersion := strings.TrimSpace(semanticVersion)
	if buildVersion != "" && buildVersion != fallbackSemanticVersion {
		return buildVersion
	}
	sourceVersion := strings.TrimSpace(sourceSemanticVersion)
	if sourceVersion != "" {
		return sourceVersion
	}
	if buildVersion != "" {
		return buildVersion
	}
	return fallbackSemanticVersion
}

func detectBuildGitSHAFrom(readBuildInfo readBuildInfoFunc) string {
	info, ok := readBuildInfo()
	if !ok {
		return ""
	}

	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			return setting.Value
		}
	}

	return ""
}

func detectGitSHAFromCheckout() string {
	return detectGitSHAFromCandidateDirs(gitCandidateDirs())
}

func gitCandidateDirs() []string {
	var dirs []string
	if _, file, _, ok := runtime.Caller(0); ok && filepath.IsAbs(file) {
		dirs = append(dirs, filepath.Dir(file))
	}
	if len(os.Args) > 0 && filepath.IsAbs(os.Args[0]) {
		dirs = append(dirs, filepath.Dir(os.Args[0]))
	}
	if executable, err := os.Executable(); err == nil && filepath.IsAbs(executable) {
		dirs = append(dirs, filepath.Dir(executable))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	return dirs
}

func detectGitSHAFromCandidateDirs(dirs []string) string {
	seen := map[string]struct{}{}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		root := findGitRootByWalkingParents(dir)
		if root == "" {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		if !isTunnelClientGitRoot(root) {
			continue
		}
		sha := gitSHAFromRoot(root)
		if sha != "" {
			return sha
		}
	}
	return ""
}

func findGitRootByWalkingParents(dir string) string {
	for current := filepath.Clean(dir); current != "" && current != "."; {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
	return ""
}

func gitSHAFromRoot(root string) string {
	gitDir := gitDirForRoot(root)
	if gitDir == "" {
		return ""
	}
	commonDir := gitCommonDir(gitDir)
	if commonDir == "" {
		return ""
	}
	head, ok := readGitMetadataFile(filepath.Join(gitDir, "HEAD"))
	if !ok {
		return ""
	}
	return resolveGitRevision(gitDir, commonDir, head, 0)
}

func gitDirForRoot(root string) string {
	dotGit := filepath.Join(root, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return dotGit
	}

	gitFile, ok := readGitMetadataFile(dotGit)
	if !ok || !strings.HasPrefix(gitFile, "gitdir:") {
		return ""
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(gitFile, "gitdir:"))
	if gitDir == "" {
		return ""
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(root, gitDir)
	}
	gitDir = filepath.Clean(gitDir)
	info, err = os.Stat(gitDir)
	if err != nil || !info.IsDir() {
		return ""
	}
	return gitDir
}

func gitCommonDir(gitDir string) string {
	commonDir, ok := readGitMetadataFile(filepath.Join(gitDir, "commondir"))
	if !ok {
		return gitDir
	}
	if commonDir == "" {
		return ""
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}
	commonDir = filepath.Clean(commonDir)
	info, err := os.Stat(commonDir)
	if err != nil || !info.IsDir() {
		return ""
	}
	return commonDir
}

func resolveGitRevision(gitDir, commonDir, revision string, depth int) string {
	const maxSymbolicRefDepth = 5

	revision = strings.TrimSpace(revision)
	if isGitObjectID(revision) {
		return strings.ToLower(revision)
	}
	if depth >= maxSymbolicRefDepth || !strings.HasPrefix(revision, "ref:") {
		return ""
	}

	ref := strings.TrimSpace(strings.TrimPrefix(revision, "ref:"))
	if !isSafeGitRef(ref) {
		return ""
	}
	for _, dir := range gitMetadataDirs(gitDir, commonDir) {
		value, ok := readGitMetadataFile(filepath.Join(dir, filepath.FromSlash(ref)))
		if !ok {
			continue
		}
		if sha := resolveGitRevision(gitDir, commonDir, value, depth+1); sha != "" {
			return sha
		}
	}
	for _, dir := range gitMetadataDirs(gitDir, commonDir) {
		if sha := gitSHAFromPackedRefs(filepath.Join(dir, "packed-refs"), ref); sha != "" {
			return sha
		}
	}
	return ""
}

func gitMetadataDirs(gitDir, commonDir string) []string {
	if gitDir == commonDir {
		return []string{gitDir}
	}
	return []string{gitDir, commonDir}
}

func gitSHAFromPackedRefs(packedRefsPath, ref string) string {
	data, err := os.ReadFile(packedRefsPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != ref {
			continue
		}
		if isGitObjectID(fields[0]) {
			return strings.ToLower(fields[0])
		}
	}
	return ""
}

func readGitMetadataFile(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

func isSafeGitRef(ref string) bool {
	return strings.HasPrefix(ref, "refs/") &&
		!strings.ContainsRune(ref, '\\') &&
		!strings.ContainsRune(ref, '\x00') &&
		path.Clean(ref) == ref
}

func isGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func isTunnelClientGitRoot(root string) bool {
	return moduleFileDeclaresTunnelClient(filepath.Join(root, "go.mod")) ||
		moduleFileDeclaresTunnelClient(filepath.Join(root, "api", "tunnel-client", "go.mod"))
}

func moduleFileDeclaresTunnelClient(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "module github.com/openai/tunnel-client" {
			return true
		}
	}
	return false
}

func buildVersion(base, sha string) string {
	if sha == "" {
		return base
	}
	return base + "+" + sha
}
