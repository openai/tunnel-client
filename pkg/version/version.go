package version

import "runtime/debug"

var (
	// semanticVersion is intentionally a var (not const) so it can be overridden
	// at build time via -ldflags, allowing tagged releases to embed the
	// version string without changing source code for each release.
	semanticVersion = "0.0.1"
	userAgentPrefix = "oai-tunnel-client/"
)

var (
	// GitSHA is populated at build time via ldflags or Go build metadata.
	GitSHA = ""
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
	Version = buildVersion(semanticVersion, GitSHA)
	UserAgent = userAgentPrefix + Version
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
