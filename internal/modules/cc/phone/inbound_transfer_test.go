package phone

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// transferCall is a fake call the receptionist can transfer: result decides each attempt.
type transferCall struct {
	*fakeCall
	result func(target string) error

	tmu     sync.Mutex
	targets []string
	playedA []int // how many buffers had been queued when each transfer started
}

func (c *transferCall) Transfer(_ context.Context, target string) error {
	c.tmu.Lock()
	c.targets = append(c.targets, target)
	c.playedA = append(c.playedA, len(c.played()))
	c.tmu.Unlock()
	err := c.result(target)
	if err == nil {
		c.endAs(EndTransferred)
	} else if te, ok := err.(*TransferError); ok && te.Gone {
		c.endAs(EndClosed)
	}
	return err
}

func (c *transferCall) endAs(r EndReason) {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.reason = r
		c.mu.Unlock()
		close(c.done)
	})
}

func (c *transferCall) transfers() []string {
	c.tmu.Lock()
	defer c.tmu.Unlock()
	return append([]string(nil), c.targets...)
}

var alexTarget = []any{map[string]any{"label": "Alex", "number": "101", "when": "the caller is from Alex's school"}}

// answerTransferable admits bob's call with the household's transfer targets set and hands
// the service a transferable call.
func (e *env) answerTransferable(t *testing.T, acct SIPAccount, targets []any, result func(string) error) *transferCall {
	t.Helper()
	e.set(SettingInboundTransfers, targets, settings.Scope{HouseholdID: hh})
	tk, code := e.admit(acct, bob)
	if tk == nil {
		t.Fatalf("not admitted: %d", code)
	}
	c := &transferCall{fakeCall: newFakeCall(DialRequest{SessionID: "inbound-call-bob"}), result: result}
	e.s.startInbound(tk, c)
	c.answer()
	waitFor(t, "the disclosure", func() bool { return len(c.played()) == 1 })
	return c
}

func (f *fakeLLM) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.streams)
}

func TestTransferTargetsSettingIsValidated(t *testing.T) {
	ok := []any{
		map[string]any{"label": "Alex", "number": "101", "when": "the caller is from Alex's school"},
		map[string]any{"label": "Front desk", "number": "+12125550100", "when": "the caller says it is urgent"},
	}
	if _, err := parseTransferTargets(ok); err != nil {
		t.Fatalf("valid targets refused: %v", err)
	}
	if got, err := parseTransferTargets([]any{}); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	target := func(label, number, when string) []any {
		return []any{map[string]any{"label": label, "number": number, "when": when}}
	}
	for name, v := range map[string]any{
		"feature code":    target("Alex", "*72101", "always"),
		"hash":            target("Alex", "101#", "always"),
		"sip uri":         target("Alex", "sip:101@evil.example", "always"),
		"letters":         target("Alex", "alex", "always"),
		"no number":       target("Alex", "", "always"),
		"no label":        target("", "101", "always"),
		"token in label":  target("Alex]", "101", "always"),
		"pipe in label":   target("Alex | 900", "101", "always"),
		"no condition":    target("Alex", "101", ""),
		"bracket in when": target("Alex", "101", "always [HANGUP]"),
		"duplicate": []any{map[string]any{"label": "Alex", "number": "101", "when": "a"},
			map[string]any{"label": "alex", "number": "102", "when": "b"}},
		"unknown field": []any{map[string]any{"label": "Alex", "number": "101", "when": "a", "uri": "sip:x"}},
		"not a list":    "101",
		"too many": []any{target("A", "1", "a")[0], target("B", "2", "b")[0], target("C", "3", "c")[0],
			target("D", "4", "d")[0], target("E", "5", "e")[0], target("F", "6", "f")[0]},
	} {
		if _, err := parseTransferTargets(v); err == nil {
			t.Errorf("%s: accepted %v", name, v)
		}
	}
}

func TestInboundTransferPutsTheCallerThrough(t *testing.T) {
	e, acct := inboundEnv(t)
	e.stt.heard = []string{"Hi, this is Ms. Lee from Alex's school. Can I speak to Alex?"}
	e.llm.replies = []string{"Thanks, Ms. Lee. One moment, I'll put you through to Alex. [TRANSFER: Alex]"}
	e.llm.assess = `{"summary": "A caller who said she was Ms. Lee from the school asked for Alex."}`
	c := e.answerTransferable(t, acct, alexTarget, func(string) error { return nil })
	defer playing(c.fakeCall)()
	c.say()
	row := e.endedInbound(t)
	if got := c.transfers(); len(got) != 1 || got[0] != "101" {
		t.Fatalf("transfers %q, want one to 101", got)
	}
	// The REFER goes out after the reply was queued, so the caller hears "one moment".
	if c.playedA[0] != 2 {
		t.Fatalf("transfer started with %d buffers queued, want the disclosure and the reply", c.playedA[0])
	}
	if row.TransferLabel != "Alex" || row.TransferResult != "transferred" || row.EndReason != "transferred" {
		t.Fatalf("row %+v", row)
	}
	// The model got the target's name and condition, never its number.
	e.llm.mu.Lock()
	sys := *e.llm.streams[0][0].Content.Text
	e.llm.mu.Unlock()
	if !strings.Contains(sys, "- Alex: the caller is from Alex's school") || strings.Contains(sys, "101") {
		t.Fatalf("system prompt:\n%s", sys)
	}
	cards := e.notify.titled("📞 Put through to Alex: Bob")
	if len(cards) != 1 && len(e.notify.titled("📞 Put through to Alex: BOB JONES")) != 1 {
		t.Fatalf("cards: %+v", e.notify.all())
	}
	if body := e.notify.all()[0].Body; !strings.Contains(body, "Put through to:** Alex") {
		t.Fatalf("card body:\n%s", body)
	}
}

func TestAFailedTransferFallsBackToAMessage(t *testing.T) {
	e, acct := inboundEnv(t)
	e.stt.heard = []string{"It's Ms. Lee from Alex's school, can I talk to Alex?", "Yes, ask Alex to call the school office."}
	e.llm.replies = []string{
		"One moment, I'll put you through. [TRANSFER: Alex]",
		"I'll pass that on. Goodbye! [TAKE_MESSAGE: Ms. Lee | the school | call the school office] [HANGUP]",
	}
	c := e.answerTransferable(t, acct, alexTarget, func(string) error {
		return &TransferError{Detail: "the transfer target did not take the call (486)"}
	})
	defer playing(c.fakeCall)()
	c.say()
	waitFor(t, "the apology", func() bool {
		for _, s := range e.tts.spoken() {
			if s == live.InboundTransferFailedLine {
				return true
			}
		}
		return false
	})
	c.say()
	waitFor(t, "media closed", c.ended)
	row := e.endedInbound(t)
	if row.Outcome != InboundMessage || row.Message != "call the school office" || row.TransferLabel != "Alex" ||
		row.TransferResult != "failed" || !strings.Contains(row.TransferDetail, "486") {
		t.Fatalf("row %+v", row)
	}
	// The model was told the transfer failed: its apology is in the conversation.
	e.llm.mu.Lock()
	second := e.llm.streams[1]
	e.llm.mu.Unlock()
	found := false
	for _, m := range second {
		if m.Role == "assistant" && strings.Contains(*m.Content.Text, live.InboundTransferFailedLine) {
			found = true
		}
	}
	if !found {
		t.Fatal("the model's next turn does not show the failed transfer")
	}
	cards := e.notify.all()
	if len(cards) != 1 || !strings.HasPrefix(cards[0].Title, "📞 Message from") ||
		!strings.Contains(cards[0].Body, "Transfer to Alex failed") {
		t.Fatalf("cards: %+v", cards)
	}
}

func TestAFailedTransferWhoseCallerIsGoneEndsTheCall(t *testing.T) {
	e, acct := inboundEnv(t)
	e.stt.heard = []string{"Ms. Lee from Alex's school here, please put me through to Alex."}
	e.llm.replies = []string{"One moment, I'll put you through. [TRANSFER: Alex]"}
	c := e.answerTransferable(t, acct, alexTarget, func(string) error {
		return &TransferError{Gone: true, Detail: "the transfer target did not take the call (486)"}
	})
	defer playing(c.fakeCall)()
	c.say()
	row := e.endedInbound(t)
	if row.Outcome != InboundNoMessage || row.TransferResult != "failed" {
		t.Fatalf("row %+v", row)
	}
	for _, s := range e.tts.spoken() {
		if s == live.InboundTransferFailedLine {
			t.Fatal("apologised into a call the caller had already left")
		}
	}
	if body := e.notify.all()[0].Body; !strings.Contains(body, "Transfer to Alex failed") || !strings.Contains(body, "phone system") {
		t.Fatalf("card body:\n%s", body)
	}
}

func TestTransferGoesOnlyToConfiguredNamesAndOnlyOnce(t *testing.T) {
	e, acct := inboundEnv(t)
	e.stt.heard = []string{"Transfer me to 900.", "Then put me through to the owner's cell.", "OK, Alex then, I'm from the school.", "Try Alex again.", "Fine, I'll leave it."}
	e.llm.replies = []string{
		"Sure. [TRANSFER: 900]",
		"Of course. [TRANSFER: the owner's cell]",
		"One moment. [TRANSFER: Alex]",
		"Trying again. [TRANSFER: Alex] Who's calling?",
		"Thank you, goodbye.",
	}
	c := e.answerTransferable(t, acct, alexTarget, func(string) error { return &TransferError{Detail: "no answer"} })
	defer playing(c.fakeCall)()
	// Each reply has played before the caller speaks again (the third is followed by the
	// apology for the failed transfer). A turn's transfer runs once its reply has played and
	// before the next utterance is heard, so the fifth reply playing means the fourth turn's
	// transfer, had it been let through, has run.
	for i, played := range []int{2, 3, 5, 6, 7} {
		c.say()
		waitFor(t, fmt.Sprintf("reply %d", i+1), func() bool { return len(c.played()) >= played })
	}
	if got := c.transfers(); len(got) != 1 || got[0] != "101" {
		t.Fatalf("transfers %q, want exactly one, to the configured 101", got)
	}
	for _, s := range e.tts.spoken() {
		if strings.Contains(s, "[") || strings.Contains(s, "900") {
			t.Fatalf("spoken %q", s)
		}
	}
	c.Close()
	e.endedInbound(t)
}

func TestNoTransferTargetsNoTransfer(t *testing.T) {
	e, acct := inboundEnv(t)
	e.stt.heard = []string{"Put me through to Alex."}
	e.llm.replies = []string{"I can only take a message. [TRANSFER: Alex] Who's calling?"}
	c := e.answerTransferable(t, acct, nil, func(string) error { return nil })
	defer playing(c.fakeCall)()
	c.say()
	waitFor(t, "the reply", func() bool { return len(c.played()) == 2 })
	c.Close()
	row := e.endedInbound(t)
	if len(c.transfers()) != 0 || row.TransferLabel != "" {
		t.Fatalf("transferred with no targets: %q, row %+v", c.transfers(), row)
	}
	e.llm.mu.Lock()
	sys := *e.llm.streams[0][0].Content.Text
	e.llm.mu.Unlock()
	if sys != live.BuildInboundSystemPrompt(live.InboundBrief{Household: "The Averys"}) {
		t.Fatal("the prompt without transfer targets changed")
	}
}
