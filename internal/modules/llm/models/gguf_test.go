package models

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeGGUF writes a minimal v3 GGUF header (no tensors): a u32 KV, a string-array KV, then
// tokenizer.chat_template when tpl is non-empty.
func writeGGUF(t *testing.T, magic, tpl string) string {
	t.Helper()
	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) { le(uint64(len(s))); b.WriteString(s) }
	kvs := uint64(2)
	if tpl != "" {
		kvs++
	}
	b.WriteString(magic)
	le(uint32(3))
	le(uint64(0))
	le(kvs)
	str("general.alignment")
	le(ggufUint32)
	le(uint32(32))
	str("tokenizer.ggml.tokens")
	le(ggufArray)
	le(ggufString)
	le(uint64(3))
	for _, s := range []string{"<s>", "tools", "</s>"} {
		str(s)
	}
	if tpl != "" {
		str("tokenizer.chat_template")
		le(ggufString)
		str(tpl)
	}
	p := filepath.Join(t.TempDir(), "m.gguf")
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGGUFChatTemplate(t *testing.T) {
	tpl := "{%- if tools %}{{ tools }}{% endif %}"
	got, err := GGUFChatTemplate(writeGGUF(t, "GGUF", tpl))
	if err != nil || got != tpl {
		t.Fatalf("template: %q, %v", got, err)
	}
	got, err = GGUFChatTemplate(writeGGUF(t, "GGUF", ""))
	if err != nil || got != "" {
		t.Fatalf("no template key: %q, %v", got, err)
	}
	if _, err := GGUFChatTemplate(writeGGUF(t, "GGML", tpl)); err == nil || !strings.Contains(err.Error(), "not a GGUF") {
		t.Fatalf("bad magic: %v", err)
	}
}
