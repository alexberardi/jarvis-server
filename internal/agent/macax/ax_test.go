package macax

import (
	"runtime"
	"testing"
)

// The frameworks must load through purego with CGO_ENABLED=0 on macOS, and the trust check must
// be callable. Prompt is never called here: it would put a dialog on a developer's screen.
func TestSupportedOnlyOnMacAndTrustedIsCallable(t *testing.T) {
	if got := Supported(); got != (runtime.GOOS == "darwin") {
		t.Fatalf("Supported() = %v on %s", got, runtime.GOOS)
	}
	t.Logf("Trusted() = %v", Trusted())
}
