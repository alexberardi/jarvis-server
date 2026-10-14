package apps

import (
	"strings"
	"testing"
)

func TestVocabulary(t *testing.T) {
	v := Vocabulary([]string{"Alacritty", "firefox", "Omawrite"}, desktopApps, 400)
	if !strings.HasPrefix(v, "Alacritty, firefox, Omawrite, Aether, ") {
		t.Fatalf("open apps first, then one-word names: %q", v)
	}
	for _, want := range []string{"Neovim", "Steam", "Omacalc", "Tensaku"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %s: %q", want, v)
		}
	}
	if strings.Contains(v, "Avahi") || strings.Contains(v, "Firefox") || strings.Contains(v, ", X,") {
		t.Errorf("descriptive names, duplicates or single letters: %q", v)
	}
	if len(v) > 400 {
		t.Fatalf("len %d", len(v))
	}
	if got := Vocabulary(nil, desktopApps, 30); len(got) > 30 || strings.HasSuffix(got, ",") || strings.HasSuffix(got, " ") {
		t.Fatalf("cut: %q", got)
	}
	if Vocabulary(nil, nil, 400) != "" {
		t.Fatal("nothing")
	}
}
