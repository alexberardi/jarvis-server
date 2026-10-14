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

// The privacy probes must be callable (and never prompt) everywhere.
func TestPrivacyProbesAreCallable(t *testing.T) {
	valid := map[string]bool{StatusGranted: true, StatusDenied: true, StatusNotAsked: true, StatusUnknown: true}
	mic, auto := MicStatus(), AutomationStatus("com.apple.systemevents")
	if !valid[mic] || !valid[auto] {
		t.Fatalf("mic=%q automation=%q", mic, auto)
	}
	if runtime.GOOS != "darwin" && (mic != StatusUnknown || auto != StatusUnknown) {
		t.Fatalf("off macOS: %q %q", mic, auto)
	}
	t.Logf("microphone: %s, automation (System Events): %s", mic, auto)
}
