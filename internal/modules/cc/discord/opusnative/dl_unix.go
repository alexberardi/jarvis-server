//go:build darwin || freebsd || linux

package opusnative

import "github.com/ebitengine/purego"

// openLib opens a shared library by name, as the speech libraries are opened.
func openLib(name string) (uintptr, error) {
	return purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
}
