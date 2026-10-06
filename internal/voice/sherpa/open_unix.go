//go:build linux || darwin

package sherpa

import (
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/ebitengine/purego"
)

var (
	libOnnxRuntime = map[string]string{"linux": "libonnxruntime.so", "darwin": "libonnxruntime.dylib"}[runtime.GOOS]
	libSherpaCAPI  = map[string]string{"linux": "libsherpa-onnx-c-api.so", "darwin": "libsherpa-onnx-c-api.dylib"}[runtime.GOOS]
)

func openLibrary(dir, name string) (uintptr, error) {
	h, err := purego.Dlopen(filepath.Join(dir, name), purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return 0, fmt.Errorf("sherpa-onnx: load %s: %w", name, err)
	}
	return h, nil
}
