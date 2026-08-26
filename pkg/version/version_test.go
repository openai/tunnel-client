package version

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestBuildVersion(t *testing.T) {
	if got := buildVersion("1.2.3", ""); got != "1.2.3" {
		t.Fatalf("expected base version, got %q", got)
	}

	if got := buildVersion("1.2.3", "deadbeef"); got != "1.2.3+deadbeef" {
		t.Fatalf("expected build metadata version, got %q", got)
	}
}

func TestEmbeddedSourceVersionIsStableRelease(t *testing.T) {
	if got := strings.TrimSpace(sourceSemanticVersion); got != "0.0.13" {
		t.Fatalf("expected source VERSION to be 0.0.13, got %q", got)
	}
}

func TestDetectBuildGitSHA(t *testing.T) {
	emptyRead := func() (*debug.BuildInfo, bool) { return nil, false }
	if got := detectBuildGitSHAFrom(emptyRead); got != "" {
		t.Fatalf("expected empty sha when build info unavailable, got %q", got)
	}

	missingSHA := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Settings: []debug.BuildSetting{{Key: "vcs.time", Value: "123"}},
		}, true
	}
	if got := detectBuildGitSHAFrom(missingSHA); got != "" {
		t.Fatalf("expected empty sha when revision missing, got %q", got)
	}

	withSHA := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "deadbeef"}},
		}, true
	}
	if got := detectBuildGitSHAFrom(withSHA); got != "deadbeef" {
		t.Fatalf("expected revision sha, got %q", got)
	}
}

func TestInitVersionUpdatesStaticBuildMetadata(t *testing.T) {
	restoreVersionGlobals(t)

	semanticVersion = "1.2.3"
	userAgentPrefix = "oai-tunnel-client/"
	GitSHA = ""
	GoVersion = "go1.26.2"
	BuildFlags = "-trimpath -buildvcs=false"
	Flavor = FlavorRuntime
	SemanticVersion = ""
	Version = ""
	UserAgent = ""

	readBuildInfo := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "deadbeef"}},
		}, true
	}

	initVersion(readBuildInfo)

	if GitSHA != "deadbeef" {
		t.Fatalf("expected GitSHA to be set from static build info, got %q", GitSHA)
	}
	if ClientName != "oai-tunnel-client" {
		t.Fatalf("expected ClientName to identify tunnel-client, got %q", ClientName)
	}
	if SemanticVersion != "1.2.3" {
		t.Fatalf("expected SemanticVersion to exclude sha, got %q", SemanticVersion)
	}
	if Version != "1.2.3+deadbeef" {
		t.Fatalf("expected Version to include sha, got %q", Version)
	}
	if UserAgent != "oai-tunnel-client/1.2.3+deadbeef" {
		t.Fatalf("expected UserAgent to include version, got %q", UserAgent)
	}

	metadata := CurrentBuildMetadata()
	if metadata.Flavor != FlavorRuntime {
		t.Fatalf("expected linked runtime flavor, got %q", metadata.Flavor)
	}
	if metadata.GoVersion != "go1.26.2" {
		t.Fatalf("expected linked Go version, got %q", metadata.GoVersion)
	}
	if metadata.BuildFlags != "-trimpath -buildvcs=false" {
		t.Fatalf("expected linked build flags, got %q", metadata.BuildFlags)
	}
}

func TestInitVersionPreservesLinkedGitSHA(t *testing.T) {
	restoreVersionGlobals(t)

	semanticVersion = "1.2.3"
	GitSHA = "linked-sha"
	GoVersion = "go1.26.2"
	Flavor = FlavorRuntimeCloudflared
	SemanticVersion = ""
	Version = ""
	UserAgent = ""

	initVersion(func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "build-info-sha"}},
		}, true
	})

	if GitSHA != "linked-sha" {
		t.Fatalf("expected linked GitSHA to win, got %q", GitSHA)
	}
	if metadata := CurrentBuildMetadata(); metadata.Flavor != FlavorRuntimeCloudflared {
		t.Fatalf("expected linked cloudflared runtime flavor, got %q", metadata.Flavor)
	}
}

func TestInitVersionWithoutGitMetadataUsesSemanticVersionForFullFlavor(t *testing.T) {
	restoreVersionGlobals(t)

	semanticVersion = "1.2.3"
	GitSHA = ""
	GoVersion = "go1.26.2"
	Flavor = FlavorFull
	SemanticVersion = ""
	Version = ""
	UserAgent = ""

	initVersion(func() (*debug.BuildInfo, bool) { return nil, false })

	if GitSHA != "" {
		t.Fatalf("expected no GitSHA without linked or build metadata, got %q", GitSHA)
	}
	if Version != "1.2.3" {
		t.Fatalf("expected semantic Version without Git metadata, got %q", Version)
	}
	if UserAgent != "oai-tunnel-client/1.2.3" {
		t.Fatalf("expected semantic UserAgent without Git metadata, got %q", UserAgent)
	}
}

func TestInitVersionWithoutGitMetadataUsesSemanticVersionForRuntimeFlavor(t *testing.T) {
	restoreVersionGlobals(t)

	semanticVersion = "1.2.3"
	GitSHA = ""
	GoVersion = "go1.26.2"
	Flavor = FlavorRuntime
	SemanticVersion = ""
	Version = ""
	UserAgent = ""

	initVersion(func() (*debug.BuildInfo, bool) { return nil, false })

	if GitSHA != "" {
		t.Fatalf("expected no GitSHA without linked or build metadata, got %q", GitSHA)
	}
	if Version != "1.2.3" {
		t.Fatalf("expected runtime version without SHA metadata, got %q", Version)
	}
}

func TestInitVersionUsesSourceVersionAndStaticDefaults(t *testing.T) {
	restoreVersionGlobals(t)

	semanticVersion = fallbackSemanticVersion
	sourceSemanticVersion = "4.5.6\n"
	userAgentPrefix = "oai-tunnel-client/"
	GitSHA = ""
	GoVersion = ""
	BuildFlags = ""
	Flavor = ""
	SemanticVersion = ""
	Version = ""
	UserAgent = ""

	initVersion(func() (*debug.BuildInfo, bool) { return nil, false })

	if SemanticVersion != "4.5.6" {
		t.Fatalf("expected SemanticVersion from source VERSION, got %q", SemanticVersion)
	}
	if Version != "4.5.6" {
		t.Fatalf("expected Version from source VERSION, got %q", Version)
	}
	if UserAgent != "oai-tunnel-client/4.5.6" {
		t.Fatalf("expected UserAgent from source VERSION, got %q", UserAgent)
	}
	metadata := CurrentBuildMetadata()
	if metadata.Flavor != FlavorFull {
		t.Fatalf("expected default full flavor, got %q", metadata.Flavor)
	}
	if metadata.GoVersion == "" {
		t.Fatal("expected compiled Go version fallback")
	}
}

func restoreVersionGlobals(t *testing.T) {
	t.Helper()

	originalSemanticVersion := semanticVersion
	originalSourceSemanticVersion := sourceSemanticVersion
	originalUserAgentPrefix := userAgentPrefix
	originalGitSHA := GitSHA
	originalGoVersion := GoVersion
	originalBuildFlags := BuildFlags
	originalFlavor := Flavor
	originalSemanticVersionGlobal := SemanticVersion
	originalVersion := Version
	originalUserAgent := UserAgent

	t.Cleanup(func() {
		semanticVersion = originalSemanticVersion
		sourceSemanticVersion = originalSourceSemanticVersion
		userAgentPrefix = originalUserAgentPrefix
		GitSHA = originalGitSHA
		GoVersion = originalGoVersion
		BuildFlags = originalBuildFlags
		Flavor = originalFlavor
		SemanticVersion = originalSemanticVersionGlobal
		Version = originalVersion
		UserAgent = originalUserAgent
	})
}
