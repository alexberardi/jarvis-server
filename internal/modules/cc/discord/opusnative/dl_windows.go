//go:build windows

package opusnative

import "syscall"

// openLib opens a DLL by name. purego has no Dlopen on Windows; its RegisterLibFunc takes the
// handle LoadLibrary returns.
func openLib(name string) (uintptr, error) {
	h, err := syscall.LoadLibrary(name)
	return uintptr(h), err
}
