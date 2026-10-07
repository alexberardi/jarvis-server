package fuzzy

import (
	"math"
	"testing"
)

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// Expectations computed with rapidfuzz 3.14.6 (CC's .venv): fuzz.WRatio(query, name) on
// inputs already normalised as the plan step does (lowercase, [^a-z0-9 ] stripped, trimmed).
var golden = []struct {
	query, name string
	want        float64
}{
	// docs/cc/11-phone.md §9 golden table.
	{"tonys pizzeria", "tonys pizzeria", 100.0000},
	{"tonys pizzaria", "tonys pizzeria", 92.8571},
	{"tonys", "tonys pizzeria", 90.0000},
	{"tony", "tonys pizzeria", 90.0000},
	{"pizzeria", "tonys pizzeria", 90.0000},
	{"tonys pizza", "tonys pizzeria", 88.0000},
	{"tonis pizza", "tonys pizzeria", 80.0000},
	{"pizza", "tonys pizzeria", 76.0000},
	{"completely unrelated dentist", "tonys pizzeria", 32.1429},
	{"", "tonys pizzeria", 0.0000},
	{"cvs", "cvs pharmacy", 90.0000},
	{"walgreens pharmacy", "cvs pharmacy", 85.5000},
	{"the pharmacy", "cvs pharmacy", 76.0000},
	{"cvs on route 9", "cvs pharmacy", 38.4615},
	{"dr smith", "dr smith dental", 90.0000},
	{"smith", "dr smith dental", 90.0000},
	{"dr smyth", "dr smith dental", 85.5000},
	{"dentist", "dr smith dental", 62.1818},
	{"auto body", "joes auto body", 90.0000},
	{"joe", "joes auto body", 90.0000},
	{"joes garage", "joes auto body", 50.6667},
	{"marios pizza", "marios", 90.0000},
	{"marios italian restaurant and pizzeria of freehold", "marios", 60.0000},
	{"pizza", "pizza hut", 90.0000},
	{"tonys pizza", "pizza hut", 67.8571},
	{"nail salon", "hair salon", 80.0000},
	{"main st vet", "main street vet", 84.6154},

	// Extra pseudo-random pairs (seeded), checked against rapidfuzz.
	{"pharmacy auto route", "pizzeria", 38.5714},
	{"smith", "pizze olive hut st pizza", 48.8571},
	{"pizzeria street pizzeria salon", "pizza a nail cvs", 48.4615},
	{"nail", "auto pizza street pizza salon", 45.0000},
	{"body pharmacy salon", "nail", 45.0000},
	{"cvs nail", "route st smith cvs salon", 85.5000},
	{"hair", "the 9", 22.2222},
	{"joes nail olive", "smith dentel street bobs", 39.9000},
	{"pizzeria nail", "hut the garden", 22.2222},
	{"dental hair pizzeria cvs", "body main bob dr pharmacy", 40.8163},
	{"pizza 9 pizzeria bob", "nail bobs garden a dr", 37.0732},
	{"hair the nail", "pizzeria a pizzeria vet", 39.4615},
	{"pizza", "route nail 9", 20.0000},
	{"burger auto garden", "tony joes smith", 24.2424},
	{"the", "st", 60.0000},
	{"king street", "euto olive xyzzy the", 36.0000},
	{"auto salon vet garden", "a body", 36.0000},
	{"burger body smith", "street phermacy pizzeria main", 35.2059},
	{"tony the", "main vet dental tony pharmacy", 85.5000},
	{"hair nail dr", "burger xyzzy", 23.7500},
	{"joes", "auto auto auto auto cvs", 36.0000},
	{"pizza st pizzeria st", "mein cvs dr hair", 33.3333},
	{"nail", "salon cvs", 51.4286},
	{"pizzeria", "heir auto", 35.2941},
}

func TestWRatioGolden(t *testing.T) {
	for _, c := range golden {
		if got := WRatio(c.query, c.name); round2(got) != round2(c.want) {
			t.Errorf("WRatio(%q, %q) = %.4f, want %.4f", c.query, c.name, got, c.want)
		}
	}
}

func TestExtractOneCutoffAndTies(t *testing.T) {
	cases := []struct {
		name    string
		book    []Choice
		query   string
		wantKey string // "" = no match
	}{
		{"tie goes first", []Choice{{"a", "tonys pizzeria"}, {"b", "tonys barber shop"}}, "tonys", "a"},
		{"tie goes first reversed", []Choice{{"b", "tonys barber shop"}, {"a", "tonys pizzeria"}}, "tonys", "b"},
		{"pharmacy tie", []Choice{{"c", "cvs pharmacy"}, {"w", "walgreens pharmacy"}}, "pharmacy", "c"},
		{"pharmacy tie reversed", []Choice{{"w", "walgreens pharmacy"}, {"c", "cvs pharmacy"}}, "pharmacy", "w"},
		{"clear winner", []Choice{{"c", "cvs pharmacy"}, {"w", "walgreens pharmacy"}}, "walgreens", "w"},
		{"exactly on cutoff", []Choice{{"t", "tonys pizzeria"}}, "tonis pizza", "t"},
		{"below cutoff", []Choice{{"t", "tonys pizzeria"}}, "pizza", ""},
		{"empty book", nil, "tonys", ""},
	}
	for _, c := range cases {
		best, score, ok := ExtractOne(c.query, c.book, 80)
		if c.wantKey == "" {
			if ok {
				t.Errorf("%s: matched %q (%.2f), want none", c.name, best.Key, score)
			}
			continue
		}
		if !ok || best.Key != c.wantKey {
			t.Errorf("%s: got %q ok=%v, want %q", c.name, best.Key, ok, c.wantKey)
		}
	}
}
