package phone

import "testing"

// A call hears the address's parts as one Address; a saved one-line Address wins.
func TestSelectForCallJoinsAddressParts(t *testing.T) {
	parts := ParseCallContext(map[string]any{"fields": []any{
		map[string]any{"key": "full_name", "value": "Avery Quill"},
		map[string]any{"key": "street", "value": "9 Oak Lane"},
		map[string]any{"key": "city", "value": "Springfield"},
		map[string]any{"key": "state", "value": "PA"},
		map[string]any{"key": "postal_code", "value": "19000"},
	}})
	got := SelectForCall(parts)
	if len(got) != 2 || got[1].Key != "address" || got[1].Value != "9 Oak Lane, Springfield, PA 19000" || got[1].Category != CatHome || got[1].Tier != TierIfAsked {
		t.Fatalf("got %+v", got)
	}
	line := ParseCallContext(map[string]any{"fields": []any{
		map[string]any{"key": "address", "value": "1 Elm St"}, map[string]any{"key": "city", "value": "Springfield"},
	}})
	if got := SelectForCall(line); len(got) != 1 || got[0].Value != "1 Elm St" {
		t.Fatalf("got %+v", got)
	}
}
