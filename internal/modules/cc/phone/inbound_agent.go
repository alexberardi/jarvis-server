package phone

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
)

// The receptionist's turn (docs/cc/phone-sip.md §3.4; loop #446): caller utterance → STT →
// live model with the inbound prompt (live/inbound.go) → its two tools → TTS. It runs on the
// same transport-neutral call loop as an outbound call (media.go: barge-in, the disclosure gate,
// awaitPlayback), but shares nothing else with the outbound pipeline: no brief, no call
// context, no escalation to the household, no guard over restricted values (it is given none),
// and only take_message and hang_up act. Any other command the model writes is dropped.

// inboundTurn is one exchange, kept in memory for the summary only (no transcript is stored).
type inboundTurn struct {
	heard, said string
	events      []string
}

// inboundAgent is one inbound call's conversation.
type inboundAgent struct {
	s        *Service
	rt       *callRuntime
	messages []chatMsg
	records  []inboundTurn
	message  *live.TakeMessage // the last take_message
	hungUp   bool              // the receptionist ended the call (hang_up)
	refused  []string          // commands it wrote that it does not have
}

func (s *Service) newInboundAgent(rt *callRuntime, b live.InboundBrief) *inboundAgent {
	a := &inboundAgent{s: s, rt: rt}
	for _, m := range live.InboundInitialMessages(b) {
		a.messages = append(a.messages, chatMsg{m[0], m[1]})
	}
	return a
}

// transcript is the conversation without the system prompt, for the summary.
func (a *inboundAgent) transcript() []chatMsg {
	var out []chatMsg
	for _, m := range a.messages {
		if m.role != "system" {
			out = append(out, m)
		}
	}
	return out
}

func (a *inboundAgent) stuckInLoop(heard string) bool {
	n := 0
	for _, r := range a.records {
		if live.SimilarLine(heard, r.heard) {
			n++
		}
	}
	return n >= live.LoopRepeatLimit-1
}

// turn runs one caller utterance and returns the reply PCM (nil = say nothing).
func (a *inboundAgent) turn(ctx context.Context, utt []int16, ms callLine) []int16 {
	s := a.s
	if s.STT == nil {
		return nil
	}
	res, err := s.STT.Transcribe(ctx, live.WAV(utt, 8000), stt.TranscribeOptions{})
	if err != nil {
		s.log().Error("phone: inbound transcription failed", "inbound_call", a.rt.sessionID, "err", err)
		return nil
	}
	heard := strings.TrimSpace(res.Text)
	if heard == "" || live.IsNonSpeech(heard) {
		return nil
	}
	user := heard
	if ms.cutOff() {
		user = live.InterruptedNote + " " + heard
	}
	a.messages = append(a.messages, chatMsg{"user", live.WithNoThink(user)})

	if a.stuckInLoop(heard) {
		// A robocall repeating itself.
		s.log().Warn("phone: inbound loop-break — the caller repeated the same line", "inbound_call", a.rt.sessionID)
		pcm, _ := s.synthesize(ctx, live.InboundGoodbyeLine)
		a.messages = append(a.messages, chatMsg{"assistant", live.InboundGoodbyeLine})
		a.hungUp = true
		ms.requestHangup()
		a.record(heard, live.InboundGoodbyeLine, []string{"loop_break", "hang_up"})
		return pcm
	}

	pcm, said, events, err := a.reply(ctx)
	if err != nil {
		s.log().Error("phone: inbound LLM turn failed", "inbound_call", a.rt.sessionID, "err", err)
		pcm, _ = s.synthesize(ctx, live.TurnFailureLine)
		a.record(heard, live.TurnFailureLine, []string{"llm_failure"})
		return pcm
	}
	if said != "" {
		a.messages = append(a.messages, chatMsg{"assistant", said})
	}
	var names []string
	hangup := false
	for _, ev := range events {
		switch e := ev.(type) {
		case live.TakeMessage:
			m := e
			a.message = &m
			names = append(names, "take_message")
		case live.Hangup:
			hangup = true
			names = append(names, "hang_up")
		case live.Refused:
			s.log().Warn("phone: the receptionist wrote a command it does not have — dropped",
				"inbound_call", a.rt.sessionID, "command", e.Name)
			a.refused = append(a.refused, e.Name)
			names = append(names, "refused:"+e.Name)
		}
	}
	if !hangup && a.message != nil && live.SoundsLikeFarewell(heard) {
		hangup = true
		names = append(names, "closed_by_caller")
	}
	if strings.TrimSpace(said) == "" {
		line := live.EmptyReplyLine
		if hangup {
			line = live.InboundGoodbyeLine
		}
		pcm, _ = s.synthesize(ctx, line)
		said = line
		a.messages = append(a.messages, chatMsg{"assistant", said})
		names = append(names, "empty_reply")
	}
	if hangup {
		a.hungUp = true
		ms.requestHangup()
	}
	a.record(heard, said, names)
	return pcm
}

func (a *inboundAgent) record(heard, said string, events []string) {
	a.records = append(a.records, inboundTurn{heard: heard, said: said, events: events})
	a.s.log().Info("phone: inbound turn", "inbound_call", a.rt.sessionID, "n", len(a.records), "events", strings.Join(events, ","))
}

// reply streams the model's reply through the inbound parser and synthesizes it sentence by
// sentence.
func (a *inboundAgent) reply(ctx context.Context) (pcm []int16, said string, events []live.ToolEvent, err error) {
	s := a.s
	if s.LLM == nil {
		return nil, "", nil, errors.New("no LLM")
	}
	tctx, cancel := context.WithTimeout(ctx, orDefault(s.TurnTimeout, defaultTurnTimeout))
	defer cancel()
	maxTok := 300
	ch, err := s.LLM.Stream(tctx, llm.ChatRequest{Label: "live", Messages: toLLM(a.messages), MaxTokens: &maxTok})
	if err != nil {
		return nil, "", nil, err
	}
	var stripper live.ThinkStripper
	parser := live.NewInboundParser()
	var splitter live.SentenceSplitter
	var spoken []string
	speak := func(text string) {
		for _, sen := range splitter.Feed(text) {
			if p, _ := s.synthesize(ctx, sen); len(p) > 0 {
				pcm = append(pcm, p...)
				spoken = append(spoken, sen)
			}
		}
	}
loop:
	for {
		select {
		case <-tctx.Done():
			if ctx.Err() == nil {
				return nil, "", nil, errTurnTimeout
			}
			return nil, "", nil, ctx.Err()
		case fr, ok := <-ch:
			if !ok {
				break loop
			}
			switch {
			case fr.Err != "":
				return nil, "", nil, errors.New(fr.Err)
			case fr.Done || fr.Cancelled:
				break loop
			case fr.Delta != "":
				text, evs := parser.Feed(stripper.Feed(fr.Delta))
				events = append(events, evs...)
				speak(text)
			}
		}
	}
	tail, evs := parser.Feed(stripper.Flush())
	events = append(events, evs...)
	speak(tail + parser.Flush())
	for _, sen := range splitter.Flush() {
		if p, _ := s.synthesize(ctx, sen); len(p) > 0 {
			pcm = append(pcm, p...)
			spoken = append(spoken, sen)
		}
	}
	return pcm, strings.Join(spoken, " "), events, nil
}

// timeUp is the call loop's check of an inbound call's time limit.
func (rt *callRuntime) timeUp() bool {
	return !rt.timeLimit.IsZero() && time.Now().After(rt.timeLimit)
}

func (rt *callRuntime) timedOut() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.timeUpSaid
}
