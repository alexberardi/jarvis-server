package packages

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// jarvis-post-install's restart-service (#478), run by python3 with systemd's directory in a temp
// dir and systemctl recorded: it try-restarts a unit this package's drop-in configures, and
// refuses any other.
func TestPostInstallWrapperRestartService(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	wrapper, _ := filepath.Abs("../../../scripts/packaging/pi/jarvis-post-install")
	root := t.TempDir()
	d := filepath.Join(root, "shairport-sync.service.d")
	_ = os.MkdirAll(d, 0o755)
	if err := os.WriteFile(filepath.Join(d, "jarvis.conf"), []byte("# managed-by: jarvis-package music\n[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	harness := `
import sys, importlib.machinery, importlib.util
from pathlib import Path
loader = importlib.machinery.SourceFileLoader("wrapper", sys.argv[1])
spec = importlib.util.spec_from_loader("wrapper", loader)
w = importlib.util.module_from_spec(spec)
loader.exec_module(w)
w._SYSTEMD_DIR = Path(sys.argv[2])
w._systemctl = lambda *a: print("systemctl " + " ".join(a))
sys.argv = ["jarvis-post-install"] + sys.argv[3:]
sys.exit(w.main())
`
	run := func(stdin string, args ...string) (string, error) {
		cmd := exec.Command(py, append([]string{"-c", harness, wrapper, root}, args...)...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run(`{"service": "shairport-sync"}`, "--package", "music", "restart-service")
	if err != nil || !strings.Contains(out, "systemctl try-restart shairport-sync\n") {
		t.Fatalf("restart: %v\n%s", err, out)
	}
	for name, c := range map[string][2]string{
		"another package's unit": {`{"service": "shairport-sync"}`, "audacy"},
		"a unit with no drop-in": {`{"service": "ssh"}`, "music"},
		"a path":                 {`{"service": "../ssh"}`, "music"},
	} {
		out, err := run(c[0], "--package", c[1], "restart-service")
		if err == nil || strings.Contains(out, "systemctl") {
			t.Errorf("%s: restarted (%v)\n%s", name, err, out)
		}
	}
}
