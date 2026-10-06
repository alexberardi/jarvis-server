//go:build contract

package contract

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if target != nil {
		fmt.Fprintf(os.Stderr, "contract: target %s (run %s)\n", target.Host, target.RunID)
	}
	code := m.Run()
	runSharedCleanups()
	os.Exit(code)
}
