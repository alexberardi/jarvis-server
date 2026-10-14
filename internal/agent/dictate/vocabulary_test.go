package dictate

import (
	"context"
	"strings"
	"testing"
)

func TestVocabularyPutsOpenAppsFirst(t *testing.T) {
	win := omawriteDesktop(true) // Alacritty (focused), firefox, omawrite
	ts := &Tools{Win: win, Apps: installed}
	v := ts.Vocabulary(context.Background())
	if !strings.HasPrefix(v, "Alacritty, Firefox, Omawrite, Neovim, Steam") {
		t.Fatalf("%q", v)
	}
	if v := (&Tools{}).Vocabulary(context.Background()); v != "" {
		t.Fatalf("nothing known: %q", v)
	}
}
