//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableANSI turns on virtual-terminal processing for a Windows console (Windows 10+), and
// reports whether escape codes will be honoured.
func enableANSI(f *os.File) bool {
	h := windows.Handle(f.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
