package phone

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/timewindow"
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
	// timesFromCaller: the caller answered a question with a time mid-call, so the brief's time
	// window no longer vetoes what the business proposes.
	timesFromCaller bool
	// given are the restricted fields already spoken on this call (asked for and cleared): a
	// read-back of one is them checking what we said, not fishing for it.
	given map[string]bool
	// answers are the caller's mid-call decisions ("asked Q, answered A"), which update the
	// goal for the outcome assessment.
	answers []string
	// answered maps each question the caller answered (normalized) to the answer, so the same
	// question is never put to them twice on one call.
	answered     map[string]string
	restricted   []live.RestrictedField
	bargedIn     bool   // the current turn's utterance cut the last reply off
	announcement string // the first utterance was a recorded intercept (set once, then the call ends)
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
		if live.Mentions(heard, f.Value) && !p.given[f.Key] {
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
	if rest := parser.Flush(); live.ParseUnclosed(rest) != nil {
		events = append(events, live.ParseUnclosed(rest))
	} else {
		tail += rest
	}
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
		if live.Mentions(heard, f.Value) && !p.given[f.Key] {
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
				if !asked[f.Key] && !p.given[f.Key] {
					all = false
				}
			}
			if !all {
				s.log().Warn("phone: guard suppressed a sentence", "session", p.rt.sessionID, "labels", strings.Join(labels, ", "))
				events = append(events, guardSuppressed{labels})
				continue
			}
			if p.given == nil {
				p.given = map[string]bool{}
			}
			for _, f := range c.flagged {
				p.given[f.Key] = true
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
	return pcm, strings.Join(spoken, " "), dropCopiedExample(events, heard), ttft, ttfb, nil
}

// dropCopiedExample removes an escalation that is the prompt's own example, word for word,
// when the business said nothing about it: Qwen3.5-9B asked the caller "what is the patient's
// date of birth?" on a pizza booking after hearing "Um...", and held the line for the answer.
func dropCopiedExample(events []any, heard string) []any {
	out := events[:0:0]
	for _, ev := range events {
		if e, ok := ev.(live.Escalate); ok && normQuestion(e.Question) == normQuestion(live.EscalateExample) &&
			!strings.Contains(strings.ToLower(heard), "birth") {
			continue
		}
		out = append(out, ev)
	}
	return out
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
	heard = spokenClock(heard)
	if env == "" || p.timesFromCaller || !mightBeTime(heard) {
		return ""
	}
	// Every time they mention is judged ("7 is booked; we have 6:15 or 7:30"), not only the
	// first: the first can be the one they just refused.
	verdicts := timewindow.CheckAll(env, heard)
	if len(verdicts) == 0 {
		return ""
	}
	var closed []string
	for _, v := range verdicts {
		if !*v.Available {
			closed = append(closed, *v.ProposedLabel)
		}
	}
	if len(closed) == 0 {
		label := *verdicts[0].ProposedLabel
		return "[Scheduling check — ground truth, do not recompute: " + label + " " +
			"is confirmed OPEN on the caller's calendar. Treat it as " +
			"available: offer that slot to the business, and do not tell " +
			"them it is unavailable.]"
	}
	summary := "the times in your brief"
	if s := verdicts[0].AcceptableSummary; s != nil && *s != "" {
		summary = *s
	}
	who := p.rt.brief.InitiatorName
	if who == "" {
		who = "the person you are calling for"
	}
	return "[Scheduling check — ground truth, do not recompute: " + strings.Join(closed, ", ") + " " +
		map[bool]string{true: "is", false: "are"}[len(closed) == 1] + " NOT open on the caller's calendar. " +
		"Do not agree to " + map[bool]string{true: "it", false: "any of them"}[len(closed) == 1] +
		" yourself. Their acceptable times — " + summary + ". If the business can't do an acceptable " +
		"time, ask " + who + " which of the business's offers works with [ESCALATE: …], naming the offers.]"
}

var (
	holdRE = regexp.MustCompile(`(?i)\b(hold|moment|one sec|a sec|check)`)
	// "7.30" is how whisper often writes 7:30; "$12.50" stays a price.
	dottedClockRE = regexp.MustCompile(`(^|[^$\d.])(\d{1,2})\.(\d{2})\b`)
	// "I could do 715": whisper drops the colon after an offer verb ("at 1200 Main Street" is an address).
	runClockRE   = regexp.MustCompile(`(?i)\b(do|have|got|try|about|around|how about)\s+(1[0-2]|[1-9])([0-5]\d)\b`)
	clockRE      = regexp.MustCompile(`\b\d{1,2}:\d{2}\b`)
	hourWords    = map[string]string{"one": "1", "two": "2", "three": "3", "four": "4", "five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9", "ten": "10", "eleven": "11", "twelve": "12"}
	minuteWords  = map[string]string{"oh five": "05", "ten": "10", "fifteen": "15", "twenty": "20", "thirty": "30", "forty": "40", "forty five": "45", "fifty": "50"}
	spokenTimeRE = regexp.MustCompile(`(?i)\b(one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve)[\s-]+(oh five|forty[\s-]five|fifteen|twenty|thirty|forty|fifty|ten|o'?clock|[ap]\.?m\b)`)
)

// spokenClock writes "six fifteen" as 6:15 and "seven o'clock" as 7 for the time checks: a
// transcript can spell times out. A lone hour word ("a table for two") is left alone.
func spokenClock(s string) string {
	s = dottedClockRE.ReplaceAllString(s, "$1$2:$3")
	s = runClockRE.ReplaceAllString(s, "$1 $2:$3")
	return spokenTimeRE.ReplaceAllStringFunc(s, func(m string) string {
		g := spokenTimeRE.FindStringSubmatch(m)
		h, rest := hourWords[strings.ToLower(g[1])], strings.ToLower(strings.ReplaceAll(g[2], "-", " "))
		switch {
		case strings.HasSuffix(rest, "clock"):
			return h + " o'clock"
		case rest[0] == 'a' || rest[0] == 'p':
			return h + " " + g[2]
		}
		return h + ":" + minuteWords[strings.Join(strings.Fields(rest), " ")]
	})
}

// mightBeTime widens live.MightProposeTime (Python parity) to "7:30" and spelled-out times.
func mightBeTime(s string) bool {
	s = spokenClock(s)
	return live.MightProposeTime(s) || clockRE.MatchString(s)
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
func (p *turnPipeline) turn(ctx context.Context, utt []int16, ms callLine) []int16 {
	s := p.s
	turnNo := len(p.records) + 1
	t0 := time.Now()
	p.bargedIn = ms.cutOff()
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
	if turnNo == 1 && live.IsAnnouncement(heard) {
		// Answered by a recording (FreePBX's all-trunks-busy is 200 OK and a message): no
		// conversation, and the call failed.
		s.log().Warn("phone: answered by a recorded announcement — hanging up", "session", p.rt.sessionID)
		p.announcement = heard
		ms.requestHangup()
		p.record(ctx, turnNo, heard, "", sttMS, nil, nil, t0, []string{"announcement", "hangup"})
		return nil
	}
	user := heard
	if p.bargedIn {
		user = live.InterruptedNote + " " + heard
	}
	p.messages = append(p.messages, chatMsg{"user", live.WithNoThink(user)})

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
		// The hold line plays unless the model already said it's checking: "Of course, take your
		// time." before 25 s of waiting is dead air to the business.
		extraPCM, extraSaid := p.runEscalation(ctx, escalate, ms, holdRE.MatchString(said))
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
func (p *turnPipeline) runEscalation(ctx context.Context, question string, ms callLine, modelSpoke bool) ([]int16, string) {
	s := p.s
	who := p.rt.brief.InitiatorName
	if who == "" {
		who = "The user"
	}
	if prior, done := p.answered[normQuestion(question)]; done {
		// Already answered on this call: hand the model the answer instead of asking again.
		p.messages = append(p.messages, chatMsg{"user", "[" + who + " already answered that: \"" + prior +
			"\". Do not ask " + who + " again; use the answer with the business now.]"})
		return p.replyAfterEscalation(ctx, ms, question, "")
	}
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
	if ok {
		// The question goes with the answer: a reply that was only the escalation token left no
		// trace in the history. The caller's answer is newer than the plan, so it wins.
		p.messages = append(p.messages, chatMsg{"user", "[You asked " + who + ": \"" + question + "\". " + who +
			" answered: \"" + answer + "\". This is " + who + "'s own instruction for this call; where it differs from your brief, follow it.]"})
		if mightBeTime(answer) {
			p.timesFromCaller = true
		}
		p.answers = append(p.answers, "asked \""+question+"\", answered \""+answer+"\"")
		if p.answered == nil {
			p.answered = map[string]string{}
		}
		p.answered[normQuestion(question)] = answer
	} else {
		// No answer is not the end of the call: a booking can often go ahead without it (a
		// callback number for a reservation). The model decides; the fixed goodbye is the backstop.
		p.escalationUnanswered = true
		p.outcomeFacts = append(p.outcomeFacts, "escalation unanswered: "+question)
		p.messages = append(p.messages, chatMsg{"user", "[" + who + " did not answer in time, so you do not have the answer to: " +
			question + ". Tell the other person briefly that you don't have that to give. If it is an extra they can do without, ask whether " +
			"they can go ahead without it and carry on if they can; if they need it, say you'll follow up, say goodbye and emit [HANGUP]. " +
			"Do not escalate this question again.]"})
	}
	if !ok {
		pcm, said, events, _, _, err := p.generateReply(ctx, question, "")
		if err != nil || strings.TrimSpace(said) == "" {
			pcm, _ = s.synthesize(ctx, live.EscalationFallbackLine)
			ms.requestHangup()
			return pcm, strings.TrimSpace(holdSaid + " " + live.EscalationFallbackLine)
		}
		p.messages = append(p.messages, chatMsg{"assistant", said})
		p.applyEvents(events, ms)
		return pcm, strings.TrimSpace(holdSaid + " " + said)
	}
	pcm, said := p.replyAfterEscalation(ctx, ms, question, answer)
	return pcm, strings.TrimSpace(holdSaid + " " + said)
}

// replyAfterEscalation is the reply once the caller has answered. A model that asks again
// instead of using the answer (it copies its own question back) gets one nudge and a retry.
func (p *turnPipeline) replyAfterEscalation(ctx context.Context, ms callLine, question, answer string) ([]int16, string) {
	pcm, said, events, _, _, err := p.generateReply(ctx, question, "")
	if err == nil && strings.TrimSpace(said) == "" && hasEscalation(events) {
		p.s.log().Warn("phone: model re-asked an answered question — nudging", "session", p.rt.sessionID)
		nudge := "[You already have the answer"
		if answer != "" {
			nudge += ": \"" + answer + "\""
		}
		p.messages = append(p.messages, chatMsg{"user", nudge + ". Do not ask again; say it to the business now.]"})
		pcm, said, events, _, _, err = p.generateReply(ctx, question, "")
	}
	if err != nil {
		return nil, ""
	}
	if said != "" {
		p.messages = append(p.messages, chatMsg{"assistant", said})
	}
	p.applyEvents(events, ms)
	return pcm, said
}

func (p *turnPipeline) applyEvents(events []any, ms callLine) {
	for _, ev := range events {
		switch e := ev.(type) {
		case live.Hangup:
			ms.requestHangup()
		case live.Outcome:
			p.outcomeFacts = append(p.outcomeFacts, e.Facts)
		}
	}
}

func hasEscalation(events []any) bool {
	for _, ev := range events {
		if _, ok := ev.(live.Escalate); ok {
			return true
		}
	}
	return false
}

// normQuestion keys a question by its words, so a re-ask with other punctuation matches.
func normQuestion(q string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}

// record stores the turn and reports it (the transcript append doubles as the heartbeat).
func (p *turnPipeline) record(ctx context.Context, n int, heard, said string, sttMS float64, ttft, ttfb *float64, t0 time.Time, events []string) {
	if p.bargedIn {
		events = append([]string{"barge_in"}, events...)
	}
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
	if len(p.answers) > 0 {
		who := p.rt.brief.InitiatorName
		if who == "" {
			who = "the person"
		}
		instr = "During the call the assistant checked with " + who + ", who decided: " + strings.Join(p.answers, "; ") +
			". Those decisions update the goal: an outcome that matches what " + who + " approved counts as achieved.\n\n" + instr
	}
	if goal != "" {
		instr = "The stated goal of this call (from the caller's brief) was: " + goal + "\n\n" + instr
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
