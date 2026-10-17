package cc

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func str(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

func mustSavePrompt(t *testing.T, m *Module, hh string, p householdPrompt) {
	t.Helper()
	if err := m.saveHouseholdPrompt(context.Background(), hh, p); err != nil {
		t.Fatal(err)
	}
}

func mustResolve(t *testing.T, m *Module, hh string) resolved {
	t.Helper()
	r, err := m.resolvePrompt(context.Background(), hh)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func builtinText(t *testing.T, name string) string {
	t.Helper()
	text, ok := prompts.Builtin(name)
	if !ok {
		t.Fatalf("no built-in %s", name)
	}
	return text
}

func TestResolvePromptSources(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	m, ctx := ve.m, context.Background()
	def := builtinText(t, prompts.Qwen3_8B)

	// No row: the install default built-in.
	r := mustResolve(t, m, voiceHH)
	if r.Source != promptSourceDefault || r.Name != prompts.Qwen3_8B || r.Text != def || r.Original != def || r.Edited || r.Fallback != "" {
		t.Fatalf("no row: %+v", r)
	}

	// A built-in pick: that file.
	pick := builtinText(t, prompts.Qwen3_14B)
	mustSavePrompt(t, m, voiceHH, householdPrompt{Source: promptSourceBuiltin, Name: prompts.Qwen3_14B})
	r = mustResolve(t, m, voiceHH)
	if r.Source != promptSourceBuiltin || r.Name != prompts.Qwen3_14B || r.Text != pick || r.Original != pick || r.Edited {
		t.Fatalf("built-in pick: %+v", r)
	}

	// An edit of it: the edited text, Edited.
	mustSavePrompt(t, m, voiceHH, householdPrompt{Source: promptSourceBuiltin, Name: prompts.Qwen3_14B, Text: str("Edited in {{room}}.")})
	r = mustResolve(t, m, voiceHH)
	if r.Text != "Edited in {{room}}." || r.Original != pick || !r.Edited {
		t.Fatalf("built-in edit: %+v", r)
	}

	// Reset (text NULL): the original again.
	mustSavePrompt(t, m, voiceHH, householdPrompt{Source: promptSourceBuiltin, Name: prompts.Qwen3_14B})
	if r = mustResolve(t, m, voiceHH); r.Text != pick || r.Edited {
		t.Fatalf("reset: %+v", r)
	}

	// An import: its original; an edit of it: the edit.
	imp := householdPrompt{Source: promptSourceImport, Name: "Pantry Butler", Original: str("Butler in {{room}}."), ToolCalling: str("native")}
	mustSavePrompt(t, m, voiceHH, imp)
	r = mustResolve(t, m, voiceHH)
	if r.Source != promptSourceImport || r.Name != "Pantry Butler" || r.Text != "Butler in {{room}}." || r.Original != r.Text || r.Edited ||
		r.Prefs != (prompts.ModePrefs{ToolCalling: "native"}) {
		t.Fatalf("import: %+v", r)
	}
	imp.Text = str("Edited butler in {{room}}.")
	mustSavePrompt(t, m, voiceHH, imp)
	if r = mustResolve(t, m, voiceHH); r.Text != "Edited butler in {{room}}." || r.Original != "Butler in {{room}}." || !r.Edited {
		t.Fatalf("import edit: %+v", r)
	}

	// Deleting the row follows the install default again.
	if err := m.deleteHouseholdPrompt(ctx, voiceHH); err != nil {
		t.Fatal(err)
	}
	if r = mustResolve(t, m, voiceHH); r.Source != promptSourceDefault || r.Text != def {
		t.Fatalf("after delete: %+v", r)
	}
}

func TestResolvePromptFallback(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	m := ve.m
	def := builtinText(t, prompts.Qwen3_8B)

	// An unknown built-in name in a row.
	mustSavePrompt(t, m, voiceHH, householdPrompt{Source: promptSourceBuiltin, Name: "Dropped_Prompt", ThinkingSwitch: str("none")})
	r := mustResolve(t, m, voiceHH)
	if r.Source != promptSourceDefault || r.Text != def || r.Fallback == "" || r.Prefs != (prompts.ModePrefs{ThinkingSwitch: "none"}) {
		t.Fatalf("unknown built-in: %+v", r)
	}

	// A stored text that no longer validates: the default, Fallback set, and warmup succeeds.
	mustSavePrompt(t, m, voiceHH, householdPrompt{Source: promptSourceImport, Name: "Old", Original: str("Hi {{gone}}.")})
	r = mustResolve(t, m, voiceHH)
	if r.Source != promptSourceDefault || r.Text != def || !strings.Contains(r.Fallback, "gone") {
		t.Fatalf("invalid text: %+v", r)
	}
	ve.start("cid-fallback", `[]`)
	conv := m.convs.get("cid-fallback")
	if conv == nil || strings.Contains(conv.messages[0].Content, "{{gone}}") || conv.promptName != prompts.Qwen3_8B {
		t.Fatalf("fallback warmup: %+v", conv)
	}
	if _, ok := m.lastInputs.get(ve.node.id); !ok {
		t.Fatal("warmup did not record its prompt inputs")
	}

	// An unknown default is still an error (D11).
	ve.set(settingPromptProvider, "Nope", settings.Scope{})
	if _, err := m.resolvePrompt(context.Background(), voiceHH); err == nil {
		t.Fatal("unknown default resolved")
	}
}

func TestHouseholdPromptsWarmup(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	withTraits(ve.m, llm.ModelTraits{ToolCalling: string(prompts.ToolsText), ToolCallingFrom: ModeSourceTemplate, Thinking: true, Local: true})
	const hh2 = "hh-2"
	ve.auth.addUser(9, hh2, "member")
	node2 := ve.createNode("node-v2", hh2)
	start2 := func(cid string) *conversation {
		t.Helper()
		ve.do("POST", "/api/v0/conversation/start", `{"conversation_id":"`+cid+`","client_tools":[]}`, node2.h()).want(200)
		return ve.m.convs.get(cid)
	}

	mustSavePrompt(t, ve.m, voiceHH, householdPrompt{Source: promptSourceImport, Name: "Native one",
		Original: str("Native prompt in {{room}}.{{tools}}"), ToolCalling: str("native")})
	ve.start("cid-a1", `[]`)
	a1 := ve.m.convs.get("cid-a1")
	b1 := start2("cid-b1")
	if !a1.mode.Native() || b1.mode.Native() {
		t.Fatalf("modes: hh1 native=%v, hh2 native=%v; want true, false", a1.mode.Native(), b1.mode.Native())
	}
	if a1.promptName != "Native one" || !strings.HasPrefix(a1.messages[0].Content, "Native prompt in ") {
		t.Fatalf("hh1 prompt %q: %q", a1.promptName, a1.messages[0].Content)
	}
	if b1.promptName != prompts.Qwen3_8B {
		t.Fatalf("hh2 prompt %q", b1.promptName)
	}

	// Editing household 1 leaves household 2's rendered prompt as it was.
	mustSavePrompt(t, ve.m, voiceHH, householdPrompt{Source: promptSourceImport, Name: "Native one",
		Original: str("Native prompt in {{room}}.{{tools}}"), Text: str("Edited native prompt."), ToolCalling: str("native")})
	ve.start("cid-a2", `[]`)
	b2 := start2("cid-b2")
	if got := ve.m.convs.get("cid-a2").messages[0].Content; got != "Edited native prompt." {
		t.Fatalf("hh1 after edit: %q", got)
	}
	if b2.messages[0].Content != b1.messages[0].Content {
		t.Fatalf("hh2 changed after hh1's edit %s", firstDiffStr(b2.messages[0].Content, b1.messages[0].Content))
	}
}
