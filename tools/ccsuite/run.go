package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/tools/ccsites/notesapp"
)

// runCmd is `ccsuite run`: every selected case against the sandbox's model, scored from the
// fixtures' state (docs/dev/computer-suite.md).
func runCmd(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("ccsuite run", flag.ContinueOnError)
	root := fs.String("root", ".", "the jarvis-server checkout")
	casesDir := fs.String("cases", "tools/ccsuite/cases", "the case files' directory (relative to --root)")
	filter := fs.String("filter", "", "only cases whose name matches this regexp")
	repeat := fs.Int("repeat", 1, "runs of each case")
	llm := fs.String("llm", "shared", "the sandbox's model: shared (the installed jarvisd's, through the tap) or scripted")
	label := fs.String("label", "", "shared: the installed jarvisd's label to use, live or background (default: as the harness env file says)")
	includePending := fs.Bool("include-pending", false, "run pending cases too (they never fail the run)")
	out := fs.String("out", "", "the run's directory (default .ccsuite/<run>)")
	rel := fs.String("rel", "", "prefix for the paths in results.json (scripts/e2e: the suite's dir in its run)")
	keep := fs.Bool("keep", false, "leave it all running afterwards, a desktop session with the agent and the browser included (ccsuite down --run DIR)")
	chromium := fs.String("chromium", "", "the Chromium binary (default: $CCSUITE_CHROMIUM, else Playwright's newest)")
	settle := fs.Duration("settle", time.Minute, "an errand is over once nothing happened for this long after the chat's answer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	switch {
	case *llm != "shared" && *llm != "scripted":
		return errors.New("--llm: shared or scripted")
	case *label != "" && *label != "live" && *label != "background":
		return errors.New("--label: live or background")
	case *label != "" && *llm != "shared":
		return errors.New("--label goes with --llm shared")
	case *repeat < 1:
		return errors.New("--repeat: at least 1")
	}
	var re *regexp.Regexp
	if *filter != "" {
		var err error
		if re, err = regexp.Compile(*filter); err != nil {
			return fmt.Errorf("--filter: %w", err)
		}
	}
	rootAbs, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(rootAbs, "scripts", "sandbox")); err != nil {
		return fmt.Errorf("--root %s is not a jarvis-server checkout", rootAbs)
	}
	cases, err := LoadCases(filepath.Join(rootAbs, *casesDir), *casesDir)
	if err != nil {
		return err
	}

	run := runID()
	dir := *out
	if dir == "" {
		dir = filepath.Join(rootAbs, ".ccsuite", run)
		// Run output is never committed (no .gitignore entry needed outside this tool).
		os.MkdirAll(filepath.Join(rootAbs, ".ccsuite"), 0o755)
		os.WriteFile(filepath.Join(rootAbs, ".ccsuite", ".gitignore"), []byte("*\n"), 0o644)
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	env := &Env{Root: rootAbs, Dir: dir, Name: "ccsuite-" + run, LLM: *llm, Label: *label, Chromium: *chromium, Keep: *keep, Settle: *settle, Log: stdout}
	fmt.Fprintf(stdout, "ccsuite: run %s in %s\n", run, dir)
	start := time.Now()
	rep := &Report{Run: run, LLM: *llm}
	defer env.Down()
	upErr := env.Up(ctx)
	rep.Model, rep.Label = env.Model, env.Label

	for _, c := range cases {
		if re != nil && !re.MatchString(c.Name) {
			continue
		}
		cr := CaseResult{Name: c.Name, Phase: c.Phase, File: c.File, CaseStatus: c.Status}
		switch {
		case skipReason(c, *llm, *includePending, runtime.GOOS) != "":
			cr.Status, cr.SkipReason = "skipped", skipReason(c, *llm, *includePending, runtime.GOOS)
		case upErr != nil:
			cr.RunResults = []RunResult{{Failure: "bring-up: " + upErr.Error()}}
			summarize(&cr)
		default:
			for i := 1; i <= *repeat && ctx.Err() == nil; i++ {
				cr.RunResults = append(cr.RunResults, runOnce(ctx, env, c, i))
			}
			summarize(&cr)
		}
		if cr.Status == "skipped" {
			fmt.Fprintf(stdout, "ccsuite: %-8s %s (%s)\n", "skipped", c.Name, cr.SkipReason)
		} else {
			fmt.Fprintf(stdout, "ccsuite: %-8s %s (%d/%d runs)\n", cr.Status, c.Name, cr.Passed, cr.Runs)
		}
		rep.Cases = append(rep.Cases, cr)
	}
	// Kept for use by hand: a desktop with the agent and the browser even when no case needed one.
	if *keep && upErr == nil && ctx.Err() == nil {
		kind := "wlroots"
		if env.desk != nil {
			kind = env.desk.kind
		}
		if _, err := env.Desktop(ctx, kind, true); err != nil {
			upErr = fmt.Errorf("--keep: %w", err)
		}
	}
	rep.DurationMS = time.Since(start).Milliseconds()
	if err := rep.write(dir, *rel); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "ccsuite: %s\n", filepath.Join(dir, "report.md"))
	if upErr != nil {
		return fmt.Errorf("bring-up: %w", upErr)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if n := rep.failedRequired(); n > 0 {
		return errFailed{n}
	}
	return nil
}

func runID() string {
	b := make([]byte, 3)
	rand.Read(b)
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// skipReason is why c isn't run, or "".
func skipReason(c Case, llm string, includePending bool, goos string) string {
	desk, _ := c.needs()
	switch {
	case c.Status == "pending" && !includePending:
		return "pending"
	case llm == "scripted" && c.Phase != "tests":
		// The plumbing run: the scripted model does no computer tasks.
		return "the scripted model does no computer tasks"
	case desk == "mac" && goos != "darwin":
		return "macOS only: the Mac test host"
	case desk == "mac":
		return "macOS desktop cases are not wired yet (piece 22)"
	}
	return ""
}

// runOnce is one run of a case: reset the fixtures, say the message, answer the cards, wait for
// the errand to be over, then gather the state and evaluate the checks. Its files are in
// cases/<name>/run-<i>/ (state.json, llm.jsonl, failure.png).
func runOnce(ctx context.Context, e *Env, c Case, i int) (r RunResult) {
	r.Dir = filepath.ToSlash(filepath.Join("cases", c.Name, fmt.Sprintf("run-%d", i)))
	dir := filepath.Join(e.Dir, r.Dir)
	os.MkdirAll(dir, 0o755)
	start := time.Now()
	defer func() { r.Seconds = float64(time.Since(start).Milliseconds()) / 1000 }()
	fail := func(f string) RunResult {
		r.Failure = f
		if e.desk != nil && e.Screenshot(ctx, filepath.Join(dir, "failure.png")) == nil {
			r.Screenshot = filepath.ToSlash(filepath.Join(r.Dir, "failure.png"))
		}
		return r
	}
	deadline := time.Now().Add(time.Duration(c.MaxMinutes * float64(time.Minute)))
	cctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	// The fixtures, as the case starts them.
	if err := e.reset(cctx); err != nil {
		return fail("reset: " + err.Error())
	}
	kind, wantBrowser := c.needs()
	node := e.Sbx["SBX_NODE_ID"] // the seeded node, for cases that need no desktop
	if kind != "" {
		if _, err := e.Desktop(cctx, kind, wantBrowser); err != nil {
			return fail("bring-up: " + err.Error())
		}
		node = e.NodeID
	}
	if err := e.Block(cctx, c.Setup.Block); err != nil {
		return fail("block: " + err.Error())
	}
	if err := e.Login(cctx, c.Setup.Login); err != nil {
		return fail("login: " + err.Error())
	}
	if strings.HasPrefix(c.Phase, "desktop-") {
		if err := e.StartNotes(cctx, c.Setup.NotesLayout); err != nil {
			return fail("notes app: " + err.Error())
		}
	}
	seen, err := e.inbox(cctx)
	if err != nil {
		return fail("inbox: " + err.Error())
	}
	known := map[string]bool{}
	for _, it := range seen {
		known[it.ID] = true
	}
	llmLog := e.Sbx["SBX_LLM_LOG"]
	offset := fileSize(llmLog)
	defer func() { r.Rounds = copyLines(llmLog, offset, filepath.Join(dir, "llm.jsonl")) }()

	// Say it, from the seeded user to the agent node, as the app's chat does.
	var (
		mu      sync.Mutex
		replies []string
		chatErr error
	)
	chatDone := make(chan struct{})
	go func() {
		defer close(chatDone)
		texts, err := e.chat(cctx, node, c.Message(e.Sites))
		mu.Lock()
		replies, chatErr = texts, err
		mu.Unlock()
	}()

	// Answer the cards until the errand is over: a completion card, or quiet for --settle after
	// the chat's answer, or max_minutes.
	var cards []Card
	sitesBefore, _ := e.sitesRaw(cctx)
	lastChange, timedOut, finished := time.Now(), false, false
	for !finished {
		select {
		case <-cctx.Done():
			timedOut, finished = ctx.Err() == nil, true
			continue
		case <-time.After(2 * time.Second):
		}
		items, err := e.inbox(cctx)
		if err != nil {
			continue
		}
		for _, it := range items {
			if known[it.ID] {
				continue
			}
			known[it.ID] = true
			lastChange = time.Now()
			card := it.card()
			if a, ok := answerFor(c.Answers, card); ok {
				if el, ok := it.button(a.Tap); ok {
					if err := e.tap(cctx, it, el); err != nil {
						card.Tapped = "failed: " + err.Error()
					} else {
						card.Tapped = el.Label
					}
				}
			}
			cards = append(cards, card)
			if card.Done() {
				finished = true // the completion card
			}
		}
		if now, err := e.sitesRaw(cctx); err == nil && !bytes.Equal(now, sitesBefore) {
			sitesBefore, lastChange = now, time.Now()
		}
		select {
		case <-chatDone:
			if time.Since(lastChange) >= e.settle() {
				finished = true
			}
		default:
			lastChange = time.Now() // still answering
		}
	}
	cancel()
	<-chatDone

	// The state, from the fixtures themselves.
	st, err := e.gather(ctx, cards)
	mu.Lock()
	st.Replies = append(replies, st.Replies...)
	mu.Unlock()
	b, _ := json.MarshalIndent(st, "", "  ")
	os.WriteFile(filepath.Join(dir, "state.json"), b, 0o644)
	if err != nil {
		return fail("reading the state: " + err.Error())
	}
	var why []string
	for _, ch := range c.Checks {
		if f := ch.Eval(&st); f != "" {
			why = append(why, ch.Kind+": "+f)
		}
	}
	if len(why) == 0 {
		r.Passed = true
		return r
	}
	if chatErr != nil && !errors.Is(chatErr, context.Canceled) && !errors.Is(chatErr, context.DeadlineExceeded) {
		why = append(why, "chat: "+chatErr.Error())
	}
	if timedOut {
		why = append(why, fmt.Sprintf("timed out after %v min", c.MaxMinutes))
	}
	return fail(strings.Join(why, "\n"))
}

func (e *Env) settle() time.Duration {
	if e.Settle > 0 {
		return e.Settle
	}
	return time.Minute
}

// answerFor is the case's answer for a card that asks: plan cards (an errand's plan) take a
// "plan" answer, any other card with buttons a "confirm" one.
func answerFor(answers []Answer, c Card) (Answer, bool) {
	if len(c.Buttons) == 0 || c.Progress {
		return Answer{}, false
	}
	kind := "confirm"
	if c.Category == "errand_plan" && !c.Step {
		kind = "plan"
	}
	for _, a := range answers {
		if a.When == kind {
			return a, true
		}
	}
	return Answer{}, false
}

// reset puts the fixtures back: the sites' state (orders, downloads, forms, sessions), the
// fake relay's pushes and the agent's ~/Documents/Jarvis.
func (e *Env) reset(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, "POST", e.Sites+"/control/reset", nil)
	if err := doJSON(req, nil); err != nil {
		return err
	}
	if fr := e.Sbx["SBX_FAKERELAY"]; fr != "" {
		req, _ := http.NewRequestWithContext(ctx, "DELETE", fr+"/control/pushes", nil)
		if err := doJSON(req, nil); err != nil {
			return err
		}
	}
	docs := e.path("agent-home", "Documents", "Jarvis")
	if err := os.RemoveAll(docs); err != nil {
		return err
	}
	return os.MkdirAll(docs, 0o755)
}

func (e *Env) sitesRaw(ctx context.Context) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", e.Sites+"/control/state", nil)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// gather reads the state the checks run on.
func (e *Env) gather(ctx context.Context, cards []Card) (State, error) {
	st := State{Cards: cards}
	for _, c := range cards {
		st.Replies = append(st.Replies, strings.TrimSpace(c.Title+"\n"+c.Body))
	}
	raw, err := e.sitesRaw(ctx)
	if err != nil {
		return st, fmt.Errorf("sites: %w", err)
	}
	if err := json.Unmarshal(raw, &st.Sites); err != nil {
		return st, fmt.Errorf("sites: %w", err)
	}
	if st.Docs, err = readDocs(e.path("agent-home", "Documents", "Jarvis")); err != nil {
		return st, err
	}
	if e.desk != nil && e.desk.notesSt != "" {
		notes, err := notesapp.NotesState(e.desk.notesSt)
		if err != nil && !os.IsNotExist(err) {
			return st, err
		}
		st.Notes = notes
	}
	if fr := e.Sbx["SBX_FAKERELAY"]; fr != "" {
		var p struct {
			Pushes []Push `json:"pushes"`
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", fr+"/control/pushes", nil)
		if err := doJSON(req, &p); err != nil {
			return st, fmt.Errorf("pushes: %w", err)
		}
		st.Pushes = p.Pushes
	}
	return st, nil
}

// chat sends message as the app's chat does (POST /api/v0/mobile/chat, server-sent events) to
// node, and returns the answers it gave.
func (e *Env) chat(ctx context.Context, node, message string) ([]string, error) {
	b, _ := json.Marshal(map[string]any{"message": message, "node_id": node, "household_id": e.Household, "timezone": "UTC"})
	req, err := http.NewRequestWithContext(ctx, "POST", e.Sbx["SBX_CC"]+"/api/v0/mobile/chat", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.Sbx["SBX_TOKEN"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("mobile/chat: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var texts []string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type     string `json:"type"`
			Message  string `json:"message"`
			FullText string `json:"full_text"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "done":
			texts = append(texts, ev.FullText)
		case "error":
			return texts, errors.New(ev.Message)
		}
	}
	return texts, sc.Err()
}

// inboxItem is an inbox card as GET /api/v0/inbox lists it.
type inboxItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Summary  string `json:"summary"`
	Body     string `json:"body"`
	Category string `json:"category"`
	Metadata struct {
		HouseholdID string    `json:"household_id"`
		WorkflowID  string    `json:"workflow_id"`
		CardType    string    `json:"card_type"`
		Elements    []element `json:"interactive_elements"`
	} `json:"metadata"`
}

// element is a card's button (docs/cc/13: the callback a tap runs).
type element struct {
	ID       string         `json:"id"`
	Label    string         `json:"label"`
	Command  string         `json:"command"`
	Callback string         `json:"callback"`
	Target   string         `json:"target"`
	Data     map[string]any `json:"data"`
}

func (e *Env) inbox(ctx context.Context) ([]inboxItem, error) {
	var items []inboxItem
	err := e.api(ctx, "GET", e.Sbx["SBX_NOTIFICATIONS"]+"/api/v0/inbox?limit=200", nil, &items)
	return items, err
}

func (it inboxItem) card() Card {
	c := Card{ID: it.ID, Title: it.Title, Body: firstNonEmpty(it.Body, it.Summary), Category: it.Category,
		Step: it.Metadata.WorkflowID != "", Progress: it.Metadata.CardType == "errand_progress"}
	for _, el := range it.Metadata.Elements {
		c.Buttons = append(c.Buttons, el.Label)
	}
	return c
}

var (
	approveRE = regexp.MustCompile(`(?i)approve|confirm|\brun\b|\byes\b|allow|continue|place|\bbuy\b|go ahead`)
	cancelRE  = regexp.MustCompile(`(?i)cancel|discard|\bstop\b|deny|\bno\b|reject|don'?t`)
)

// button is the card's button for tap (approve, cancel), by its label, callback or id.
func (it inboxItem) button(tap string) (element, bool) {
	want, other := approveRE, cancelRE
	if tap == "cancel" {
		want, other = cancelRE, approveRE
	}
	for _, el := range it.Metadata.Elements {
		s := el.Label + " " + el.Callback + " " + el.ID
		if want.MatchString(s) && !other.MatchString(el.Label) {
			return el, true
		}
	}
	return element{}, false
}

// tap presses a card's button as the phone does (POST /api/v0/callbacks).
func (e *Env) tap(ctx context.Context, it inboxItem, el element) error {
	body := map[string]any{"command_name": el.Command, "callback_name": el.Callback, "data": el.Data,
		"household_id": firstNonEmpty(it.Metadata.HouseholdID, e.Household)}
	if el.Data == nil {
		body["data"] = map[string]any{}
	}
	if el.Target != "" && el.Target != "server" {
		body["target_node_id"] = el.Target
	}
	return e.api(ctx, "POST", e.Sbx["SBX_CC"]+"/api/v0/callbacks", body, nil)
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

// copyLines is the model calls logged after offset (one JSON line each, tools/pipeline tap),
// copied to dst so they outlive the sandbox's purge.
func copyLines(path string, offset int64, dst string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0
	}
	var out bytes.Buffer
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) > 0 {
			out.Write(sc.Bytes())
			out.WriteByte('\n')
			n++
		}
	}
	os.WriteFile(dst, out.Bytes(), 0o644)
	return n
}
