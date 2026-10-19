package main

import (
	"os"
	"os/exec"
)

// The suite runs on Linux (and macOS); on Windows it only has to build.

func ownGroup(*exec.Cmd) {}

func joinGroup(*exec.Cmd, int) {}

func signalGroup(cmd *exec.Cmd, _ bool) { cmd.Process.Kill() }

func killGroup(pid int, _ bool) {
	if p, err := os.FindProcess(pid); err == nil {
		p.Kill()
	}
}

func groupAlive(int) bool { return false }
