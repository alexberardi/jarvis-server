package prompts

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

func fixtureDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "fixtures", "golden", "prompts")
}

func loadFixture(t *testing.T, name string) any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtureDir(t), name))
	if err != nil {
		t.Fatal(err)
	}
	v, err := pyjson.Loads(string(raw))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return v
}

func og(v any, k string) any {
	x, _ := v.(*pyjson.Object).Get(k)
	return x
}

func toTools(v any) []Tool {
	var out []Tool
	for _, t := range v.([]any) {
		out = append(out, t.(*pyjson.Object))
	}
	return out
}

func toFlags(v any) []CommandFlag {
	var out []CommandFlag
	for _, f := range v.([]any) {
		fo := f.(*pyjson.Object)
		name, _ := og(fo, "command_name").(string)
		allow, _ := og(fo, "allow_direct_answer").(bool)
		out = append(out, CommandFlag{CommandName: name, AllowDirectAnswer: allow})
	}
	return out
}

// toContext maps a fixture node_context dict onto the typed Context.
func toContext(t *testing.T, nc *pyjson.Object) (Context, Characterization) {
	t.Helper()
	ctx := Context{Room: DefaultRoom, VoiceMode: DefaultVoiceMode}
	var ch Characterization
	for _, k := range nc.Keys() {
		v, _ := nc.Get(k)
		switch k {
		case "room":
			ctx.Room = v.(string)
		case "voice_mode":
			ctx.VoiceMode = v.(string)
		case "household_persona":
			ctx.HouseholdPersona = v.(string)
		case "date_keys":
			for _, d := range v.([]any) {
				ctx.DateKeys = append(ctx.DateKeys, d.(string))
			}
		case "agents":
			ctx.Agents = v.(*pyjson.Object)
		case "room_hierarchy":
			for _, r := range v.([]any) {
				ro := r.(*pyjson.Object)
				room := Room{ID: og(ro, "id").(string), Name: og(ro, "name").(string)}
				if p, ok := og(ro, "parent_room_id").(string); ok {
					room.ParentRoomID = p
				}
				ctx.RoomHierarchy = append(ctx.RoomHierarchy, room)
			}
		case "characterization":
			ch.Text = v.(string)
		default:
			t.Fatalf("unmapped node_context key %q", k)
		}
	}
	return ctx, ch
}

func firstDiff(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	lo := max(0, i-80)
	return "at byte " + itoa(i) + ":\n got …" + a[lo:min(len(a), i+80)] + "\nwant …" + b[lo:min(len(b), i+80)]
}

func itoa(i int) string { return pyjson.Repr(i) }

// G1: all 88 prompt fixtures (4 providers × 22 cases) byte for byte: messages[0], the user
// suffix, response_format, the native flag and (native providers) the tools payload.
func TestG1PromptsGolden(t *testing.T) {
	inputs := loadFixture(t, "_inputs.json")
	toolSets := og(inputs, "tool_sets").(*pyjson.Object)
	index := loadFixture(t, "_index.json").([]any)
	if len(index) != 88 {
		t.Fatalf("index has %d fixtures, want 88", len(index))
	}
	matched := 0
	for _, row := range index {
		file := og(row, "file").(string)
		fx := loadFixture(t, file)
		p, err := Lookup(og(fx, "provider").(string))
		if err != nil {
			t.Fatal(err)
		}
		in := og(fx, "inputs")
		ctx, ch := toContext(t, og(in, "node_context").(*pyjson.Object))
		ch.Enabled = og(in, "characterization_injection").(bool)
		tools := toTools(og(toolSets, og(in, "tools").(string)))
		flags := toFlags(og(in, "command_flags"))

		got, _ := AssembleSystemPrompt(p, ctx, tools, flags, ch)
		want := og(fx, "system_prompt").(string)
		ok := true
		if got != want {
			ok = false
			t.Errorf("%s system_prompt %s", file, firstDiff(got, want))
		}
		if s := p.UserMessageSuffix(og(in, "include_thinking").(bool)); s != og(fx, "user_message_suffix").(string) {
			ok = false
			t.Errorf("%s suffix %q", file, s)
		}
		if p.SupportsNativeTools() != og(fx, "supports_native_tools").(bool) {
			ok = false
			t.Errorf("%s supports_native_tools", file)
		}
		var rf any
		if f := p.ResponseFormat(); f != nil {
			rf = f
		}
		if a, b := pyjson.Dumps(rf, true), pyjson.Dumps(og(fx, "response_format"), true); a != b {
			ok = false
			t.Errorf("%s response_format %s want %s", file, a, b)
		}
		if p.SupportsNativeTools() {
			got := p.BuildTools(tools)
			want := og(fx, "native_tools").([]any)
			if len(got) != len(want) {
				ok = false
				t.Errorf("%s native_tools: %d, want %d", file, len(got), len(want))
			} else {
				for i := range got {
					if a, b := CompactASCII(got[i]), CompactASCII(want[i]); a != b {
						ok = false
						t.Errorf("%s native_tools[%d] %s", file, i, firstDiff(a, b))
					}
				}
			}
		} else if _, has := fx.(*pyjson.Object).Get("native_tools"); has {
			t.Errorf("%s: text provider fixture has native_tools", file)
		}
		if ok {
			matched++
		}
	}
	t.Logf("G1: %d/88 fixtures byte-exact", matched)
}

// _blocks.json: the generated rules.go constants equal every core_rules str constant.
func TestRulesConstantsEqualPython(t *testing.T) {
	consts := og(loadFixture(t, "_blocks.json"), "constants").(*pyjson.Object)
	if consts.Len() != len(ruleConstants) {
		t.Errorf("fixture has %d constants, rules.go %d", consts.Len(), len(ruleConstants))
	}
	for _, k := range consts.Keys() {
		v, _ := consts.Get(k)
		got, ok := ruleConstants[k]
		if !ok {
			t.Errorf("%s missing from rules.go", k)
			continue
		}
		if got != v.(string) {
			t.Errorf("%s differs: %s", k, firstDiff(got, v.(string)))
		}
	}
	p := loadFixture(t, "_persona.json")
	if og(p, "PERSONA_FRAME").(string) != PersonaFrame || og(p, "DEFAULT_PERSONA").(string) != DefaultPersona ||
		og(p, "DEFAULT_PERSONA_PRESET_ID").(string) != DefaultPersonaPresetID ||
		pyjson.Repr(og(p, "PERSONA_MAX_CHARS")) != itoa(PersonaMaxChars) {
		t.Error("persona constants differ from _persona.json")
	}
	presets := og(p, "PERSONA_PRESETS").([]any)
	if len(presets) != len(PersonaPresets) {
		t.Fatalf("%d presets, want %d", len(PersonaPresets), len(presets))
	}
	for i, pr := range presets {
		want := PersonaPreset{og(pr, "id").(string), og(pr, "label").(string), og(pr, "text").(string)}
		if PersonaPresets[i] != want {
			t.Errorf("preset %d = %+v, want %+v", i, PersonaPresets[i], want)
		}
	}
	if def := og(loadFixture(t, "_inputs.json"), "personas"); og(def, "default").(string) != DefaultPersona {
		t.Error("DefaultPersona differs from _inputs.json")
	}
}

// _blocks.json: per-turn blocks.
func TestPerTurnBlocksGolden(t *testing.T) {
	blocks := og(loadFixture(t, "_blocks.json"), "blocks").(*pyjson.Object)
	got := map[string]string{
		"speaker_name_only":            SpeakerBlock("Alex", ""),
		"speaker_with_memories":        SpeakerBlock("Alex", "- [preference] Likes oat milk\n- [fact] Dog is named Leo"),
		"speaker_memories_only":        SpeakerBlock("", "- [fact] Dog is named Leo"),
		"speaker_unknown":              SpeakerBlock("", ""),
		"ambient":                      AmbientBlock("Time: 7:45 PM\nWeather: 52F cloudy"),
		"ambient_empty":                AmbientBlock(""),
		"personality_reminder_default": PersonalityReminder(DefaultPersona),
		"personality_reminder_empty":   PersonalityReminder(""),
		"characterization":             CharacterizationSection("Alex likes short answers."),
		"characterization_empty":       CharacterizationSection(""),
	}
	if blocks.Len() != len(got) {
		t.Errorf("fixture has %d blocks, test covers %d", blocks.Len(), len(got))
	}
	for _, k := range blocks.Keys() {
		v, _ := blocks.Get(k)
		if got[k] != v.(string) {
			t.Errorf("%s: %s", k, firstDiff(got[k], v.(string)))
		}
	}
	if SpeakerBlock("default", "") != UnknownSpeakerBlock {
		t.Error(`speaker "default" must be unknown`)
	}
}

func TestRecentlyShownGolden(t *testing.T) {
	for _, row := range loadFixture(t, "_recently_shown.json").([]any) {
		var items []*ReferencedItem
		for _, it := range og(row, "items").([]any) {
			o, ok := it.(*pyjson.Object)
			if !ok {
				items = append(items, nil)
				continue
			}
			ri := &ReferencedItem{}
			if v, ok := o.Get("ref_id"); ok {
				ri.RefID = v.(string)
			}
			if v, ok := o.Get("label"); ok {
				ri.Label = v.(string)
			}
			if acts, ok := og(o, "actions").([]any); ok {
				for _, a := range acts {
					ri.Actions = append(ri.Actions, a.(string))
				}
			}
			items = append(items, ri)
		}
		if got, want := RecentlyShownBlock(items), og(row, "block").(string); got != want {
			t.Errorf("%s: %s", og(row, "name"), firstDiff(got, want))
		}
	}
}

// G5: parse_response and sanitize_text per provider. The one intended difference (D22, a D8
// fix): 8B/9B sanitize_text now unwraps <message>, so rows whose input has a <message>
// wrapper differ there and must equal the 14B behaviour instead.
func TestG5ParseGolden(t *testing.T) {
	rows := loadFixture(t, "_parse.json").([]any)
	if len(rows) != 52 {
		t.Fatalf("%d parse rows, want 52", len(rows))
	}
	exact, divergent := 0, 0
	for _, row := range rows {
		name := og(row, "provider").(string)
		p, err := Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		raw := og(row, "raw").(string)
		got, ok := p.ParseResponse(raw)
		want, wantOK := og(row, "parse_response").(string)
		if ok != wantOK || got != want {
			t.Errorf("%s parse_response(%q) = %q,%v want %q,%v", name, raw, got, ok, want, wantOK)
		}
		san := p.SanitizeText(raw)
		wantSan := og(row, "sanitize_text").(string)
		if (name == Qwen3_8B || name == Qwen3_5_9B) && strings.Contains(raw, "<message>") {
			q14, _ := Lookup(Qwen3_14B)
			if san == wantSan {
				t.Errorf("%s sanitize(%q): expected the D22 <message> divergence", name, raw)
			}
			if san != q14.SanitizeText(raw) {
				t.Errorf("%s sanitize(%q) = %q, want the 14B result", name, raw, san)
			}
			divergent++
			continue
		}
		if san != wantSan {
			t.Errorf("%s sanitize_text(%q) = %q want %q", name, raw, san, wantSan)
		}
		exact++
	}
	t.Logf("G5: %d rows exact, %d rows the documented D22 <message> sanitize divergence", exact, divergent)
}

func TestLookup(t *testing.T) {
	for _, n := range []string{Qwen3_14B, Qwen3_8B, Qwen3_5_9B, ChatGPT} {
		p, err := Lookup(n)
		if err != nil || p.Name() != n {
			t.Fatalf("Lookup(%s) = %v, %v", n, p, err)
		}
	}
	for _, n := range []string{"", "qwen3_14b_compressed", "Qwen25MediumUntrained", "JarvisToolModel", "Qwen3LargeUntrained"} {
		if _, err := Lookup(n); err == nil {
			t.Errorf("Lookup(%q) must fail (D11)", n)
		}
	}
	names := Names()
	if !sort.StringsAreSorted(names) || len(names) != 4 {
		t.Errorf("Names() = %v", names)
	}
}

func TestProviderFlags(t *testing.T) {
	cases := []struct {
		name            string
		native, force   bool
		suffix, suffix2 string
		caps            string
	}{
		{Qwen3_14B, false, true, "/no_think", "/think", `{"provider_name":"Qwen3_14B_Compressed","model_family":"qwen","size_tier":"large","training_tier":"untrained","use_tool_classifier":true,"supports_native_tools":false}`},
		{Qwen3_8B, false, true, "/no_think", "/think", `{"provider_name":"Qwen3_8B_Compressed","model_family":"qwen","size_tier":"medium","training_tier":"untrained","use_tool_classifier":true,"supports_native_tools":false}`},
		{Qwen3_5_9B, true, true, "/no_think", "/think", `{"provider_name":"Qwen3_5_9B_Compressed","model_family":"qwen3.5","size_tier":"medium","training_tier":"untrained","use_tool_classifier":true,"supports_native_tools":true}`},
		{ChatGPT, true, false, "", "", `{"provider_name":"ChatGPTOpenAI","model_family":"openai","size_tier":"large","training_tier":"untrained","use_tool_classifier":false,"supports_native_tools":true}`},
	}
	for _, c := range cases {
		p, _ := Lookup(c.name)
		if p.SupportsNativeTools() != c.native || p.ForceToolCalls() != c.force ||
			p.UserMessageSuffix(false) != c.suffix || p.UserMessageSuffix(true) != c.suffix2 {
			t.Errorf("%s flags wrong", c.name)
		}
		if got := mustJSON(t, p.Capabilities()); got != c.caps {
			t.Errorf("%s caps %s", c.name, got)
		}
		if o, cl := p.ThinkDelimiters(); o != "<think>" || cl != "</think>" {
			t.Errorf("%s think delimiters", c.name)
		}
	}
}
