package sysinfo

import (
	"runtime"
	"testing"
)

func TestHostFacts(t *testing.T) {
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
	default:
		t.Skip("no host facts on " + runtime.GOOS)
	}
	if m := TotalMemory(); m < 64<<20 {
		t.Errorf("TotalMemory = %d, want at least 64 MiB", m)
	}
	if Release() == "" {
		t.Error("Release is empty")
	}
	if DiskFree(t.TempDir()) == 0 {
		t.Error("DiskFree of the temp dir is 0")
	}
	if DiskFree("/definitely/not/a/path/\x00") != 0 {
		t.Error("DiskFree of a bad path is not 0")
	}
}
