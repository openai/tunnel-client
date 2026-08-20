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

func TestEmbeddedSourceVersionIsStableRelease(t *testing.T) {
	if got := strings.TrimSpace(sourceSemanticVersion); got != "0.0.12" {
		t.Fatalf("expected source VERSION to be 0.0.12, got %q", got)
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
	const sha = "ad0e6ff2e60a55267f6f03de5bd2c2cba0e5f4e9"

	t.Run("loose ref and bazel symlink", func(t *testing.T) {
		root, versionDir := newTunnelClientCheckout(t, true)
		gitDir := filepath.Join(root, ".git")
		writeTestFile(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/main\n")
		writeTestFile(t, filepath.Join(gitDir, "refs", "heads", "main"), sha+"\n")

		if got := detectGitSHAFromCandidateDirs([]string{versionDir}); got != sha {
			t.Fatalf("expected checkout sha %q, got %q", sha, got)
		}

		bazelOutput := t.TempDir()
		bazelBin := filepath.Join(root, "bazel-bin")
		if err := os.Symlink(bazelOutput, bazelBin); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		got := detectGitSHAFromCandidateDirs([]string{
			filepath.Join(bazelBin, "api", "tunnel-client", "cmd", "client"),
		})
		if got != sha {
			t.Fatalf("expected checkout sha through bazel-bin symlink %q, got %q", sha, got)
		}
	})

	t.Run("linked worktree", func(t *testing.T) {
		root, versionDir := newTunnelClientCheckout(t, false)
		commonDir := filepath.Join(t.TempDir(), "common.git")
		worktreeGitDir := filepath.Join(commonDir, "worktrees", "tunnel-client")
		if err := os.MkdirAll(worktreeGitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(root, ".git"), "gitdir: "+worktreeGitDir+"\n")
		writeTestFile(t, filepath.Join(worktreeGitDir, "HEAD"), "ref: refs/heads/topic\n")
		writeTestFile(t, filepath.Join(worktreeGitDir, "commondir"), "../..\n")
		writeTestFile(t, filepath.Join(commonDir, "refs", "heads", "topic"), sha+"\n")

		if got := detectGitSHAFromCandidateDirs([]string{versionDir}); got != sha {
			t.Fatalf("expected linked-worktree sha %q, got %q", sha, got)
		}
	})

	t.Run("detached head", func(t *testing.T) {
		root, versionDir := newTunnelClientCheckout(t, false)
		writeTestFile(t, filepath.Join(root, ".git", "HEAD"), sha+"\n")

		if got := detectGitSHAFromCandidateDirs([]string{versionDir}); got != sha {
			t.Fatalf("expected detached-head sha %q, got %q", sha, got)
		}
	})

	t.Run("packed ref", func(t *testing.T) {
		root, versionDir := newTunnelClientCheckout(t, false)
		gitDir := filepath.Join(root, ".git")
		writeTestFile(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/main\n")
		writeTestFile(t, filepath.Join(gitDir, "packed-refs"), "# pack-refs with: peeled fully-peeled sorted\n"+sha+" refs/heads/main\n")

		if got := detectGitSHAFromCandidateDirs([]string{versionDir}); got != sha {
			t.Fatalf("expected packed-ref sha %q, got %q", sha, got)
		}
	})

	t.Run("rejects unsafe ref", func(t *testing.T) {
		root, versionDir := newTunnelClientCheckout(t, false)
		writeTestFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/../../outside\n")
		writeTestFile(t, filepath.Join(root, ".git", "outside"), sha+"\n")

		if got := detectGitSHAFromCandidateDirs([]string{versionDir}); got != "" {
			t.Fatalf("expected no sha for unsafe ref, got %q", got)
		}
	})
}

func TestFindGitRootByWalkingParentsStopsAtRoot(t *testing.T) {
	root := filepath.VolumeName(os.TempDir()) + string(os.PathSeparator)
	if got := findGitRootByWalkingParents(root); got != "" {
		t.Fatalf("expected no git root at filesystem root, got %q", got)
	}
}

func TestInitVersionUpdatesStaticBuildMetadata(t *testing.T) {
	restoreVersionGlobals(t)

	semanticVersion = "1.2.3"
	userAgentPrefix = "oai-tunnel-client/"
	checkoutCalled := false
	detectCheckoutGitSHA = func() string {
		checkoutCalled = true
		return "checkout-sha"
	}
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
	if checkoutCalled {
		t.Fatal("expected build info sha to win over checkout detection")
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

func TestInitVersionFallsBackToCheckoutGitSHAForFullFlavor(t *testing.T) {
	restoreVersionGlobals(t)

	semanticVersion = "1.2.3"
	GitSHA = ""
	GoVersion = "go1.26.2"
	Flavor = FlavorFull
	SemanticVersion = ""
	Version = ""
	UserAgent = ""
	detectCheckoutGitSHA = func() string { return "checkout-sha" }

	initVersion(func() (*debug.BuildInfo, bool) { return nil, false })

	if GitSHA != "checkout-sha" {
		t.Fatalf("expected full flavor to use checkout sha, got %q", GitSHA)
	}
	if Version != "1.2.3+checkout-sha" {
		t.Fatalf("expected Version to include checkout sha, got %q", Version)
	}
}

func TestInitVersionSkipsCheckoutGitSHAForRuntimeFlavor(t *testing.T) {
	restoreVersionGlobals(t)

	semanticVersion = "1.2.3"
	GitSHA = ""
	GoVersion = "go1.26.2"
	Flavor = FlavorRuntime
	SemanticVersion = ""
	Version = ""
	UserAgent = ""
	detectCheckoutGitSHA = func() string {
		t.Fatal("runtime flavor must not probe checkout metadata")
		return ""
	}

	initVersion(func() (*debug.BuildInfo, bool) { return nil, false })

	if GitSHA != "" {
		t.Fatalf("expected runtime flavor without linked SHA to stay empty, got %q", GitSHA)
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
	detectCheckoutGitSHA = func() string { return "" }

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
	originalDetectCheckoutGitSHA := detectCheckoutGitSHA
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
		detectCheckoutGitSHA = originalDetectCheckoutGitSHA
		GitSHA = originalGitSHA
		GoVersion = originalGoVersion
		BuildFlags = originalBuildFlags
		Flavor = originalFlavor
		SemanticVersion = originalSemanticVersionGlobal
		Version = originalVersion
		UserAgent = originalUserAgent
	})
}

func newTunnelClientCheckout(t *testing.T, nestedModule bool) (string, string) {
	t.Helper()

	root := t.TempDir()
	moduleDir := root
	if nestedModule {
		moduleDir = filepath.Join(root, "api", "tunnel-client")
	}
	versionDir := filepath.Join(moduleDir, "pkg", "version")
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(moduleDir, "go.mod"), "module github.com/openai/tunnel-client\n")
	return root, versionDir
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
