package ort

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/voice/sherpa"
)

func load(t *testing.T) *Runtime {
	t.Helper()
	// Not t.TempDir(): Windows keeps loaded DLLs locked (as in the sherpa tests).
	r, err := LoadEmbedded(filepath.Join(os.TempDir(), "jarvis-sherpa-test"))
	if errors.Is(err, sherpa.ErrUnsupported) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	return r
}

// TestSmokeORTLoad binds the embedded onnxruntime's C API on each CI OS: OrtGetApiBase, API v28,
// the version string, the environment, and the error path (GetErrorMessage + ReleaseStatus).
// No model files needed; internal/voice/oww's golden test exercises Run.
func TestSmokeORTLoad(t *testing.T) {
	r := load(t)
	if !strings.HasPrefix(r.Version, "1.") {
		t.Fatalf("version %q", r.Version)
	}
	t.Logf("onnxruntime %s, C API v%d", r.Version, APIVersion)
	if again := load(t); again != r {
		t.Error("loading the same library twice made a second environment")
	}
	missing := filepath.Join(t.TempDir(), "no-such-model.onnx")
	_, err := r.NewSession(missing, []string{"in"}, []string{"out"}, Options{})
	if err == nil || !strings.Contains(err.Error(), "onnxruntime:") || !strings.Contains(err.Error(), "no-such-model.onnx") {
		t.Fatalf("missing model: err = %v", err)
	}
}

func TestRunValidatesInputsBeforeCallingC(t *testing.T) {
	s := &Session{h: 1, inNames: []uintptr{0}}
	for _, in := range [][]Tensor{
		{},
		{{Shape: []int64{2}, F32: []float32{1}}},
		{{Shape: []int64{1}}},
		{{Shape: []int64{1}, F32: []float32{1}, I64: []int64{1}}},
	} {
		if _, err := s.Run(in...); err == nil {
			t.Errorf("Run(%v) succeeded", in)
		}
	}
	closed := &Session{}
	if _, err := closed.Run(Tensor{Shape: []int64{1}, F32: []float32{1}}); err == nil {
		t.Error("Run on a closed session succeeded")
	}
}
