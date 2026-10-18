package voice

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The Pi install stages the wake models at install time (go-pi-node.md §6): the names, hashes
// and release it uses must be the ones the agent checks.
func TestInstallPiPinsTheWakeModels(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "install-pi.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// Windows checkouts convert the script to CRLF.
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	got := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^(?:WAKE_MODELS=")?([\w.]+\.onnx) ([0-9a-f]{64})"?$`).FindAllStringSubmatch(s, -1) {
		got[m[1]] = m[2]
	}
	if len(got) != len(WakeModels) {
		t.Errorf("install-pi.sh pins %d wake models %v, want %d", len(got), got, len(WakeModels))
	}
	for name, want := range WakeModels {
		if got[name] != want {
			t.Errorf("install-pi.sh: %s sha256 %q, want %s", name, got[name], want)
		}
	}
	if !strings.Contains(s, "\nOWW_RELEASE="+strings.TrimSuffix(owwRelease, "/")+"\n") {
		t.Errorf("install-pi.sh: OWW_RELEASE is not %s", owwRelease)
	}
}
