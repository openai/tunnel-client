package version

import (
	_ "embed"
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
	semanticVersion = fallbackSemanticVersion
	userAgentPrefix = ClientName + "/"
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

func buildVersion(base, sha string) string {
	if sha == "" {
		return base
	}
	return base + "+" + sha
}
