package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/simline"
	_ "modernc.org/sqlite"
)

// A call scenario is a phone call end to end against a sandbox up with --phone-sim: the request
// said to Jarvis, the plan it makes (number lookup included), the "Call now" tap, the call
// itself against a callee the model plays from Callee, and the result card. Files are JSON
// (calls/*.json), one scenario or an array.
type CallScenario struct {
	Name    string `json:"name"`
	Request string `json:"request"`
	// Callee is who answers: a persona the model plays, with what they know and what they do.
	// "{today}" is replaced by the day the call runs ("Friday, November 6, 2026").
	Callee string `json:"callee"`
	// Number, when set, is the number the plan must have found.
	Number string `json:"number,omitempty"`
	// CallContext is the caller's saved call details (phone_calls.call_context entries: key,
	// value, and optionally label, category, tier); none clears them.
	CallContext []map[string]string `json:"call_context,omitempty"`
	// Answers are what the caller answers Jarvis's mid-call questions with ("The call needs your
	// input"), in order; a question past the last goes unanswered.
	Answers []string `json:"answers,omitempty"`
	// AnswerAfter delays each answer this many seconds (past the call's ~25 s escalation
	// window, the caller answers too late).
	AnswerAfter float64 `json:"answer_after,omitempty"`
	// Expect is what must hold for the scenario to pass.
	Expect CallExpect `json:"expect"`
	// Contact, when set, is put in the household's phonebook first (a business the web
	// lookup can't find): {"name": ..., "number": ...}.
	Contact map[string]string `json:"contact,omitempty"`
	// NodeContext, when set, is what the household's commands share with the plan, answered
	// by a stand-in node (callnode.go).
	NodeContext *NodeContext `json:"node_context,omitempty"`
	// Variant is how pipeline call-gen made this scenario (callvariant.go); none for a
	// hand-written one.
	Variant *Variant `json:"variant,omitempty"`
}

// CallExpect is a call scenario's checks (callcheck.go). Text checks ignore case; Says and Never
// also match with spaces, dashes and commas removed, so a number Jarvis reads digit by digit
// still counts.
type CallExpect struct {
	// Achieved is the result card's verdict (✅ true, ⚠️ false); nil accepts either.
	Achieved *bool `json:"achieved,omitempty"`
	// Card is text the result card must contain; CardTimes are times (h:mm) it must name,
	// however written ("7:30 PM", "half seven").
	Card      []string `json:"card,omitempty"`
	CardTimes []string `json:"card_times,omitempty"`
	// Says is text Jarvis must say during the call; Never is text it must not.
	Says  []string `json:"says,omitempty"`
	Never []string `json:"never,omitempty"`
	// NoParty: the request gave no party size, so Jarvis must not state one.
	NoParty bool `json:"no_party,omitempty"`
	// NotUser are the names of the person Jarvis calls for: it never claims to be them.
	NotUser []string `json:"not_user,omitempty"`
}

func contains(text, want string) bool {
	t, w := strings.ToLower(text), strings.ToLower(want)
	return strings.Contains(t, w) || strings.Contains(squash(t), squash(w))
}

// spokenDigits are the words the voice path reads digits as ("seven, seven, one").
var spokenDigits = strings.NewReplacer("zero", "0", "oh", "0", "one", "1", "two", "2", "three", "3", "four", "4",
	"five", "5", "six", "6", "seven", "7", "eight", "8", "nine", "9", "dash", "")

// phonetic is a spelled letter ("D as in Dog", "X as in X-ray").
var phonetic = regexp.MustCompile(`(?i)\b([a-z]) as in [a-z]+(?:-[a-z]+)?`)

// squash drops separators and reads spoken digits and spelled letters, so "D as in Dog, X,
// dash, seven, seven" is "dx77".
func squash(s string) string {
	s = phonetic.ReplaceAllString(s, "$1")
	return strings.NewReplacer(" ", "", "-", "", ",", "", ".", "").Replace(spokenDigits.Replace(s))
}

// calleeRules frame every persona: they are a person on a phone line, saying lines aloud.
const calleeRules = `You are playing a person who just answered a phone call at work, for a test of a phone assistant.
Stay in character. Reply with only the words you say aloud on the phone: one or two short spoken sentences, no stage directions, no lists, no markdown.
Only when the conversation is over, in the same reply as your goodbye, end it with [HANGUP]. Never hang up in a reply that asks a question, and never in your first reply.
If the line is silent, react as a person would ("Hello? Are you still there?").
Follow your character's instructions exactly: don't invent requirements, demands or problems they don't mention.

Your character:
`

// callOpts are a run's settings beyond the scenarios.
type callOpts struct {
	dial        string
	sip         bool
	out         string        // directory for one record per call
	judgeModel  string        // "" skips the judge
	judgeTap    string        // where the judge's model is reached
	maxTurn     time.Duration // a turn slower than this is a long silence
	retries     int           // harness failures retried; product failures never
	voiceLine   bool          // the simulated line speaks (TTS/whisper both ways); see callee
	dbPath      string        // the sandbox's database, for the session's transcript
	tapLog      string
	concurrency int
}

func runCalls(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("call", flag.ContinueOnError)
	sandbox := fs.String("sandbox", "", "sandbox name (scripts/sandbox up --phone-sim)")
	model := fs.String("model", "live", "model label the callee is played by (through the sandbox's tap)")
	calleeTap := fs.String("callee-tap", "", "tap the callee is played through instead of the sandbox's (another sandbox's, to hold the business's model fixed)")
	only := fs.String("only", "", "run only these scenarios (comma-separated names)")
	repeat := fs.Int("repeat", 1, "run each scenario this many times and report its pass rate")
	dial := fs.String("dial", "", "call this number instead of the plan's, as if edited on its card (the SIP demo's softphone)")
	you := fs.Bool("you", false, "you play the business (type its lines on stdin) and answer Jarvis's questions (lines starting with >)")
	conc := fs.Int("concurrency", 1, "calls at once (plans are still made one at a time; keep it to 2-3 on a shared model)")
	out := fs.String("out", "", "write one JSON record per call here (transcript, checks, judge) for pipeline call-report")
	judge := fs.String("judge", "", "model label that judges each call (e.g. background); empty skips the judge")
	judgeTap := fs.String("judge-tap", "", "tap the judge is reached through instead of the sandbox's")
	maxTurn := fs.Duration("max-turn", 8*time.Second, "a call turn slower than this fails the silence check")
	retries := fs.Int("retries", 2, "retry a call this many times when it failed alongside a harness error (tap, network, callee model); product failures are never retried")
	voiceLine := fs.Bool("voice-line", false, "the sandbox's simulated line is in voice mode (real TTS and whisper both ways): the callee hears Jarvis through the line's Heard text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("call: name scenario files (tools/pipeline/calls/*.json)")
	}
	env, dir, _, err := sandboxEnv(*sandbox)
	if err != nil {
		return err
	}
	// Without the simulated line the call goes out over the sandbox's real phone provider
	// (--sip to the FreePBX VM), and a person answers it.
	sip := env["SBX_PHONE_SIM"] == ""
	if sip && !*you {
		return errors.New("call: this sandbox has no simulated phone line (scripts/sandbox up --phone-sim); over SIP, pass --you")
	}
	if *you || sip {
		*conc = 1
	}
	d, err := sandboxDriver(*sandbox)
	if err != nil {
		return err
	}
	scs, err := loadCallScenarios(fs.Args(), *only)
	if err != nil {
		return err
	}
	opts := callOpts{dial: *dial, sip: sip, out: *out, judgeModel: *judge, judgeTap: env["SBX_LLM_TAP"], maxTurn: *maxTurn,
		retries: *retries, voiceLine: *voiceLine, dbPath: filepath.Join(dir, "home", "jarvis.db"), tapLog: filepath.Join(dir, "llm.jsonl"),
		concurrency: max(1, *conc)}
	if *judgeTap != "" {
		opts.judgeTap = *judgeTap
	}
	if opts.out != "" {
		if err := os.MkdirAll(opts.out, 0o755); err != nil {
			return err
		}
	}
	cl := &callee{tap: env["SBX_LLM_TAP"], model: *model, out: stdout, voice: *voiceLine}
	if *calleeTap != "" {
		cl.tap = *calleeTap
	}
	if *you {
		cl.you = readHuman(os.Stdin, sip)
		fmt.Fprintln(stdout, "You are the business: type its lines (end one with /hangup to hang up). Answer Jarvis's questions to you with a leading >.")
	}
	if sip {
		fmt.Fprintln(stdout, "Over SIP: answer the softphone and speak as the business; type only your answers to Jarvis's questions (>).")
	} else {
		srv := &http.Server{Addr: env["SBX_PHONE_SIM"], Handler: cl, ReadHeaderTimeout: 10 * time.Second}
		errc := make(chan error, 1)
		go func() { errc <- srv.ListenAndServe() }()
		defer srv.Close()
		select {
		case err := <-errc:
			return fmt.Errorf("call: callee server: %w", err)
		case <-time.After(200 * time.Millisecond):
		}
	}
	if opts.concurrency > 1 {
		api := newUserAPI(env)
		if hh, err := api.household(ctx, env); err == nil {
			_ = api.put(env["SBX_CC"]+"/api/v0/mobile/household/"+hh+"/settings/"+"phone_calls.max_concurrent_calls", map[string]any{"value": opts.concurrency + 2}) // +2: the next plan, and a call still in wrapup after its card
		}
	}

	type job struct{ i, rep int }
	var jobs []job
	for i := range scs {
		for n := range *repeat {
			jobs = append(jobs, job{i, n})
		}
	}
	run := &callRun{d: d, env: env, cl: cl, opts: opts, stdout: &lockedWriter{w: stdout}}
	failed := 0
	passes := make([]int, len(scs))
	var mu sync.Mutex
	queue := make(chan job)
	var wg sync.WaitGroup
	for range opts.concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				ok := run.one(ctx, scs[j.i], j.rep)
				mu.Lock()
				if ok {
					passes[j.i]++
				} else {
					failed++
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		queue <- j
	}
	close(queue)
	wg.Wait()
	fmt.Fprintln(stdout, "── pass rate")
	for i, sc := range scs {
		fmt.Fprintf(stdout, "  %d/%d  %s\n", passes[i], *repeat, sc.Name)
	}
	if failed > 0 {
		return errFailed{failed}
	}
	return ctx.Err()
}

func loadCallScenarios(paths []string, only string) ([]CallScenario, error) {
	var scs []CallScenario
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var many []CallScenario
		if err := json.Unmarshal(b, &many); err != nil {
			var one CallScenario
			if err := json.Unmarshal(b, &one); err != nil {
				return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
			}
			many = []CallScenario{one}
		}
		for _, sc := range many {
			if only == "" || slices.Contains(strings.Split(only, ","), sc.Name) {
				scs = append(scs, sc)
			}
		}
	}
	return scs, nil
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// callRun is one pipeline call run's shared state.
type callRun struct {
	d      *Driver
	env    map[string]string
	cl     *callee
	opts   callOpts
	planMu sync.Mutex // one plan at a time: plan cards are told apart by order only
	stdout *lockedWriter
}

// harnessError is a failure of the harness, not the product: retried.
type harnessError struct{ err error }

func (h harnessError) Error() string { return "harness: " + h.err.Error() }

// one runs a scenario (retrying harness failures), writes its record and reports it.
func (r *callRun) one(ctx context.Context, sc CallScenario, rep int) bool {
	var rec *CallRecord
	for attempt := 0; ; attempt++ {
		var buf bytes.Buffer
		var out io.Writer = &buf
		if r.opts.concurrency == 1 {
			out = r.stdout
		}
		rec = r.attempt(ctx, sc, rep, out)
		rec.Attempts = attempt + 1
		if r.opts.concurrency > 1 {
			r.stdout.Write(buf.Bytes())
		}
		if rec.HarnessError == "" || rec.passed() || attempt >= r.opts.retries || ctx.Err() != nil {
			break
		}
		fmt.Fprintf(r.stdout, "  ↻ %s: %s, retrying\n", sc.Name, rec.HarnessError)
	}
	if rec.passed() {
		fmt.Fprintf(r.stdout, "  ✓ %s\n\n", sc.Name)
	} else {
		fmt.Fprintf(r.stdout, "  ✗ %s: %s\n\n", sc.Name, strings.Join(rec.failures(), "; "))
	}
	if r.opts.out != "" {
		name := strings.NewReplacer("/", "_", " ", "_").Replace(sc.Name)
		b, _ := json.MarshalIndent(rec, "", "  ")
		_ = os.WriteFile(filepath.Join(r.opts.out, fmt.Sprintf("%s.r%d.json", name, rep)), append(b, '\n'), 0o644)
	}
	return rec.passed()
}

func (r *callRun) attempt(ctx context.Context, sc CallScenario, rep int, out io.Writer) *CallRecord {
	rec := &CallRecord{Name: sc.Name, Rep: rep, Variant: sc.Variant, Request: sc.Request, Expect: sc.Expect, Voice: r.opts.voiceLine,
		CallContext: sc.CallContext}
	start := time.Now()
	tapFrom := tapSize(r.opts.tapLog)
	err := r.call(ctx, sc, rec, out)
	rec.Seconds = time.Since(start).Seconds()
	var he harnessError
	switch {
	case errors.As(err, &he):
		rec.HarnessError = he.err.Error()
	case err != nil:
		rec.Fails = append(rec.Fails, err.Error())
	}
	// A failure alongside a model error the harness caused (the tap, a busy upstream) or a
	// callee that couldn't answer is the harness's; it is retried.
	if rec.HarnessError == "" && !rec.passed() {
		if n := upstreamErrors(readTap(r.opts.tapLog, tapFrom)); n > 0 {
			rec.HarnessError = fmt.Sprintf("%d model calls failed upstream during the call", n)
		} else if rec.CalleeErrors > 0 {
			rec.HarnessError = fmt.Sprintf("the callee model failed %d times", rec.CalleeErrors)
		}
	}
	return rec
}

// upstreamErrors counts tap entries that failed for reasons outside the product: no answer,
// a 5xx or a broken connection.
func upstreamErrors(es []TapEntry) int {
	n := 0
	for _, e := range es {
		// "context canceled" is jarvisd giving up on a slow model call itself (its 20 s turn
		// budget): the product's behaviour under load, not a broken harness.
		if (e.Status == 0 || e.Status >= 500 || e.Error != "") && !strings.Contains(e.Error, "context canceled") {
			n++
		}
	}
	return n
}

func newUserAPI(env map[string]string) *userAPI {
	return &userAPI{token: env["SBX_TOKEN"], authURL: env["SBX_AUTH"], email: env["SBX_EMAIL"], password: env["SBX_PASSWORD"]}
}

func (a *userAPI) household(ctx context.Context, env map[string]string) (string, error) {
	var hhs struct {
		Households []struct {
			ID string `json:"id"`
		} `json:"households"`
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, env["SBX_CC"]+"/api/v0/nodes/pairings", nil)
	if err := a.do(req, &hhs); err != nil || len(hhs.Households) == 0 {
		return "", fmt.Errorf("household: %v", err)
	}
	return hhs.Households[0].ID, nil
}

// call plays one call into rec. A returned harnessError is the harness's failure; any other
// error is the product's.
func (r *callRun) call(ctx context.Context, sc CallScenario, rec *CallRecord, stdout io.Writer) error {
	env, cl := r.env, r.cl
	fmt.Fprintf(stdout, "══ %s\n", sc.Name)
	api := newUserAPI(env)
	// One call is set up at a time, from its saved details to its start: the plan and the
	// call's start both read the user's saved details, and plan cards are told apart by order.
	r.planMu.Lock()
	unlock := sync.OnceFunc(r.planMu.Unlock)
	defer unlock()

	plan, err := func() (inboxItem, error) {
		ctxJSON, _ := json.Marshal(sc.CallContext)
		if sc.CallContext == nil {
			ctxJSON = []byte("[]")
		}
		if err := api.put(env["SBX_CC"]+"/settings/phone_calls.call_context?user_id="+env["SBX_USER_ID"],
			map[string]any{"value": string(ctxJSON)}); err != nil {
			return inboxItem{}, harnessError{fmt.Errorf("call context: %w", err)}
		}
		if sc.Contact != nil {
			hh, err := api.household(ctx, env)
			if err != nil {
				return inboxItem{}, harnessError{err}
			}
			// A second add of the same contact is refused; the first is what counts.
			_ = api.send(http.MethodPost, env["SBX_CC"]+"/api/v0/mobile/household/"+hh+"/phone-contacts",
				map[string]any{"name": sc.Contact["name"], "number": sc.Contact["number"]}, nil)
		}
		if sc.NodeContext != nil {
			stop, err := contextNode(ctx, env, *sc.NodeContext)
			if err != nil {
				return inboxItem{}, harnessError{fmt.Errorf("stand-in node: %w", err)}
			}
			defer stop() // the plan reads the node's context; the call itself doesn't
		}
		since := time.Now().UTC() // no margin: the call set up just before this one has a session from moments ago
		log := r.d.Run(ctx, Scenario{Name: sc.Name, Speaker: "primary", Turns: []Turn{{Say: sc.Request}}})
		printLog(stdout, log, false)
		// The plan is this request's session: plan cards are posted once drafted, so a slow
		// one would otherwise be taken for the next request's.
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			if sid := newestSession(r.opts.dbPath, since, sc.Contact["name"]); sid != "" {
				plan, err := api.waitItem(ctx, env["SBX_NOTIFICATIONS"], since, "Call plan", time.Until(deadline), sid)
				if err != nil {
					return inboxItem{}, fmt.Errorf("no call plan: %w", err)
				}
				return plan, nil
			}
			if refused, err := api.waitItem(ctx, env["SBX_NOTIFICATIONS"], since, "Call not started", 0, ""); err == nil {
				return refused, nil
			}
			select {
			case <-ctx.Done():
				return inboxItem{}, harnessError{ctx.Err()}
			case <-time.After(time.Second):
			}
		}
		return inboxItem{}, errors.New("no call plan within 90s")
	}()
	if err != nil {
		return err
	}
	rec.Plan = plan.Title
	if strings.Contains(plan.Title, "not started") {
		err := fmt.Errorf("%s: %s", plan.Title, plan.Summary)
		if strings.Contains(plan.Summary, "already a call in progress") && r.opts.concurrency > 1 {
			time.Sleep(30 * time.Second) // our own calls hold the household's slots: wait, then retry
			return harnessError{err}
		}
		return err
	}
	confirm, ok := element(plan, "confirm_call")
	if !ok {
		return errors.New("the plan card has no Call now")
	}
	fmt.Fprintf(stdout, "\n📋 %s\n%s\n", plan.Title, indent(plan.Body))
	number, _ := confirm.Data["dialed_number"].(string)
	details, _ := confirm.Data["details"].(string)
	for _, f := range editable(plan) {
		switch f.Key {
		case "dialed_number":
			if number == "" {
				number = f.Value
			}
		case "details":
			if details == "" {
				details = f.Value
			}
		}
	}
	rec.SessionID, _ = confirm.Data["session_id"].(string)
	rec.Number, rec.Details = number, details
	fmt.Fprintf(stdout, "   number: %s\n   details:\n%s\n", orDash(number), indent(details))
	if r.opts.dial != "" {
		number = r.opts.dial
	} else if sc.Number != "" && number != sc.Number {
		return fmt.Errorf("the plan's number is %q, want %q", number, sc.Number)
	}
	if number == "" {
		return errors.New("the plan has no number to call")
	}

	today := time.Now()
	persona := strings.ReplaceAll(sc.Callee, "{today}", today.Format("Monday, January 2, 2006"))
	st := cl.expect(rec.SessionID, persona, stdout)
	defer cl.forget(rec.SessionID)
	confirm.Data["dialed_number"], confirm.Data["details"] = number, details
	confirmAt := time.Now().UTC().Add(-time.Second)
	if err := api.tap(ctx, env["SBX_CC"], confirm, plan.Metadata["household_id"]); err != nil {
		return fmt.Errorf("Call now: %w", err)
	}
	fmt.Fprintf(stdout, "\n☎  Call now → %s\n", number)
	select { // the call has read the saved details once it speaks
	case <-st.started:
	case <-time.After(30 * time.Second):
	case <-ctx.Done():
	}
	unlock()
	watch, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	note := func(who, text string) { cl.mu.Lock(); st.add(who, text); cl.mu.Unlock() }
	go api.answerQuestions(watch, env, confirmAt, rec.SessionID, sc.Answers, time.Duration(sc.AnswerAfter*float64(time.Second)), cl.you, note, stdout)
	resultWithin := 3 * time.Minute
	ended := true
	if r.opts.sip {
		resultWithin = 15 * time.Minute // a person on a real line: the result card says when it's over
	} else {
		select {
		case <-st.ended:
		case <-time.After(12 * time.Minute):
			ended = false
		case <-ctx.Done():
			return harnessError{ctx.Err()}
		}
	}
	var result inboxItem
	if ended {
		result, err = api.waitItem(ctx, env["SBX_NOTIFICATIONS"], confirmAt, "Call finished", resultWithin, rec.SessionID)
	}
	stopWatch()
	rec.Lines, rec.CalleeErrors = cl.snapshot(st)
	rec.Card = result
	rec.Achieved = strings.Contains(result.Title, "✅")
	if s, err := readSession(r.opts.dbPath, rec.SessionID); err == nil {
		rec.State, rec.Transcript, rec.Outcome = s.state, s.turns, s.outcome
	} else {
		fmt.Fprintf(stdout, "   (transcript: %v)\n", err)
	}
	if !ended {
		rec.Fails = append(rec.Fails, "the call did not end within 12 minutes")
	} else if err != nil {
		rec.Fails = append(rec.Fails, "no result card: "+err.Error())
	}
	fmt.Fprintf(stdout, "\n📬 %s\n%s\n\n", result.Title, indent(result.Body))
	if cl.you != nil {
		return nil // a person played the business: the scenario's expectations are theirs to judge
	}
	rec.Started, rec.Ended, rec.Contact = today, ended, sc.Contact
	rec.Checks = runChecks(sc.Expect, rec.facts(), r.opts.maxTurn)
	if r.opts.judgeModel != "" && ended {
		rec.Judge = judgeCall(ctx, r.opts.judgeTap, r.opts.judgeModel, rec)
		if rec.Judge.Error == "" {
			fmt.Fprintf(stdout, "   ⚖ %d/5 role=%v: %s\n", rec.Judge.Overall, rec.Judge.RoleConsistent, clip(rec.Judge.Summary, 200))
		}
	}
	return nil
}

// CallRecord is one call as pipeline call --out writes it: everything call-report reads.
type CallRecord struct {
	Name         string              `json:"name"`
	Rep          int                 `json:"rep"`
	Variant      *Variant            `json:"variant,omitempty"`
	Request      string              `json:"request"`
	CallContext  []map[string]string `json:"call_context,omitempty"`
	Expect       CallExpect          `json:"expect"`
	Plan         string              `json:"plan,omitempty"`
	SessionID    string              `json:"session_id,omitempty"`
	Number       string              `json:"number,omitempty"`
	Details      string              `json:"details,omitempty"`
	Lines        []callLine          `json:"lines,omitempty"`
	Transcript   []transcriptTurn    `json:"transcript,omitempty"` // the session's transcript_json
	Outcome      json.RawMessage     `json:"outcome,omitempty"`
	State        string              `json:"state,omitempty"`
	Card         inboxItem           `json:"card"`
	Achieved     bool                `json:"achieved"`
	Fails        []string            `json:"fails,omitempty"` // the call failed before its checks
	Checks       []CheckResult       `json:"checks,omitempty"`
	Judge        *Judgement          `json:"judge,omitempty"`
	HarnessError string              `json:"harness_error,omitempty"`
	CalleeErrors int                 `json:"callee_errors,omitempty"`
	Attempts     int                 `json:"attempts"`
	Seconds      float64             `json:"seconds"`
	Voice        bool                `json:"voice_line,omitempty"`
	Started      time.Time           `json:"started"`
	Ended        bool                `json:"ended"`
	Contact      map[string]string   `json:"contact,omitempty"`
	file         string
}

// facts are what the checks read, from the record alone (call-report --recheck re-runs them).
func (c *CallRecord) facts() callFacts {
	contact := c.Contact
	if contact == nil {
		contact = map[string]string{"number": c.Number}
	}
	return callFacts{Request: c.Request, Details: c.Details, Context: c.CallContext, Contact: contact, Lines: c.Lines,
		Card: c.Card, Turns: c.Transcript, Ended: c.Ended, State: c.State, Today: c.Started}
}

func (c *CallRecord) failures() []string {
	out := slices.Clone(c.Fails)
	if c.HarnessError != "" {
		out = append(out, "harness: "+c.HarnessError)
	}
	for _, ch := range c.Checks {
		if !ch.Pass {
			out = append(out, ch.Name+": "+ch.Detail)
		}
	}
	return out
}

func (c *CallRecord) passed() bool { return len(c.failures()) == 0 }

type sessionRow struct {
	state   string
	turns   []transcriptTurn
	outcome json.RawMessage
}

// newestSession is the newest call session created since then (to contact, when given), or "".
// Plans are drafted by a queued job, so a request from an earlier, interrupted run can still
// create its session now: the contact tells them apart.
func newestSession(path string, since time.Time, contact string) string {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return ""
	}
	defer db.Close()
	var id string
	_ = db.QueryRow(`SELECT id FROM cc_phone_call_sessions WHERE created_at >= ? AND (? = '' OR contact_name = ?)
		ORDER BY created_at DESC LIMIT 1`, since.UTC().Format("2006-01-02T15:04:05.000Z"), contact, contact).Scan(&id)
	return id
}

// readSession reads the call's session from the sandbox's database (read-only): its state,
// transcript_json (with each turn's timings) and outcome_json.
func readSession(path, id string) (sessionRow, error) {
	var s sessionRow
	if id == "" {
		return s, errors.New("no session id")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return s, err
	}
	defer db.Close()
	var tj, oj sql.NullString
	if err := db.QueryRow(`SELECT state, transcript_json, outcome_json FROM cc_phone_call_sessions WHERE id = ?`, id).Scan(&s.state, &tj, &oj); err != nil {
		return s, err
	}
	if tj.Valid && tj.String != "" {
		if err := json.Unmarshal([]byte(tj.String), &s.turns); err != nil {
			return s, fmt.Errorf("transcript_json: %w", err)
		}
	}
	if oj.Valid && json.Valid([]byte(oj.String)) {
		s.outcome = json.RawMessage(oj.String)
	}
	return s, nil
}

// callee plays the far end of every call on the line, told apart by session.
type callee struct {
	tap, model string
	out        io.Writer
	you        *human // --you: a person types the business's lines
	voice      bool   // the line is in voice mode: the persona hears what the line heard

	mu    sync.Mutex
	calls map[string]*callState
}

// callState is one call's far end.
type callState struct {
	persona  string
	out      io.Writer
	start    time.Time
	messages []map[string]string
	lines    []callLine
	ended    chan struct{}
	started  chan struct{} // closed on the call's first turn
	hold     int           // >0: on hold (the persona said [HOLD]); counts turns spent there
	errs     int
}

func (s *callState) add(who, text string) {
	s.lines = append(s.lines, callLine{Who: who, Text: text, AtMS: time.Since(s.start).Milliseconds()})
}

func (c *callee) snapshot(s *callState) ([]callLine, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(s.lines), s.errs
}

// expect readies the callee for a call's session.
func (c *callee) expect(session, persona string, out io.Writer) *callState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls == nil {
		c.calls = map[string]*callState{}
	}
	st := &callState{persona: persona, out: out, start: time.Now(), ended: make(chan struct{}), started: make(chan struct{})}
	c.calls[session] = st
	return st
}

func (c *callee) forget(session string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.calls, session)
}

// lineTurn is simline.Turn plus what the line heard of Jarvis in voice mode.
// shortcut: Heard is the voice line's field as planned in flight; rename it if the line ships it differently.
type lineTurn struct {
	simline.Turn
	Heard []string `json:"heard,omitempty"`
}

func (c *callee) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var t lineTurn
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	st := c.calls[t.SessionID]
	if st == nil && len(c.calls) == 1 && t.SessionID == "" {
		for _, s := range c.calls {
			st = s
		}
	}
	if st == nil {
		c.mu.Unlock()
		fmt.Fprintf(c.out, "   (a turn for unknown call %q)\n", t.SessionID)
		_ = json.NewEncoder(w).Encode(simline.Reply{})
		return
	}
	select {
	case <-st.started:
	default:
		close(st.started)
	}
	said := strings.Join(t.Said, " ")
	if said != "" {
		fmt.Fprintf(st.out, "   🤖 %s\n", said)
		st.add("jarvis", said)
	}
	heard := said
	if c.voice && len(t.Heard) > 0 {
		heard = strings.Join(t.Heard, " ")
	}
	switch {
	case heard != "":
		st.messages = append(st.messages, map[string]string{"role": "user", "content": heard})
	case t.Event == "silence":
		st.add("event", "(silence)")
		st.messages = append(st.messages, map[string]string{"role": "user", "content": "(silence on the line)"})
	}
	if t.Event == "ended" {
		c.mu.Unlock()
		fmt.Fprintln(st.out, "   ── call ended")
		select {
		case <-st.ended:
		default:
			close(st.ended)
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	// On hold the business says nothing until the line goes quiet or the caller has spoken
	// twice into the silence; then it comes back.
	if st.hold > 0 {
		if t.Event != "silence" && st.hold < 3 {
			st.hold++
			c.mu.Unlock()
			_ = json.NewEncoder(w).Encode(simline.Reply{})
			return
		}
		st.hold = 0
		st.messages = append(st.messages, map[string]string{"role": "user", "content": "(you are back on the line after the hold)"})
	}
	msgs := append([]map[string]string{{"role": "system", "content": calleeRules + st.persona}}, st.messages...)
	c.mu.Unlock()
	var reply string
	var err error
	if c.you != nil {
		fmt.Fprint(st.out, "   🧑 › ")
		select {
		case reply = <-c.you.business:
			if strings.HasSuffix(reply, "/hangup") {
				reply = strings.TrimSuffix(reply, "/hangup") + " [HANGUP]"
			}
		case <-r.Context().Done():
			return
		}
	} else {
		reply, err = c.chat(r.Context(), msgs)
	}
	c.mu.Lock()
	if err != nil {
		st.errs++
		c.mu.Unlock()
		fmt.Fprintf(st.out, "   (callee model failed: %v)\n", err)
		_ = json.NewEncoder(w).Encode(simline.Reply{})
		return
	}
	hangup := strings.Contains(reply, "[HANGUP]")
	hold := strings.Contains(reply, "[HOLD]")
	reply = strings.TrimSpace(strings.NewReplacer("[HANGUP]", "", "[HOLD]", "").Replace(reply))
	st.messages = append(st.messages, map[string]string{"role": "assistant", "content": reply})
	replies := 0
	for _, m := range st.messages {
		if m["role"] == "assistant" {
			replies++
		}
	}
	// The persona model sometimes ends the call on its greeting or mid-question; a person doesn't.
	if hangup && c.you == nil && (replies < 2 || strings.Contains(reply, "?")) {
		hangup = false
	}
	if hold && !hangup {
		st.hold = 1
	}
	if reply != "" {
		st.add("host", reply)
	}
	c.mu.Unlock()
	if reply != "" && c.you == nil {
		fmt.Fprintf(st.out, "   🧑 %s\n", reply)
	}
	if hold && !hangup {
		fmt.Fprintln(st.out, "   (puts the line on hold)")
	}
	if hangup {
		fmt.Fprintln(st.out, "   (hangs up)")
	}
	_ = json.NewEncoder(w).Encode(simline.Reply{Say: reply, Hangup: hangup})
}

func (c *callee) chat(ctx context.Context, msgs []map[string]string) (string, error) {
	return chatComplete(ctx, c.tap, c.model, msgs, 200, 0.7, 0, nil)
}

// chatComplete is one OpenAI-style chat call through a tap (or any compatible base URL); seed
// 0 sends none. extra adds fields (jarvisd's API takes reasoning_budget: 0 for no thinking).
func chatComplete(ctx context.Context, base, model string, msgs []map[string]string, maxTokens int, temp float64, seed uint64, extra map[string]any) (string, error) {
	req := map[string]any{"model": model, "messages": msgs, "max_tokens": maxTokens, "temperature": temp}
	if seed != 0 {
		req["seed"] = seed
	}
	maps.Copy(req, extra)
	body, _ := json.Marshal(req)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return "", fmt.Errorf("%s: %s", resp.Status, b)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", errors.New("no choices")
	}
	return out.Choices[0].Message.Content, nil
}

// userAPI calls the sandbox as the primary (the seeded superuser's token). A long run outlives
// the token, so a 401 logs in again (authURL, email, password) and retries once.
type userAPI struct {
	token                    string
	authURL, email, password string
}

func (a *userAPI) relogin() error {
	b, _ := json.Marshal(map[string]string{"email": a.email, "password": a.password})
	resp, err := http.Post(a.authURL+"/auth/login", "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || out.AccessToken == "" {
		return fmt.Errorf("login: %s", resp.Status)
	}
	a.token = out.AccessToken
	return nil
}

type inboxItem struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Summary   string         `json:"summary"`
	Body      string         `json:"body"`
	CreatedAt string         `json:"created_at"`
	Metadata  map[string]any `json:"metadata"`
}

type cardElement struct {
	Command  string         `json:"command"`
	Callback string         `json:"callback"`
	Data     map[string]any `json:"data"`
}

type cardField struct {
	Key   string `json:"data_key"`
	Value string `json:"initial"`
}

func (a *userAPI) do(req *http.Request, out any) error {
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized && a.email != "" && a.relogin() == nil {
		resp.Body.Close()
		retry := req.Clone(req.Context())
		if req.GetBody != nil {
			if retry.Body, err = req.GetBody(); err != nil {
				return err
			}
		}
		retry.Header.Set("Authorization", "Bearer "+a.token)
		if resp, err = http.DefaultClient.Do(retry); err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s %s: %s: %s", req.Method, req.URL.Path, resp.Status, b)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (a *userAPI) put(url string, body any) error { return a.send(http.MethodPut, url, body, nil) }

func (a *userAPI) send(method, url string, body, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(method, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return a.do(req, out)
}

// tap presses a card's button as the app does (a server callback job) and waits for its result:
// an error is the refusal the app would show (the daily limit, an expired plan).
func (a *userAPI) tap(ctx context.Context, cc string, el cardElement, hh any) error {
	var job struct {
		ID string `json:"id"`
	}
	if err := a.send(http.MethodPost, cc+"/api/v0/callbacks", map[string]any{"command_name": el.Command,
		"callback_name": el.Callback, "data": el.Data, "household_id": hh}, &job); err != nil {
		return err
	}
	for range 30 {
		var st struct {
			Status       string  `json:"status"`
			ErrorMessage *string `json:"error_message"`
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cc+"/api/v0/callbacks/"+job.ID+"/status", nil)
		if err != nil {
			return err
		}
		if err := a.do(req, &st); err != nil {
			return err
		}
		if st.ErrorMessage != nil && *st.ErrorMessage != "" {
			return errors.New(*st.ErrorMessage)
		}
		if st.Status != "pending" && st.Status != "running" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("the tap got no result in 30s")
}

// answerQuestions plays the caller's phone during a call: each "The call needs your input"
// card of this session gets the next of answers, after delay, tapped as Send answer; past the
// last, questions go unanswered. note records the question and answer on the call's timeline.
func (a *userAPI) answerQuestions(ctx context.Context, env map[string]string, since time.Time, session string, answers []string, delay time.Duration, you *human, note func(who, text string), out io.Writer) {
	seen := map[string]bool{}
	for ctx.Err() == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, env["SBX_NOTIFICATIONS"]+"/api/v0/inbox?limit=20", nil)
		if err != nil {
			return
		}
		var items []inboxItem
		if a.do(req, &items) == nil {
			for _, it := range items {
				if seen[it.ID] || !strings.Contains(it.Title, "needs your input") || created(it).Before(since) || !ofSession(it, session) {
					continue
				}
				seen[it.ID] = true
				n := len(seen)
				fmt.Fprintf(out, "   📱 %s\n", it.Summary)
				note("event", "(Jarvis asks the caller) "+it.Summary)
				if you != nil {
					fmt.Fprintln(out, "   📱 (answer with > …)")
					select {
					case ans := <-you.answers:
						answers = append(answers[:min(n-1, len(answers))], ans)
					case <-ctx.Done():
						return
					}
				}
				if n > len(answers) {
					fmt.Fprintln(out, "   📱 (no answer)")
					continue
				}
				el, ok := element(it, "escalation_answer")
				if !ok {
					continue
				}
				if delay > 0 {
					select {
					case <-time.After(delay):
					case <-ctx.Done():
						return
					}
				}
				el.Data["answer"] = answers[n-1]
				fmt.Fprintf(out, "   📱 you answer: %s\n", answers[n-1])
				note("caller", answers[n-1])
				if err := a.tap(ctx, env["SBX_CC"], el, it.Metadata["household_id"]); err != nil {
					fmt.Fprintf(out, "   📱 answer failed: %v\n", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// ofSession: the card is about this call (any card, when session is "").
func ofSession(it inboxItem, session string) bool {
	return session == "" || it.Metadata["session_id"] == session
}

func created(it inboxItem) time.Time {
	at, err := time.Parse(time.RFC3339Nano, it.CreatedAt)
	if err != nil { // the inbox's timestamps are UTC without a zone
		at, _ = time.Parse("2006-01-02T15:04:05.999999999", it.CreatedAt)
	}
	return at
}

// waitItem waits for the newest inbox item created after since whose title contains title
// (one of "a|b"), of the session when one is given.
func (a *userAPI) waitItem(ctx context.Context, base string, since time.Time, title string, within time.Duration, session string) (inboxItem, error) {
	deadline := time.Now().Add(within)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v0/inbox?limit=20", nil)
		if err != nil {
			return inboxItem{}, err
		}
		var items []inboxItem
		if err := a.do(req, &items); err != nil {
			return inboxItem{}, err
		}
		for _, it := range items {
			if created(it).After(since) && ofSession(it, session) && slices.ContainsFunc(strings.Split(title, "|"), func(t string) bool { return strings.Contains(it.Title, t) }) {
				return it, nil
			}
		}
		if time.Now().After(deadline) {
			return inboxItem{}, fmt.Errorf("no %q card within %s", title, within)
		}
		select {
		case <-ctx.Done():
			return inboxItem{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func element(it inboxItem, callback string) (cardElement, bool) {
	var els []cardElement
	b, _ := json.Marshal(it.Metadata["interactive_elements"])
	_ = json.Unmarshal(b, &els)
	for _, e := range els {
		if e.Callback == callback {
			if e.Data == nil {
				e.Data = map[string]any{}
			}
			return e, true
		}
	}
	return cardElement{}, false
}

func editable(it inboxItem) []cardField {
	var fs []cardField
	b, _ := json.Marshal(it.Metadata["editable_fields"])
	_ = json.Unmarshal(b, &fs)
	return fs
}

func indent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "   —"
	}
	return "   " + strings.ReplaceAll(s, "\n", "\n   ")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// human is a person at the terminal: lines starting with ">" answer Jarvis's questions to the
// caller, every other line is the business speaking (over SIP the business speaks on the
// softphone, so every line is an answer).
type human struct{ business, answers chan string }

func readHuman(r io.Reader, answersOnly bool) *human {
	h := &human{business: make(chan string, 8), answers: make(chan string, 8)}
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			switch {
			case line == "":
			case answersOnly || strings.HasPrefix(line, ">"):
				h.answers <- strings.TrimSpace(strings.TrimPrefix(line, ">"))
			default:
				h.business <- line
			}
		}
	}()
	return h
}
