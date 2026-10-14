//go:build !darwin

package main

// redirectLaunchdOutput: macOS only (systemd's journal and the spawn log need nothing).
func redirectLaunchdOutput() {}
