//go:build !windows

package main

import "os"

// enableANSI reports whether f takes ANSI escape codes: every unix terminal does.
func enableANSI(*os.File) bool { return true }
