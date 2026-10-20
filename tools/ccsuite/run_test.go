package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A run's model calls are the log's lines after the offset, kept in the run's own llm.jsonl.
func TestCopyLines(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "llm.jsonl")
	os.WriteFile(log, []byte("{\"a\":1}\n"), 0o644)
	offset := fileSize(log)
	f, _ := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("{\"b\":2}\n\n{\"c\":3}\n")
	f.Close()
	dst := filepath.Join(dir, "run-1", "llm.jsonl")
	os.MkdirAll(filepath.Dir(dst), 0o755)
	if n := copyLines(log, offset, dst); n != 2 {
		t.Fatalf("rounds = %d", n)
	}
	if b, _ := os.ReadFile(dst); string(b) != "{\"b\":2}\n{\"c\":3}\n" {
		t.Fatalf("copy = %q", b)
	}
	if n := copyLines(filepath.Join(dir, "missing"), 0, dst); n != 0 {
		t.Fatalf("missing log: %d", n)
	}
}
