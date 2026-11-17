package voice

import (
	"context"
	"encoding/json"
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

// node_context.agents is the package agents' context, as the Python node sent it (CC lists the
// devices in the prompt from it); none, and it is left out.
func TestNodeContextCarriesAgentContext(t *testing.T) {
	p := &Pipeline{}
	if _, ok := p.nodeContext(context.Background())["agents"]; ok {
		t.Fatal("no AgentContext: agents is left out")
	}
	ha := json.RawMessage(`{"device_controls":{}}`)
	p.AgentContext = func(context.Context) map[string]json.RawMessage {
		return map[string]json.RawMessage{"home_assistant": ha}
	}
	a, _ := p.nodeContext(context.Background())["agents"].(map[string]json.RawMessage)
	if string(a["home_assistant"]) != string(ha) {
		t.Fatalf("agents = %v", a)
	}
	p.AgentContext = func(context.Context) map[string]json.RawMessage { return nil }
	if _, ok := p.nodeContext(context.Background())["agents"]; ok {
		t.Fatal("empty agent context: agents is left out")
	}
}
