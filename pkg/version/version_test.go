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

func TestEmbeddedSourceVersionIsNextDevRelease(t *testing.T) {
	if got := strings.TrimSpace(sourceSemanticVersion); got != "0.0.12-dev" {
		t.Fatalf("expected source VERSION to be 0.0.12-dev, got %q", got)
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

func TestInitVersionUpdatesGlobals(t *testing.T) {
	originalSemanticVersion := semanticVersion
	originalSourceSemanticVersion := sourceSemanticVersion
	originalUserAgentPrefix := userAgentPrefix
	originalGitSHA := GitSHA
	originalSemanticVersionGlobal := SemanticVersion
	originalVersion := Version
	originalUserAgent := UserAgent

	semanticVersion = "1.2.3"
	userAgentPrefix = "oai-tunnel-client/"
	GitSHA = ""
	SemanticVersion = ""
	Version = ""
	UserAgent = ""

	t.Cleanup(func() {
		semanticVersion = originalSemanticVersion
		sourceSemanticVersion = originalSourceSemanticVersion
		userAgentPrefix = originalUserAgentPrefix
		GitSHA = originalGitSHA
		SemanticVersion = originalSemanticVersionGlobal
		Version = originalVersion
		UserAgent = originalUserAgent
	})

	readBuildInfo := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "deadbeef"}},
		}, true
	}

	initVersion(readBuildInfo)

	if GitSHA != "deadbeef" {
		t.Fatalf("expected GitSHA to be set, got %q", GitSHA)
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
}

func TestInitVersionUsesSourceVersionWithoutBuildMetadata(t *testing.T) {
	originalSemanticVersion := semanticVersion
	originalSourceSemanticVersion := sourceSemanticVersion
	originalUserAgentPrefix := userAgentPrefix
	originalGitSHA := GitSHA
	originalSemanticVersionGlobal := SemanticVersion
	originalVersion := Version
	originalUserAgent := UserAgent

	semanticVersion = fallbackSemanticVersion
	sourceSemanticVersion = "4.5.6\n"
	userAgentPrefix = "oai-tunnel-client/"
	GitSHA = ""
	SemanticVersion = ""
	Version = ""
	UserAgent = ""

	t.Cleanup(func() {
		semanticVersion = originalSemanticVersion
		sourceSemanticVersion = originalSourceSemanticVersion
		userAgentPrefix = originalUserAgentPrefix
		GitSHA = originalGitSHA
		SemanticVersion = originalSemanticVersionGlobal
		Version = originalVersion
		UserAgent = originalUserAgent
	})

	emptyRead := func() (*debug.BuildInfo, bool) { return nil, false }

	initVersion(emptyRead)

	if GitSHA != "" {
		t.Fatalf("expected GitSHA to remain empty without build metadata, got %q", GitSHA)
	}
	if SemanticVersion != "4.5.6" {
		t.Fatalf("expected SemanticVersion from source VERSION, got %q", SemanticVersion)
	}
	if Version != "4.5.6" {
		t.Fatalf("expected Version from source VERSION, got %q", Version)
	}
	if UserAgent != "oai-tunnel-client/4.5.6" {
		t.Fatalf("expected UserAgent from source VERSION, got %q", UserAgent)
	}
}
