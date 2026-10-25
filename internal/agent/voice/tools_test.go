package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/desktop"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// The voice side of client tools (go-pi-node.md §1.1 row 18). The chained and single-call
// tests run the real desktop toolset over a fake OS backend, so what a voice turn offers and
// runs is exactly what the MQTT dispatcher serves.

// fakeOS is a host.Backend that records what it was asked to do.
type fakeOS struct {
	mu      sync.Mutex
	volume  int
	actions []string
	windows []host.Window // nil: Firefox and a terminal
}

func (f *fakeOS) Platform() string { return "linux" }
func (f *fakeOS) Capabilities(context.Context) []string {
	return []string{"open", "volume.get", "volume.set", "windows.list", "windows.minimize", "windows.focus", "windows.close"}
}
func (f *fakeOS) GetVolume(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.volume, nil
}
func (f *fakeOS) ApplyVolume(_ context.Context, l int) error {
	return f.act(fmt.Sprintf("volume %d", l))
}
func (f *fakeOS) ListWindows(context.Context) ([]host.Window, error) {
	if f.windows != nil {
		return f.windows, nil
	}
	return []host.Window{{ID: "0x1", Title: "Inbox", App: "firefox"}, {ID: "0x2", Title: "notes", App: "kitty"}}, nil
}
func (f *fakeOS) MinimizeWindow(_ context.Context, id string) error { return f.act("minimize " + id) }
func (f *fakeOS) FocusWindow(_ context.Context, id string) error    { return f.act("focus " + id) }
func (f *fakeOS) CloseWindow(_ context.Context, id string) error    { return f.act("close " + id) }
func (f *fakeOS) ArrangeWindow(_ context.Context, id string, a host.WindowAction, _ string) error {
	return f.act(string(a) + " " + id)
}
func (f *fakeOS) LaunchApp(_ context.Context, p string) error { return f.act("launch " + p) }
func (f *fakeOS) Open(_ context.Context, t string) error      { return f.act("open " + t) }
func (f *fakeOS) act(s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(s, "volume ") {
		fmt.Sscanf(s, "volume %d", &f.volume)
	}
	f.actions = append(f.actions, s)
	return nil
}
func (f *fakeOS) did() []string { f.mu.Lock(); defer f.mu.Unlock(); return slices.Clone(f.actions) }

func desktopTools(os *fakeOS) node.Toolset {
	return node.Multi{desktop.New(host.New(os), desktop.OpenPolicy{}, nil)}
}

// funcTools is a Toolset of plain functions (for failures, hangs and panics).
type funcTools map[string]func(ctx context.Context, args map[string]any) map[string]any

func (f funcTools) Available(context.Context) []node.ToolDef {
	var out []node.ToolDef
	for name := range f {
		out = append(out, node.Spec{Name: name, Description: name}.Render())
	}
	return out
}
func (f funcTools) Call(ctx context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	fn, ok := f[name]
	if !ok {
		return nil, false
	}
	return fn(ctx, args), true
}

func call(id, name, args string) ToolCall {
	var c ToolCall
	c.ID, c.Function.Name, c.Function.Arguments = id, name, args
	return c
}

func toolCalls(calls ...ToolCall) func(context.Context) (Reply, error) {
	return func(context.Context) (Reply, error) {
		return Reply{Control: &Control{StopReason: "tool_calls", ToolCalls: calls}}, nil
	}
}

func complete(msg string) *Control { return &Control{StopReason: "complete", AssistantMessage: &msg} }

// steps answers successive continues in order.
func steps(t *testing.T, answers ...*Control) func([]ToolResult) (*Control, error) {
	var mu sync.Mutex
	i := 0
	return func([]ToolResult) (*Control, error) {
		mu.Lock()
		defer mu.Unlock()
		if i >= len(answers) {
			t.Errorf("unexpected continue #%d", i+1)
			return complete("?"), nil
		}
		i++
		return answers[i-1], nil
	}
}

func toolRig(t *testing.T, tools node.Toolset, first func(context.Context) (Reply, error), cont func([]ToolResult) (*Control, error)) (*rig, Turn) {
	t.Helper()
	srv := newServer()
	srv.reply, srv.cont, srv.speakPCM = first, cont, pcmOf(300)
	r := newRig(t, oneWake(), srv)
	r.p.Tools = tools
	stop := r.run(t)
	tr := r.turn(t)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	return r, tr
}

func outputs(rs []ToolResult) string {
	b, _ := json.Marshal(rs)
	return string(b)
}

func TestToolCallRunsLocallyAndSpeaksTheFinalReply(t *testing.T) {
	os := &fakeOS{volume: 70}
	r, tr := toolRig(t, desktopTools(os),
		toolCalls(call("c1", desktop.ToolSetVolume, `{"volume": 30}`)),
		steps(t, complete("Volume set to 30 percent.")))

	if tr.Outcome != OutcomeSpoken || tr.Err != nil || tr.Message != "Volume set to 30 percent." {
		t.Fatalf("turn %+v", tr)
	}
	if got := os.did(); !slices.Equal(got, []string{"volume 30"}) {
		t.Fatalf("OS did %v", got)
	}
	if len(tr.Tools) != 1 || tr.Tools[0].Name != desktop.ToolSetVolume || !tr.Tools[0].Success {
		t.Fatalf("tools %+v", tr.Tools)
	}
	if got := r.srv.callList(); !slices.Equal(got[slices.Index(got, "stream"):], []string{"stream", "continue", "speak", "end"}) {
		t.Fatalf("calls %v", got)
	}
	if got := outputs(r.srv.results[0]); got != `[{"tool_call_id":"c1","output":{"requested":30,"success":true,"volume":30}}]` {
		t.Fatalf("posted %s", got)
	}
	// The conversation was started with the desktop tools, both renderings, from the same defs.
	want := desktopTools(os).Available(context.Background())
	if len(r.srv.tools.ClientTools) != len(want) || len(r.srv.tools.Commands) != len(want) {
		t.Fatalf("start offered %d client tools / %d commands, want %d", len(r.srv.tools.ClientTools), len(r.srv.tools.Commands), len(want))
	}
	for i, d := range want {
		if a, _ := json.Marshal(r.srv.tools.ClientTools[i]); string(a) != mustJSON(d.ClientTool) {
			t.Fatalf("client tool %d differs from the dispatcher's", i)
		}
	}
	// Speaker: wake chime, then the spoken reply; no error chime.
	if outs := r.spk.all(); len(outs) != 2 || len(outs[1].pcm) != 300 {
		t.Fatalf("%d playbacks", len(outs))
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestChainedToolCalls(t *testing.T) {
	// "minimise firefox": the model has to list windows first, then minimise by id.
	os := &fakeOS{}
	var seen [][]ToolResult
	cont := func(rs []ToolResult) (*Control, error) {
		seen = append(seen, rs)
		if len(seen) == 1 {
			return &Control{StopReason: "tool_calls", ToolCalls: []ToolCall{call("c2", desktop.ToolWindow, `{"window":"0x1","action":"minimize"}`)}}, nil
		}
		return complete("Minimised Firefox."), nil
	}
	r, tr := toolRig(t, desktopTools(os), toolCalls(call("c1", desktop.ToolList, `{}`)), cont)
	// The minimise is a desktop command: the island showed it, so nothing is spoken (Q9), even
	// though this fake command-center still replied.
	if tr.Outcome != OutcomeSilent || tr.Message != "" {
		t.Fatalf("turn %+v", tr)
	}
	if got := os.did(); !slices.Equal(got, []string{"minimize 0x1"}) {
		t.Fatalf("OS did %v", got)
	}
	if len(seen) != 2 || seen[0][0].ToolCallID != "c1" || seen[1][0].ToolCallID != "c2" {
		t.Fatalf("continues %s", mustJSON(seen))
	}
	if ws, _ := seen[0][0].Output["windows"].([]host.Window); len(ws) != 2 {
		t.Fatalf("list output %v", seen[0][0].Output)
	}
	if n := strings.Count(strings.Join(r.srv.callList(), ","), "continue"); n != 2 {
		t.Fatalf("%d continues", n)
	}
}

func TestSeveralCallsInOneRoundGoBackTogetherInOrder(t *testing.T) {
	os := &fakeOS{volume: 10}
	r, tr := toolRig(t, desktopTools(os),
		toolCalls(call("a", desktop.ToolGetVolume, `{}`), call("b", desktop.ToolSetVolume, `{"volume":"45%"}`)),
		steps(t, complete("It was 10; now 45.")))
	if tr.Outcome != OutcomeSpoken || len(tr.Tools) != 2 {
		t.Fatalf("turn %+v", tr)
	}
	rs := r.srv.results[0]
	if len(r.srv.results) != 1 || len(rs) != 2 || rs[0].ToolCallID != "a" || rs[1].ToolCallID != "b" ||
		rs[0].Output["volume"] != 10 || rs[1].Output["volume"] != 45 {
		t.Fatalf("posted %s", outputs(rs))
	}
}

func TestFailingToolIsReportedAndSpokenNotTrusted(t *testing.T) {
	tools := funcTools{"lock_door": func(context.Context, map[string]any) map[string]any {
		return node.Fail("the lock is offline")
	}}
	r, tr := toolRig(t, tools, toolCalls(call("c1", "lock_door", `{}`)),
		steps(t, complete("Done, the door is locked."))) // a small model claiming success
	if got := outputs(r.srv.results[0]); got != `[{"tool_call_id":"c1","output":{"error":"the lock is offline","success":false}}]` {
		t.Fatalf("posted %s", got)
	}
	if tr.Outcome != OutcomeSpoken || r.srv.spoken != "Sorry, that didn't work: the lock is offline" {
		t.Fatalf("turn %+v, spoke %q", tr, r.srv.spoken)
	}
	if tr.Tools[0].Success || tr.Tools[0].Error != "the lock is offline" {
		t.Fatalf("tools %+v", tr.Tools)
	}

	// The command's own failure_message wins over the raw error.
	fm := "I couldn't reach the lock."
	c := call("c1", "lock_door", `{}`)
	c.FailureMessage = &fm
	r, _ = toolRig(t, tools, toolCalls(c), steps(t, complete("Locked!")))
	if r.srv.spoken != "Sorry, that didn't work: I couldn't reach the lock." {
		t.Fatalf("spoke %q", r.srv.spoken)
	}

	// One success in the round: the model's reply is trusted.
	tools["get_time"] = func(context.Context, map[string]any) map[string]any { return map[string]any{"success": true} }
	r, _ = toolRig(t, tools, toolCalls(call("c1", "lock_door", `{}`), call("c2", "get_time", `{}`)),
		steps(t, complete("It's noon, but the lock is offline.")))
	if r.srv.spoken != "It's noon, but the lock is offline." {
		t.Fatalf("spoke %q", r.srv.spoken)
	}
}

// A parked action (dictate.Confirmer.Ask) is success:false because nothing was done yet, but
// it's a question for the user, not a failure (#420): the model's question is spoken, even when
// the call carries the failure_message text mode asks for on every call.
func TestParkedActionIsAskedNotReportedAsAFailure(t *testing.T) {
	tools := funcTools{"browser_fill": func(context.Context, map[string]any) map[string]any {
		return map[string]any{"success": false, "done": false, "status": "awaiting_confirmation",
			"confirmation_id": "confirm-1", "question": "Fill the search field and submit?"}
	}}
	fm := "I couldn't fill the search field."
	c := call("c1", "browser_fill", `{}`)
	c.FailureMessage = &fm
	r, tr := toolRig(t, tools, toolCalls(c), steps(t, complete("Fill the search field and submit?")))
	if tr.Outcome != OutcomeSpoken || r.srv.spoken != "Fill the search field and submit?" {
		t.Fatalf("turn %+v, spoke %q", tr, r.srv.spoken)
	}
	if tr.Tools[0].Success { // the trace stays honest: nothing was done
		t.Fatalf("tools %+v", tr.Tools)
	}
}

func TestUnknownToolIsAFailureResult(t *testing.T) {
	os := &fakeOS{}
	r, tr := toolRig(t, desktopTools(os), toolCalls(call("c1", "rm_rf", `{"path":"/"}`)),
		steps(t, complete("I can't do that.")))
	if got := outputs(r.srv.results[0]); got != `[{"tool_call_id":"c1","output":{"error":"Unknown tool: rm_rf","success":false}}]` {
		t.Fatalf("posted %s", got)
	}
	if len(os.did()) != 0 || tr.Outcome != OutcomeSpoken {
		t.Fatalf("OS did %v; turn %+v", os.did(), tr)
	}

	// A node with no tools offers none and answers every call the same way.
	r, _ = toolRig(t, nil, toolCalls(call("c1", desktop.ToolSetVolume, `{"volume":1}`)), steps(t, complete("Sorry.")))
	if r.srv.tools.ClientTools != nil || outputs(r.srv.results[0]) != `[{"tool_call_id":"c1","output":{"error":"Unknown tool: desktop_set_volume","success":false}}]` {
		t.Fatalf("offered %v, posted %s", r.srv.tools, outputs(r.srv.results[0]))
	}
}

func TestHungOrPanickingToolTimesOutInsteadOfHoldingTheTurn(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	tools := funcTools{
		"hang":  func(context.Context, map[string]any) map[string]any { <-release; return nil }, // ignores ctx
		"crash": func(context.Context, map[string]any) map[string]any { panic("boom") },
	}
	srv := newServer()
	srv.reply = toolCalls(call("c1", "hang", `{}`), call("c2", "crash", `{}`))
	srv.cont, srv.speakPCM = steps(t, complete("ok")), pcmOf(10)
	r := newRig(t, oneWake(), srv)
	r.p.Tools = tools
	r.p.Cfg.ToolTimeout = 50 * time.Millisecond
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	rs := r.srv.results[0]
	if e, _ := rs[0].Output["error"].(string); !strings.Contains(e, "hang timed out after 50ms") {
		t.Fatalf("hang result %v", rs[0].Output)
	}
	if e, _ := rs[1].Output["error"].(string); !strings.Contains(e, "crash crashed: boom") {
		t.Fatalf("crash result %v", rs[1].Output)
	}
	if !strings.HasPrefix(r.srv.spoken, "Sorry, that didn't work: hang timed out") || tr.Outcome != OutcomeSpoken {
		t.Fatalf("spoke %q, turn %+v", r.srv.spoken, tr)
	}
}

func TestRepeatedIdenticalRoundStops(t *testing.T) {
	tools := funcTools{"lights": func(context.Context, map[string]any) map[string]any { return node.Fail("hub offline") }}
	same := &Control{StopReason: "tool_calls", ToolCalls: []ToolCall{call("c2", "lights", `{"on": true}`)}}
	r, tr := toolRig(t, tools, toolCalls(call("c1", "lights", `{"on":true}`)), steps(t, same))
	if len(r.srv.results) != 1 || tr.Outcome != OutcomeSpoken || r.srv.spoken != "Sorry, that didn't work: hub offline" {
		t.Fatalf("continues %d, turn %+v, spoke %q", len(r.srv.results), tr, r.srv.spoken)
	}
}

func TestToolRoundsAreBounded(t *testing.T) {
	n := 0
	tools := funcTools{"step": func(context.Context, map[string]any) map[string]any { return map[string]any{"success": true} }}
	cont := func([]ToolResult) (*Control, error) {
		n++ // a fresh argument every round, so it isn't the repeat guard that stops it
		return &Control{StopReason: "tool_calls", ToolCalls: []ToolCall{call("c", "step", fmt.Sprintf(`{"n":%d}`, n))}}, nil
	}
	r, tr := toolRig(t, tools, toolCalls(call("c", "step", `{"n":0}`)), cont)
	if tr.Outcome != OutcomeError || !strings.Contains(tr.Err.Error(), "10 rounds") || len(r.srv.results) != maxToolRounds {
		t.Fatalf("turn %+v after %d continues", tr, len(r.srv.results))
	}
	if outs := r.spk.all(); !slices.Equal(outs[len(outs)-1].pcm, errorChime) {
		t.Fatal("no error chime")
	}
}

func TestContinueFailurePlaysTheErrorChime(t *testing.T) {
	os := &fakeOS{}
	r, tr := toolRig(t, desktopTools(os), toolCalls(call("c1", desktop.ToolGetVolume, `{}`)),
		func([]ToolResult) (*Control, error) { return nil, errors.New("503 llm down") })
	if tr.Outcome != OutcomeError || !strings.Contains(tr.Err.Error(), "llm down") {
		t.Fatalf("turn %+v", tr)
	}
	if outs := r.spk.all(); !slices.Equal(outs[len(outs)-1].pcm, errorChime) {
		t.Fatal("no error chime")
	}
	if !slices.Contains(r.srv.callList(), "end") {
		t.Fatal("conversation/end must still be sent")
	}
}

func TestContinueEndingNotForMeOrSilentSaysNothing(t *testing.T) {
	os := &fakeOS{}
	for _, stop := range []string{"not_for_me", "complete"} {
		r, tr := toolRig(t, desktopTools(os), toolCalls(call("c1", desktop.ToolGetVolume, `{}`)),
			steps(t, &Control{StopReason: stop}))
		if want := map[string]Outcome{"not_for_me": OutcomeNotForMe, "complete": OutcomeSilent}[stop]; tr.Outcome != want {
			t.Fatalf("%s: outcome %s", stop, tr.Outcome)
		}
		if slices.Contains(r.srv.callList(), "speak") || len(r.spk.all()) != 1 {
			t.Fatalf("%s: something was played", stop)
		}
	}
}

// A desktop command the computer can't do is a failure the island shows: the error chime, no
// "Sorry, that didn't work" spoken over it.
func TestIslandOnlyFailureChimesInsteadOfSpeaking(t *testing.T) {
	os := &fakeOS{}
	r, tr := toolRig(t, desktopTools(os), toolCalls(call("c1", desktop.ToolWindow, `{"action":"unsupported"}`)),
		steps(t, complete("")))
	if tr.Outcome != OutcomeSilent || tr.Message != "" {
		t.Fatalf("turn %+v", tr)
	}
	outs := r.spk.all()
	if len(outs) != 2 { // the wake chime, then the error chime: no speech
		t.Fatalf("%d playbacks", len(outs))
	}
	if got := os.did(); len(got) != 0 {
		t.Fatalf("OS did %v", got)
	}
}

// An ambiguous window name puts a numbered list on the island (Q9): nothing is spoken, and the
// next window is the choice window, 8 s and listening at once, even with follow-ups off.
func TestIslandChoiceOpensTheChoiceWindow(t *testing.T) {
	os := &fakeOS{windows: []host.Window{{ID: "0x1", Title: "build", App: "Terminal"}, {ID: "0x2", Title: "logs", App: "Terminal"}}}
	srv := newServer()
	srv.reply, srv.speakPCM = toolCalls(call("c1", desktop.ToolWindow, `{"action":"focus","window":"terminal"}`)), pcmOf(300)
	var posted []ToolResult
	srv.cont = func(rs []ToolResult) (*Control, error) { posted = rs; return complete(""), nil }
	mic := oneWake()
	r := newRig(t, mic, srv)
	r.p.Tools = desktopTools(os)
	r.p.Cfg.FollowUp.ListenSecs = 0 // follow-ups off: the choice window still opens
	ind := &recIndicator{}
	r.p.Indicator = ind
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if tr.Outcome != OutcomeSilent || tr.Message != "" || tr.FollowUpEnd != EndTimeout {
		t.Fatalf("turn %+v", tr)
	}
	if len(posted) != 1 || posted[0].Output["choice"] != true || posted[0].Output["island_only"] != true {
		t.Fatalf("posted %s", outputs(posted))
	}
	if got := followUpWindows(ind); !slices.Equal(got, []time.Duration{8 * time.Second}) {
		t.Fatalf("windows %v, want the 8 s choice window", got)
	}
	if outs := r.spk.all(); len(outs) != 1 { // the wake chime only: no error chime, no speech
		t.Fatalf("%d playbacks", len(outs))
	}
}

func TestClampChoiceListen(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{{60, 30}, {30, 30}, {8, 8}} {
		if got := ClampChoiceListen(c.in); got != c.want {
			t.Errorf("ClampChoiceListen(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// Voice test, 2026-10-24: "maximize Firefox… restore it" needs one wake word. A silent desktop
// command keeps listening like a spoken reply, starting at once.
func TestIslandOnlySuccessKeepsListening(t *testing.T) {
	os := &fakeOS{}
	srv := newServer()
	srv.reply, srv.speakPCM = toolCalls(call("c1", desktop.ToolWindow, `{"action":"minimize","window":"firefox"}`)), pcmOf(300)
	srv.cont = func([]ToolResult) (*Control, error) { return complete(""), nil }
	mic := oneWake()
	r := newRig(t, mic, srv)
	r.p.Tools = desktopTools(os)
	ind := &recIndicator{}
	r.p.Indicator = ind
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if tr.Outcome != OutcomeSilent || tr.FollowUpEnd != EndTimeout {
		t.Fatalf("turn %+v", tr)
	}
	if got := followUpWindows(ind); !slices.Equal(got, []time.Duration{3 * time.Second}) {
		t.Fatalf("windows %v, want one 3 s follow-up", got)
	}
}
