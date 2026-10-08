package ocrq

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/modules/ocr"
)

func load(t *testing.T, v any, parts ...string) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(append([]string{filepath.Dir(file), "..", "..", "..", "..", "fixtures", "golden", "recipes"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

func canon(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var x any
	_ = json.Unmarshal(raw, &x)
	out, _ := json.Marshal(x)
	return string(out)
}

// toPy decodes a golden JSON value the way the coercion sees a decoded reply.
func toPy(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s // a string reply: Coerce parses it
	}
	v, err := pyjson.Loads(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// b5Fixed are the coercion rows legacy rejected and jarvisd fixes (B5 widened, §12.0).
var b5Fixed = map[string]string{
	"numeric_servings_B5": `{"title":"Stew","description":null,"ingredients":[{"name":"beef","quantity":null,"unit":null,"notes":null},{"name":"carrot","quantity":null,"unit":null,"notes":null},{"name":"potato","quantity":null,"unit":null,"notes":null}],"steps":["Brown","Simmer"],"prep_time_minutes":0,"cook_time_minutes":0,"total_time_minutes":0,"servings":"4","tags":[],"source":{"type":"ocr","original_filename":null,"ocr_tier_used":null}}`,
	"float_servings":      `{"title":"Stew","description":null,"ingredients":[{"name":"beef","quantity":null,"unit":null,"notes":null},{"name":"carrot","quantity":null,"unit":null,"notes":null},{"name":"potato","quantity":null,"unit":null,"notes":null}],"steps":["Brown","Simmer"],"prep_time_minutes":0,"cook_time_minutes":0,"total_time_minutes":0,"servings":"2.5","tags":[],"source":{"type":"ocr","original_filename":null,"ocr_tier_used":null}}`,
	"dict_quantity":       `{"title":"Cake","description":null,"ingredients":[{"name":"flour","quantity":"1.5","unit":"cups","notes":null},{"name":"sugar","quantity":"1","unit":null,"notes":null},{"name":"egg","quantity":null,"unit":"each","notes":null}],"steps":["Mix","Bake"],"prep_time_minutes":0,"cook_time_minutes":0,"total_time_minutes":0,"servings":null,"tags":[],"source":{"type":"ocr","original_filename":null,"ocr_tier_used":null}}`,
	"float_total":         `{"title":"Roast","description":null,"ingredients":[{"name":"a","quantity":null,"unit":null,"notes":null},{"name":"b","quantity":null,"unit":null,"notes":null},{"name":"c","quantity":null,"unit":null,"notes":null}],"steps":["x","y"],"prep_time_minutes":8,"cook_time_minutes":3,"total_time_minutes":10,"servings":null,"tags":[],"source":{"type":"ocr","original_filename":null,"ocr_tier_used":null}}`,
}

func TestGoldenCoerce(t *testing.T) {
	var g struct {
		Rows []struct {
			Name    string          `json:"name"`
			Input   json.RawMessage `json:"input"`
			Out     json.RawMessage `json:"out"`
			Error   string          `json:"error"`
			Message string          `json:"message"`
		} `json:"coerce_recipe_draft"`
	}
	load(t, &g, "llm_parse.json")
	if len(g.Rows) < 30 {
		t.Fatalf("golden has %d rows", len(g.Rows))
	}
	seen := 0
	for _, r := range g.Rows {
		got, err := Coerce(toPy(t, r.Input), "ocr")
		if fixed, ok := b5Fixed[r.Name]; ok {
			seen++
			if r.Error == "" {
				t.Errorf("%s: legacy no longer fails here; drop the override", r.Name)
			}
			if err != nil || canon(t, got) != canon(t, json.RawMessage(fixed)) {
				t.Errorf("%s (B5 fixed): got %s %v", r.Name, canon(t, got), err)
			}
			continue
		}
		if r.Error != "" {
			if err == nil {
				t.Errorf("%s: want %s %q, got %s", r.Name, r.Error, r.Message, canon(t, got))
			} else if r.Error == "ValueError" && err.Error() != r.Message {
				t.Errorf("%s: message %q, want %q", r.Name, err.Error(), r.Message)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", r.Name, err)
			continue
		}
		if c, w := canon(t, got), canon(t, r.Out); c != w {
			t.Errorf("%s:\n got %s\nwant %s", r.Name, c, w)
		}
	}
	if seen != len(b5Fixed) {
		t.Fatalf("B5 overrides matched %d of %d rows", seen, len(b5Fixed))
	}
}

func TestGoldenQualityAndReadings(t *testing.T) {
	var g struct {
		Score []struct {
			Name       string          `json:"name"`
			Confidence *float64        `json:"confidence"`
			Text       string          `json:"text"`
			Out        json.RawMessage `json:"out"`
		} `json:"score_quality"`
		Rank     []string `json:"engine_rank"`
		Readings []struct {
			Input []struct {
				Provider   *string  `json:"provider"`
				ReceivedAt *string  `json:"received_at"`
				Results    []Result `json:"results"`
			} `json:"input"`
			Out struct {
				Order    []*string  `json:"order"`
				Texts    [][]string `json:"texts"`
				GateText *string    `json:"gate_text"`
			} `json:"out"`
		} `json:"readings"`
		P3 []struct {
			Name  string `json:"name"`
			Draft Draft  `json:"draft"`
			Out   bool   `json:"out"`
		} `json:"p3_trigger"`
	}
	load(t, &g, "ocr.json")
	if len(g.Score) == 0 || len(g.Readings) == 0 || len(g.P3) == 0 {
		t.Fatal("empty golden")
	}
	for _, r := range g.Score {
		if c, w := canon(t, ScoreQuality(r.Text, r.Confidence)), canon(t, r.Out); c != w {
			t.Errorf("score %s:\n got %s\nwant %s", r.Name, c, w)
		}
	}
	if !reflect.DeepEqual(g.Rank, ocr.EngineRank) {
		t.Errorf("engine rank %v, want %v", ocr.EngineRank, g.Rank)
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	for i, c := range g.Readings {
		var in []Reading
		for _, r := range c.Input {
			in = append(in, Reading{Provider: str(r.Provider), ReceivedAt: str(r.ReceivedAt), Results: r.Results})
		}
		sorted := Sort(in)
		var order, wantOrder []string
		for _, r := range sorted {
			order = append(order, r.Provider)
		}
		for _, p := range c.Out.Order {
			wantOrder = append(wantOrder, str(p))
		}
		if !reflect.DeepEqual(order, wantOrder) {
			t.Errorf("readings %d order %v, want %v", i, order, wantOrder)
		}
		texts := [][]string{}
		for _, tx := range Texts(sorted) {
			texts = append(texts, []string{tx.Provider, tx.Text})
		}
		if canon(t, texts) != canon(t, c.Out.Texts) {
			t.Errorf("readings %d texts %v, want %v", i, texts, c.Out.Texts)
		}
		if g := GateText(Texts(sorted)); g != str(c.Out.GateText) {
			t.Errorf("readings %d gate text %q, want %v", i, g, c.Out.GateText)
		}
	}
	for _, r := range g.P3 {
		if got := NeedsCleanup(&r.Draft); got != r.Out {
			t.Errorf("p3 trigger %s: %v, want %v", r.Name, got, r.Out)
		}
	}
}

type request struct {
	Body struct {
		Temperature float64 `json:"temperature"`
		MaxTokens   int     `json:"max_tokens"`
		Messages    []struct {
			Content string `json:"content"`
		} `json:"messages"`
	} `json:"body"`
}

func TestGoldenPrompts(t *testing.T) {
	var p2 struct {
		Inputs struct {
			One        [][]string `json:"one"`
			Two        [][]string `json:"two"`
			Unlabelled [][]string `json:"unlabelled"`
		} `json:"inputs"`
		Requests struct {
			One        request         `json:"one_reading"`
			Two        request         `json:"two_readings"`
			Unlabelled request         `json:"two_unlabelled_readings"`
			Draft      json.RawMessage `json:"draft_from_reply"`
		} `json:"requests"`
	}
	load(t, &p2, "prompts", "P2_ocr_structuring.json")
	texts := func(in [][]string) []Text {
		var out []Text
		for _, p := range in {
			out = append(out, Text{Provider: p[0], Text: p[1]})
		}
		return out
	}
	for name, c := range map[string]struct {
		in  []Text
		req request
	}{"one": {texts(p2.Inputs.One), p2.Requests.One}, "two": {texts(p2.Inputs.Two), p2.Requests.Two},
		"unlabelled": {texts(p2.Inputs.Unlabelled), p2.Requests.Unlabelled}} {
		m := c.req.Body.Messages
		if got := P2System(len(c.in)); got != m[0].Content {
			t.Errorf("P2 %s system:\n got %q\nwant %q", name, got, m[0].Content)
		}
		if got := P2User(c.in); got != m[1].Content {
			t.Errorf("P2 %s user:\n got %q\nwant %q", name, got, m[1].Content)
		}
		if c.req.Body.MaxTokens != 1100 || c.req.Body.Temperature != 0 {
			t.Errorf("P2 params %+v", c.req.Body)
		}
	}
	good := `{"title": "Crepes", "description": "Thin.", "ingredients": [{"name": "flour", "quantity": "1", "unit": "cup"}, ` +
		`{"name": "eggs", "quantity": "3"}, {"name": "milk", "quantity": "2", "unit": "cups"}], "steps": ["Beat", "Cook"]}`
	d, err := Coerce(good, "ocr")
	if err != nil || canon(t, d) != canon(t, p2.Requests.Draft) {
		t.Fatalf("draft from reply: %s %v", canon(t, d), err)
	}

	var p3 struct {
		Requests struct {
			Live request `json:"model_live"`
		} `json:"requests"`
	}
	load(t, &p3, "prompts", "P3_draft_cleanup.json")
	m := p3.Requests.Live.Body.Messages
	if m[0].Content != P3System || P3User(d) != m[1].Content || p3.Requests.Live.Body.MaxTokens != 1000 {
		t.Errorf("P3:\n got %q\nwant %q", P3User(d), m[1].Content)
	}

	var p1r struct {
		Inputs struct {
			Broken string `json:"broken"`
		} `json:"inputs"`
		Requests []request `json:"requests"`
	}
	load(t, &p1r, "prompts", "P1r_json_repair_draft.json")
	if want := p1r.Requests[0].Body.Messages[1].Content; want != "Schema: "+DraftSchemaHint+"\nMalformed JSON:\n"+p1r.Inputs.Broken {
		t.Errorf("P1r draft hint: want %q", want)
	}
}
