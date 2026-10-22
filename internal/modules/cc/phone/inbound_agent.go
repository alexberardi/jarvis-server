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
// and only take_message, hang_up and (to the household's configured targets, once) transfer
// act. Any other command the model writes is dropped.

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
	targets  []TransferTarget  // the household's transfer targets (phone.inbound.transfer_targets)
	transfer *transferAttempt  // the call's one transfer, once tried
}

// transferAttempt is what became of a call's transfer (cc_phone_inbound_calls.transfer_*).
type transferAttempt struct{ Label, Result, Detail string }

// Transfer results.
const (
	transferDone   = "transferred"
	transferFailed = "failed"
)

// transferTimeout bounds a whole transfer: the REFER and the wait for the PBX's final NOTIFY
// (package sip's 30 s), with the check that the caller is still there.
const transferTimeout = 45 * time.Second

func (s *Service) newInboundAgent(rt *callRuntime, b live.InboundBrief, targets []TransferTarget) *inboundAgent {
	a := &inboundAgent{s: s, rt: rt, targets: targets}
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
	var transfer *TransferTarget
	for _, ev := range events {
		switch e := ev.(type) {
		case live.Transfer:
			t := a.target(e.Label)
			if t == nil || a.transfer != nil || transfer != nil {
				// Not a configured target (the parser only passes those), or a second try.
				s.log().Warn("phone: the receptionist tried a transfer it may not — dropped",
					"inbound_call", a.rt.sessionID, "target", e.Label, "already_tried", a.transfer != nil)
				a.refused = append(a.refused, "TRANSFER")
				names = append(names, "transfer_refused")
				continue
			}
			transfer = t
			names = append(names, "transfer")
		case live.TakeMessage:
			if e == (live.TakeMessage{}) {
				names = append(names, "take_message_empty")
				continue
			}
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
	if transfer != nil {
		// Transferring hands the call on: no hang-up of our own.
		hangup = false
		a.transfer = &transferAttempt{Label: transfer.Label}
		t := *transfer
		ms.requestTransfer(func() []int16 { return a.runTransfer(ctx, t) })
	}
	if hangup && strings.HasSuffix(strings.TrimSpace(said), "?") {
		// It asked the caller something: hanging up now would cut the answer off.
		hangup = false
		names = append(names, "hang_up_after_question_ignored")
	}
	if !hangup && a.message != nil && live.SoundsLikeFarewell(heard) {
		hangup = true
		names = append(names, "closed_by_caller")
	}
	if strings.TrimSpace(said) == "" {
		line := live.EmptyReplyLine
		switch {
		case transfer != nil:
			line = live.InboundTransferLine
		case hangup:
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

// labels are the transfer targets' names, the only ones [TRANSFER: name] acts on.
func (a *inboundAgent) labels() []string {
	out := make([]string, len(a.targets))
	for i, t := range a.targets {
		out[i] = t.Label
	}
	return out
}

// target is the configured transfer target named label.
func (a *inboundAgent) target(label string) *TransferTarget {
	for i := range a.targets {
		if strings.EqualFold(a.targets[i].Label, label) {
			return &a.targets[i]
		}
	}
	return nil
}

// runTransfer puts the caller through to t once the reply that announced it has played. It
// returns what to say next: nothing when the call is over (transferred, or the PBX had already
// taken the caller), else the apology, after which the receptionist takes a message.
func (a *inboundAgent) runTransfer(ctx context.Context, t TransferTarget) []int16 {
	s := a.s
	log := s.log().With("inbound_call", a.rt.sessionID, "target", t.Label)
	call := a.rt.activeCall()
	err := ErrUnsupported
	if tr, ok := call.(transferer); ok {
		log.Info("phone: inbound call: transferring")
		tctx, cancel := context.WithTimeout(ctx, transferTimeout)
		err = tr.Transfer(tctx, t.Number)
		cancel()
	}
	if err == nil {
		a.transfer.Result = transferDone
		log.Info("phone: inbound call transferred")
		return nil
	}
	a.transfer.Result, a.transfer.Detail = transferFailed, transferDetail(err)
	var te *TransferError
	gone := errors.As(err, &te) && te.Gone
	select {
	case <-call.Done():
		gone = true
	default:
	}
	log.Warn("phone: inbound transfer failed", "detail", a.transfer.Detail, "caller_gone", gone)
	if gone {
		return nil
	}
	// The model's next turn sees the apology as its own last words.
	if n := len(a.messages); n > 0 && a.messages[n-1].role == "assistant" {
		a.messages[n-1].content += " " + live.InboundTransferFailedLine
	} else {
		a.messages = append(a.messages, chatMsg{"assistant", live.InboundTransferFailedLine})
	}
	pcm, _ := s.synthesize(ctx, live.InboundTransferFailedLine)
	return pcm
}

// transferDetail is a failed transfer's reason for the household, in plain words.
func transferDetail(err error) string {
	var te *TransferError
	switch {
	case errors.As(err, &te) && te.Gone:
		return te.Detail + "; the phone system had already taken the caller"
	case errors.As(err, &te):
		return te.Detail
	case errors.Is(err, ErrUnsupported):
		return "this phone line cannot transfer calls"
	}
	return err.Error()
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
	parser := live.NewInboundParser(a.labels()...)
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
