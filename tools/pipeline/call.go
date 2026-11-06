package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/simline"
)

// A call scenario is a phone call end to end against a sandbox up with --phone-sim: the request
// said to Jarvis, the plan it makes (number lookup included), the "Call now" tap, the call
// itself against a callee the model plays from Callee, and the result card. Files are JSON
// (calls/*.json), one scenario or an array.
type CallScenario struct {
	Name    string `json:"name"`
	Request string `json:"request"`
	// Callee is who answers: a persona the model plays, with what they know and what they do.
	Callee string `json:"callee"`
	// Number, when set, is the number the plan must have found.
	Number string `json:"number,omitempty"`
	// CallContext is the caller's saved call details (phone_calls.call_context entries: key,
	// value, and optionally label, category, tier); none clears them.
	CallContext []map[string]string `json:"call_context,omitempty"`
	// Answers are what the caller answers Jarvis's mid-call questions with ("The call needs your
	// input"), in order; a question past the last goes unanswered.
	Answers []string `json:"answers,omitempty"`
	// Expect is what must hold for the scenario to pass.
	Expect CallExpect `json:"expect"`
	// Contact, when set, is put in the household's phonebook first (a business the web
	// lookup can't find): {"name": ..., "number": ...}.
	Contact map[string]string `json:"contact,omitempty"`
	// NodeContext, when set, is what the household's commands share with the plan, answered
	// by a stand-in node (callnode.go).
	NodeContext *NodeContext `json:"node_context,omitempty"`
}

// CallExpect is a call scenario's checks. Text checks ignore case; Says and Never also match
// with spaces, dashes and commas removed, so a number Jarvis reads digit by digit still counts.
type CallExpect struct {
	// Achieved is the result card's verdict (✅ true, ⚠️ false); nil accepts either.
	Achieved *bool `json:"achieved,omitempty"`
	// Card is text the result card must contain.
	Card []string `json:"card,omitempty"`
	// Says is text Jarvis must say during the call; Never is text it must not.
	Says  []string `json:"says,omitempty"`
	Never []string `json:"never,omitempty"`
}

// check reports what failed: the result card and everything Jarvis said.
func (e CallExpect) check(card inboxItem, said string) []string {
	var fails []string
	achieved := strings.Contains(card.Title, "✅")
	if e.Achieved != nil && achieved != *e.Achieved {
		fails = append(fails, fmt.Sprintf("card verdict %v, want %v", achieved, *e.Achieved))
	}
	for _, t := range e.Card {
		if !contains(card.Title+" "+card.Summary+" "+card.Body, t) {
			fails = append(fails, fmt.Sprintf("card lacks %q", t))
		}
	}
	for _, t := range e.Says {
		if !contains(said, t) {
			fails = append(fails, fmt.Sprintf("never said %q", t))
		}
	}
	for _, t := range e.Never {
		if contains(said, t) {
			fails = append(fails, fmt.Sprintf("said %q", t))
		}
	}
	return fails
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

func runCalls(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("call", flag.ContinueOnError)
	sandbox := fs.String("sandbox", "", "sandbox name (scripts/sandbox up --phone-sim)")
	model := fs.String("model", "live", "model label the callee is played by (through the sandbox's tap)")
	only := fs.String("only", "", "run only these scenarios (comma-separated names)")
	repeat := fs.Int("repeat", 1, "run each scenario this many times and report its pass rate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("call: name scenario files (tools/pipeline/calls/*.json)")
	}
	env, _, _, err := sandboxEnv(*sandbox)
	if err != nil {
		return err
	}
	if env["SBX_PHONE_SIM"] == "" {
		return errors.New("call: this sandbox has no simulated phone line (scripts/sandbox up --phone-sim)")
	}
	d, err := sandboxDriver(*sandbox)
	if err != nil {
		return err
	}
	var scs []CallScenario
	for _, p := range fs.Args() {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var many []CallScenario
		if err := json.Unmarshal(b, &many); err != nil {
			var one CallScenario
			if err := json.Unmarshal(b, &one); err != nil {
				return fmt.Errorf("%s: %w", filepath.Base(p), err)
			}
			many = []CallScenario{one}
		}
		for _, sc := range many {
			if *only == "" || slices.Contains(strings.Split(*only, ","), sc.Name) {
				scs = append(scs, sc)
			}
		}
	}
	cl := &callee{tap: env["SBX_LLM_TAP"], model: *model, out: stdout}
	srv := &http.Server{Addr: env["SBX_PHONE_SIM"], Handler: cl, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	defer srv.Close()
	select {
	case err := <-errc:
		return fmt.Errorf("call: callee server: %w", err)
	case <-time.After(200 * time.Millisecond):
	}
	failed := 0
	passes := make([]int, len(scs))
	for i, sc := range scs {
		for n := 0; n < *repeat; n++ {
			if err := runCall(ctx, d, env, cl, sc, stdout); err != nil {
				failed++
				fmt.Fprintf(stdout, "  ✗ %s: %v\n\n", sc.Name, err)
				continue
			}
			passes[i]++
			fmt.Fprintf(stdout, "  ✓ %s\n\n", sc.Name)
		}
	}
	fmt.Fprintln(stdout, "── pass rate")
	for i, sc := range scs {
		fmt.Fprintf(stdout, "  %d/%d  %s\n", passes[i], *repeat, sc.Name)
	}
	if failed > 0 {
		return errFailed{failed}
	}
	return nil
}

func runCall(ctx context.Context, d *Driver, env map[string]string, cl *callee, sc CallScenario, stdout io.Writer) error {
	fmt.Fprintf(stdout, "══ %s\n", sc.Name)
	api := &userAPI{token: env["SBX_TOKEN"], authURL: env["SBX_AUTH"], email: env["SBX_EMAIL"], password: env["SBX_PASSWORD"]}
	ctxJSON, _ := json.Marshal(sc.CallContext)
	if sc.CallContext == nil {
		ctxJSON = []byte("[]")
	}
	if err := api.put(env["SBX_CC"]+"/settings/phone_calls.call_context?user_id="+env["SBX_USER_ID"],
		map[string]any{"value": string(ctxJSON)}); err != nil {
		return fmt.Errorf("call context: %w", err)
	}
	if sc.Contact != nil {
		var hhs struct {
			Households []struct {
				ID string `json:"id"`
			} `json:"households"`
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, env["SBX_CC"]+"/api/v0/nodes/pairings", nil)
		if err := api.do(req, &hhs); err != nil || len(hhs.Households) == 0 {
			return fmt.Errorf("household: %v", err)
		}
		// A second add of the same contact is refused; the first is what counts.
		_ = api.send(http.MethodPost, env["SBX_CC"]+"/api/v0/mobile/household/"+hhs.Households[0].ID+"/phone-contacts",
			map[string]any{"name": sc.Contact["name"], "number": sc.Contact["number"]}, nil)
	}
	if sc.NodeContext != nil {
		stop, err := contextNode(ctx, env, *sc.NodeContext)
		if err != nil {
			return fmt.Errorf("stand-in node: %w", err)
		}
		defer stop()
	}
	since := time.Now().UTC().Add(-2 * time.Second)

	log := d.Run(ctx, Scenario{Name: sc.Name, Speaker: "primary", Turns: []Turn{{Say: sc.Request}}})
	printLog(stdout, log, false)

	plan, err := api.waitItem(ctx, env["SBX_NOTIFICATIONS"], since, "Call plan|Call not started", 90*time.Second)
	if err != nil {
		return fmt.Errorf("no call plan: %w", err)
	}
	if strings.Contains(plan.Title, "not started") {
		return fmt.Errorf("%s: %s", plan.Title, plan.Summary)
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
	fmt.Fprintf(stdout, "   number: %s\n   details:\n%s\n", orDash(number), indent(details))
	if sc.Number != "" && number != sc.Number {
		return fmt.Errorf("the plan's number is %q, want %q", number, sc.Number)
	}
	if number == "" {
		return errors.New("the plan has no number to call")
	}

	ended := cl.expect(sc.Callee)
	confirm.Data["dialed_number"], confirm.Data["details"] = number, details
	confirmAt := time.Now().UTC().Add(-time.Second)
	if err := api.tap(ctx, env["SBX_CC"], confirm, plan.Metadata["household_id"]); err != nil {
		return fmt.Errorf("Call now: %w", err)
	}
	fmt.Fprintf(stdout, "\n☎  Call now → %s\n", number)
	watch, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go api.answerQuestions(watch, env, confirmAt, sc.Answers, stdout)
	select {
	case <-ended:
	case <-time.After(12 * time.Minute):
		return errors.New("the call did not end within 12 minutes")
	case <-ctx.Done():
		return ctx.Err()
	}
	result, err := api.waitItem(ctx, env["SBX_NOTIFICATIONS"], confirmAt, "Call finished", 3*time.Minute)
	if err != nil {
		return fmt.Errorf("no result card: %w", err)
	}
	fmt.Fprintf(stdout, "\n📬 %s\n%s\n\n", result.Title, indent(result.Body))
	if fails := sc.Expect.check(result, cl.saidSoFar()); len(fails) > 0 {
		return errors.New(strings.Join(fails, "; "))
	}
	return nil
}

// callee plays the far end of the current call: the simline driver.
type callee struct {
	tap, model string
	out        io.Writer

	mu       sync.Mutex
	persona  string
	messages []map[string]string
	said     []string // everything Jarvis said on this call
	ended    chan struct{}
}

func (c *callee) saidSoFar() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.said, " ")
}

// expect readies the callee for the next call and returns a channel closed when it ends.
func (c *callee) expect(persona string) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.persona = persona
	c.messages = nil
	c.said = nil
	c.ended = make(chan struct{})
	return c.ended
}

func (c *callee) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var t simline.Turn
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	said := strings.Join(t.Said, " ")
	if said != "" {
		fmt.Fprintf(c.out, "   🤖 %s\n", said)
	}
	c.mu.Lock()
	c.said = append(c.said, t.Said...)
	ended := c.ended
	persona := c.persona
	switch {
	case said != "":
		c.messages = append(c.messages, map[string]string{"role": "user", "content": said})
	case t.Event == "silence":
		c.messages = append(c.messages, map[string]string{"role": "user", "content": "(silence on the line)"})
	}
	msgs := append([]map[string]string{{"role": "system", "content": calleeRules + persona}}, c.messages...)
	c.mu.Unlock()
	if t.Event == "ended" {
		fmt.Fprintln(c.out, "   ── call ended")
		if ended != nil {
			select {
			case <-ended:
			default:
				close(ended)
			}
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	reply, err := c.chat(r.Context(), msgs)
	if err != nil {
		fmt.Fprintf(c.out, "   (callee model failed: %v)\n", err)
		_ = json.NewEncoder(w).Encode(simline.Reply{})
		return
	}
	hangup := strings.Contains(reply, "[HANGUP]")
	reply = strings.TrimSpace(strings.ReplaceAll(reply, "[HANGUP]", ""))
	c.mu.Lock()
	c.messages = append(c.messages, map[string]string{"role": "assistant", "content": reply})
	replies := 0
	for _, m := range c.messages {
		if m["role"] == "assistant" {
			replies++
		}
	}
	c.mu.Unlock()
	// The persona model sometimes ends the call on its greeting or mid-question; a person doesn't.
	if hangup && (replies < 2 || strings.HasSuffix(reply, "?")) {
		hangup = false
	}
	if reply != "" {
		fmt.Fprintf(c.out, "   🧑 %s\n", reply)
	}
	if hangup {
		fmt.Fprintln(c.out, "   (hangs up)")
	}
	_ = json.NewEncoder(w).Encode(simline.Reply{Say: reply, Hangup: hangup})
}

func (c *callee) chat(ctx context.Context, msgs []map[string]string) (string, error) {
	body, _ := json.Marshal(map[string]any{"model": c.model, "messages": msgs, "max_tokens": 200, "temperature": 0.7})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tap+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
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
// card gets the next of answers, tapped as Send answer; past the last, questions go unanswered.
func (a *userAPI) answerQuestions(ctx context.Context, env map[string]string, since time.Time, answers []string, out io.Writer) {
	seen := map[string]bool{}
	for ctx.Err() == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, env["SBX_NOTIFICATIONS"]+"/api/v0/inbox?limit=20", nil)
		if err != nil {
			return
		}
		var items []inboxItem
		if a.do(req, &items) == nil {
			for _, it := range items {
				if seen[it.ID] || !strings.Contains(it.Title, "needs your input") || created(it).Before(since) {
					continue
				}
				seen[it.ID] = true
				n := len(seen)
				fmt.Fprintf(out, "   📱 %s\n", it.Summary)
				if n > len(answers) {
					fmt.Fprintln(out, "   📱 (no answer)")
					continue
				}
				el, ok := element(it, "escalation_answer")
				if !ok {
					continue
				}
				el.Data["answer"] = answers[n-1]
				fmt.Fprintf(out, "   📱 you answer: %s\n", answers[n-1])
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

func created(it inboxItem) time.Time {
	at, err := time.Parse(time.RFC3339Nano, it.CreatedAt)
	if err != nil { // the inbox's timestamps are UTC without a zone
		at, _ = time.Parse("2006-01-02T15:04:05.999999999", it.CreatedAt)
	}
	return at
}

// waitItem waits for the newest inbox item created after since whose title contains title.
func (a *userAPI) waitItem(ctx context.Context, base string, since time.Time, title string, within time.Duration) (inboxItem, error) {
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
			if created(it).After(since) && slices.ContainsFunc(strings.Split(title, "|"), func(t string) bool { return strings.Contains(it.Title, t) }) {
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
