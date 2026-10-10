package ort

import (
	"path/filepath"

	"github.com/alexberardi/jarvis-server/internal/voice/sherpa"
)

// LoadEmbedded loads the onnxruntime library embedded in this binary (the one sherpa-onnx
// uses), extracting it under baseDir first if needed. It returns sherpa.ErrUnsupported on
// targets that embed no native libraries.
func LoadEmbedded(baseDir string) (*Runtime, error) {
	dir, err := sherpa.LibDir(baseDir)
	if err != nil {
		return nil, err
	}
	return Load(filepath.Join(dir, sherpa.OnnxRuntimeLib))
}
