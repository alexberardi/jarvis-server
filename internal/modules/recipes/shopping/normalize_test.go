package shopping

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGoldenNormalizeName(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "fixtures", "golden", "recipes", "normalize_name.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Source string `json:"source"`
		Input  string `json:"input"`
		Out    string `json:"out"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) < 300 {
		t.Fatalf("golden has %d rows", len(rows))
	}
	for _, r := range rows {
		if got := NormalizeName(r.Input); got != r.Out {
			t.Errorf("NormalizeName(%q) [%s] = %q, want %q", r.Input, r.Source, got, r.Out)
		}
	}
}

func TestNormalizeNameEdges(t *testing.T) {
	for in, want := range map[string]string{
		"2% milk":                            "2% milk",
		"1 1/2 lb beef sirloin, sliced thin": "beef sirloin",
		"lbsx flour":                         "lbsx flour",
		"   ":                                "",
		"Olive oil, divided\n":               "olive oil",
	} {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}
