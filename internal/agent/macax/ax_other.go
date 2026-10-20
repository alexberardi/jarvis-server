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

// ListenTrusted is false off macOS.
func ListenTrusted() bool { return false }

// ListenPrompt does nothing off macOS.
func ListenPrompt() bool { return false }

// MicStatus is "unknown" off macOS.
func MicStatus() string { return StatusUnknown }

// AutomationStatus is "unknown" off macOS.
func AutomationStatus(string) string { return StatusUnknown }
