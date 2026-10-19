//go:build !darwin && !linux

package input

// Native is nil where no backend exists yet (Windows).
func Native() Input { return nil }
