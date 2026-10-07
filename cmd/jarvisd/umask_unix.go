//go:build unix

package main

import "syscall"

// restrictUmask makes new files and directories owner-only.
func restrictUmask() { syscall.Umask(0o077) }
