package extract

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Goldens dumped from the legacy Python code (tools/golden/export_recipes.py).

func repoPath(parts ...string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(append([]string{filepath.Dir(file), "..", "..", "..", ".."}, parts...)...)
}

func load(t *testing.T, v any, parts ...string) {
	t.Helper()
	raw, err := os.ReadFile(repoPath(parts...))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

// canon re-encodes a value through any so key order and number forms compare equal.
func canon(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var x any
	if err := json.Unmarshal(raw, &x); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(x)
	return string(out)
}

type pageRow struct {
	Name string          `json:"name"`
	HTML string          `json:"html"`
	Out  json.RawMessage `json:"out"`
}

func urlOf(out json.RawMessage) string {
	var o struct {
		SourceURL string `json:"source_url"`
	}
	_ = json.Unmarshal(out, &o)
	if o.SourceURL == "" {
		return "https://example.com/test"
	}
	return o.SourceURL
}

func TestGoldenSchemaOrgAndHeuristic(t *testing.T) {
	var g struct {
		SchemaOrg []pageRow `json:"schema_org"`
		Heuristic []pageRow `json:"heuristic"`
	}
	load(t, &g, "fixtures", "golden", "recipes", "extract.json")
	if len(g.SchemaOrg) == 0 || len(g.Heuristic) == 0 {
		t.Fatal("empty golden")
	}
	for _, r := range g.SchemaOrg {
		got, err := SchemaOrg(ScriptTexts(r.HTML), urlOf(r.Out))
		if err != nil {
			t.Errorf("schema_org %s: %v", r.Name, err)
			continue
		}
		if r.Name == "jsonld_list_lowercase_type" {
			// B25's day fix: P1DT2H is 1560 minutes (legacy: None).
			if got == nil || got.EstimatedTimeMinutes == nil || *got.EstimatedTimeMinutes != 1560 {
				t.Errorf("B25 day fix: %+v", got)
				continue
			}
			got.EstimatedTimeMinutes = nil
		}
		if c, w := canon(t, got), canon(t, r.Out); c != w {
			t.Errorf("schema_org %s:\n got %s\nwant %s", r.Name, c, w)
		}
	}
	for _, r := range g.Heuristic {
		got := Heuristic(r.HTML, urlOf(r.Out))
		if c, w := canon(t, got), canon(t, r.Out); c != w {
			t.Errorf("heuristic %s:\n got %s\nwant %s", r.Name, c, w)
		}
	}
}

func TestGoldenIngestion(t *testing.T) {
	var g struct {
		Ingestion []struct {
			Name  string `json:"name"`
			Input Input  `json:"input"`
			Out   struct {
				ParseResult  json.RawMessage `json:"parse_result"`
				LLMCalls     int             `json:"llm_calls"`
				MarkComplete *struct {
					Status     string          `json:"status"`
					ResultJSON json.RawMessage `json:"result_json"`
				} `json:"mark_complete"`
			} `json:"out"`
		} `json:"ingestion"`
	}
	load(t, &g, "fixtures", "golden", "recipes", "extract.json")
	var in struct {
		Webview []struct {
			Name     string `json:"name"`
			LLMReply string `json:"llm_reply"`
		} `json:"webview_payloads"`
	}
	load(t, &in, "tools", "golden", "inputs", "recipes_pages.json")
	replies := map[string]string{}
	for _, w := range in.Webview {
		if w.LLMReply != "" {
			replies[w.Name] = w.LLMReply
		}
	}
	if len(g.Ingestion) == 0 || len(replies) == 0 {
		t.Fatal("empty golden")
	}
	for _, c := range g.Ingestion {
		calls := 0
		chat := func(_ context.Context, system, user string) (string, error) {
			calls++
			if r, ok := replies[c.Name]; ok {
				return r, nil
			}
			return "{}", nil // the dumper's default reply
		}
		res, err := Ingest(context.Background(), c.Input, chat, nil)
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		var want map[string]any
		_ = json.Unmarshal(c.Out.ParseResult, &want)
		var got map[string]any
		_ = json.Unmarshal([]byte(canon(t, res)), &got)
		if want["error_code"] == "llm_failed" {
			// pydantic's message text is not reproduced; the code is the contract.
			want["error_message"], got["error_message"] = nil, nil
		}
		if g, w := canon(t, got), canon(t, want); g != w {
			t.Errorf("%s parse_result:\n got %s\nwant %s", c.Name, g, w)
		}
		if calls != c.Out.LLMCalls {
			t.Errorf("%s: %d LLM calls, want %d", c.Name, calls, c.Out.LLMCalls)
		}
		if c.Out.MarkComplete != nil {
			if g, w := canon(t, JobResult(res)), canon(t, c.Out.MarkComplete.ResultJSON); g != w {
				t.Errorf("%s result_json:\n got %s\nwant %s", c.Name, g, w)
			}
		}
	}
}

func TestGoldenJSONHelpers(t *testing.T) {
	var g struct {
		Repair []struct {
			Input string  `json:"input"`
			Out   *string `json:"out"`
		} `json:"try_local_json_repair"`
		Strip []struct {
			Input string `json:"input"`
			Out   string `json:"out"`
		} `json:"strip_invalid_control_chars"`
		Parse []struct {
			Input string          `json:"input"`
			Out   json.RawMessage `json:"out"`
			Error string          `json:"error"`
		} `json:"parse_llm_json_content"`
	}
	load(t, &g, "fixtures", "golden", "recipes", "llm_parse.json")
	if len(g.Repair) == 0 || len(g.Strip) == 0 || len(g.Parse) == 0 {
		t.Fatal("empty golden")
	}
	for _, r := range g.Repair {
		got, ok := LocalRepair(r.Input)
		switch {
		case r.Out == nil && ok:
			t.Errorf("repair(%q) = %q, want none", r.Input, got)
		case r.Out != nil && (!ok || got != *r.Out):
			t.Errorf("repair(%q) = %q %v, want %q", r.Input, got, ok, *r.Out)
		}
	}
	for _, r := range g.Strip {
		if got := StripControlChars(r.Input); got != r.Out {
			t.Errorf("strip(%q) = %q, want %q", r.Input, got, r.Out)
		}
	}
	for _, r := range g.Parse {
		got, err := ParseJSONContent(r.Input)
		if r.Error != "" {
			if err == nil {
				t.Errorf("parse(%q) = %v, want error", r.Input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parse(%q): %v", r.Input, err)
			continue
		}
		if c, w := canon(t, got), canon(t, r.Out); c != w {
			t.Errorf("parse(%q) = %s, want %s", r.Input, c, w)
		}
	}
}

type promptRequest struct {
	Body struct {
		Model          string         `json:"model"`
		Temperature    float64        `json:"temperature"`
		ResponseFormat map[string]any `json:"response_format"`
		MaxTokens      int            `json:"max_tokens"`
		Messages       []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	} `json:"body"`
}

func TestGoldenP1(t *testing.T) {
	var g struct {
		Inputs map[string]struct {
			URL  string `json:"url"`
			HTML string `json:"html"`
		} `json:"inputs"`
		Requests map[string]promptRequest `json:"requests"`
	}
	load(t, &g, "fixtures", "golden", "recipes", "prompts", "P1_url_extract.json")
	if len(g.Requests) != 2 {
		t.Fatalf("want 2 requests, got %d", len(g.Requests))
	}
	for name, req := range g.Requests {
		in := g.Inputs[name]
		title, content, err := LLMContent(in.HTML)
		if err != nil {
			t.Fatal(err)
		}
		b := req.Body
		if b.Temperature != 0 || b.MaxTokens != 800 || b.ResponseFormat["type"] != "json_object" || len(b.Messages) != 2 {
			t.Fatalf("%s: params %+v", name, b)
		}
		if b.Messages[0].Content != P1System {
			t.Errorf("%s system:\n got %q\nwant %q", name, P1System, b.Messages[0].Content)
		}
		if got := P1User(in.URL, title, content); got != b.Messages[1].Content {
			t.Errorf("%s user:\n got %q\nwant %q", name, got, b.Messages[1].Content)
		}
	}
	var r struct {
		Inputs struct {
			Broken string `json:"broken"`
		} `json:"inputs"`
		Requests []promptRequest `json:"requests"`
	}
	load(t, &r, "fixtures", "golden", "recipes", "prompts", "P1r_json_repair_url.json")
	m := r.Requests[0].Body.Messages
	if m[0].Content != P1rSystem || m[1].Content != P1rUser(P1RecipeSchemaHint, r.Inputs.Broken) {
		t.Errorf("P1r:\n got %q\nwant %q", P1rUser(P1RecipeSchemaHint, r.Inputs.Broken), m[1].Content)
	}
	if r.Requests[0].Body.MaxTokens != 800 {
		t.Errorf("P1r max_tokens %d", r.Requests[0].Body.MaxTokens)
	}
}
