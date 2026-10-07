package dates

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func golden(t *testing.T, name string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "fixtures", "golden", "llm", name)
}

type row struct {
	Text     string   `json:"text"`
	DateKeys []string `json:"date_keys"`
}

// G6: every corpus row (4,987) gives exactly the legacy matcher's keys.
func TestExtractCorpus(t *testing.T) {
	f, err := os.Open(golden(t, "date_keys_corpus.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n, bad := 0, 0
	for sc.Scan() {
		var r row
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		n++
		if got := Extract(r.Text); !slices.Equal(got, r.DateKeys) {
			bad++
			if bad <= 20 {
				t.Errorf("Extract(%q) = %q, want %q", r.Text, got, r.DateKeys)
			}
		}
	}
	if n != 4987 {
		t.Fatalf("corpus rows: %d, want 4987", n)
	}
	if bad > 0 {
		t.Fatalf("%d/%d corpus rows differ", bad, n)
	}
}

// G6 edge file: false positives, negatives, non-ASCII, Python's Unicode classes, CC-style text.
func TestExtractEdge(t *testing.T) {
	raw, err := os.ReadFile(golden(t, "date_keys_edge.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []row
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if got := Extract(r.Text); !slices.Equal(got, r.DateKeys) {
			t.Errorf("Extract(%q) = %q, want %q", r.Text, got, r.DateKeys)
		}
	}
}

func TestExtractNeverNil(t *testing.T) {
	if got := Extract(""); got == nil || len(got) != 0 {
		t.Fatalf("Extract(\"\") = %#v, want []", got)
	}
}

func TestVocabulary(t *testing.T) {
	raw, err := os.ReadFile(golden(t, "vocabulary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Version    string            `json:"version"`
		StaticKeys []string          `json:"static_keys"`
		Dynamic    []DynamicPattern  `json:"dynamic_patterns"`
		Patterns   map[string]string `json:"patterns"`
		Notes      map[string]string `json:"notes"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if v.Version != Version || !slices.Equal(v.StaticKeys, Vocabulary) || !slices.Equal(v.Dynamic, DynamicPatterns) {
		t.Fatalf("vocabulary differs:\n got %v %q %v\nwant %v %q %v", Version, Vocabulary, DynamicPatterns, v.Version, v.StaticKeys, v.Dynamic)
	}
	if len(Vocabulary) != 64 {
		t.Fatalf("%d static keys, want 64", len(Vocabulary))
	}
	for _, kv := range TimePatterns {
		if v.Patterns[kv.Key] != kv.Value {
			t.Errorf("pattern %s = %q, want %q", kv.Key, kv.Value, v.Patterns[kv.Key])
		}
	}
	for _, kv := range Notes {
		if v.Notes[kv.Key] != kv.Value {
			t.Errorf("note %s = %q, want %q", kv.Key, kv.Value, v.Notes[kv.Key])
		}
	}
	if len(v.Patterns) != len(TimePatterns) || len(v.Notes) != len(Notes) {
		t.Fatal("pattern/note counts differ")
	}
}

func BenchmarkExtract(b *testing.B) {
	for range b.N {
		Extract("remind me tomorrow morning at 7:30pm to call mom, and again in 2 hours")
	}
}
