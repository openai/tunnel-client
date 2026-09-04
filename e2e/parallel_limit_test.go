package e2e_test

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
)

type runtimeArtifactBuildKey struct {
	packagePath, binaryName, flavor string
}

// Built binaries are immutable and remain available until every test finishes.
var runtimeArtifactBuilds = struct {
	sync.Mutex
	paths map[runtimeArtifactBuildKey]string
	dirs  []string
}{paths: make(map[runtimeArtifactBuildKey]string)}

// TestMain caps test.parallel so at most two e2e tests run concurrently.
func TestMain(m *testing.M) {
	const maxParallel = 2

	if f := flag.Lookup("test.parallel"); f != nil {
		if cur, err := strconv.Atoi(f.Value.String()); err != nil || cur > maxParallel {
			_ = f.Value.Set(strconv.Itoa(maxParallel))
		}
		fmt.Fprintf(os.Stderr, "e2e: test.parallel=%s\n", f.Value.String())
	}

	code := m.Run()
	for _, dir := range runtimeArtifactBuilds.dirs {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(os.Stderr, "clean up runtime artifact directory: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}
