package cc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func TestCompactionSettingsValidation(t *testing.T) {
	ce := newChatEnv(t, prompts.ChatGPT)
	s := ce.m.Settings()
	ctx := context.Background()
	for _, c := range []struct {
		key string
		v   any
		ok  bool
	}{
		{settingCompactThreshold, 0.75, true}, {settingCompactThreshold, 0.3, true}, {settingCompactThreshold, 0.95, true},
		{settingCompactThreshold, 0.29, false}, {settingCompactThreshold, 0.96, false}, {settingCompactThreshold, "x", false},
		{settingCompactHard, 0.9, true}, {settingCompactHard, 0.98, true}, {settingCompactHard, 0.99, false}, {settingCompactHard, 0.4, false},
	} {
		err := s.Set(ctx, c.key, c.v, settings.Scope{})
		if (err == nil) != c.ok {
			t.Errorf("%s = %v: err %v, want ok=%v", c.key, c.v, err, c.ok)
		}
		if err != nil && !errors.Is(err, settings.ErrInvalidValue) && c.v != "x" {
			t.Errorf("%s = %v: %v is not ErrInvalidValue", c.key, c.v, err)
		}
	}
	// Defaults.
	ce2 := newChatEnv(t, prompts.ChatGPT)
	if a, h := ce2.m.compactThresholds(ctx); a != 0.75 || h != 0.90 {
		t.Fatalf("defaults %v %v", a, h)
	}
	// The hard threshold is never below the async one.
	ce2.set(settingCompactThreshold, 0.9, settings.Scope{})
	ce2.set(settingCompactHard, 0.6, settings.Scope{})
	if a, h := ce2.m.compactThresholds(ctx); a != 0.9 || h != 0.9 {
		t.Fatalf("clamped %v %v", a, h)
	}
}

// compactEnv is a chat env with a 1000-token live context and captured jobs.
func compactEnv(t *testing.T) (*chatEnv, *jobLog, string) {
	t.Helper()
	ce := newChatEnv(t, prompts.ChatGPT)
	ce.eng.mu.Lock()
	ce.eng.ctxLen = 1000
	ce.eng.mu.Unlock()
	jl := &jobLog{}
	ce.m.convJobHook = jl.hook
	return ce, jl, ce.warm("tok-7")
}

func (ce *chatEnv) say(cid, msg, answer string) {
	ce.t.Helper()
	ce.eng.say(answer)
	if f := lastFrame(ce.t, ce.chat("tok-7", map[string]any{"message": msg, "conversation_id": cid})); f["type"] != "done" {
		ce.t.Fatalf("turn %q: %v", msg, f)
	}
}

func history(conv *conversation) []chatMsg {
	conv.mu.Lock()
	defer conv.mu.Unlock()
	return cloneMsgs(conv.messages)
}

func TestCompactionAsyncAtThreshold(t *testing.T) {
	var logBuf safeBuffer
	withEnvExtra(t, envExtra{log: &logBuf})
	ce, jobs, cid := compactEnv(t)
	conv := ce.m.convs.get(cid)
	sys0 := history(conv)[0].Content

	ce.eng.promptTokens.Store(700) // 70 %: below the 75 % default
	for i := 1; i <= 4; i++ {
		ce.say(cid, fmt.Sprintf("question %d", i), fmt.Sprintf("answer %d", i))
	}
	if n := len(jobs.of(convCompactJob)); n != 0 {
		t.Fatalf("%d compaction jobs below the threshold", n)
	}
	ce.eng.promptTokens.Store(760)
	ce.say(cid, "question 5", "answer 5")
	got := jobs.of(convCompactJob)
	if len(got) != 1 || got[0].ConversationID != cid {
		t.Fatalf("compaction jobs %+v", got)
	}

	n := len(ce.eng.requests())
	ce.eng.say("The user asked questions 1 to 3 and got answers 1 to 3.")
	ce.m.compactJob(context.Background(), got[0])
	reqs := ce.eng.requests()[n:]
	if len(reqs) != 1 || reqs[0]["model"] != "fake-background.gguf" {
		t.Fatalf("summarizer requests %d", len(reqs))
	}
	in := fmtMessages(reqs[0])
	if !strings.Contains(in, "question 1") || !strings.Contains(in, "answer 3") || strings.Contains(in, "question 4") {
		t.Fatalf("summarizer input %s", in)
	}

	h := history(conv)
	if h[0].Content != sys0 {
		t.Fatal("messages[0] changed")
	}
	if !h[1].summary || h[1].Role != "system" || !strings.Contains(h[1].Content, "questions 1 to 3") {
		t.Fatalf("messages[1] %+v", h[1])
	}
	var rest []string
	for _, m := range h[2:] {
		if !m.transient {
			rest = append(rest, m.Role+":"+firstLine(m.Content))
		}
	}
	if j := strings.Join(rest, "|"); j != "user:question 4|assistant:answer 4|user:question 5|assistant:answer 5" {
		t.Fatalf("kept %s", j)
	}
	if conv.promptTokens != 0 {
		t.Fatal("prompt size not reset")
	}

	// The next turn sends messages[0] byte-exact, then the summary.
	ce.eng.promptTokens.Store(300)
	ce.say(cid, "question 6", "answer 6")
	msgs := messagesOf(ce.eng.last())
	if msgs[0]["content"] != sys0 || !strings.HasPrefix(msgs[1]["content"].(string), summaryPrefix) {
		t.Fatalf("next request starts %v / %v", msgs[0]["role"], msgs[1]["content"])
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "cc: conversation compacted") || !strings.Contains(logs, "mode=async") {
		t.Fatalf("no compaction log:\n%s", logs)
	}
	if strings.Contains(logs, "questions 1 to 3") || strings.Contains(logs, "question 4") {
		t.Fatalf("compaction log has content:\n%s", logs)
	}
}

func TestCompactionSyncAtHardThreshold(t *testing.T) {
	ce, jobs, cid := compactEnv(t)
	conv := ce.m.convs.get(cid)
	sys0 := history(conv)[0].Content
	ce.eng.promptTokens.Store(100)
	for i := 1; i <= 3; i++ {
		ce.say(cid, fmt.Sprintf("question %d", i), fmt.Sprintf("answer %d", i))
	}
	ce.eng.promptTokens.Store(920) // the async job is queued but never runs
	ce.say(cid, "question 4", "answer 4")
	if len(jobs.of(convCompactJob)) != 1 {
		t.Fatal("no async job")
	}
	n := len(ce.eng.requests())
	ce.eng.promptTokens.Store(200)
	ce.eng.say("Summary: questions 1 and 2.")
	ce.say(cid, "question 5", "answer 5")
	reqs := ce.eng.requests()[n:]
	if len(reqs) != 2 || reqs[0]["model"] != "fake-background.gguf" || reqs[1]["model"] != "fake.gguf" {
		t.Fatalf("requests %d", len(reqs))
	}
	turn := messagesOf(reqs[1])
	if turn[0]["content"] != sys0 || turn[1]["content"] != summaryPrefix+"Summary: questions 1 and 2." {
		t.Fatalf("turn after sync compaction starts %v", turn[1]["content"])
	}
	if s := fmtMessages(reqs[1]); strings.Contains(s, "question 1") || !strings.Contains(s, "question 3") || !strings.Contains(s, "question 4") {
		t.Fatalf("turn request %s", s)
	}
	// The queued async job finds nothing left to do.
	n = len(ce.eng.requests())
	ce.m.compactJob(context.Background(), jobs.of(convCompactJob)[0])
	if len(ce.eng.requests()) != n {
		t.Fatal("stale async job compacted again")
	}
	// Below the hard threshold nothing happens synchronously.
	ce.eng.promptTokens.Store(800)
	ce.say(cid, "question 6", "answer 6")
	n = len(ce.eng.requests())
	ce.say(cid, "question 7", "answer 7")
	if len(ce.eng.requests())-n != 1 {
		t.Fatal("compacted synchronously below the hard threshold")
	}
}

// TestCompactionNoClobber: a turn committed while the summary was being written survives the
// splice; a change inside the summarized part makes the job give up.
func TestCompactionNoClobber(t *testing.T) {
	ce, _, cid := compactEnv(t)
	conv := ce.m.convs.get(cid)
	ce.eng.promptTokens.Store(800)
	for i := 1; i <= 4; i++ {
		ce.say(cid, fmt.Sprintf("question %d", i), fmt.Sprintf("answer %d", i))
	}
	release := make(chan struct{})
	started := make(chan struct{})
	ce.eng.mu.Lock()
	ce.eng.respond = func(body map[string]any) (engineReply, bool) {
		if mt, _ := body["max_tokens"].(float64); mt == compactSummaryTokens {
			close(started)
			<-release
			return engineReply{content: "Earlier: one and two."}, true
		}
		return engineReply{}, false
	}
	ce.eng.mu.Unlock()
	done := make(chan struct{})
	go func() { ce.m.compactJob(context.Background(), convJob{ConversationID: cid}); close(done) }()
	<-started
	ce.say(cid, "question 5", "answer 5")
	close(release)
	<-done
	var kept []string
	for _, m := range history(conv)[1:] {
		if !m.transient {
			kept = append(kept, firstLine(m.Content))
		}
	}
	j := strings.Join(kept, "|")
	if !strings.HasPrefix(j, summaryPrefix[:len(summaryPrefix)-1]) || !strings.HasSuffix(j, "question 3|answer 3|question 4|answer 4|question 5|answer 5") ||
		strings.Contains(j, "question 1") {
		t.Fatalf("history %s", j)
	}

	// Stale: the summarized part changed during the call.
	ce.eng.mu.Lock()
	ce.eng.respond = func(body map[string]any) (engineReply, bool) {
		if mt, _ := body["max_tokens"].(float64); mt == compactSummaryTokens {
			conv.mu.Lock()
			msgs := cloneMsgs(conv.messages)
			msgs[3].Content = "edited"
			conv.messages = msgs
			conv.mu.Unlock()
			return engineReply{content: "Stale summary."}, true
		}
		return engineReply{}, false
	}
	ce.eng.mu.Unlock()
	ce.eng.promptTokens.Store(800)
	ce.say(cid, "question 6", "answer 6")
	before := history(conv)
	ce.m.compactJob(context.Background(), convJob{ConversationID: cid})
	after := history(conv)
	if len(after) != len(before) || strings.Contains(fmt.Sprint(after), "Stale summary.") {
		t.Fatal("a stale summary was spliced in")
	}
}

func tc(id, name string) parse.ToolCall {
	return parse.ToolCall{ID: id, Type: "function", Function: parse.FunctionCall{Name: name, Arguments: "{}"}}
}

// TestPlanCompactionKeepsToolPairs: cuts fall only where a user message opens a new exchange,
// so a tool call keeps its result, the text path's format prompt stays with its turn, and
// the transient blocks before a kept user message stay with it.
func TestPlanCompactionKeepsToolPairs(t *testing.T) {
	sys := chatMsg{Role: "system", Content: "SYS", id: 1}
	msgs := []chatMsg{sys,
		{Role: "user", Content: "u1"}, {Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"}, {Role: "assistant", ToolCalls: []parse.ToolCall{tc("c1", "get_weather")}},
		{Role: "tool", ToolCallID: "c1", Content: "sunny"}, {Role: "assistant", Content: "a2"},
		// text path: the assistant's parsed call, then the format prompt as a user message
		{Role: "user", Content: "u3"}, {Role: "assistant", Content: "calling", ToolCalls: []parse.ToolCall{tc("c2", "x")}},
		{Role: "user", Content: "format these results"}, {Role: "assistant", Content: "a3"},
		transientSys("speaker block"),
		{Role: "user", Content: "u4"}, {Role: "assistant", ToolCalls: []parse.ToolCall{tc("c3", "y")}},
		{Role: "tool", ToolCallID: "c3", Content: "r3"},
	}
	p, ok := planCompaction(msgs)
	if !ok {
		t.Fatal("nothing to compact")
	}
	tail := msgs[p.cut:]
	if tail[0].Content != "u3" {
		t.Fatalf("cut at %q, want the last two turns (u3 with its format prompt, u4)", tail[0].Content)
	}
	if p.turnsDone != 2 || len(p.older) != 6 {
		t.Fatalf("older %d turns %d", len(p.older), p.turnsDone)
	}
	// u4's transient block moves with it.
	if got := turnStarts(msgs); msgs[got[len(got)-1]].Content != "speaker block" {
		t.Fatalf("last turn starts at %q", msgs[got[len(got)-1]].Content)
	}
	// Two turns or fewer: nothing to do.
	if _, ok := planCompaction(msgs[:7]); ok {
		t.Fatal("compacted two turns")
	}
	in := renderForSummary(p.older)
	for _, want := range []string{"User: u1", "Assistant: a1", "(Assistant called get_weather", "Tool result (tool): sunny"} {
		if !strings.Contains(in, want) {
			t.Fatalf("summary input lacks %q:\n%s", want, in)
		}
	}
}

func TestRebaseCommit(t *testing.T) {
	m := func(id uint64, c string) chatMsg { return chatMsg{Role: "user", Content: c, id: id} }
	base := []chatMsg{m(1, "sys"), m(2, "a"), m(3, "b"), m(4, "tool")}
	// A compaction (2 → summary 9) and a description (3 edited) happened during the stream.
	current := []chatMsg{m(1, "sys"), m(9, "summary"), m(3, "b described"), m(4, "tool")}
	// The text path dropped the tool message and added a prompt and an answer.
	commit := []chatMsg{m(1, "sys"), m(2, "a"), m(3, "b"), m(0, "prompt"), m(0, "answer")}
	got := rebaseCommit(base, current, commit)
	var s []string
	for _, x := range got {
		s = append(s, x.Content)
	}
	if strings.Join(s, "|") != "sys|summary|b described|prompt|answer" {
		t.Fatalf("rebased %v", s)
	}
}
