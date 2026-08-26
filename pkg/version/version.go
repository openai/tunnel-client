package version

import (
	_ "embed"
	"runtime"
	"runtime/debug"
	"strings"
)

const (
	// ClientName identifies this binary in structured control-plane metadata.
	ClientName              = "oai-tunnel-client"
	fallbackSemanticVersion = "0.0.1"

	// WireProtocolHeaderName carries the dated tunnel-client/control-plane wire
	// contract supported by this binary. It is distinct from both the binary
	// release version and MCP's per-request protocol version.
	WireProtocolHeaderName = "X-Tunnel-Client-Wire-Protocol-Version"
	// WireProtocolVersion is the current dated tunnel-client/control-plane wire
	// contract. Missing headers identify legacy clients; future versions use
	// the same YYYY-MM-DD shape as MCP protocol versions.
	WireProtocolVersion = "2026-08-25"

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
	semanticVersion = fallbackSemanticVersion
	userAgentPrefix = ClientName + "/"
)

//go:embed VERSION
var sourceSemanticVersion string

var (
	// GitSHA is populated at build time via ldflags or static Go build metadata.
	// Builds never discover it from checkout files at runtime.
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

func buildVersion(base, sha string) string {
	if sha == "" {
		return base
	}
	return base + "+" + sha
}
