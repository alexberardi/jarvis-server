package browser

import (
	"os/exec"
	"testing"
)

// TestBackgroundJS runs background.js against a fake browser API (testdata/background_test.mjs):
// record_start's website-access check, the pages it watches, and cannot_watch.
func TestBackgroundJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node isn't installed")
	}
	out, err := exec.Command(node, "--test", "testdata/background_test.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("node --test: %v\n%s", err, out)
	}
}
