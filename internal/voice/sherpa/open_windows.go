//go:build windows

package sherpa

import (
	"fmt"
	"path/filepath"
	"syscall"
)

const (
	libOnnxRuntime = "onnxruntime.dll"
	libSherpaCAPI  = "sherpa-onnx-c-api.dll"
)

func openLibrary(dir, name string) (uintptr, error) {
	h, err := syscall.LoadLibrary(filepath.Join(dir, name))
	if err != nil {
		return 0, fmt.Errorf("sherpa-onnx: load %s: %w", name, err)
	}
	return uintptr(h), nil
}
