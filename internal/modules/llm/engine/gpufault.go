package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// GPU loss (docs/llm/01 §3.10). A GPU that worked can stop working under jarvisd: an
// unattended driver upgrade installs new NVIDIA userspace libraries while the old kernel module
// stays loaded until the next reboot, and NVML then fails with "Driver/library version
// mismatch" (prod outage 2026-10-10). jarvisd must not quietly run such a box's models on the
// CPU: it says so, and fails requests at once with a reason a person can act on.

// Fault kinds.
const (
	// FaultDriverMismatch: the loaded NVIDIA kernel module and the installed userspace
	// libraries differ. A reboot loads the new module.
	FaultDriverMismatch = "driver_mismatch"
	// FaultGPUUnavailable: a label expects a GPU that detection does not see (no device, no
	// driver, …).
	FaultGPUUnavailable = "gpu_unavailable"
)

// StateGPUUnavailable is the NotReadyError state of a label that is expected to run on a GPU
// that isn't usable. Callers fail at once with its Reason (a sentence for the user).
const StateGPUUnavailable = "gpu_unavailable"

// User-facing sentences, spoken by voice and shown in chat.
const (
	UserMsgDriverUpdated  = "The GPU driver was updated; reboot the server to finish (Jarvis can't use the GPU until then)."
	UserMsgGPUUnavailable = "The server's GPU isn't available, so Jarvis can't run its model. Check the GPU driver or reboot the server."
)

// GPUFault says why GPUs that should be usable are not. It is reported to the admin.
type GPUFault struct {
	Kind string `json:"kind"`
	// KernelVersion and LibraryVersion are the NVIDIA kernel module and userspace library
	// versions, when known (driver_mismatch).
	KernelVersion  string `json:"kernel_version,omitempty"`
	LibraryVersion string `json:"library_version,omitempty"`
	// Labels lists the labels that can't run because of it (gpu_unavailable).
	Labels []string `json:"labels,omitempty"`
	// Message is the admin's warning.
	Message string `json:"message"`
	// UserMessage is what a request to an affected label fails with.
	UserMessage string    `json:"user_message"`
	DetectedAt  time.Time `json:"detected_at,omitzero"`
}

// same reports whether two faults describe the same condition (ignoring when).
func (f *GPUFault) same(g *GPUFault) bool {
	if f == nil || g == nil {
		return f == g
	}
	return f.Kind == g.Kind && f.KernelVersion == g.KernelVersion && f.LibraryVersion == g.LibraryVersion
}

// DriverMismatchFault builds the driver/library mismatch fault; either version may be unknown.
func DriverMismatchFault(kernel, library string) *GPUFault {
	msg := "NVIDIA driver updated (driver/library version mismatch): reboot the server."
	if kernel != "" && library != "" {
		msg = fmt.Sprintf("NVIDIA driver updated (kernel %s, libraries %s): reboot the server.", kernel, library)
	}
	return &GPUFault{Kind: FaultDriverMismatch, KernelVersion: kernel, LibraryVersion: library,
		Message: msg, UserMessage: UserMsgDriverUpdated}
}

// UnavailableFault is the generic fault for labels whose GPU is gone.
func UnavailableFault(labels []string) *GPUFault {
	return &GPUFault{Kind: FaultGPUUnavailable, Labels: labels,
		Message: fmt.Sprintf("GPU unavailable: %s can't run (no usable GPU detected, and Jarvis won't fall back to the CPU). "+
			"Check the GPU driver or reboot the server; if this server no longer has a GPU, set the GPU backend to cpu.",
			strings.Join(labels, ", ")),
		UserMessage: UserMsgGPUUnavailable}
}

// kernelVersionRE finds the version in /proc/driver/nvidia/version's first line, e.g.
//
//	NVRM version: NVIDIA UNIX x86_64 Kernel Module  580.173.04  Thu Jun 5 …
//	NVRM version: NVIDIA UNIX Open Kernel Module for x86_64  580.173.04  Release Build …
var kernelVersionRE = regexp.MustCompile(`Kernel Module\b.*?\s(\d+\.\d+(?:\.\d+)*)(?:\s|$)`)

// ParseNVIDIAKernelVersion returns the kernel module version from /proc/driver/nvidia/version
// ("" when absent).
func ParseNVIDIAKernelVersion(proc string) string {
	for _, line := range strings.Split(proc, "\n") {
		if m := kernelVersionRE.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// libVersionRE matches a versioned NVIDIA library file name: libnvidia-ml.so.580.178.04.
var libVersionRE = regexp.MustCompile(`^lib(?:nvidia-ml|cuda)\.so\.(\d+\.\d+(?:\.\d+)*)$`)

// NVIDIALibraryVersion returns the driver version in a libnvidia-ml/libcuda file name ("" for
// an unversioned name such as libnvidia-ml.so.1).
func NVIDIALibraryVersion(name string) string {
	if m := libVersionRE.FindStringSubmatch(filepath.Base(name)); m != nil {
		return m[1]
	}
	return ""
}

// nvmlMismatch reports nvidia-smi's "Failed to initialize NVML: Driver/library version
// mismatch".
func nvmlMismatch(out string) bool {
	return strings.Contains(strings.ToLower(out), "driver/library version mismatch")
}

// DriverProbe compares the loaded NVIDIA kernel module with the userspace libraries. It reads
// files only (no root, no GPU library loaded in process), so it is cheap enough to run every
// few minutes.
type DriverProbe struct {
	ReadFile     func(string) ([]byte, error)
	Glob         func(string) ([]string, error)
	EvalSymlinks func(string) (string, error)
	// ProcPath is the kernel module's version file; default /proc/driver/nvidia/version.
	ProcPath string
	// LibDirs are searched for libnvidia-ml.so.* and libcuda.so.*; default the usual
	// multiarch and lib64 directories.
	LibDirs []string
}

// DefaultLibDirs are where distributions install the NVIDIA userspace libraries.
var DefaultLibDirs = []string{"/usr/lib/x86_64-linux-gnu", "/usr/lib/aarch64-linux-gnu", "/usr/lib64", "/usr/lib",
	"/lib/x86_64-linux-gnu", "/lib/aarch64-linux-gnu", "/lib64"}

// HostDriverProbe reads the real filesystem.
func HostDriverProbe() DriverProbe {
	return DriverProbe{ReadFile: os.ReadFile, Glob: filepath.Glob, EvalSymlinks: filepath.EvalSymlinks}
}

// Versions returns the kernel module and userspace library versions ("" when unknown: no
// NVIDIA driver loaded, or the libraries weren't found).
func (p DriverProbe) Versions() (kernel, library string) {
	proc := p.ProcPath
	if proc == "" {
		proc = "/proc/driver/nvidia/version"
	}
	if p.ReadFile == nil {
		return "", ""
	}
	b, err := p.ReadFile(proc)
	if err != nil {
		return "", ""
	}
	kernel = ParseNVIDIAKernelVersion(string(b))
	if kernel == "" {
		return "", ""
	}
	return kernel, p.libraryVersion()
}

// libraryVersion is the version the loader would use: the target of libnvidia-ml.so.1 (else
// libcuda.so.1), else the one versioned file present. Several different versioned files and
// no symlink is ambiguous ("").
func (p DriverProbe) libraryVersion() string {
	dirs := p.LibDirs
	if len(dirs) == 0 {
		dirs = DefaultLibDirs
	}
	for _, lib := range []string{"libnvidia-ml", "libcuda"} {
		for _, d := range dirs {
			if p.EvalSymlinks != nil {
				if t, err := p.EvalSymlinks(filepath.Join(d, lib+".so.1")); err == nil {
					if v := NVIDIALibraryVersion(t); v != "" {
						return v
					}
				}
			}
		}
	}
	if p.Glob == nil {
		return ""
	}
	var found []string
	for _, lib := range []string{"libnvidia-ml", "libcuda"} {
		for _, d := range dirs {
			matches, _ := p.Glob(filepath.Join(d, lib+".so.*.*"))
			for _, m := range matches {
				if v := NVIDIALibraryVersion(m); v != "" && !slices.Contains(found, v) {
					found = append(found, v)
				}
			}
		}
		if len(found) > 0 {
			break
		}
	}
	if len(found) == 1 {
		return found[0]
	}
	return ""
}

// Check returns a driver_mismatch fault when the kernel module and libraries disagree, else
// nil (also when either is unknown).
func (p DriverProbe) Check() *GPUFault {
	k, l := p.Versions()
	if k == "" || l == "" || k == l {
		return nil
	}
	return DriverMismatchFault(k, l)
}
