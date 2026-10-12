//go:build !darwin

// Package macax is the macOS Accessibility trust check; elsewhere it reports unsupported.
package macax

// Supported is false off macOS.
func Supported() bool { return false }

// Trusted is false off macOS.
func Trusted() bool { return false }

// Prompt does nothing off macOS.
func Prompt() bool { return false }

// ScreenTrusted is false off macOS.
func ScreenTrusted() bool { return false }

// ScreenPrompt does nothing off macOS.
func ScreenPrompt() bool { return false }
