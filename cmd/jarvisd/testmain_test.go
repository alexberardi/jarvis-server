package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/config"
)

// TestMain keeps every test off the host's /etc/jarvisd/jarvisd.env: bootstrap reads it after
// the home's env file, so a system install on the test box (root:jarvisd 0640) made
// TestBootstrapHomeAndEnvFile fail with "permission denied", and a readable one would leak
// the host's settings into the tests.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "jarvisd-test-etc")
	if err != nil {
		panic(err)
	}
	config.SystemEnvFile = filepath.Join(dir, "jarvisd.env") // never created
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
