//go:build !darwin

package input

// Native is nil off macOS until the Linux and Windows backends land.
func Native() Input { return nil }
