//go:build darwin

package ax

import "testing"

// A pid can be reused once its app quits, so each recording starts with no bundle ids
// remembered.
func TestForgetApps(t *testing.T) {
	bundleIDs.Store(int32(4242), "com.example.gone")
	ForgetApps()
	if v, ok := bundleIDs.Load(int32(4242)); ok {
		t.Fatalf("still remembered: %v", v)
	}
}
