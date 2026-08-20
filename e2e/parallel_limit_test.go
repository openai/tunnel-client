package e2e_test

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"testing"
)

// TestMain caps test.parallel so at most two e2e tests run concurrently.
func TestMain(m *testing.M) {
	const maxParallel = 2

	if f := flag.Lookup("test.parallel"); f != nil {
		if cur, err := strconv.Atoi(f.Value.String()); err != nil || cur > maxParallel {
			_ = f.Value.Set(strconv.Itoa(maxParallel))
		}
		fmt.Fprintf(os.Stderr, "e2e: test.parallel=%s\n", f.Value.String())
	}

	os.Exit(m.Run())
}
