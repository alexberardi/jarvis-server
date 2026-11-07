//go:build unix

package update

import (
	"archive/tar"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestExtractHoldsOffForks: while an extracted program is open for writing, no fork may run, or
// the child inherits the descriptor and running the program fails with ETXTBSY ("text file
// busy": the agent-upgrade e2e's flake, when the sidecar started during the update check). The
// archive is a FIFO, so the extraction is paused, deterministically, with its output open.
func TestExtractHoldsOffForks(t *testing.T) {
	const app = "Jarvis Automation Agent.app"
	top := "jarvis-agent-v1.0.0-linux-amd64/"
	archive := tarGz(t,
		tarEntry{name: top + AgentProgram, body: []byte("bin"), mode: 0o755, typ: tar.TypeReg},
		tarEntry{name: top + app + "/Contents/MacOS/" + AgentProgram, body: []byte("bin"), mode: 0o755, typ: tar.TypeReg})
	for name, extract := range map[string]func(archive, dst string) error{
		"ExtractProgram": func(a, d string) error { return ExtractProgram(a, d, AgentProgram) },
		"ExtractTree":    func(a, d string) error { return ExtractTree(a, d, app) },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			fifo := filepath.Join(dir, "a.tar.gz")
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- extract(fifo, filepath.Join(dir, "out")) }()
			w, err := os.OpenFile(fifo, os.O_WRONLY, 0) // returns once the extraction opened the archive
			if err != nil {
				t.Fatal(err)
			}
			held := !syscall.ForkLock.TryLock()
			if !held {
				syscall.ForkLock.Unlock()
			}
			_, _ = w.Write(archive)
			_ = w.Close()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if !held {
				t.Fatal("a fork could run while the extracted program was open for writing")
			}
		})
	}
}
