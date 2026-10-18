package watches

import (
	"strings"
	"testing"
)

func webWatch() Watch {
	return Watch{Title: "TV price", SourceKind: SourceWeb, Source: Source{URL: "https://www.example.com/tv", Extract: "the price"},
		Condition: Condition{Op: OpLT, Value: "500"}, FirePolicy: FireOnce}
}

func TestWatchValidate(t *testing.T) {
	signal := Watch{Title: "Door", SourceKind: SourceSignal, Source: Source{Kind: "door"},
		Condition: Condition{Op: OpChanged, Field: "state"}, FirePolicy: FireEveryChange}
	if err := webWatch().Validate(); err != nil {
		t.Fatalf("web: %v", err)
	}
	if err := signal.Validate(); err != nil {
		t.Fatalf("signal: %v", err)
	}
	tests := []struct {
		name string
		edit func(*Watch)
		want string
	}{
		{"no title", func(w *Watch) { w.Title = " " }, "watch: a title is required"},
		{"unknown source", func(w *Watch) { w.SourceKind = "email" }, `watch: unknown source kind "email"`},
		{"url and query", func(w *Watch) { w.Source.Query = "tv deals" }, "watch: a web watch needs a URL or a query, not both"},
		{"neither url nor query", func(w *Watch) { w.Source.URL = "" }, "watch: a web watch needs a URL or a query, not both"},
		{"not http", func(w *Watch) { w.Source.URL = "ftp://example.com/tv" }, "watch: the URL must be http or https"},
		{"unparsable url", func(w *Watch) { w.Source.URL = "https://exa mple.com/%zz" }, "watch: the URL must be http or https"},
		{"no host", func(w *Watch) { w.Source.URL = "https:///tv" }, "watch: the URL must be http or https"},
		{"no extract", func(w *Watch) { w.Source.Extract = "" }, "watch: a web watch needs what to read (extract)"},
		{"signal without kind", func(w *Watch) { *w = signal; w.Source.Kind = "" }, "watch: a signal watch needs a signal kind"},
		{"unknown fire policy", func(w *Watch) { w.FirePolicy = "" }, `watch: unknown fire policy ""`},
		{"cooldown without seconds", func(w *Watch) { w.FirePolicy = FireCooldown }, "watch: a cooldown needs cooldown_seconds > 0"},
		{"bad condition", func(w *Watch) { w.Condition = Condition{Op: OpLT, Value: "cheap"} }, "watch: condition: lt needs a number"},
		{"absent_for on web", func(w *Watch) {
			w.Condition = Condition{Op: OpAbsentFor, Field: "state", Value: "open", For: "1h"}
		}, "watch: condition: absent_for only works on signals"},
	}
	for _, tt := range tests {
		w := webWatch()
		tt.edit(&w)
		err := w.Validate()
		if err == nil || err.Error() != tt.want {
			t.Errorf("%s: Validate() = %v; want %q", tt.name, err, tt.want)
		}
	}
	w := webWatch()
	w.FirePolicy, w.CooldownSeconds = FireCooldown, 3600
	if err := w.Validate(); err != nil {
		t.Errorf("cooldown with seconds: %v", err)
	}
	w = webWatch()
	w.Source.URL, w.Source.Query = "", "65 inch tv deals"
	if err := w.Validate(); err != nil {
		t.Errorf("query: %v", err)
	}
}

func TestDescribe(t *testing.T) {
	sig := func(c Condition) Watch {
		return Watch{Title: "t", SourceKind: SourceSignal, Source: Source{Kind: "door"}, Condition: c, FirePolicy: FireOnce}
	}
	query := webWatch()
	query.Source.URL, query.Source.Query = "", "65 inch tv deals"
	tests := []struct {
		name string
		w    Watch
		want string
	}{
		{"lt with url", webWatch(), "the price to drop below 500 on example.com"},
		{"query", query, "the price to drop below 500 in searches for “65 inch tv deals”"},
		{"gt", sig(Condition{Op: OpGT, Field: "temperature", Value: "80"}), "the temperature to go above 80"},
		{"changed, no field", sig(Condition{Op: OpChanged}), "the signal to change"},
		{"equals", sig(Condition{Op: OpEquals, Field: "state", Value: "open"}), "the state to be open"},
		{"contains", sig(Condition{Op: OpContains, Value: "package"}), "the signal to mention package"},
		{"absent_for", sig(Condition{Op: OpAbsentFor, Field: "state", Value: "closed", For: "2h"}), "the state not to be closed for 2h"},
	}
	for _, tt := range tests {
		if got := Describe(tt.w); got != tt.want {
			t.Errorf("%s: Describe() = %q; want %q", tt.name, got, tt.want)
		}
	}
	lt := sig(Condition{Op: OpLT, Field: "battery", Value: "20"})
	if got := Describe(lt); !strings.HasPrefix(got, "the battery to drop below 20") {
		t.Errorf("lt signal: %q", got)
	}
}

func TestSourceLabel(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.example.com/tv":       "example.com",
		"http://shop.example.co.uk:8080/x": "shop.example.co.uk",
		"%zz":                              "",
		"":                                 "",
	} {
		if got := SourceLabel(in); got != want {
			t.Errorf("SourceLabel(%q) = %q; want %q", in, got, want)
		}
	}
}
