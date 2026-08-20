package mockmcpserver

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"testing"
)

// TestMain caps test.parallel so at most two tests run concurrently in this package.
func TestMain(m *testing.M) {
	const maxParallel = 2

	if f := flag.Lookup("test.parallel"); f != nil {
		if cur, err := strconv.Atoi(f.Value.String()); err != nil || cur > maxParallel {
			_ = f.Value.Set(strconv.Itoa(maxParallel))
		}
		fmt.Fprintf(os.Stderr, "mockmcpserver: test.parallel=%s\n", f.Value.String())
	}

	os.Exit(m.Run())
}
