package engines

import (
	"os/exec"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Closing the job's last handle (what happens when jarvisd dies) kills the engines in it.
func TestKillOnCloseJobKillsMembers(t *testing.T) {
	j, err := newKillOnCloseJob()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ping", "-n", "60", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	if err := assign(j, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case <-exited:
		t.Fatal("exited before the job closed")
	case <-time.After(300 * time.Millisecond):
	}
	windows.CloseHandle(j)
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("job close did not kill its member")
	}
}

// The supervisor puts every engine it starts in the shared job.
func TestContainUsesSharedJob(t *testing.T) {
	cmd := exec.Command("ping", "-n", "60", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	if err := contain(cmd.Process); err != nil {
		t.Fatal(err)
	}
	j, _ := killOnCloseJob()
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(p)
	var in int32
	r, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob").Call(
		uintptr(p), uintptr(j), uintptr(unsafe.Pointer(&in)))
	if r == 0 || in == 0 {
		t.Fatalf("not in job: %v", err)
	}
}
