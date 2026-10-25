package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/pinode"
)

// pythonNode copies pinode's fixture Python node (a provisioned one) into a temp dir.
func pythonNode(t *testing.T) pinode.PythonNode {
	t.Helper()
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS("../../internal/agent/pinode/testdata/pynode")); err != nil {
		t.Fatal(err)
	}
	return pinode.PythonNode{InstallDir: filepath.Join(dst, "opt-jarvis-node"), SecretDir: filepath.Join(dst, "home-pi-.jarvis")}
}

// The agent started on a Pi with no node.json: it adopts the Python node, goes to setup mode
// only when there is none, and stops (never setup mode, never a second node) when the adoption fails.
func TestAgentStartAdoptsThePythonNode(t *testing.T) {
	ctx, log := context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("none to adopt", func(t *testing.T) {
		py := pythonNode(t)
		_ = os.Remove(filepath.Join(py.SecretDir, ".provisioned"))
		if _, err := loadOrAdopt(ctx, log, filepath.Join(t.TempDir(), "node.json"), py); !errors.Is(err, node.ErrNotEnrolled) {
			t.Fatalf("got %v, want ErrNotEnrolled (setup mode)", err)
		}
	})

	t.Run("adoption fails", func(t *testing.T) {
		py := pythonNode(t) // no .venv: the DB can't be read
		path := filepath.Join(t.TempDir(), "node.json")
		if _, err := loadOrAdopt(ctx, log, path, py); err == nil || errors.Is(err, node.ErrNotEnrolled) {
			t.Fatalf("got %v, want the adoption's error", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("node.json written")
		}
	})

	t.Run("adopted", func(t *testing.T) {
		py := pythonNode(t)
		interp := os.Getenv("JARVIS_ADOPT_PYTHON")
		if runtime.GOOS == "windows" {
			t.Skip("the Pi node is Linux")
		}
		if interp == "" {
			interp = "python3"
			if exec.Command(interp, "-I", "-c", "import sqlcipher3, cryptography").Run() != nil {
				t.Skip("no python3 with sqlcipher3 and cryptography (set JARVIS_ADOPT_PYTHON)")
			}
		}
		bin := filepath.Join(py.InstallDir, ".venv", "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, "python"), []byte("#!/bin/sh\nexec '"+interp+"' \"$@\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		c, err := loadOrAdopt(ctx, log, filepath.Join(t.TempDir(), "jarvis-agent", "node.json"), py)
		if err != nil || c.APIKey() != "pi-kitchen-7f3a:test-node-key-0123456789" || c.Room != "kitchen" {
			t.Fatalf("creds: %+v %v", c, err)
		}
	})
}
