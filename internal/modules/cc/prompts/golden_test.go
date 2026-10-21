package prompts

import (
	"encoding/json"
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

// fixtureMode is the mode that reproduces a legacy provider fixture: 14B/8B text + suffix,
// 9B native + suffix, ChatGPT native with no suffix.
func fixtureMode(provider string) Mode {
	switch provider {
	case Qwen3_14B, Qwen3_8B:
		return Mode{ToolCalling: ToolsText, ThinkingSuffix: true}
	case Qwen3_5_9B:
		return Mode{ToolCalling: ToolsNative, ThinkingSuffix: true}
	}
	return Mode{ToolCalling: ToolsNative}
}

// G1: all 88 prompt fixtures (4 providers × 22 cases) byte for byte, rendered from the
// built-in text with the fixture's mode: messages[0], the user suffix, the native flag, the
// native tools payload, and response_format on text-mode fixtures (native never sends one).
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
		prov := og(fx, "provider").(string)
		text, found := Builtin(prov)
		if !found {
			t.Fatalf("%s: no built-in %q", file, prov)
		}
		mode := fixtureMode(prov)
		in := og(fx, "inputs")
		ctx, ch := toContext(t, og(in, "node_context").(*pyjson.Object))
		ch.Enabled = og(in, "characterization_injection").(bool)
		tools := toTools(og(toolSets, og(in, "tools").(string)))
		flags := toFlags(og(in, "command_flags"))

		got, _ := SystemPrompt(text, Inputs{Context: ctx, Tools: tools, Flags: flags, ToolCalling: mode.ToolCalling}, ch)
		want := og(fx, "system_prompt").(string)
		goldenDump(t, map[string]string{"file": file, "system_prompt": got})
		ok := true
		if got != want {
			ok = false
			t.Errorf("%s system_prompt %s", file, firstDiff(got, want))
		}
		if s := mode.UserMessageSuffix(og(in, "include_thinking").(bool)); s != og(fx, "user_message_suffix").(string) {
			ok = false
			t.Errorf("%s suffix %q", file, s)
		}
		if mode.Native() != og(fx, "supports_native_tools").(bool) {
			ok = false
			t.Errorf("%s supports_native_tools", file)
		}
		if mode.Native() {
			if mode.ResponseFormat() != nil {
				ok = false
				t.Errorf("%s: native mode sends a response_format", file)
			}
			// The fixture is the legacy ToolBuilder.build output; what the server sends
			// (NativeTools) is that payload with the Jarvis extensions stripped (D8, 02.Q7).
			want := toTools(og(fx, "native_tools"))
			built := buildTools(tools, nativeBuild)
			parts := make([]string, len(built))
			for i, bt := range built {
				parts[i] = pyjson.Dumps(bt, false)
			}
			goldenDump(t, map[string]string{"file": file, "native_tools": "[" + strings.Join(parts, ", ") + "]"})
			for _, c := range []struct {
				what      string
				got, want []Tool
			}{
				{"native_tools", buildTools(tools, nativeBuild), want},
				{"NativeTools", NativeTools(tools), StripJarvisExtensions(want)},
			} {
				if len(c.got) != len(c.want) {
					ok = false
					t.Errorf("%s %s: %d, want %d", file, c.what, len(c.got), len(c.want))
					continue
				}
				for i := range c.got {
					if a, b := CompactASCII(c.got[i]), CompactASCII(c.want[i]); a != b {
						ok = false
						t.Errorf("%s %s[%d] %s", file, c.what, i, firstDiff(a, b))
					}
				}
			}
		} else {
			if a, b := pyjson.Dumps(mode.ResponseFormat(), true), pyjson.Dumps(og(fx, "response_format"), true); a != b {
				ok = false
				t.Errorf("%s response_format %s want %s", file, a, b)
			}
			if _, has := fx.(*pyjson.Object).Get("native_tools"); has {
				t.Errorf("%s: text-mode fixture has native_tools", file)
			}
		}
		if ok {
			matched++
		}
	}
	t.Logf("G1: %d/88 fixtures byte-exact", matched)
}

func TestBuiltins(t *testing.T) {
	want := []string{ChatGPT, Qwen3_14B, Qwen3_5_9B, Qwen3_8B}
	sort.Strings(want)
	if got := BuiltinNames(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("BuiltinNames = %v, want %v", got, want)
	}
	for _, n := range want {
		text, ok := Builtin(n)
		if !ok {
			t.Fatalf("Builtin(%q) missing", n)
		}
		if err := Validate(text); err != nil {
			t.Errorf("%s: %v", n, err)
		}
		if strings.Contains(text, "{terminology}") {
			t.Errorf("%s contains {terminology}", n)
		}
	}
	if _, ok := Builtin("nope"); ok {
		t.Error("Builtin(nope) found")
	}
}

// D11: only the exact built-in names; no default, no case folding, no remap of the dropped
// legacy names, and no path forms of a real name.
func TestCheckBuiltin(t *testing.T) {
	for _, n := range BuiltinNames() {
		if err := CheckBuiltin(n); err != nil {
			t.Errorf("CheckBuiltin(%q) = %v", n, err)
		}
	}
	for _, n := range []string{
		"", "qwen3_14b_compressed", "Qwen25MediumUntrained", "JarvisToolModel", "Qwen3LargeUntrained",
		"./Qwen3_8B_Compressed", "x/../Qwen3_8B_Compressed", "../builtin/Qwen3_8B_Compressed",
	} {
		err := CheckBuiltin(n)
		if err == nil {
			t.Errorf("CheckBuiltin(%q) accepted", n)
			continue
		}
		if _, ok := Builtin(n); ok {
			t.Errorf("Builtin(%q) found", n)
		}
		for _, opt := range BuiltinNames() {
			if !strings.Contains(err.Error(), opt) {
				t.Errorf("CheckBuiltin(%q) error %q does not name %s", n, err, opt)
			}
		}
	}
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
	for k, v := range got {
		goldenDump(t, map[string]string{"file": "_blocks.json", "block": k, "value": v})
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

// G5: one cleanup for every model (docs/decisions/2026-10-16-prompts-as-text.md). For each
// raw input, ParseResponse and SanitizeText give the Qwen3_14B row's legacy values; the other
// providers' rows that differ from their own legacy value are counted and logged.
func TestG5ParseGolden(t *testing.T) {
	rows := loadFixture(t, "_parse.json").([]any)
	if len(rows) != 52 {
		t.Fatalf("%d parse rows, want 52", len(rows))
	}
	type legacy struct {
		parse   string
		parseOK bool
		san     string
	}
	byRaw := map[string]legacy{}
	for _, row := range rows {
		if og(row, "provider").(string) == Qwen3_14B {
			p, ok := og(row, "parse_response").(string)
			byRaw[og(row, "raw").(string)] = legacy{p, ok, og(row, "sanitize_text").(string)}
		}
	}
	if len(byRaw) != 13 {
		t.Fatalf("%d Qwen3_14B rows, want 13", len(byRaw))
	}
	parseDiff, sanDiff := map[string]int{}, map[string]int{}
	for _, row := range rows {
		name, raw := og(row, "provider").(string), og(row, "raw").(string)
		want, ok := byRaw[raw]
		if !ok {
			t.Fatalf("%s raw %q has no Qwen3_14B row", name, raw)
		}
		if name == Qwen3_14B {
			got, gotOK := ParseResponse(raw)
			if gotOK != want.parseOK || got != want.parse {
				t.Errorf("parse_response(%q) = %q,%v want %q,%v", raw, got, gotOK, want.parse, want.parseOK)
			}
			if san := SanitizeText(raw); san != want.san {
				t.Errorf("sanitize_text(%q) = %q want %q", raw, san, want.san)
			}
			continue
		}
		p, pOK := og(row, "parse_response").(string)
		if pOK != want.parseOK || p != want.parse {
			parseDiff[name]++
		}
		if og(row, "sanitize_text").(string) != want.san {
			sanDiff[name]++
		}
	}
	for _, n := range []string{Qwen3_8B, Qwen3_5_9B, ChatGPT} {
		t.Logf("G5 %s: %d/13 parse_response and %d/13 sanitize_text rows differ from legacy", n, parseDiff[n], sanDiff[n])
	}
}

// goldenDump appends what the Go side renders to $JARVIS_GOLDEN_DUMP (JSON lines), so a
// deliberate prompt change can re-baseline the fixtures (tools/golden/rebaseline.py) instead of
// hand-editing them. The fixtures came from the legacy Python stack until the prompt diet of
// 2026-10-20 (decision 2026-10-20-prompt-diet); since then the Go builder is the reference.
func goldenDump(t *testing.T, row map[string]string) {
	path := os.Getenv("JARVIS_GOLDEN_DUMP")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := json.Marshal(row)
	if _, err := f.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}
