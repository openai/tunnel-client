package version

import (
	"os"
	"path/filepath"
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

func TestDetectGitSHAFromCandidateDirs(t *testing.T) {
	root := t.TempDir()
	moduleDir := filepath.Join(root, "api", "tunnel-client")
	versionDir := filepath.Join(moduleDir, "pkg", "version")
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(moduleDir, "go.mod"),
		[]byte("module go.openai.org/api/tunnel-client\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	originalRunGitCommand := runGitCommand
	t.Cleanup(func() {
		runGitCommand = originalRunGitCommand
	})

	sha := "ad0e6ff2e60a55267f6f03de5bd2c2cba0e5f4e9"
	runGitCommand = func(dir string, args ...string) string {
		if strings.Join(args, " ") == "rev-parse --show-toplevel" {
			switch filepath.Clean(dir) {
			case filepath.Clean(versionDir):
				return root
			case filepath.Clean(filepath.Join(root, "bazel-bin", "api", "tunnel-client", "cmd", "client")):
				return ""
			}
		}
		if filepath.Clean(dir) == filepath.Clean(root) && strings.Join(args, " ") == "rev-parse HEAD" {
			return sha
		}
		return ""
	}

	got := detectGitSHAFromCandidateDirs([]string{versionDir})
	if got != sha {
		t.Fatalf("expected checkout sha %q, got %q", sha, got)
	}

	bazelOutput := t.TempDir()
	bazelBin := filepath.Join(root, "bazel-bin")
	if err := os.Symlink(bazelOutput, bazelBin); err != nil {
		t.Fatal(err)
	}

	got = detectGitSHAFromCandidateDirs([]string{
		filepath.Join(bazelBin, "api", "tunnel-client", "cmd", "client"),
	})
	if got != sha {
		t.Fatalf("expected checkout sha through bazel-bin symlink %q, got %q", sha, got)
	}
}

func TestFindGitRootByWalkingParentsStopsAtRoot(t *testing.T) {
	root := filepath.VolumeName(os.TempDir()) + string(os.PathSeparator)
	if got := findGitRootByWalkingParents(root); got != "" {
		t.Fatalf("expected no git root at filesystem root, got %q", got)
	}
}

func TestInitVersionUpdatesGlobals(t *testing.T) {
	originalSemanticVersion := semanticVersion
	originalSourceSemanticVersion := sourceSemanticVersion
	originalUserAgentPrefix := userAgentPrefix
	originalDetectCheckoutGitSHA := detectCheckoutGitSHA
	originalGitSHA := GitSHA
	originalSemanticVersionGlobal := SemanticVersion
	originalVersion := Version
	originalUserAgent := UserAgent

	semanticVersion = "1.2.3"
	userAgentPrefix = "oai-tunnel-client/"
	detectCheckoutGitSHA = func() string { return "" }
	GitSHA = ""
	SemanticVersion = ""
	Version = ""
	UserAgent = ""

	t.Cleanup(func() {
		semanticVersion = originalSemanticVersion
		sourceSemanticVersion = originalSourceSemanticVersion
		userAgentPrefix = originalUserAgentPrefix
		detectCheckoutGitSHA = originalDetectCheckoutGitSHA
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

func TestInitVersionUsesSourceVersionWhenBuildVersionIsFallback(t *testing.T) {
	originalSemanticVersion := semanticVersion
	originalSourceSemanticVersion := sourceSemanticVersion
	originalUserAgentPrefix := userAgentPrefix
	originalDetectCheckoutGitSHA := detectCheckoutGitSHA
	originalGitSHA := GitSHA
	originalSemanticVersionGlobal := SemanticVersion
	originalVersion := Version
	originalUserAgent := UserAgent

	semanticVersion = fallbackSemanticVersion
	sourceSemanticVersion = "4.5.6\n"
	userAgentPrefix = "oai-tunnel-client/"
	detectCheckoutGitSHA = func() string { return "" }
	GitSHA = ""
	SemanticVersion = ""
	Version = ""
	UserAgent = ""

	t.Cleanup(func() {
		semanticVersion = originalSemanticVersion
		sourceSemanticVersion = originalSourceSemanticVersion
		userAgentPrefix = originalUserAgentPrefix
		detectCheckoutGitSHA = originalDetectCheckoutGitSHA
		GitSHA = originalGitSHA
		SemanticVersion = originalSemanticVersionGlobal
		Version = originalVersion
		UserAgent = originalUserAgent
	})

	emptyRead := func() (*debug.BuildInfo, bool) { return nil, false }

	initVersion(emptyRead)

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
