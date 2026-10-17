package cc

import (
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

const promptBase = "/api/v0/households/" + voiceHH + "/prompt"

// promptEnv is a voice env (default Qwen3_8B, text + suffix traits) with a household admin.
func promptEnv(t *testing.T) (*voiceEnv, hdr) {
	t.Helper()
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	return ve, bearer(ve.auth.addUser(20, voiceHH, authn.RoleAdmin))
}

func TestHouseholdPromptGet(t *testing.T) {
	ve, admin := promptEnv(t)
	got := ve.do("GET", promptBase, nil, admin).want(200).json()
	def := builtinText(t, prompts.Qwen3_8B)
	if got["source"] != promptSourceDefault || got["name"] != prompts.Qwen3_8B || got["text"] != def || got["original"] != def ||
		got["edited"] != false || got["fallback"] != "" {
		t.Fatalf("GET: %v", got)
	}
	if b := got["builtins"].([]any); len(b) != len(prompts.BuiltinNames()) {
		t.Fatalf("builtins: %v", b)
	}
	if v := got["variables"].([]any); len(v) != len(prompts.Variables) || v[0].(map[string]any)["name"] != "room" {
		t.Fatalf("variables: %v", v)
	}
	if p := got["prefs"].(map[string]any); p["tool_calling"] != "" || p["thinking_switch"] != "" {
		t.Fatalf("prefs: %v", p)
	}
	tc, th := got["tool_calling"].(map[string]any), got["thinking_switch"].(map[string]any)
	if tc["effective"] != "text" || tc["source"] != ModeSourceTemplate || th["effective"] != "suffix" || th["source"] != ModeSourceTemplate {
		t.Fatalf("modes: %v %v", tc, th)
	}
}

func TestHouseholdPromptPutAndReset(t *testing.T) {
	ve, admin := promptEnv(t)
	def := builtinText(t, prompts.Qwen3_8B)
	edit := def + "\nEnd every reply with the word Over."

	got := ve.do("PUT", promptBase, map[string]any{"text": edit}, admin).want(200).json()
	if got["text"] != edit || got["edited"] != true || got["source"] != promptSourceBuiltin || got["name"] != prompts.Qwen3_8B || got["original"] != def {
		t.Fatalf("PUT: %v", got)
	}
	ve.start("cid-edit", `[]`)
	if sys := ve.m.convs.get("cid-edit").messages[0].Content; !strings.Contains(sys, "End every reply with the word Over.") {
		t.Fatalf("warmup after the edit: %q", sys)
	}

	got = ve.do("POST", promptBase+"/reset", nil, admin).want(200).json()
	if got["text"] != def || got["edited"] != false || got["name"] != prompts.Qwen3_8B {
		t.Fatalf("reset: %v", got)
	}
	ve.start("cid-reset", `[]`)
	if sys := ve.m.convs.get("cid-reset").messages[0].Content; strings.Contains(sys, "Over.") {
		t.Fatalf("warmup after reset: %q", sys)
	}
	// A reset with nothing to reset is a no-op.
	ve.do("POST", promptBase+"/reset", nil, admin).want(200)
}

func TestHouseholdPromptPutRefusals(t *testing.T) {
	ve, admin := promptEnv(t)
	got := ve.do("PUT", promptBase, map[string]any{"text": "Hi.\nYou are in {{nope}}."}, admin).want(400).json()
	if got["detail"] != "line 2: unknown variable {{nope}}" || got["line"] != float64(2) || got["variable"] != "nope" {
		t.Fatalf("unknown variable: %v", got)
	}
	ve.do("PUT", promptBase, map[string]any{"text": "  \n"}, admin).detail(400, "the prompt is empty")
	ve.do("PUT", promptBase, map[string]any{"text": "Hi.", "tool_calling": "bogus"}, admin).want(400)
	ve.do("PUT", promptBase, map[string]any{"text": "Hi.", "thinking_switch": "native"}, admin).want(400)
	ve.do("PUT", promptBase, map[string]any{"tool_calling": "text"}, admin).want(400) // text is required
	if r := mustResolve(t, ve.m, voiceHH); r.Source != promptSourceDefault {
		t.Fatalf("a refused PUT saved something: %+v", r)
	}
	// Turns still work.
	ve.start("cid-after-refusal", `[]`)
}

func TestHouseholdPromptPutValues(t *testing.T) {
	ve, admin := promptEnv(t)
	got := ve.do("PUT", promptBase, map[string]any{"text": "Hi in {{room}}.", "tool_calling": "native", "thinking_switch": "none"}, admin).want(200).json()
	if tc := got["tool_calling"].(map[string]any); tc["effective"] != "native" || tc["source"] != ModeSourcePrompt {
		t.Fatalf("tool_calling: %v", tc)
	}
	if p := got["prefs"].(map[string]any); p["tool_calling"] != "native" || p["thinking_switch"] != "none" {
		t.Fatalf("prefs: %v", p)
	}
	// Absent keeps the stored value; "" clears it.
	got = ve.do("PUT", promptBase, map[string]any{"text": "Hi.", "thinking_switch": ""}, admin).want(200).json()
	if p := got["prefs"].(map[string]any); p["tool_calling"] != "native" || p["thinking_switch"] != "" {
		t.Fatalf("prefs after a partial PUT: %v", p)
	}
	if th := got["thinking_switch"].(map[string]any); th["source"] != ModeSourceTemplate {
		t.Fatalf("thinking_switch after clearing: %v", th)
	}
	got = ve.do("GET", promptBase, nil, admin).want(200).json()
	if tc := got["tool_calling"].(map[string]any); tc["effective"] != "native" || tc["source"] != ModeSourcePrompt || got["text"] != "Hi." {
		t.Fatalf("GET: %v", got)
	}
}

func TestHouseholdPromptBase(t *testing.T) {
	ve, admin := promptEnv(t)
	got := ve.do("PUT", promptBase+"/base", map[string]any{"builtin": prompts.Qwen3_14B}, admin).want(200).json()
	if got["source"] != promptSourceBuiltin || got["name"] != prompts.Qwen3_14B || got["text"] != builtinText(t, prompts.Qwen3_14B) {
		t.Fatalf("pick: %v", got)
	}
	ve.do("PUT", promptBase+"/base", map[string]any{"builtin": "Nope"}, admin).detail(400, "Unknown built-in prompt")
	got = ve.do("PUT", promptBase+"/base", map[string]any{"builtin": ""}, admin).want(200).json()
	if got["source"] != promptSourceDefault || got["name"] != prompts.Qwen3_8B {
		t.Fatalf("follow the default: %v", got)
	}
	if row, _ := ve.m.loadHouseholdPrompt(t.Context(), voiceHH); row != nil {
		t.Fatalf("row kept: %+v", row)
	}
}

func TestHouseholdPromptImport(t *testing.T) {
	ve, admin := promptEnv(t)
	plain := hdr{"Content-Type": "text/plain"}
	for k, v := range admin {
		plain[k] = v
	}
	text := "Imported prompt in {{room}}.{{tools}}"
	got := ve.do("POST", promptBase+"/import?name=My+prompt&tool_calling=text", text, plain).want(200).json()
	if got["source"] != promptSourceImport || got["name"] != "My prompt" || got["text"] != text || got["original"] != text || got["edited"] != false {
		t.Fatalf("import: %v", got)
	}
	if p := got["prefs"].(map[string]any); p["tool_calling"] != "text" || p["thinking_switch"] != "" {
		t.Fatalf("import prefs: %v", p)
	}
	row, err := ve.m.loadHouseholdPrompt(t.Context(), voiceHH)
	if err != nil || row == nil || row.ToolCalling.String != "text" || row.ThinkingSwitch.Valid {
		t.Fatalf("stored: %+v %v", row, err)
	}

	got = ve.do("POST", promptBase+"/import?name=Bad", "Hi {{gone}}", plain).want(400).json()
	if got["line"] != float64(1) || got["variable"] != "gone" {
		t.Fatalf("invalid import: %v", got)
	}
	ve.do("POST", promptBase+"/import?name=Big", strings.Repeat("a", prompts.MaxPromptBytes+1), plain).
		detail(400, "the prompt is longer than 32 KB")
	ve.do("POST", promptBase+"/import", text, plain).detail(400, "name is required")
	ve.do("POST", promptBase+"/import?name="+strings.Repeat("n", promptNameMax+1), text, plain).
		detail(400, "name is longer than 80 characters")
	ve.do("POST", promptBase+"/import?name=X&tool_calling=bogus", text, plain).want(400)
	ve.do("POST", promptBase+"/import?name=X&thinking_switch=text", text, plain).want(400)
	if r := mustResolve(t, ve.m, voiceHH); r.Name != "My prompt" {
		t.Fatalf("a refused import replaced the prompt: %+v", r)
	}
	// An exactly-at-the-cap prompt is accepted.
	ve.do("POST", promptBase+"/import?name=Full", strings.Repeat("a", prompts.MaxPromptBytes), plain).want(200)
}

func TestHouseholdPromptPreview(t *testing.T) {
	ve, admin := promptEnv(t)
	text := builtinText(t, prompts.Qwen3_8B)

	// No warmup yet: rendered from the node row and the server tools, partial.
	got := ve.do("POST", promptBase+"/preview", map[string]any{"text": text, "node_id": ve.node.id}, admin).want(200).json()
	if got["partial"] != true || got["tool_calling"] != "text" || got["tools_via_api"] != float64(0) || got["user_suffix"] != "/no_think" {
		t.Fatalf("partial preview: %v", got)
	}
	if sys := got["system_prompt"].(string); !strings.Contains(sys, "kitchen") || strings.Contains(sys, "{{") {
		t.Fatalf("partial system prompt: %q", sys)
	}

	// After a warmup: exactly what the warmup sent.
	ve.start("cid-preview", `[]`)
	want := ve.m.convs.get("cid-preview").messages[0].Content
	got = ve.do("POST", promptBase+"/preview", map[string]any{"text": text, "node_id": ve.node.id}, admin).want(200).json()
	if got["partial"] != false || got["system_prompt"] != want {
		t.Fatalf("cached preview differs from warmup %s", firstDiffStr(got["system_prompt"].(string), want))
	}
	// A draft renders with the same inputs.
	got = ve.do("POST", promptBase+"/preview", map[string]any{"text": "Room {{room}}.", "node_id": ve.node.id}, admin).want(200).json()
	if got["system_prompt"] != "Room kitchen." {
		t.Fatalf("draft preview: %v", got)
	}

	// Native: the tools go through the API.
	ve.do("PUT", promptBase, map[string]any{"text": text, "tool_calling": "native"}, admin).want(200)
	got = ve.do("POST", promptBase+"/preview", map[string]any{"text": text, "node_id": ve.node.id}, admin).want(200).json()
	if got["tool_calling"] != "native" || got["tools_via_api"].(float64) == 0 {
		t.Fatalf("native preview: %v", got)
	}

	got = ve.do("POST", promptBase+"/preview", map[string]any{"text": "{{nope}}", "node_id": ve.node.id}, admin).want(400).json()
	if got["variable"] != "nope" {
		t.Fatalf("invalid draft: %v", got)
	}
	ve.do("POST", promptBase+"/preview", map[string]any{"text": text, "node_id": "no-such-node"}, admin).detail(404, "Node not found")
	other := ve.createNode("node-other", "hh-2")
	ve.do("POST", promptBase+"/preview", map[string]any{"text": text, "node_id": other.id}, admin).detail(404, "Node not found")
}

func TestHouseholdPromptAccess(t *testing.T) {
	ve, _ := promptEnv(t)
	otherAdmin := bearer(ve.auth.addUser(21, "hh-2", authn.RoleAdmin))
	plain := func(h hdr) hdr { h["Content-Type"] = "text/plain"; return h }
	routes := []struct {
		method, path string
		body         any
	}{
		{"GET", promptBase, nil},
		{"PUT", promptBase, map[string]any{"text": "Hi."}},
		{"POST", promptBase + "/reset", nil},
		{"PUT", promptBase + "/base", map[string]any{"builtin": ""}},
		{"POST", promptBase + "/import?name=X", "Hi."},
		{"POST", promptBase + "/preview", map[string]any{"text": "Hi.", "node_id": ve.node.id}},
	}
	for _, rt := range routes {
		ve.do(rt.method, rt.path, rt.body, plain(hdr{})).want(401)
		// tok-7 is newVoiceEnv's member of voiceHH.
		ve.do(rt.method, rt.path, rt.body, plain(bearer("tok-7"))).detail(403, "User has member role, requires admin or higher")
		ve.do(rt.method, rt.path, rt.body, plain(otherAdmin)).detail(403, "User is not a member of this household")
	}
	if r := mustResolve(t, ve.m, voiceHH); r.Source != promptSourceDefault {
		t.Fatalf("a refused write saved something: %+v", r)
	}
}
