package voice

import (
	"context"
	"testing"
)

func TestNodeContextCarriesTheVocabulary(t *testing.T) {
	p := &Pipeline{Cfg: Config{Timezone: "Europe/Paris"}}
	if nc := p.nodeContext(context.Background()); nc["timezone"] != "Europe/Paris" || nc["stt_vocabulary"] != nil {
		t.Fatalf("no vocabulary: %v", nc)
	}
	p.Vocabulary = func(context.Context) string { return "Omawrite, Neovim" }
	if nc := p.nodeContext(context.Background()); nc["stt_vocabulary"] != "Omawrite, Neovim" {
		t.Fatalf("%v", nc)
	}
	p.Vocabulary = func(context.Context) string { return "" }
	if _, ok := p.nodeContext(context.Background())["stt_vocabulary"]; ok {
		t.Fatal("an empty vocabulary is left out")
	}
}
