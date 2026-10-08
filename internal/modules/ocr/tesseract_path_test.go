package ocr

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func noPath(string) (string, error) { return "", errors.New("not found") }

// A LaunchDaemon starts with PATH=/usr/bin:/bin:/usr/sbin:/sbin, so a Homebrew tesseract is
// not on it (A10e: the MBP's rc4 had no OCR engine at all). The well-known package-manager
// directories are searched after PATH.
func TestFindTesseractFallsBackToPackageManagerDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec bits")
	}
	empty, brew := t.TempDir(), t.TempDir()
	bin := filepath.Join(brew, "tesseract")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findTesseract(noPath, []string{empty, brew}); got != bin {
		t.Fatalf("findTesseract = %q, want %q", got, bin)
	}

	// A symlink (Homebrew links bin/tesseract into the Cellar) counts.
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "tesseract")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	if got := findTesseract(noPath, []string{linkDir}); got != link {
		t.Fatalf("findTesseract via symlink = %q, want %q", got, link)
	}
}

func TestFindTesseractPrefersPath(t *testing.T) {
	brew := t.TempDir()
	if err := os.WriteFile(filepath.Join(brew, "tesseract"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	onPath := func(string) (string, error) { return "/usr/bin/tesseract", nil }
	if got := findTesseract(onPath, []string{brew}); got != "/usr/bin/tesseract" {
		t.Fatalf("findTesseract = %q, want the PATH hit", got)
	}
}

func TestFindTesseractSkipsNonExecutablesAndDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec bits")
	}
	plain, dir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(plain, "tesseract"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "tesseract"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findTesseract(noPath, []string{plain, dir, filepath.Join(dir, "missing")}); got != "" {
		t.Fatalf("findTesseract = %q, want none", got)
	}
}
