//go:build !darwin

package install

func nativeLoginItems() LoginItems { return nil }

// SMAppServiceAvailable reports whether SMAppService exists (macOS 13+).
func SMAppServiceAvailable() bool { return false }
