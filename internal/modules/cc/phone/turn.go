package phone

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/timewindow"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
)

// The live call turn (gateway services/turn_pipeline.py): utterance → STT → live model
// (think-strip → tool tokens → sentences) → spoken-output guard → TTS → 8 kHz PCM, with the
// deterministic loop-breakers, the hang-up deferral, the scheduling verdict and the bounded
// escalation. All in process (D16).

type turnRecord struct {
	n                    int
	heard, said          string
	sttMS, totalMS       float64
	llmTTFTMS, ttsTTFBMS *float64
	events               []string
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }

func optRound(p *float64) any {
	if p == nil {
		return nil
	}
	return round1(*p)
}

// asEvent is TurnRecord.as_event (the transcript_json entry).
func (r turnRecord) asEvent() map[string]any {
	ev := r.events
	if ev == nil {
		ev = []string{}
	}
	return map[string]any{
		"n": r.n, "heard": r.heard, "said": r.said,
		"timings": map[string]any{"stt_ms": round1(r.sttMS), "llm_ttft_ms": optRound(r.llmTTFTMS),
			"tts_ttfb_ms": optRound(r.ttsTTFBMS), "total_ms": round1(r.totalMS)},
		"events": ev,
	}
}

// guardSuppressed is a sentence the guard withheld; it carries labels, never values.
type guardSuppressed struct{ labels []string }

type chatMsg struct{ role, content string }

type turnPipeline struct {
	s                    *Service
	rt                   *callRuntime
	messages             []chatMsg
	outcomeFacts         []string
	outcomeEarlier       bool
	records              []turnRecord
	escalationUnanswered bool
	restricted           []live.RestrictedField
}

func (s *Service) newTurnPipeline(rt *callRuntime, restricted []live.RestrictedField) *turnPipeline {
	p := &turnPipeline{s: s, rt: rt, restricted: restricted}
	for _, m := range live.InitialMessages(rt.brief) {
		p.messages = append(p.messages, chatMsg{m[0], m[1]})
	}
	return p
}

func toLLM(ms []chatMsg) []llm.Message {
	out := make([]llm.Message, len(ms))
	for i, m := range ms {
		out[i] = llm.Message{Role: m.role, Content: llm.TextContent(m.content)}
	}
	return out
}

func hasName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func msSince(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

// synthesize renders one sentence to 8 kHz PCM, dictating identifiers character by
// character (audio only; the transcript keeps the readable form).
func (s *Service) synthesize(ctx context.Context, text string) ([]int16, *float64) {
	text = strings.TrimSpace(text)
	if text == "" || s.TTS == nil {
		return nil, nil
	}
	t0 := time.Now()
	pcm, rate, err := s.TTS.SynthesizePCM(ctx, live.FormatForSpeech(text))
	if err != nil {
		s.log().Error("phone: TTS failed", "err", err)
		return nil, nil
	}
	ttfb := msSince(t0)
	if rate > 0 && rate != 8000 {
		pcm = live.Resample(pcm, rate, 8000)
	}
	return pcm, &ttfb
}

var errTurnTimeout = errors.New("LLM turn timed out")

// generateReply streams one reply and runs every sentence through the guard. A sentence that
// would disclose a restricted value is not synthesized until the classifier clears it (the
// secret never reaches TTS otherwise).
func (p *turnPipeline) generateReply(ctx context.Context, heard, schedulingNote string) (pcm []int16, said string, events []any, ttft, ttfb *float64, err error) {
	s := p.s
	if s.LLM == nil {
		return nil, "", nil, nil, nil, errors.New("no LLM")
	}
	msgs := p.messages
	if schedulingNote != "" {
		msgs = append(append([]chatMsg(nil), p.messages...), chatMsg{"system", schedulingNote})
	}
	tctx, cancel := context.WithTimeout(ctx, orDefault(s.TurnTimeout, defaultTurnTimeout))
	defer cancel()
	maxTok := 400
	t0 := time.Now()
	ch, err := s.LLM.Stream(tctx, llm.ChatRequest{Label: "live", Messages: toLLM(msgs), MaxTokens: &maxTok})
	if err != nil {
		return nil, "", nil, nil, nil, err
	}

	var stripper live.ThinkStripper
	var parser live.TokenParser
	var splitter live.SentenceSplitter
	type candidate struct {
		sentence string
		pcm      []int16
		flagged  []live.RestrictedField
	}
	var cands []candidate
	var verdict chan map[string]bool
	fishing := false
	for _, f := range p.restricted {
		if live.Mentions(heard, f.Value) {
			fishing = true
		}
	}
	handle := func(sentence string) {
		if fishing && live.IsAffirmation(sentence) {
			s.log().Warn("phone: guard suppressed a bare confirmation", "session", p.rt.sessionID)
			events = append(events, guardSuppressed{[]string{"confirmation"}})
			return
		}
		if flagged := live.FindRestricted(sentence, p.restricted); len(flagged) > 0 {
			if verdict == nil {
				verdict = make(chan map[string]bool, 1)
				go func() { verdict <- p.askedKeys(ctx, heard) }()
			}
			cands = append(cands, candidate{sentence: sentence, flagged: flagged})
			return
		}
		pcm, tb := s.synthesize(ctx, sentence)
		if tb != nil && ttfb == nil {
			ttfb = tb
		}
		if len(pcm) > 0 {
			cands = append(cands, candidate{sentence: sentence, pcm: pcm})
		}
	}
	feed := func(text string) {
		for _, sen := range splitter.Feed(text) {
			handle(sen)
		}
	}

loop:
	for {
		select {
		case <-tctx.Done():
			if ctx.Err() == nil {
				err = errTurnTimeout
			} else {
				err = ctx.Err()
			}
			break loop
		case fr, ok := <-ch:
			if !ok {
				break loop
			}
			switch {
			case fr.Err != "":
				err = errors.New(fr.Err)
				break loop
			case fr.Done || fr.Cancelled:
				break loop
			case fr.Delta != "":
				if ttft == nil {
					v := msSince(t0)
					ttft = &v
				}
				text, evs := parser.Feed(stripper.Feed(fr.Delta))
				for _, e := range evs {
					events = append(events, e)
				}
				if text != "" {
					feed(text)
				}
			}
		}
	}
	if err != nil {
		if verdict != nil {
			<-verdict
		}
		return nil, "", nil, ttft, ttfb, err
	}
	tail, evs := parser.Feed(stripper.Flush())
	for _, e := range evs {
		events = append(events, e)
	}
	tail += parser.Flush()
	if tail != "" {
		feed(tail)
	}
	for _, sen := range splitter.Flush() {
		handle(sen)
	}

	asked := map[string]bool{}
	if verdict != nil {
		asked = <-verdict
	}
	// Confirmation fishing: a value the callee already stated is not something they are
	// asking us to provide.
	for _, f := range p.restricted {
		if live.Mentions(heard, f.Value) {
			delete(asked, f.Key)
		}
	}
	var spoken []string
	for _, c := range cands {
		if len(c.flagged) > 0 {
			all := true
			labels := make([]string, len(c.flagged))
			for i, f := range c.flagged {
				labels[i] = f.Label
				if !asked[f.Key] {
					all = false
				}
			}
			if !all {
				s.log().Warn("phone: guard suppressed a sentence", "session", p.rt.sessionID, "labels", strings.Join(labels, ", "))
				events = append(events, guardSuppressed{labels})
				continue
			}
			var tb *float64
			c.pcm, tb = s.synthesize(ctx, c.sentence)
			if tb != nil && ttfb == nil {
				ttfb = tb
			}
		}
		if len(c.pcm) == 0 {
			continue
		}
		spoken = append(spoken, c.sentence)
		pcm = append(pcm, c.pcm...)
	}
	return pcm, strings.Join(spoken, " "), events, ttft, ttfb, nil
}

// askedKeys is SpokenOutputGuard.asked_keys: which restricted fields the callee asked for,
// judged from the LABELS only. Empty on any doubt (fail closed).
func (p *turnPipeline) askedKeys(ctx context.Context, heard string) map[string]bool {
	out := map[string]bool{}
	if len(p.restricted) == 0 || strings.TrimSpace(heard) == "" || p.s.LLM == nil {
		return out
	}
	cctx, cancel := context.WithTimeout(ctx, live.ClassifyTimeout)
	defer cancel()
	var msgs []chatMsg
	for _, m := range live.ClassifierMessages(heard, p.restricted) {
		msgs = append(msgs, chatMsg{m[0], m[1]})
	}
	maxTok := live.ClassifyMaxTokens
	resp, err := p.s.LLM.Chat(cctx, llm.ChatRequest{Label: "live", Messages: toLLM(msgs), MaxTokens: &maxTok})
	if err != nil || resp == nil {
		p.s.log().Warn("phone: ask-classifier unavailable, suppressing", "err", err)
		return out
	}
	for i := range live.ParseVerdict(strings.TrimSpace(resp.Content), len(p.restricted)) {
		out[p.restricted[i-1].Key] = true
	}
	return out
}

// schedulingNote is the deterministic availability verdict for a time the callee proposed,
// injected into this generation only. Fails open: no note.
func (p *turnPipeline) schedulingNote(heard string) string {
	env := p.rt.brief.Constraints
	if env == "" || !live.MightProposeTime(heard) {
		return ""
	}
	v := timewindow.Check(env, heard)
	if !v.TimeDetected || v.Available == nil {
		return ""
	}
	label := "that time"
	if v.ProposedLabel != nil && *v.ProposedLabel != "" {
		label = *v.ProposedLabel
	}
	if *v.Available {
		return "[Scheduling check — ground truth, do not recompute: " + label + " " +
			"is confirmed OPEN on the caller's calendar. Treat it as " +
			"available: offer that slot to the business, and do not tell " +
			"them it is unavailable.]"
	}
	summary := "the times in your brief"
	if v.AcceptableSummary != nil && *v.AcceptableSummary != "" {
		summary = *v.AcceptableSummary
	}
	return "[Scheduling check — ground truth, do not recompute: " + label + " is " +
		"NOT open on the caller's calendar. Do not agree to it. Offer a " +
		"time from — " + summary + "]"
}

func (p *turnPipeline) stuckInLoop(heard string) bool {
	n := 0
	for _, r := range p.records {
		if live.SimilarLine(heard, r.heard) {
			n++
		}
	}
	return n >= live.LoopRepeatLimit-1
}

func (p *turnPipeline) jarvisLooping(said string) bool {
	if !strings.Contains(said, "?") || len(live.NormalizeLine(said)) < 15 {
		return false
	}
	n := 0
	for _, r := range p.records {
		if live.SimilarLine(said, r.said) {
			n++
		}
	}
	return n >= live.LoopRepeatLimit-1
}

// turn runs one caller utterance and returns the reply PCM (nil = say nothing).
func (p *turnPipeline) turn(ctx context.Context, utt []int16, ms *mediaSession) []int16 {
	s := p.s
	turnNo := len(p.records) + 1
	t0 := time.Now()
	if s.STT == nil {
		return nil
	}
	res, err := s.STT.Transcribe(ctx, live.WAV(utt, 8000), stt.TranscribeOptions{})
	if err != nil {
		s.log().Error("phone: transcription failed", "session", p.rt.sessionID, "err", err)
		return nil
	}
	heard := strings.TrimSpace(res.Text)
	sttMS := msSince(t0)
	if heard == "" || live.IsNonSpeech(heard) {
		return nil
	}
	p.messages = append(p.messages, chatMsg{"user", live.WithNoThink(heard)})

	if p.stuckInLoop(heard) {
		s.log().Warn("phone: loop-break — far end repeated the same line", "session", p.rt.sessionID)
		pcm, tb := s.synthesize(ctx, live.LoopBreakGoodbyeLine)
		p.messages = append(p.messages, chatMsg{"assistant", live.LoopBreakGoodbyeLine})
		ms.requestHangup()
		p.record(ctx, turnNo, heard, live.LoopBreakGoodbyeLine, sttMS, nil, tb, t0, []string{"loop_break", "hangup"})
		return pcm
	}

	note := p.schedulingNote(heard)
	pcm, said, events, ttft, ttfb, err := p.generateReply(ctx, heard, note)
	if err != nil {
		s.log().Error("phone: LLM turn failed", "session", p.rt.sessionID, "turn", turnNo, "err", err)
		pcm, _ = s.synthesize(ctx, live.TurnFailureLine)
		p.record(ctx, turnNo, heard, live.TurnFailureLine, sttMS, nil, nil, t0, []string{"llm_failure"})
		return pcm
	}
	if said != "" {
		p.messages = append(p.messages, chatMsg{"assistant", said})
	}

	var names []string
	hangup, outcomeThisTurn, escalate := false, false, ""
	hasEscalate := false
	for _, ev := range events {
		switch e := ev.(type) {
		case live.Hangup:
			hangup = true
			names = append(names, "hangup")
		case live.Outcome:
			p.outcomeFacts = append(p.outcomeFacts, e.Facts)
			outcomeThisTurn = true
			names = append(names, "outcome")
		case live.Escalate:
			escalate, hasEscalate = e.Question, true
			names = append(names, "escalate")
		case live.Dtmf:
			names = append(names, "dtmf_ignored")
		case guardSuppressed:
			names = append(names, "guard_suppressed")
		}
	}

	if hasEscalate && !hangup {
		extraPCM, extraSaid := p.runEscalation(ctx, escalate, ms, said != "")
		pcm = append(pcm, extraPCM...)
		if extraSaid != "" {
			said = strings.TrimSpace(said + " " + extraSaid)
		}
	}

	if !hangup && p.jarvisLooping(said) {
		s.log().Warn("phone: self-loop — model repeated its own reply", "session", p.rt.sessionID)
		pcm, ttfb = s.synthesize(ctx, live.LoopBreakGoodbyeLine)
		said = live.LoopBreakGoodbyeLine
		p.messages = append(p.messages, chatMsg{"assistant", said})
		hangup = true
		names = append(names, "loop_break_self")
	}

	// The first recorded outcome never ends the call in the same reply unless they already
	// signed off: the business must get a chance to confirm.
	theyClosed := live.SoundsLikeFarewell(heard)
	if hangup && outcomeThisTurn && !p.outcomeEarlier && !theyClosed {
		hangup = false
		names = append(names, "hangup_deferred")
		ms.armIdleHangup()
	}
	if !hangup {
		if theyClosed && turnNo > 1 {
			hangup = true
			names = append(names, "closed_by_callee")
		} else if p.outcomeEarlier && live.SoundsLikeFarewell(said) {
			hangup = true
			names = append(names, "closed_out")
		}
	}
	if outcomeThisTurn {
		p.outcomeEarlier = true
	}

	if strings.TrimSpace(said) == "" {
		if theyClosed && !hangup {
			hangup = true
			names = append(names, "closed_by_callee")
		}
		recovery := live.EmptyReplyLine
		switch {
		case hangup:
			recovery = live.FallbackGoodbyeLine
		case hasName(names, "guard_suppressed"):
			recovery = live.GuardSuppressedLine
		}
		pcm, _ = s.synthesize(ctx, recovery)
		said = recovery
		p.messages = append(p.messages, chatMsg{"assistant", said})
		names = append(names, "empty_reply")
	}
	if hangup {
		ms.requestHangup()
	}
	p.record(ctx, turnNo, heard, said, sttMS, ttft, ttfb, t0, names)
	return pcm
}

// runEscalation opens the bounded window, pushes the question, holds the line and either
// feeds the answer back to the model or falls back to "I'll call you back" and ends.
func (p *turnPipeline) runEscalation(ctx context.Context, question string, ms *mediaSession, modelSpoke bool) ([]int16, string) {
	s := p.s
	if !p.rt.escalation.Open() {
		pcm, _ := s.synthesize(ctx, live.EscalationFallbackLine)
		ms.requestHangup()
		p.escalationUnanswered = true
		return pcm, live.EscalationFallbackLine
	}
	if _, err := s.escalate(ctx, p.rt.sessionID, question); err != nil {
		s.log().Error("phone: escalation post failed", "err", err)
	}
	holdSaid := ""
	if !modelSpoke {
		hold, _ := s.synthesize(ctx, live.HoldLine)
		holdSaid = live.HoldLine
		ms.speak(hold)
	}
	answer, ok := p.rt.escalation.Wait(ctx)
	if !ok {
		p.escalationUnanswered = true
		p.outcomeFacts = append(p.outcomeFacts, "escalation unanswered: "+question)
		pcm, _ := s.synthesize(ctx, live.EscalationFallbackLine)
		ms.requestHangup()
		return pcm, strings.TrimSpace(holdSaid + " " + live.EscalationFallbackLine)
	}
	who := p.rt.brief.InitiatorName
	if who == "" {
		who = "The user"
	}
	p.messages = append(p.messages, chatMsg{"user", "[" + who + " answered your question: " + answer + "]"})
	pcm, said, events, _, _, err := p.generateReply(ctx, question, "")
	if err != nil {
		return nil, holdSaid
	}
	if said != "" {
		p.messages = append(p.messages, chatMsg{"assistant", said})
	}
	for _, ev := range events {
		switch e := ev.(type) {
		case live.Hangup:
			ms.requestHangup()
		case live.Outcome:
			p.outcomeFacts = append(p.outcomeFacts, e.Facts)
		}
	}
	return pcm, strings.TrimSpace(holdSaid + " " + said)
}

// record stores the turn and reports it (the transcript append doubles as the heartbeat).
func (p *turnPipeline) record(ctx context.Context, n int, heard, said string, sttMS float64, ttft, ttfb *float64, t0 time.Time, events []string) {
	r := turnRecord{n: n, heard: heard, said: said, sttMS: sttMS, llmTTFTMS: ttft, ttsTTFBMS: ttfb,
		totalMS: msSince(t0), events: events}
	p.records = append(p.records, r)
	p.s.log().Info("phone: turn", "session", p.rt.sessionID, "n", n, "total_ms", r.totalMS, "events", strings.Join(events, ","))
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := p.s.appendTurn(wctx, p.rt.sessionID, r.asEvent()); err != nil {
		p.s.log().Error("phone: turn event failed", "err", err)
	}
}

// transcript is the user-facing transcript (no system prompt).
func (p *turnPipeline) transcript() []chatMsg {
	var out []chatMsg
	for _, m := range p.messages {
		if m.role != "system" {
			out = append(out, m)
		}
	}
	return out
}

// assess is the post-call assessment on the background model → (summary, goal_achieved).
// goal_achieved is nil when the model is unavailable or its verdict can't be parsed.
func (s *Service) assess(ctx context.Context, p *turnPipeline, goal string) (string, *bool) {
	tr := p.transcript()
	// The transcript always starts with the disclosure; with no caller turn there was no
	// conversation.
	if len(p.records) == 0 {
		f := false
		return "The call ended before any conversation took place.", &f
	}
	goal = strings.TrimSpace(goal)
	instr := live.SummaryInstruction
	if goal != "" {
		instr = "The stated goal of this call (from the caller's brief) was: " + goal + "\n\n" + live.SummaryInstruction
	}
	msgs := append(tr, chatMsg{"user", instr})
	fallback := func() (string, *bool) {
		if len(p.outcomeFacts) > 0 {
			return "Call completed. Recorded facts: " + strings.Join(p.outcomeFacts, "; "), nil
		}
		return "Call completed (summary unavailable).", nil
	}
	if s.LLM == nil {
		return fallback()
	}
	actx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := s.LLM.Chat(actx, llm.ChatRequest{Label: "background", Messages: toLLM(msgs)})
	if err != nil || resp == nil {
		s.log().Error("phone: wrapup assessment failed", "err", err)
		return fallback()
	}
	summary, achieved := live.ParseAssessment(live.StripThinkText(resp.Content))
	if summary == "" {
		summary = "Call completed."
	}
	return summary, achieved
}
