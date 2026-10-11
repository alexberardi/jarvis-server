package voice

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/desktop"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// The follow-up window and validation questions (go-pi-node.md §1.1 rows 16 and 19). The
// user is scripted per listening event: each time the pipeline starts listening without the
// wake word (after dropping what it captured while it spoke), the microphone plays the next
// answer. Nothing here races a timer: windows are counted in captured chunks.

// utterance is about a second of speech.
var utterance = rep(speech, 12)

// listens records where the pipeline listened without the wake word, in order.
type listens struct {
	mu     sync.Mutex
	stages []string
}

func (l *listens) list() []string { l.mu.Lock(); defer l.mu.Unlock(); return slices.Clone(l.stages) }

// answer scripts the user: the i-th wake-free listen hears answers[i] (nil: nobody speaks),
// after a moment of room noise so the quiet-wait sees the reply has ended.
func answer(r *rig, mic *fakeCapture, answers ...[][]int16) *listens {
	l := &listens{}
	r.p.listening = func(stage string) {
		l.mu.Lock()
		i := len(l.stages)
		l.stages = append(l.stages, stage)
		l.mu.Unlock()
		if i < len(answers) && answers[i] != nil {
			mic.push(rep(noise, 4), answers[i])
		}
	}
	return l
}

func reps(n int, s string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func fromStream(calls []string) []string { return calls[slices.Index(calls, "stream"):] }

func TestValidationQuestionIsAskedAndTheAnswerContinued(t *testing.T) {
	srv := newServer()
	srv.text, srv.texts, srv.speakPCM = "Cancel my timer.", []string{"The kitchen one."}, pcmOf(300)
	srv.reply = func(context.Context) (Reply, error) {
		return Reply{Control: &Control{StopReason: "validation_required", Validation: &ValidationRequest{
			Question: "Which timer, kitchen or oven?", ParameterName: "timer_name", Options: []any{"kitchen", "oven"}}}}, nil
	}
	srv.cont = steps(t, complete("Cancelled the kitchen timer."))
	mic := oneWake()
	r := newRig(t, mic, srv)
	l := answer(r, mic, utterance)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()

	if tr.Outcome != OutcomeSpoken || tr.Err != nil || tr.Message != "Cancelled the kitchen timer." {
		t.Fatalf("turn %+v", tr)
	}
	q := "Which timer, kitchen or oven? Your options are: kitchen, oven"
	if !slices.Equal(tr.Validations, []Validation{{Question: q, Answer: "The kitchen one."}}) {
		t.Fatalf("validations %+v", tr.Validations)
	}
	if !slices.Equal(srv.said, []string{q, "Cancelled the kitchen timer."}) {
		t.Fatalf("said %q", srv.said)
	}
	// The answer goes back as Python's send_validation_response does.
	want := `[{"tool_call_id":"validation-response","output":{"answer":"The kitchen one.","options":["kitchen","oven"],"parameter_name":"timer_name","question":"Which timer, kitchen or oven?"}}]`
	if len(srv.results) != 1 || outputs(srv.results[0]) != want {
		t.Fatalf("continues %v", srv.results)
	}
	if got := fromStream(srv.callList()); !slices.Equal(got, []string{"stream", "speak", "transcribe", "continue", "speak", "end"}) {
		t.Fatalf("calls %v", got)
	}
	// Then the usual follow-up window, which nobody used.
	if got := l.list(); !slices.Equal(got, []string{"validation", "follow_up"}) || tr.FollowUpEnd != EndTimeout {
		t.Fatalf("listened %v, window %s", got, tr.FollowUpEnd)
	}
	if n := len(r.spk.all()); n != 3 {
		t.Fatalf("%d playbacks, want chime + question + reply", n)
	}
}

func TestValidationWithNoAnswerSaysSoAndAQuestionMayFollowAQuestion(t *testing.T) {
	srv := newServer()
	srv.texts, srv.speakPCM = []string{" [BLANK_AUDIO]", "Oven."}, pcmOf(10)
	ask := &Control{StopReason: "validation_required", Validation: &ValidationRequest{Question: "Which timer?", ParameterName: "name"}}
	srv.reply = func(context.Context) (Reply, error) { return Reply{Control: ask}, nil }
	srv.cont = steps(t, ask, complete("Cancelled the oven timer."))
	mic := oneWake()
	r := newRig(t, mic, srv)
	answer(r, mic, nil, utterance)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if !slices.Equal(tr.Validations, []Validation{{"Which timer?", notCaught}, {"Which timer?", "Oven."}}) {
		t.Fatalf("validations %+v", tr.Validations)
	}
	var first []map[string]any
	_ = json.Unmarshal([]byte(outputs(srv.results[0])), &first)
	if out := first[0]["output"].(map[string]any); out["answer"] != notCaught || out["options"] != nil {
		t.Fatalf("first answer %v", out)
	}
	if tr.Outcome != OutcomeSpoken || tr.Message != "Cancelled the oven timer." {
		t.Fatalf("turn %+v", tr)
	}
}

func TestFollowUpWindowClosesQuietlyWhenNobodySpeaks(t *testing.T) {
	mic := oneWake()
	r := newRig(t, mic, newServer())
	var readsAtOpen int
	l := answer(r, mic)
	inner := r.p.listening
	r.p.listening = func(stage string) { readsAtOpen = mic.readCount(); inner(stage) }
	var readsAtEnd int
	r.p.OnTurn = func(tr Turn) { readsAtEnd = mic.readCount(); r.turns <- tr }
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if tr.Outcome != OutcomeSpoken || tr.FollowUpEnd != EndTimeout || len(tr.FollowUps) != 0 {
		t.Fatalf("turn %+v", tr)
	}
	if got := l.list(); !slices.Equal(got, []string{"follow_up"}) {
		t.Fatalf("listened %v", got)
	}
	if got := fromStream(r.srv.callList()); !slices.Equal(got, []string{"stream", "end"}) {
		t.Fatalf("calls %v", got)
	}
	if n := len(r.spk.all()); n != 2 {
		t.Fatalf("%d playbacks: the window must open silently", n)
	}
	// The window is 8 s of audio (100 chunks), not an instant.
	want := frames(DefaultFollowUp().ListenSecs)
	if d := readsAtEnd - readsAtOpen; d < want-1 || d > want+chunkQueue+4 {
		t.Fatalf("window lasted %d chunks, want ~%d", d, want)
	}
}

func TestChainedFollowUpsContinueTheSameConversation(t *testing.T) {
	srv := newServer()
	srv.texts, srv.speakPCM = []string{"And what's the date?", "Thanks for that."}, pcmOf(50)
	srv.followUp = func(req StreamRequest) (*Control, error) {
		if req.VoiceCommand == "And what's the date?" {
			return complete("It's October tenth."), nil
		}
		return complete("You're welcome."), nil
	}
	mic := oneWake()
	r := newRig(t, mic, srv)
	l := answer(r, mic, utterance, utterance)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()

	if tr.Message != "It is three." || len(tr.FollowUps) != 2 || tr.FollowUpEnd != EndTimeout {
		t.Fatalf("turn %+v", tr)
	}
	for i, want := range []string{"It's October tenth.", "You're welcome."} {
		f := tr.FollowUps[i]
		if f.Outcome != OutcomeSpoken || f.Message != want || f.RecordedSecs < 1 || f.RecordedSecs > 2.5 {
			t.Fatalf("follow-up %d: %+v", i+1, f)
		}
	}
	if got := l.list(); !slices.Equal(got, reps(3, "follow_up")) {
		t.Fatalf("listened %v", got)
	}
	for i, c := range srv.commands {
		if c.ConversationID != "conv-1" || c.TurnSource != "follow_up" || c.FollowUpIteration == nil ||
			*c.FollowUpIteration != i+1 || c.WakeConfidence != nil {
			t.Fatalf("command %d: %+v", i, c)
		}
	}
	if got := fromStream(srv.callList()); !slices.Equal(got,
		[]string{"stream", "transcribe", "command", "speak", "transcribe", "command", "speak", "end"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestFollowUpAnswerRunsAToolInTheSameLoop(t *testing.T) {
	os := &fakeOS{volume: 70}
	srv := newServer()
	srv.text, srv.texts, srv.speakPCM = "Change the volume.", []string{"Thirty percent please."}, pcmOf(50)
	ask := "What level should I set it to?"
	srv.reply = control("complete", &ask)
	srv.followUp = func(req StreamRequest) (*Control, error) {
		return &Control{StopReason: "tool_calls", ToolCalls: []ToolCall{call("c1", desktop.ToolSetVolume, `{"volume": 30}`)}}, nil
	}
	srv.cont = steps(t, complete("Done, it's at 30 percent."))
	mic := oneWake()
	r := newRig(t, mic, srv)
	r.p.Tools = desktopTools(os)
	answer(r, mic, utterance)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()

	if tr.Message != ask || len(tr.Tools) != 0 || len(tr.FollowUps) != 1 {
		t.Fatalf("turn %+v", tr)
	}
	f := tr.FollowUps[0]
	if f.Transcript != "Thirty percent please." || f.Message != "Done, it's at 30 percent." || len(f.Tools) != 1 ||
		f.Tools[0].Name != desktop.ToolSetVolume || !f.Tools[0].Success {
		t.Fatalf("follow-up %+v", f)
	}
	if got := os.did(); !slices.Equal(got, []string{"volume 30"}) {
		t.Fatalf("OS did %v", got)
	}
	if got := outputs(srv.results[0]); got != `[{"tool_call_id":"c1","output":{"requested":30,"success":true,"volume":30}}]` {
		t.Fatalf("posted %s", got)
	}
	if got := fromStream(srv.callList()); !slices.Equal(got,
		[]string{"stream", "speak", "transcribe", "command", "continue", "speak", "end"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestServerEndsTheFollowUpWindow(t *testing.T) {
	signalled := func(sig node.Signals) node.Toolset {
		return funcTools{"finish": func(context.Context, map[string]any) map[string]any {
			return map[string]any{"success": true, node.SignalsKey: sig}
		}}
	}
	finished := func(req StreamRequest) (*Control, error) {
		return &Control{StopReason: "tool_calls", ToolCalls: []ToolCall{call("f1", "finish", `{}`)}}, nil
	}
	cases := []struct {
		name     string
		followUp func(StreamRequest) (*Control, error)
		tools    node.Toolset
		end      FollowUpEnd
		outcome  Outcome
	}{
		{"end_of_exchange", func(StreamRequest) (*Control, error) {
			c := complete("Goodnight.")
			c.EndOfExchange = true
			return c, nil
		}, nil, EndOfExchange, OutcomeSpoken},
		{"not_for_me", func(StreamRequest) (*Control, error) { return &Control{StopReason: "not_for_me"}, nil }, nil, EndNotForMe, OutcomeNotForMe},
		{"clear_history from a command", finished, signalled(node.Signals{ClearHistory: true}), EndClearHistory, OutcomeSpoken},
		{"error", func(StreamRequest) (*Control, error) { return &Control{StopReason: "error"}, nil }, nil, EndError, OutcomeError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newServer()
			srv.texts, srv.speakPCM, srv.followUp = []string{"Turn everything off."}, pcmOf(10), c.followUp
			srv.cont = steps(t, complete("All off."))
			mic := oneWake()
			r := newRig(t, mic, srv)
			r.p.Tools = c.tools
			l := answer(r, mic, utterance, utterance)
			stop := r.run(t)
			tr := r.turn(t)
			_ = stop()
			if tr.FollowUpEnd != c.end || len(tr.FollowUps) != 1 || tr.FollowUps[0].Outcome != c.outcome {
				t.Fatalf("window %s, follow-ups %+v", tr.FollowUpEnd, tr.FollowUps)
			}
			if got := l.list(); !slices.Equal(got, []string{"follow_up"}) {
				t.Fatalf("listened %v: the window should not reopen", got)
			}
			if !slices.Contains(srv.callList(), "end") {
				t.Fatal("conversation/end must still be sent")
			}
		})
	}
}

func TestTerminalOrNotForMeReplyOpensNoWindow(t *testing.T) {
	bye := "Goodnight."
	for _, c := range []struct {
		reply func(context.Context) (Reply, error)
		end   FollowUpEnd
	}{
		{func(context.Context) (Reply, error) {
			return Reply{Control: &Control{StopReason: "complete", AssistantMessage: &bye, EndOfExchange: true}}, nil
		}, EndOfExchange},
		{control("not_for_me", nil), EndNotForMe},
	} {
		srv := newServer()
		srv.reply, srv.speakPCM = c.reply, pcmOf(10)
		mic := oneWake()
		r := newRig(t, mic, srv)
		l := answer(r, mic, utterance)
		stop := r.run(t)
		tr := r.turn(t)
		_ = stop()
		if tr.FollowUpEnd != c.end || len(l.list()) != 0 || slices.Contains(srv.callList(), "command") {
			t.Fatalf("%s: window %s, listened %v", c.end, tr.FollowUpEnd, l.list())
		}
	}
}

func TestFollowUpNoiseAndSelfEchoAreNotSent(t *testing.T) {
	cases := []struct {
		name    string
		reply   string
		texts   []string
		answers int
		end     FollowUpEnd
		got     []Outcome
	}{
		// Whisper repeating the same phrase on similar noise: the first is sent, the repeats are
		// noise, and two in a row close the window.
		{"noise twice", "It is three.", []string{"The weather is nice.", "The weather is nice.", "The weather is nice."}, 3, EndNoise, []Outcome{OutcomeSilent, OutcomeNoise, OutcomeNoise}},
		// The mic picked up the tail of our own reply.
		{"self echo", "Here is sad music.", []string{"Sad music."}, 1, EndEcho, []Outcome{OutcomeEcho}},
		{"non-speech", "It is three.", []string{"(wind blowing)"}, 1, EndNonSpeech, []Outcome{OutcomeNonSpeech}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newServer()
			srv.texts = c.texts
			srv.reply = func(context.Context) (Reply, error) { return audioReply(24000, c.reply, pcmOf(100)), nil }
			mic := oneWake()
			r := newRig(t, mic, srv)
			answer(r, mic, utterance, utterance, utterance)
			stop := r.run(t)
			tr := r.turn(t)
			_ = stop()
			var got []Outcome
			for _, f := range tr.FollowUps {
				got = append(got, f.Outcome)
			}
			// Only answers that got past the filters reach the server.
			sent, commands := 0, 0
			for _, o := range c.got {
				if o != OutcomeNoise && o != OutcomeEcho && o != OutcomeNonSpeech {
					sent++
				}
			}
			for _, call := range srv.callList() {
				if call == "command" {
					commands++
				}
			}
			if tr.FollowUpEnd != c.end || !slices.Equal(got, c.got) || commands != sent {
				t.Fatalf("window %s, follow-ups %v, calls %v", tr.FollowUpEnd, got, srv.callList())
			}
		})
	}
}

func TestFollowUpsStopAfterFiveIterations(t *testing.T) {
	srv := newServer()
	srv.texts = []string{"one more thing", "and another", "then this", "and that", "last one", "too many"}
	srv.followUp = func(req StreamRequest) (*Control, error) { return complete("Sure."), nil }
	srv.speakPCM = pcmOf(10)
	mic := oneWake()
	r := newRig(t, mic, srv)
	l := answer(r, mic, utterance, utterance, utterance, utterance, utterance, utterance)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if tr.FollowUpEnd != EndMaxIterations || len(tr.FollowUps) != 5 || len(l.list()) != 5 || len(srv.commands) != 5 {
		t.Fatalf("window %s after %d follow-ups, %d listens", tr.FollowUpEnd, len(tr.FollowUps), len(l.list()))
	}
}

func TestWaitForInputSpeaksTheCommandAndDeferredStepRunsLast(t *testing.T) {
	ran := make(chan []string, 1)
	var l *listens
	tools := funcTools{"play_music": func(context.Context, map[string]any) map[string]any {
		return map[string]any{"success": true, "message": "Playing jazz.", node.SignalsKey: node.Signals{
			WaitForInput: true,
			OnResponseComplete: func() error {
				ran <- l.list()
				return nil
			},
		}}
	}}
	srv := newServer()
	srv.reply = toolCalls(call("c1", "play_music", `{"genre":"jazz"}`))
	// The model wants another round; a command waiting for input doesn't get one.
	srv.cont = steps(t, &Control{StopReason: "tool_calls", ToolCalls: []ToolCall{call("c2", "play_music", `{"genre":"blues"}`)}})
	srv.speakPCM = pcmOf(10)
	mic := oneWake()
	r := newRig(t, mic, srv)
	r.p.Tools = tools
	l = answer(r, mic)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if tr.Message != "Playing jazz." || len(tr.Tools) != 1 || len(srv.results) != 1 {
		t.Fatalf("turn %+v, %d continues", tr, len(srv.results))
	}
	if strings.Contains(outputs(srv.results[0]), "signals") {
		t.Fatalf("node-local signals were posted: %s", outputs(srv.results[0]))
	}
	select {
	case stages := <-ran:
		if !slices.Equal(stages, []string{"follow_up"}) {
			t.Fatalf("deferred step ran with listens %v; want after the follow-up window", stages)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("on_response_complete never ran")
	}
}

func TestFollowUpDisabled(t *testing.T) {
	mic := oneWake()
	r := newRig(t, mic, newServer())
	r.p.Cfg.FollowUp.ListenSecs = 0
	l := answer(r, mic, utterance)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if tr.FollowUpEnd != EndDisabled || len(l.list()) != 0 {
		t.Fatalf("window %s, listened %v", tr.FollowUpEnd, l.list())
	}
}

// Goldens computed with N:core/conversation_filters.py (jarvis-node-setup 0.3.4), except that
// lone words are no longer noise (Python's allowlist rule was dropped on purpose).
func TestFollowUpFiltersMatchPython(t *testing.T) {
	noise := []struct {
		text, prev string
		want       bool
	}{
		{"", "", true}, {"Okay.", "", false}, {"Yes.", "", false}, {"stop!", "", false}, {"Kitchen", "", false},
		{"The kitchen one.", "", false}, {"The kitchen one.", "the kitchen one", true}, {"  Thanks  ", "", false}, {"Kitchen.", "kitchen", true},
		{"Set it to 30.", "Set it to 30", true}, {"Set it to 30.", "set it to 40", false}, {"CANCEL?", "", false},
		{"yes please", "", false},
	}
	for _, c := range noise {
		if got := IsFollowUpNoise(c.text, c.prev); got != c.want {
			t.Errorf("IsFollowUpNoise(%q, %q) = %v, want %v", c.text, c.prev, got, c.want)
		}
	}
	echo := []struct {
		text, said string
		want       bool
	}{
		{"sad music", "Here is sad music", true}, {"Here is sad music.", "Here is sad music", true},
		{"yes, set it for 8 PM", "I'll set the alarm for 8 PM", false}, {"play some sad music now", "Here is sad music", false},
		{"what time is it", "It's three o'clock", false}, {"It's three o'clock.", "It's three o'clock", true},
		{"three o'clock", "It's three o'clock.", true}, {"the kitchen", "Which timer, kitchen or oven?", false},
		{"kitchen timer please", "Which timer, kitchen or oven?", false}, {"", "", false},
		{"Volume set to thirty percent", "Volume's set to thirty percent", false},
	}
	for _, c := range echo {
		if got := LooksLikeSelfEcho(c.text, c.said); got != c.want {
			t.Errorf("LooksLikeSelfEcho(%q, %q) = %v, want %v", c.text, c.said, got, c.want)
		}
	}
}

func TestVoiceCommandWire(t *testing.T) {
	var body map[string]any
	var path string
	reply := `{"commands": [], "request_information": {"voice_command": "the oven", "conversation_id": "c1"}, ` +
		`"stop_reason": "validation_required", "tool_calls": [], "validation_request": {"question": "Which oven?", ` +
		`"parameter_name": "oven", "options": ["top", "bottom"], "tool_call_id": null}, "assistant_message": null, ` +
		`"reasoning": null, "end_of_exchange": null}`
	s := cc(t, func(w http.ResponseWriter, r *http.Request, b []byte) {
		path = r.URL.Path
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(reply))
	})
	it := 2
	c, err := s.VoiceCommand(context.Background(), StreamRequest{VoiceCommand: "the oven", ConversationID: "c1",
		TurnSource: "follow_up", FollowUpIteration: &it})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/v0/voice/command" || mustJSON(body) != `{"conversation_id":"c1","follow_up_iteration":2,"turn_source":"follow_up","voice_command":"the oven"}` {
		t.Fatalf("%s %s", path, mustJSON(body))
	}
	v := c.Validation
	if c.StopReason != "validation_required" || c.EndOfExchange || v == nil || v.spoken() != "Which oven? Your options are: top, bottom" ||
		v.answer("top").ToolCallID != "validation-response" {
		t.Fatalf("control %+v", c)
	}

	// jarvisd's processing error: 200, stop_reason "complete", the reason in commands.
	reply = `{"commands": [{"success": false, "command_name": null, "parameters": null, "errors": {"type": "processing_error", ` +
		`"message": "Failed to process command: boom"}}], "stop_reason": "complete", "end_of_exchange": true}`
	if _, err := s.VoiceCommand(context.Background(), StreamRequest{VoiceCommand: "x", ConversationID: "c1"}); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	reply = `{"commands": [], "stop_reason": "complete", "assistant_message": "Bye.", "end_of_exchange": true}`
	if c, err := s.VoiceCommand(context.Background(), StreamRequest{}); err != nil || !c.EndOfExchange {
		t.Fatalf("%+v, %v", c, err)
	}
}
