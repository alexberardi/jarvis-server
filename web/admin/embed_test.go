package adminui

import (
	"bytes"
	"testing"
)

func TestPlaceholderEmbedded(t *testing.T) {
	if !bytes.Contains(Placeholder(), []byte("admin UI not built")) {
		t.Fatal("placeholder.html is not embedded")
	}
}
