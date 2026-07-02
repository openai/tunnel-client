package version

import (
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
)

const (
	// ClientName identifies this binary in structured control-plane metadata.
	ClientName              = "oai-tunnel-client"
	fallbackSemanticVersion = "0.0.1"
)

var (
	// semanticVersion is intentionally a var (not const) so it can be overridden
	// at build time via -ldflags, allowing tagged releases to embed the
	// version string without changing source code for each release.
	semanticVersion      = fallbackSemanticVersion
	userAgentPrefix      = ClientName + "/"
	detectCheckoutGitSHA = detectGitSHAFromCheckout
	runGitCommand        = runGitCommandWithExec
)

//go:embed VERSION
var sourceSemanticVersion string

var (
	// GitSHA is populated at build time via ldflags or Go build metadata.
	GitSHA = ""
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
	if GitSHA == "" {
		GitSHA = detectBuildGitSHAFrom(readBuildInfo)
	}
	if GitSHA == "" {
		GitSHA = detectCheckoutGitSHA()
	}
	baseVersion := effectiveSemanticVersion()
	SemanticVersion = baseVersion
	Version = buildVersion(baseVersion, GitSHA)
	UserAgent = userAgentPrefix + Version
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
		root := gitRootForCandidateDir(dir)
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
		sha := gitOutput(root, "rev-parse", "HEAD")
		if sha != "" {
			return sha
		}
	}
	return ""
}

func gitRootForCandidateDir(dir string) string {
	if root := gitOutput(dir, "rev-parse", "--show-toplevel"); root != "" {
		return root
	}
	return findGitRootByWalkingParents(dir)
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

func gitOutput(dir string, args ...string) string {
	return runGitCommand(dir, args...)
}

func runGitCommandWithExec(dir string, args ...string) string {
	cmdArgs := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", cmdArgs...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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
