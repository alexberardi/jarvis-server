package main

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func main() {
	self, err := os.Executable()
	if err != nil {
		fatal(err.Error())
	}
	agent := agentBeside(self)
	cmd := exec.Command(agent, os.Args[1:]...)
	// CREATE_NO_WINDOW: a console without a window, which the agent's console children inherit.
	// Not DETACHED_PROCESS: with no console at all, each console child would open its own window.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		fatal("Couldn't start " + agent + ": " + err.Error())
	}
	_ = cmd.Process.Release()
}

// fatal says what went wrong in a message box (there is no console to print to) and exits 1.
func fatal(msg string) {
	text, _ := windows.UTF16PtrFromString(msg)
	title, _ := windows.UTF16PtrFromString("Jarvis Automation Agent")
	_, _ = windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONERROR)
	os.Exit(1)
}
