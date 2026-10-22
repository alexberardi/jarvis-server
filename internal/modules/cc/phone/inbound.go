package phone

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/sip"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Inbound calls (docs/cc/phone-sip.md §3.4, §4 step 3, §7; loop #446): a household whose SIP
// extension the PBX routes calls to can have Jarvis answer them as a receptionist/screener. It
// says it is an AI, asks who is calling and why, then takes a message or hangs up; the household
// gets one notification with the caller, the reason and a summary, and the recording is kept
// like an outbound call's. The caller is untrusted: caller ID is a hint shown as unverified,
// never authentication, and the agent (inbound_agent.go) knows only the household's name and
// has only take_message and hang_up. Calls come only from the registrar (package sip's guard).
//
// Off by default, per household (phone.inbound.enabled, and phone_calls.enabled). A call above
// the household's concurrent cap or its daily cap is refused 486, so the PBX sends it to
// voicemail; a call no household takes is refused 480, as before inbound existed.

// Inbound setting keys (cc settings; household scope for enabled; not in the mobile household
// settings yet).
const (
	SettingInboundEnabled       = "phone.inbound.enabled"
	SettingInboundAnswerDelay   = "phone.inbound.answer_delay_seconds"
	SettingInboundMaxSeconds    = "phone.inbound.max_call_seconds"
	SettingInboundCallsPerDay   = "phone.inbound.calls_per_day"
	SettingInboundMaxConcurrent = "phone.inbound.max_concurrent_calls"
)

const (
	defaultInboundMaxSeconds = 180
	defaultInboundPerDay     = 20
	inboundRecordingPrefix   = recordingPrefix // phone-calls/<household>/inbound-<id>.wav
	inboundStartTimeout      = 15 * time.Second
	inboundOverrun           = 20 * time.Second // after the time limit, the supervisor's hard stop
)

// Inbound outcomes (cc_phone_inbound_calls.outcome).
const (
	InboundMessage   = "message"
	InboundNoMessage = "no_message"
	InboundHungUp    = "hung_up"
	InboundFailed    = "failed"
)

func intBetween(lo, hi int64) func(any) error {
	return func(v any) error {
		n, ok := v.(int64)
		if !ok || n < lo || n > hi {
			return fmt.Errorf("a whole number from %d to %d", lo, hi)
		}
		return nil
	}
}

func inboundDefinitions() []settings.Definition {
	return []settings.Definition{
		{Key: SettingInboundEnabled, Category: "phone_calls", Type: settings.Bool, Default: false,
			Description: "Inbound calls: Jarvis answers the calls the PBX routes to the household's SIP extension as a " +
				"receptionist (says it is an AI, asks who is calling and why, takes a message or hangs up on spam). " +
				"Set per household (default off); phone_calls.enabled must be on too. Callers get nothing of the household's."},
		{Key: SettingInboundAnswerDelay, Category: "phone_calls", Type: settings.Int, Default: int64(0), Validate: intBetween(0, 60),
			Description: "Inbound calls: seconds an incoming call rings before Jarvis answers, so people in a ring group " +
				"can pick up first (0 answers at once; for a direct route)."},
		{Key: SettingInboundMaxSeconds, Category: "phone_calls", Type: settings.Int, Default: int64(defaultInboundMaxSeconds),
			Validate: intBetween(30, 600),
			Description: "Inbound calls: the longest an answered call may last; Jarvis then says goodbye and hangs up."},
		{Key: SettingInboundCallsPerDay, Category: "phone_calls", Type: settings.Int, Default: int64(defaultInboundPerDay),
			Validate: intBetween(1, 500),
			Description: "Inbound calls: how many Jarvis answers per household per day (UTC); later calls get busy (voicemail)."},
		{Key: SettingInboundMaxConcurrent, Category: "phone_calls", Type: settings.Int, Default: int64(1), Validate: intBetween(1, 5),
			Description: "Inbound calls: how many Jarvis answers at once per household; another gets busy (voicemail)."},
	}
}

// inboundState counts the inbound calls each household has admitted and not yet finished.
type inboundState struct {
	mu     sync.Mutex
	active map[string]int
}

// inboundTicket is one admitted call: its household, its caller and its prewarmed disclosure.
type inboundTicket struct {
	hh         string
	acct       SIPAccount
	caller     sip.Incoming
	delay      time.Duration
	brief      live.InboundBrief
	disclosure []int16
	release    func()
}

// offerInbound is acct's UA's Config.Inbound: it admits the call or refuses it.
func (s *Service) offerInbound(ctx context.Context, acct SIPAccount, in sip.Incoming) sip.Admission {
	t, refuse := s.admitInbound(ctx, acct, in)
	if t == nil {
		return sip.Admission{Refuse: refuse}
	}
	return sip.Admission{
		Delay:     t.delay,
		Answered:  func(c *sip.Call) { s.startInbound(t, &sipCallAdapter{c: c}) },
		Abandoned: func(string) { t.release() },
	}
}

// admitInbound decides an incoming call on acct's line: the household it belongs to, its gates
// and caps, and the disclosure (no disclosure, no answer). A nil ticket comes with the status
// to refuse with: 480 when no household takes the call, 486 above a cap.
func (s *Service) admitInbound(ctx context.Context, acct SIPAccount, in sip.Incoming) (*inboundTicket, int) {
	log := s.log().With("call_id", in.CallID, "caller", in.Number)
	hh := s.inboundHousehold(ctx, acct)
	if hh == "" || !s.Enabled(ctx, hh) {
		log.Info("phone: inbound call refused: no household answers calls on this extension", "account", acct.String())
		return nil, sip.RefuseUnavailable
	}
	log = log.With("household", hh)
	s.inbound.mu.Lock()
	if s.inbound.active == nil {
		s.inbound.active = map[string]int{}
	}
	active := s.inbound.active[hh]
	s.inbound.mu.Unlock()
	if max := s.intSetting(ctx, SettingInboundMaxConcurrent, hh, 1); int64(active) >= max {
		log.Info("phone: inbound call refused busy: the household's concurrent cap", "active", active, "cap", max)
		return nil, sip.RefuseBusy
	}
	today, err := s.inboundToday(ctx, hh)
	if err != nil {
		log.Error("phone: inbound call refused: counting today's calls failed", "err", err)
		return nil, sip.RefuseUnavailable
	}
	if max := s.intSetting(ctx, SettingInboundCallsPerDay, hh, defaultInboundPerDay); today >= max {
		log.Info("phone: inbound call refused busy: the household's daily cap", "today", today, "cap", max)
		return nil, sip.RefuseBusy
	}
	brief := live.InboundBrief{Household: s.householdName(ctx, hh)}
	disclosure, _ := s.synthesize(ctx, live.BuildInboundDisclosure(brief))
	if len(disclosure) == 0 {
		log.Error("phone: inbound call refused: the disclosure could not be synthesized (TTS down?)")
		return nil, sip.RefuseUnavailable
	}
	// Reserve the slot now: a second call while this one rings counts against the cap.
	s.inbound.mu.Lock()
	if int64(s.inbound.active[hh]) >= s.intSetting(ctx, SettingInboundMaxConcurrent, hh, 1) {
		s.inbound.mu.Unlock()
		return nil, sip.RefuseBusy
	}
	s.inbound.active[hh]++
	s.inbound.mu.Unlock()
	var once sync.Once
	t := &inboundTicket{hh: hh, acct: acct, caller: in, brief: brief, disclosure: disclosure,
		delay: time.Duration(s.intSetting(ctx, SettingInboundAnswerDelay, hh, 0)) * time.Second,
		release: func() {
			once.Do(func() {
				s.inbound.mu.Lock()
				s.inbound.active[hh]--
				s.inbound.mu.Unlock()
			})
		}}
	log.Info("phone: inbound call admitted", "answer_delay", t.delay)
	return t, 0
}

// inboundHousehold is the household whose calls acct's extension answers: the one household
// that turned inbound on (its own phone.inbound.enabled) and whose SIP account is acct. None, or
// more than one sharing the account (whose message would it be?), is "".
func (s *Service) inboundHousehold(ctx context.Context, acct SIPAccount) string {
	if s.Settings == nil {
		return ""
	}
	vals, err := s.Settings.HouseholdValues(ctx, SettingInboundEnabled)
	if err != nil {
		s.log().Warn("phone: reading inbound households failed", "err", err)
		return ""
	}
	var match []string
	for _, v := range vals {
		if on, _ := v.Value.(bool); !on {
			continue
		}
		if kind, err := s.providerKind(ctx, v.HouseholdID); err != nil || kind != ProviderSIP {
			continue
		}
		a, _, err := s.sipAccount(ctx, v.HouseholdID)
		if err != nil || a.Server != acct.Server || a.Extension != acct.Extension || a.LocalPort != acct.LocalPort {
			continue
		}
		match = append(match, v.HouseholdID)
	}
	if len(match) > 1 {
		s.log().Error("phone: inbound calls refused: several households turned inbound on for one SIP account; "+
			"give each its own extension", "account", acct.String(), "households", strings.Join(match, ", "))
		return ""
	}
	if len(match) == 0 {
		return ""
	}
	return match[0]
}

// householdNamer is the auth module's household names (cc's NameResolver also has it).
type householdNamer interface {
	HouseholdNames(ctx context.Context) (map[string]string, error)
}

// householdName is hh's display name for the disclosure; "" when unknown.
func (s *Service) householdName(ctx context.Context, hh string) string {
	n, ok := s.Names.(householdNamer)
	if !ok {
		return ""
	}
	names, err := n.HouseholdNames(ctx)
	if err != nil {
		return ""
	}
	return names[hh]
}

// inboundToday counts the household's answered inbound calls since UTC midnight.
func (s *Service) inboundToday(ctx context.Context, hh string) (int64, error) {
	now := s.now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	var n int64
	err := s.DB.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM cc_phone_inbound_calls
		WHERE household_id = ? AND started_at >= ?`, hh, dbTime(day)).Scan(&n)
	return n, err
}

// startInbound runs an answered call on its own goroutine.
func (s *Service) startInbound(t *inboundTicket, call Call) {
	s.init()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runInbound(t, call)
	}()
}

// InboundCall is one row of cc_phone_inbound_calls.
type InboundCall struct {
	ID, HouseholdID, CallID                    string
	CallerNumber, CallerIDName, CallerIDSource string
	CallerName, Reason, Message, Summary       string
	Outcome, EndReason, AudioKey               string
	Turns                                      int
	StartedAt, EndedAt                         time.Time
	Duration                                   *int64
}

// runInbound is one answered call: its row, the receptionist's loop, the time limit, then the
// summary, the recording, the outcome and the notification.
func (s *Service) runInbound(t *inboundTicket, call Call) {
	defer t.release()
	defer call.Close()
	base := s.baseCtx()
	ctx, cancel := context.WithCancel(base)
	defer cancel()
	id := uuid4()
	start := s.now()
	limit := orDefault(s.InboundTimeLimit,
		time.Duration(s.intSetting(ctx, SettingInboundMaxSeconds, t.hh, defaultInboundMaxSeconds))*time.Second)
	if _, err := s.DB.Write.ExecContext(ctx, `INSERT INTO cc_phone_inbound_calls
		(id, household_id, provider, call_id, caller_number, caller_id_name, caller_id_source, started_at)
		VALUES (?, ?, 'sip', ?, ?, ?, ?, ?)`, id, t.hh, nullStr(call.ID()), nullStr(t.caller.Number),
		nullStr(t.caller.Name), nullStr(t.caller.Source), dbTime(start)); err != nil {
		s.log().Error("phone: recording an inbound call failed", "err", err)
	}
	rt := &callRuntime{sessionID: id, householdID: t.hh, recorder: &live.Recorder{}, disclosure: t.disclosure,
		ctx: ctx, cancel: cancel, started: make(chan struct{}), done: make(chan struct{}), call: call, callID: call.ID(),
		timeLimit: time.Now().Add(limit), timeUpLine: live.InboundTimeUpLine}
	agent := s.newInboundAgent(rt, t.brief)
	rt.inbound = agent
	s.log().Info("phone: inbound call answered", "inbound_call", id, "household", t.hh, "caller", t.caller.Number, "limit", limit)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runMedia(rt, call)
	}()

	endReason := ""
	select {
	case <-rt.started:
		endReason = s.superviseInbound(ctx, rt, limit)
	case <-rt.done:
	case <-time.After(inboundStartTimeout):
		endReason = "no_media"
	case <-ctx.Done():
	}
	if endReason == "" || endReason == "no_media" {
		s.endCallQuietly(call)
		cancel()
		<-s.waitDone(rt, hangupGrace)
		if endReason == "" {
			endReason = s.inboundEnd(base, rt, call)
		}
	}
	wctx, wcancel := context.WithTimeout(context.WithoutCancel(base), 90*time.Second)
	defer wcancel()
	s.finishInbound(wctx, id, t, rt, agent, endReason, start)
}

// superviseInbound waits for the call to end. The call loop says goodbye at the time limit
// itself; past it by inboundOverrun (a turn that hung), the call is ended here. It returns the
// end reason.
func (s *Service) superviseInbound(ctx context.Context, rt *callRuntime, limit time.Duration) string {
	stop := time.NewTimer(limit + inboundOverrun)
	defer stop.Stop()
	select {
	case <-rt.done:
	case <-ctx.Done():
		s.endCallQuietly(rt.activeCall())
		<-s.waitDone(rt, hangupGrace)
		return "shutdown"
	case <-stop.C:
		s.log().Warn("phone: inbound call overran its time limit — ending it", "inbound_call", rt.sessionID)
		s.endCallQuietly(rt.activeCall())
		rt.cancel()
		<-s.waitDone(rt, hangupGrace)
		return "time_limit"
	}
	return s.inboundEnd(ctx, rt, rt.activeCall())
}

// inboundEnd says why a call's media ended: the caller hung up, we did (the receptionist, or the
// time limit), or the media failed.
func (s *Service) inboundEnd(ctx context.Context, rt *callRuntime, call Call) string {
	switch {
	case rt.timedOut():
		return "time_limit"
	case ctx.Err() != nil:
		return "shutdown"
	}
	switch r := call.EndReason(); r {
	case EndRemote:
		return "caller_hung_up"
	case EndClosed:
		return "jarvis_hung_up"
	case EndNone:
		return "jarvis_hung_up"
	default:
		return string(r)
	}
}

// finishInbound writes the call's outcome and posts the household's notification.
func (s *Service) finishInbound(ctx context.Context, id string, t *inboundTicket, rt *callRuntime, a *inboundAgent, endReason string, start time.Time) {
	outcome := InboundNoMessage
	started := false
	select {
	case <-rt.started:
		started = true
	default:
	}
	switch {
	case a.message != nil:
		outcome = InboundMessage
	case a.hungUp:
		outcome = InboundHungUp
	case !started:
		outcome = InboundFailed
	}
	summary := s.summarizeInbound(ctx, a)
	audioKey := ""
	if s.Blobs != nil && started {
		key := inboundRecordingPrefix + t.hh + "/inbound-" + id + ".wav"
		if _, err := s.Blobs.Put(ctx, key, bytes.NewReader(rt.recorder.WAV()), "audio/wav"); err != nil {
			s.log().Error("phone: inbound recording upload failed", "inbound_call", id, "err", err)
		} else {
			audioKey = key
		}
	}
	var msg live.TakeMessage
	if a.message != nil {
		msg = *a.message
	}
	end := s.now()
	dur := int64(end.Sub(start).Seconds())
	if _, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_phone_inbound_calls SET caller_name = ?, reason = ?,
		message = ?, summary = ?, outcome = ?, end_reason = ?, audio_object_key = ?, turns = ?, ended_at = ?,
		duration_seconds = ? WHERE id = ?`, nullStr(msg.CallerName), nullStr(msg.Reason), nullStr(msg.Message),
		nullStr(summary), outcome, nullStr(endReason), nullStr(audioKey), len(a.records), dbTime(end), dur, id); err != nil {
		s.log().Error("phone: inbound call outcome failed", "inbound_call", id, "err", err)
	}
	s.log().Info("phone: inbound call ended", "inbound_call", id, "outcome", outcome, "end", endReason,
		"turns", len(a.records), "refused_commands", strings.Join(a.refused, ","))
	call, err := s.inboundCall(ctx, id)
	if err != nil {
		s.log().Error("phone: reading the inbound call back failed", "inbound_call", id, "err", err)
		return
	}
	s.postInboundCard(ctx, call)
}

// summarizeInbound is the notification's summary from the background model, or the message
// itself when the model is unavailable.
func (s *Service) summarizeInbound(ctx context.Context, a *inboundAgent) string {
	fallback := func() string {
		switch {
		case a.message != nil:
			return "The caller left a message."
		case len(a.records) == 0:
			return "The caller did not say anything."
		}
		return "The caller did not leave a message."
	}
	if len(a.records) == 0 || s.LLM == nil {
		return fallback()
	}
	msgs := append(a.transcript(), chatMsg{"user", live.InboundSummaryInstruction})
	actx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := s.LLM.Chat(actx, llm.ChatRequest{Label: "background", Messages: toLLM(msgs)})
	if err != nil || resp == nil {
		s.log().Error("phone: inbound summary failed", "err", err)
		return fallback()
	}
	summary, _ := live.ParseAssessment(live.StripThinkText(resp.Content))
	if summary == "" {
		return fallback()
	}
	return summary
}

// inboundCall reads one row.
func (s *Service) inboundCall(ctx context.Context, id string) (*InboundCall, error) {
	var c InboundCall
	var callID, num, cidName, cidSrc, name, reason, msg, summary, outcome, end, audio, ended sql.NullString
	var started string
	var dur sql.NullInt64
	err := s.DB.Read.QueryRowContext(ctx, `SELECT id, household_id, call_id, caller_number, caller_id_name,
		caller_id_source, caller_name, reason, message, summary, outcome, end_reason, audio_object_key, turns,
		started_at, ended_at, duration_seconds FROM cc_phone_inbound_calls WHERE id = ?`, id).Scan(
		&c.ID, &c.HouseholdID, &callID, &num, &cidName, &cidSrc, &name, &reason, &msg, &summary, &outcome, &end,
		&audio, &c.Turns, &started, &ended, &dur)
	if err != nil {
		return nil, err
	}
	c.CallID, c.CallerNumber, c.CallerIDName, c.CallerIDSource = callID.String, num.String, cidName.String, cidSrc.String
	c.CallerName, c.Reason, c.Message, c.Summary = name.String, reason.String, msg.String, summary.String
	c.Outcome, c.EndReason, c.AudioKey = outcome.String, end.String, audio.String
	c.StartedAt, c.EndedAt = parseTS(started), parseTS(ended.String)
	if dur.Valid {
		c.Duration = &dur.Int64
	}
	return &c, nil
}

// closeInterruptedInbound marks calls that were live when jarvisd stopped as failed: nothing
// will finish them now.
func (s *Service) closeInterruptedInbound(ctx context.Context) {
	if s.DB == nil {
		return
	}
	if _, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_phone_inbound_calls SET ended_at = ?,
		outcome = COALESCE(outcome, 'failed'), end_reason = COALESCE(end_reason, 'interrupted') WHERE ended_at IS NULL`,
		dbTime(s.now())); err != nil && !errors.Is(err, context.Canceled) {
		s.log().Warn("phone: closing interrupted inbound calls failed", "err", err)
	}
}

// postInboundCard is the household's notification of a call: who (the caller's own words and
// the caller ID, both marked unverified), why, the message and the summary. No transcript.
func (s *Service) postInboundCard(ctx context.Context, c *InboundCall) {
	who := firstNonEmpty(c.CallerName, c.CallerIDName, c.CallerNumber, "an unknown caller")
	var title string
	switch c.Outcome {
	case InboundMessage:
		title = "📞 Message from " + who
	case InboundHungUp:
		title = "🚫 Call screened: " + who
	default:
		title = "📞 Missed call from " + who
	}
	var b strings.Builder
	if c.CallerName != "" {
		fmt.Fprintf(&b, "**Caller said they are:** %s\n", c.CallerName)
	}
	cid := c.CallerNumber
	if c.CallerIDName != "" {
		cid = strings.TrimSpace(cid + " (" + c.CallerIDName + ")")
	}
	if cid == "" {
		cid = "none"
	}
	fmt.Fprintf(&b, "**Caller ID:** %s, not verified\n", cid)
	if c.Reason != "" {
		fmt.Fprintf(&b, "**Reason:** %s\n", c.Reason)
	}
	if c.Message != "" {
		fmt.Fprintf(&b, "**Message:** %s\n", c.Message)
	}
	fmt.Fprintf(&b, "\n%s\n\n_Jarvis answered this call as your receptionist. What the caller said, and their caller ID, "+
		"are their own claims: Jarvis cannot verify who called._", c.Summary)
	meta := hhMeta(c.HouseholdID)
	meta["inbound_call_id"] = c.ID
	meta["direction"] = "inbound"
	meta["audio_available"] = c.AudioKey != ""
	if c.AudioKey != "" {
		meta["audio_object_key"] = c.AudioKey
	}
	s.postCard(ctx, card{HouseholdID: c.HouseholdID, Title: title, Summary: c.Summary, Body: b.String(), Metadata: meta})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
