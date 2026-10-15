package deviceid

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxMachineIDFallsBack(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	placeholder := filepath.Join(dir, "placeholder")
	dbus := filepath.Join(dir, "dbus")
	if err := os.WriteFile(placeholder, []byte("uninitialized\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbus, []byte("abc123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := firstMachineID([]string{missing, placeholder, dbus}); err != nil || got != "abc123" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := firstMachineID([]string{missing, placeholder}); err == nil {
		t.Fatal("no usable file should be an error")
	}
}
