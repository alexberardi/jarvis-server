package main

import "os/exec"

// The suite runs on Linux (and macOS); on Windows it only has to build.

func ownGroup(*exec.Cmd) {}

func signalGroup(cmd *exec.Cmd, _ bool) { cmd.Process.Kill() }
