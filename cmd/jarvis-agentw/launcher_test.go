package main

import (
	"path/filepath"
	"testing"
)

func TestAgentBeside(t *testing.T) {
	dir := filepath.Join("Programs", "Jarvis Automation Agent")
	if got, want := agentBeside(filepath.Join(dir, "jarvis-agentw.exe")), filepath.Join(dir, "jarvis-agent.exe"); got != want {
		t.Fatalf("agentBeside = %q, want %q", got, want)
	}
}
