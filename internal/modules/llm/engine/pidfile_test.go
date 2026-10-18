package engine

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The resolver sweeps its pid dir once, on first use, before any engine starts.
func TestResolverSweepsPIDDirOnFirstUse(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "llm-deadbeef.pid")
	if err := os.WriteFile(stale, []byte(`{"pid":2147483646,"exe":"/x/llama-server","start":"1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &Resolver{PIDDir: dir}
	_ = r.Instances()
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale pid file not swept: %v", err)
	}
}

// Engines the resolver starts leave a pid file in PIDDir while they run.
func TestResolverEnginesWritePIDFiles(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("pid files are written on darwin and linux only")
	}
	g := newRig(t)
	g.r.PIDDir = t.TempDir()
	g.src.set(g.llm(LabelLive, g.model("qwen.gguf")))
	ep := g.ready(LabelLive)
	if _, err := os.Stat(filepath.Join(g.r.PIDDir, ep.Engine+".pid")); err != nil {
		t.Fatalf("engine pid file: %v", err)
	}
}
