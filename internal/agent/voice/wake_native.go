//go:build linux || darwin

package voice

import (
	"path/filepath"

	"github.com/alexberardi/jarvis-server/internal/voice/ort"
	"github.com/alexberardi/jarvis-server/internal/voice/oww"
)

// Supported reports whether this build can run the voice pipeline.
const Supported = true

// LoadDetector loads the embedded onnxruntime (extracted under cacheDir/native) and the
// hey_jarvis openWakeWord pipeline from modelsDir. close releases the sessions.
func LoadDetector(cacheDir, modelsDir string) (d Detector, close func(), err error) {
	rt, err := ort.LoadEmbedded(filepath.Join(cacheDir, "native"))
	if err != nil {
		return nil, nil, err
	}
	det, err := oww.New(rt, modelsDir, oww.HeyJarvisModel, ort.Options{Threads: 1})
	if err != nil {
		return nil, nil, err
	}
	return det, det.Close, nil
}
