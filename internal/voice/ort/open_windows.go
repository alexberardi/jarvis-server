//go:build windows

package ort

import (
	"fmt"
	"syscall"
	"unsafe"
)

func openLibrary(path string) (uintptr, error) {
	h, err := syscall.LoadLibrary(path)
	if err != nil {
		return 0, fmt.Errorf("onnxruntime: load %s: %w", path, err)
	}
	return uintptr(h), nil
}

// modelPath: ORTCHAR_T is wchar_t on Windows, so model paths are UTF-16.
func modelPath(p string) (uintptr, any) {
	w, err := syscall.UTF16FromString(p)
	if err != nil { // a NUL in the path: let ORT fail on the empty string
		w = []uint16{0}
	}
	return uintptr(unsafe.Pointer(&w[0])), w
}
