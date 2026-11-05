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
}

// calleeRules frame every persona: they are a person on a phone line, saying lines aloud.
const calleeRules = `You are playing a person who just answered a phone call at work, for a test of a phone assistant.
Stay in character. Reply with only the words you say aloud on the phone: one or two short spoken sentences, no stage directions, no lists, no markdown.
When the conversation is over (you have said goodbye, or you want to end the call), end your reply with [HANGUP].
If the line is silent, react as a person would ("Hello? Are you still there?").

Your character:
`

func runCalls(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("call", flag.ContinueOnError)
	sandbox := fs.String("sandbox", "", "sandbox name (scripts/sandbox up --phone-sim)")
	model := fs.String("model", "live", "model label the callee is played by (through the sandbox's tap)")
	only := fs.String("only", "", "run only the scenario with this name")
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
			if *only == "" || sc.Name == *only {
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
	for _, sc := range scs {
		if err := runCall(ctx, d, env, cl, sc, stdout); err != nil {
			failed++
			fmt.Fprintf(stdout, "  ✗ %s: %v\n\n", sc.Name, err)
		}
	}
	if failed > 0 {
		return errFailed{failed}
	}
	return nil
}

func runCall(ctx context.Context, d *Driver, env map[string]string, cl *callee, sc CallScenario, stdout io.Writer) error {
	fmt.Fprintf(stdout, "══ %s\n", sc.Name)
	api := &userAPI{token: env["SBX_TOKEN"]}
	since := time.Now().UTC().Add(-2 * time.Second)

	log := d.Run(ctx, Scenario{Name: sc.Name, Speaker: "primary", Turns: []Turn{{Say: sc.Request}}})
	printLog(stdout, log, false)

	plan, err := api.waitItem(ctx, env["SBX_NOTIFICATIONS"], since, "Call plan", 90*time.Second)
	if err != nil {
		return fmt.Errorf("no call plan: %w", err)
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
	if err := api.post(env["SBX_CC"]+"/api/v0/callbacks", map[string]any{
		"command_name": confirm.Command, "callback_name": confirm.Callback, "data": confirm.Data,
		"household_id": plan.Metadata["household_id"],
	}); err != nil {
		return fmt.Errorf("Call now: %w", err)
	}
	fmt.Fprintf(stdout, "\n☎  Call now → %s\n", number)
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
	return nil
}

// callee plays the far end of the current call: the simline driver.
type callee struct {
	tap, model string
	out        io.Writer

	mu       sync.Mutex
	persona  string
	messages []map[string]string
	ended    chan struct{}
}

// expect readies the callee for the next call and returns a channel closed when it ends.
func (c *callee) expect(persona string) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.persona = persona
	c.messages = nil
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
	c.mu.Unlock()
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

// userAPI calls the sandbox as the primary (the seeded superuser's token).
type userAPI struct{ token string }

type inboxItem struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
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

func (a *userAPI) post(url string, body any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return a.do(req, nil)
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
			at, err := time.Parse(time.RFC3339Nano, it.CreatedAt)
			if err != nil { // the inbox's timestamps are UTC without a zone
				at, _ = time.Parse("2006-01-02T15:04:05.999999999", it.CreatedAt)
			}
			if at.After(since) && strings.Contains(it.Title, title) {
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
