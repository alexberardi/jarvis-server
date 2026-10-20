//go:build darwin

package ax

import (
	"os"
	"testing"
)

// A pid can be reused once its app quits, so each recording starts with no bundle ids
// remembered.
func TestForgetApps(t *testing.T) {
	bundleIDs.Store(int32(4242), "com.example.gone")
	ForgetApps()
	if v, ok := bundleIDs.Load(int32(4242)); ok {
		t.Fatalf("still remembered: %v", v)
	}
}

// bundleID asks AppKit inside its own autorelease pool; a process with no bundle (this test
// binary) has no id, and asking again after ForgetApps still works.
func TestBundleIDOwnProcess(t *testing.T) {
	pid := int32(os.Getpid())
	for range 3 {
		ForgetApps()
		if id := bundleID(pid); id != "" {
			t.Fatalf("bundleID(self) = %q, want none", id)
		}
	}
}
