//go:build linux || darwin

package ort

import (
	"fmt"

	"github.com/ebitengine/purego"
)

func openLibrary(path string) (uintptr, error) {
	h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return 0, fmt.Errorf("onnxruntime: load %s: %w", path, err)
	}
	return h, nil
}

// modelPath: ORTCHAR_T is char on Unix.
func modelPath(p string) (uintptr, any) {
	var cs cstrs
	return cs.of(p), &cs
}
