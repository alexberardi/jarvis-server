package cc

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// G1 through the real warmup path: /conversation/start with the real dev node's tool set
// must put the byte-exact legacy messages[0] into the conversation (fixtures/golden/prompts,
// case real_node), and the native providers must send the byte-exact tools payload.

func goldenPrompts(t *testing.T, name string) *pyjson.Object {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "fixtures", "golden", "prompts", name))
	if err != nil {
		t.Fatal(err)
	}
	v, err := pyjson.Loads(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return v.(*pyjson.Object)
}

func og2(o *pyjson.Object, k string) any { v, _ := o.Get(k); return v }

// stubTool is a registered server tool with a legacy definition and no behaviour.
type stubTool struct{ name string }

func (s stubTool) Name() string               { return s.name }
func (s stubTool) Definition() *pyjson.Object { return servertools.LegacyDefinition(s.name) }
func (s stubTool) Execute(context.Context, servertools.Call, servertools.Turn) (any, error) {
	return servertools.Obj("ok", true), nil
}

// realNodeRequest builds the /conversation/start body: the given client tools (examples
// dropped, so the command flags come from available_commands in fixture order) and the
// fixture's command flags as available_commands.
func realNodeRequest(t *testing.T, fx *pyjson.Object, clientTools []any) string {
	t.Helper()
	var tools []any
	for _, ct := range clientTools {
		o := ct.(*pyjson.Object)
		c := pyjson.NewObject()
		for _, k := range o.Keys() {
			if k == "examples" {
				continue
			}
			v, _ := o.Get(k)
			c.Set(k, v)
		}
		tools = append(tools, c)
	}
	var cmds []any
	for _, f := range og2(og2(fx, "inputs").(*pyjson.Object), "command_flags").([]any) {
		fo := f.(*pyjson.Object)
		cmds = append(cmds, servertools.Obj("command_name", og2(fo, "command_name"), "description", "d",
			"parameters", []any{}, "allow_direct_answer", og2(fo, "allow_direct_answer")))
	}
	body := servertools.Obj("conversation_id", "g1", "node_context", servertools.Obj("timezone", "UTC"),
		"available_commands", cmds, "client_tools", tools)
	return pyjson.Compact(body)
}

func TestG1ThroughWarmup(t *testing.T) {
	inputs := goldenPrompts(t, "_inputs.json")
	nodeDev := og2(og2(inputs, "tool_sets").(*pyjson.Object), "node_dev").([]any)
	for _, provider := range []string{prompts.Qwen3_14B, prompts.Qwen3_8B, prompts.Qwen3_5_9B, prompts.ChatGPT} {
		fx := goldenPrompts(t, provider+"__real_node.json")
		var keys []string
		for _, k := range og2(og2(og2(fx, "inputs").(*pyjson.Object), "node_context").(*pyjson.Object), "date_keys").([]any) {
			keys = append(keys, k.(string))
		}
		want := og2(fx, "system_prompt").(string)

		// (a) every tool arrives from the node; no server tools are offered.
		t.Run(provider+"/client", func(t *testing.T) {
			ve := newVoiceEnv(t, provider, func(m *Module) { m.tools = servertools.NewRegistry(); m.dateKeys = keys })
			ve.do("POST", "/api/v0/conversation/start", realNodeRequest(t, fx, nodeDev), ve.node.h()).want(200)
			assertWarmPrompt(t, ve, "g1", want)
		})

		// (b) native providers: the 12 server tools come from the registry through the D22
		// gates (all open here), the rest from the node — the same tool list, byte-exact.
		native, _ := og2(fx, "supports_native_tools").(bool)
		if !native {
			continue
		}
		t.Run(provider+"/registry", func(t *testing.T) {
			ve := newVoiceEnv(t, provider, func(m *Module) {
				m.tools = servertools.NewRegistry()
				for _, d := range nodeDev[:12] {
					m.tools.Register(stubTool{toolName(d.(*pyjson.Object))})
				}
				m.dateKeys = keys
			})
			ve.stt.recognition = true
			ve.set(settingWebSearch, true, settings.Scope{HouseholdID: voiceHH})
			ve.do("POST", "/api/v0/conversation/start", realNodeRequest(t, fx, nodeDev[12:]), ve.node.h()).want(200)
			assertWarmPrompt(t, ve, "g1", want)
			// The warmup sent exactly the fixture's native tools payload.
			warm := ve.eng.requests()[0]
			gotTools := warm["tools"].([]any)
			wantTools := og2(fx, "native_tools").([]any)
			if len(gotTools) != len(wantTools) {
				t.Fatalf("warmup tools %d, want %d", len(gotTools), len(wantTools))
			}
			conv := ve.m.convs.get("g1")
			for i, nt := range prompts.NativeTools(conv.provider, conv.tools) {
				if a, b := prompts.CompactASCII(nt), prompts.CompactASCII(wantTools[i]); a != b {
					t.Fatalf("native tool %d %s", i, firstDiffStr(a, b))
				}
			}
		})
	}
}

func assertWarmPrompt(t *testing.T, ve *voiceEnv, cid, want string) {
	t.Helper()
	conv := ve.m.convs.get(cid)
	if conv == nil {
		t.Fatal("conversation not cached")
	}
	got := conv.messages[0].Content
	if got != want {
		t.Fatalf("messages[0] differs from the G1 fixture %s", firstDiffStr(got, want))
	}
	// The warmup inference carried the same bytes.
	warm := ve.eng.requests()[0]
	if s := messagesOf(warm)[0]["content"]; s != want {
		t.Fatal("warmup request system prompt differs")
	}
	if mt, _ := warm["max_tokens"].(float64); mt != 1 {
		t.Fatalf("warmup max_tokens %v, want 1 (D40 01.Q10)", warm["max_tokens"])
	}
}

func firstDiffStr(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	lo := max(0, i-60)
	return "at byte " + itoaInt(i) + ":\n got …" + a[lo:min(len(a), i+60)] + "\nwant …" + b[lo:min(len(b), i+60)]
}

func itoaInt(i int) string { return pyjson.Repr(i) }
