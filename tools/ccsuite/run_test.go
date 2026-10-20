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

func TestSkipReason(t *testing.T) {
	browser := Case{Name: "web_form", Phase: "browser", Status: "required", Target: "browser"}
	for _, c := range []struct {
		c       Case
		llm     string
		pending bool
		want    string
	}{
		{browser, "shared", false, ""},
		{browser, "scripted", false, "the scripted model does no computer tasks"},
		{Case{Phase: "browser", Status: "pending"}, "shared", false, "pending"},
		{Case{Phase: "browser", Status: "pending"}, "shared", true, ""},
		{Case{Phase: "tests", Status: "required"}, "scripted", false, ""},
	} {
		if got := skipReason(c.c, c.llm, c.pending, "linux"); got != c.want {
			t.Errorf("%+v %s: %q, want %q", c.c, c.llm, got, c.want)
		}
	}
}
