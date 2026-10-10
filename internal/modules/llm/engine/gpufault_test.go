package engine

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseNVIDIAKernelVersion(t *testing.T) {
	for in, want := range map[string]string{
		"NVRM version: NVIDIA UNIX x86_64 Kernel Module  580.173.04  Thu Jun  5 09:21:27 UTC 2025\nGCC version:  gcc version 13.3.0\n": "580.173.04",
		"NVRM version: NVIDIA UNIX Open Kernel Module for x86_64  610.57.04  Release Build  (root@omarchy)  \n":                        "610.57.04",
		"NVRM version: NVIDIA UNIX Open Kernel Module for aarch64  580.95.05  Release Build  (dvs-builder@U16)\n":                      "580.95.05",
		"NVRM version: NVIDIA UNIX x86_64 Kernel Module  470.256.02":                                                                   "470.256.02",
		"":                                 "",
		"GCC version:  gcc version 13.3.0": "",
	} {
		if got := ParseNVIDIAKernelVersion(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestNVIDIALibraryVersion(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/lib/x86_64-linux-gnu/libnvidia-ml.so.580.178.04": "580.178.04",
		"libcuda.so.580.173.04":                                "580.173.04",
		"/usr/lib/libnvidia-ml.so.1":                           "",
		"/usr/lib/libnvidia-ml.so":                             "",
		"/usr/lib/libnvidia-mlx.so.580.1":                      "",
	} {
		if got := NVIDIALibraryVersion(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// fakeFS is a DriverProbe over in-memory files and symlinks.
type fakeFS struct {
	files map[string]string // path -> content
	links map[string]string // path -> target
}

func (f fakeFS) probe() DriverProbe {
	return DriverProbe{
		ReadFile: func(p string) ([]byte, error) {
			if c, ok := f.files[p]; ok {
				return []byte(c), nil
			}
			return nil, fs.ErrNotExist
		},
		EvalSymlinks: func(p string) (string, error) {
			if t, ok := f.links[p]; ok {
				return t, nil
			}
			if _, ok := f.files[p]; ok {
				return p, nil
			}
			return "", fs.ErrNotExist
		},
		Glob: func(pat string) ([]string, error) {
			var out []string
			for p := range f.files {
				if ok, _ := filepath.Match(pat, p); ok {
					out = append(out, p)
				}
			}
			return out, nil
		},
		LibDirs: []string{"/usr/lib/x86_64-linux-gnu", "/usr/lib"},
	}
}

const procMismatch = "NVRM version: NVIDIA UNIX x86_64 Kernel Module  580.173.04  Thu Jun  5 09:21:27 UTC 2025\n"

func TestDriverProbe(t *testing.T) {
	lib := "/usr/lib/x86_64-linux-gnu/"
	t.Run("mismatch through the loader's symlink (the 2026-10-10 outage)", func(t *testing.T) {
		f := fakeFS{files: map[string]string{"/proc/driver/nvidia/version": procMismatch,
			lib + "libnvidia-ml.so.580.178.04": ""},
			links: map[string]string{lib + "libnvidia-ml.so.1": lib + "libnvidia-ml.so.580.178.04"}}
		got := f.probe().Check()
		if got == nil || got.Kind != FaultDriverMismatch || got.KernelVersion != "580.173.04" || got.LibraryVersion != "580.178.04" {
			t.Fatalf("%+v", got)
		}
		if got.Message != "NVIDIA driver updated (kernel 580.173.04, libraries 580.178.04): reboot the server." {
			t.Fatalf("admin message %q", got.Message)
		}
		if got.UserMessage != UserMsgDriverUpdated {
			t.Fatalf("user message %q", got.UserMessage)
		}
	})
	t.Run("versions agree", func(t *testing.T) {
		f := fakeFS{files: map[string]string{"/proc/driver/nvidia/version": procMismatch, lib + "libnvidia-ml.so.580.173.04": ""},
			links: map[string]string{lib + "libnvidia-ml.so.1": lib + "libnvidia-ml.so.580.173.04"}}
		if got := f.probe().Check(); got != nil {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("no NVIDIA driver: no fault", func(t *testing.T) {
		f := fakeFS{files: map[string]string{lib + "libnvidia-ml.so.580.178.04": ""}}
		if got := f.probe().Check(); got != nil {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("libraries not found: no fault", func(t *testing.T) {
		f := fakeFS{files: map[string]string{"/proc/driver/nvidia/version": procMismatch}}
		if got := f.probe().Check(); got != nil {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("versioned file without a symlink, libcuda only", func(t *testing.T) {
		f := fakeFS{files: map[string]string{"/proc/driver/nvidia/version": procMismatch, "/usr/lib/libcuda.so.580.178.04": ""}}
		if got := f.probe().Check(); got == nil || got.LibraryVersion != "580.178.04" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("two versions left behind and no symlink: ambiguous, no fault", func(t *testing.T) {
		f := fakeFS{files: map[string]string{"/proc/driver/nvidia/version": procMismatch,
			lib + "libnvidia-ml.so.580.178.04": "", lib + "libnvidia-ml.so.580.173.04": ""}}
		if got := f.probe().Check(); got != nil {
			t.Fatalf("%+v", got)
		}
	})
}

func TestDetectorDriverFault(t *testing.T) {
	smiMismatch := "Failed to initialize NVML: Driver/library version mismatch\nNVML library version: 580.178\n"
	t.Run("probe fault is reported and logged at ERROR with both versions", func(t *testing.T) {
		var buf bytes.Buffer
		d := &Detector{Platform: Platform{"linux", "amd64"}, Log: slog.New(slog.NewTextHandler(&buf, nil)),
			Run:    fakeRunner(map[string]string{"nvidia-smi": smiMismatch}),
			Driver: func() *GPUFault { return DriverMismatchFault("580.173.04", "580.178.04") }}
		h := d.Hardware(context.Background(), false)
		if h.Fault == nil || h.Fault.Kind != FaultDriverMismatch || h.Flavour != FlavourCPU || h.Fault.DetectedAt.IsZero() {
			t.Fatalf("%+v", h)
		}
		log := buf.String()
		for _, s := range []string{"level=ERROR", "reboot required", "kernel_module=580.173.04", "libraries=580.178.04"} {
			if !strings.Contains(log, s) {
				t.Fatalf("log lacks %q:\n%s", s, log)
			}
		}
		// Re-detecting the same fault keeps its first time and doesn't log again.
		buf.Reset()
		first := h.Fault.DetectedAt
		h = d.Hardware(context.Background(), true)
		if !h.Fault.DetectedAt.Equal(first) || buf.Len() != 0 {
			t.Fatalf("%v vs %v, log %q", h.Fault.DetectedAt, first, buf.String())
		}
	})
	t.Run("nvidia-smi's mismatch text alone", func(t *testing.T) {
		d := &Detector{Platform: Platform{"linux", "amd64"}, Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "nvidia-smi" {
				return []byte(smiMismatch), errors.New("exit status 18")
			}
			return nil, errors.New("not found")
		}}
		h := d.Hardware(context.Background(), false)
		if h.Fault == nil || h.Fault.Kind != FaultDriverMismatch || !strings.Contains(h.Fault.Message, "reboot the server") {
			t.Fatalf("%+v", h.Fault)
		}
	})
	t.Run("healthy GPU: no fault", func(t *testing.T) {
		d := &Detector{Platform: Platform{"linux", "amd64"}, Driver: func() *GPUFault { return nil },
			Run: fakeRunner(map[string]string{"nvidia-smi": "0, NVIDIA GeForce RTX 3090, 24576, 400\n"})}
		if h := d.Hardware(context.Background(), false); h.Fault != nil || h.Flavour != FlavourCUDA {
			t.Fatalf("%+v", h)
		}
	})
	t.Run("DriverChanged sees an upgrade while running", func(t *testing.T) {
		var fault *GPUFault
		d := &Detector{Platform: Platform{"linux", "amd64"}, Run: fakeRunner(nil), Driver: func() *GPUFault { return fault }}
		d.Hardware(context.Background(), false)
		if d.DriverChanged() {
			t.Fatal("nothing changed")
		}
		fault = DriverMismatchFault("580.173.04", "580.178.04")
		if !d.DriverChanged() {
			t.Fatal("the upgrade went unnoticed")
		}
		d.Hardware(context.Background(), true)
		if d.DriverChanged() {
			t.Fatal("re-detection should settle it")
		}
		if (&Detector{}).DriverChanged() {
			t.Fatal("no driver check: never changed")
		}
	})
}
