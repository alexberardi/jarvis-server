//go:build !(linux || darwin || windows)

package ort

import "errors"

func openLibrary(string) (uintptr, error) {
	return 0, errors.New("onnxruntime: unsupported platform")
}

func modelPath(p string) (uintptr, any) {
	var cs cstrs
	return cs.of(p), &cs
}
