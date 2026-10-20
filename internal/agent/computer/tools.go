package computer

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// Browser is what the executor needs of the browser extension (browser.Tools; BrowserOf).
type Browser interface {
	OpenBackground(ctx context.Context, rawURL string) (tab, window string, err error)
	Snapshot(ctx context.Context, tab string, max int) (computeruse.Snapshot, error)
	Act(ctx context.Context, tab string, gen int, ref, op, value string, keys []string) (browser.ActResult, error)
	Screenshot(ctx context.Context, window string) (screen.Shot, computeruse.Geometry, error)
	CloseTab(ctx context.Context, tab string) error
}

// Approver checks an approval token with jarvisd (node.CC.VerifyApproval): single use, bound to
// the action's digest.
type Approver interface {
	VerifyApproval(ctx context.Context, token, digest string) (bool, error)
}

// BrowserOf is the extension's tools as the executor's Browser.
func BrowserOf(t *browser.Tools) Browser { return extBrowser{t} }

type extBrowser struct{ *browser.Tools }

func (b extBrowser) CloseTab(ctx context.Context, tab string) error {
	out, _ := b.Call(ctx, browser.ToolCloseTab, map[string]any{"tab_id": tab}, node.Request{})
	if out["success"] != true {
		return fmt.Errorf("%v", out["error"])
	}
	return nil
}

// Tools is the computer-task executor's node tools (blueprint P3 pieces 8 and 22):
// computer_observe, computer_act, computer_files and computer_stop. cc's computer-task loop calls
// them; the model never sees them (computeruse.IsInternalTool). The rules are enforced here,
// whatever cc decided: the switch, the built-in deny list, the batch's blocked sites and apps, the
// risk of each action (a confirm-level one runs only with a token cc minted for exactly it) and no
// typing into password fields. A browser task acts on page elements by ref in a task window
// behind the user's; coordinates are for desktop targets (C3), which are attended (desktop.go).
type Tools struct {
	Browser  Browser         // nil: no browser tasks
	Desktop  *Desktop        // nil: no desktop tasks
	CC       func() Approver // nil, or returning nil: not connected to jarvisd, nothing risky runs
	Switch   *Switch
	Activity *Activity
	// Home is the user's home folder for computer_files ("": os.UserHomeDir).
	Home string

	once sync.Once
	rs   *Runs
	desk desktopState
}

// runs is the executor's runs; the first call registers them with Activity's Stop.
func (t *Tools) runs() *Runs {
	t.once.Do(func() {
		t.rs = &Runs{}
		if t.Activity != nil {
			t.Activity.OnStop(t.rs.StopAll)
		}
	})
	return t.rs
}

var (
	runParam    = node.Param{Name: "run_id", Type: "string", Required: true, Description: "the computer task's run id"}
	targetParam = node.Param{Name: "target", Type: "object", Required: true, Description: `{"kind": "browser"|"desktop", "site"?, "app"?}`}

	observeSpec = node.Spec{Name: computeruse.ToolObserve, Description: "Look at a computer task's window: its elements and/or a screenshot.",
		Params: []node.Param{runParam, targetParam,
			{Name: "url", Type: "string", Description: "the page to open the task window on (the first observe of a run)"},
			{Name: "snapshot", Type: "bool", Description: "list the interactive elements"},
			{Name: "screenshot", Type: "bool", Description: "screenshot the task window"}}}
	actSpec = node.Spec{Name: computeruse.ToolAct, Description: "Do a computer task's actions, in order, stopping at the first that fails.",
		Params: []node.Param{runParam, targetParam,
			{Name: "actions", Type: "array", Required: true, Description: "computeruse actions"},
			{Name: "blocked", Type: "object", Description: `{"sites": [], "apps": []}`}}}
	filesSpec = node.Spec{Name: computeruse.ToolFiles, Description: "List, read or write a file in Downloads or ~/Documents/Jarvis.",
		Params: []node.Param{
			{Name: "op", Type: "string", Required: true, Enum: []string{"list", "read", "write"}},
			{Name: "path", Type: "string", Required: true},
			{Name: "content_b64", Type: "string", Description: "write: the file's bytes"}}}
	stopSpec = node.Spec{Name: computeruse.ToolStop, Description: "Stop a computer task.",
		Params: []node.Param{runParam}}
)

func (t *Tools) Available(context.Context) []node.ToolDef {
	return []node.ToolDef{observeSpec.Render(), actSpec.Render(), filesSpec.Render(), stopSpec.Render()}
}

func (t *Tools) Call(ctx context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	switch name {
	case computeruse.ToolObserve, computeruse.ToolAct, computeruse.ToolFiles:
		if err := t.Switch.Allow(targetKind(args)); err != nil {
			return failCode("computer_control_off", "%v", err), true
		}
	case computeruse.ToolStop:
	default:
		return nil, false
	}
	switch name {
	case computeruse.ToolObserve:
		var a observeArgs
		if err := decodeArgs(args, &a); err != nil {
			return failCode("invalid_argument", "%v", err), true
		}
		return t.observe(ctx, a), true
	case computeruse.ToolAct:
		var a actArgs
		if err := decodeArgs(args, &a); err != nil {
			return failCode("invalid_argument", "%v", err), true
		}
		return t.act(ctx, a), true
	case computeruse.ToolFiles:
		return t.files(args), true
	}
	id, _ := args["run_id"].(string)
	if id == "" {
		return failCode("invalid_argument", "run_id is required"), true
	}
	t.runs().Stop(id)
	if t.Activity != nil && !t.runs().Live() {
		t.endDesktop()
	}
	return map[string]any{"success": true, "run_id": id}, true
}

// targetKind is a call's target kind for the switch's message: desktop, else browser.
func targetKind(args map[string]any) string {
	var a struct {
		Target target `json:"target"`
	}
	if decodeArgs(args, &a) == nil && a.Target.Kind == TargetDesktop {
		return TargetDesktop
	}
	return TargetBrowser
}

// target is a computeruse.Target, or just its kind ("browser").
type target computeruse.Target

func (t *target) UnmarshalJSON(b []byte) error {
	var kind string
	if json.Unmarshal(b, &kind) == nil {
		*t = target{Kind: kind}
		return nil
	}
	return json.Unmarshal(b, (*computeruse.Target)(t))
}

type observeArgs struct {
	RunID      string `json:"run_id"`
	Target     target `json:"target"`
	URL        string `json:"url"`
	Snapshot   bool   `json:"snapshot"`
	Screenshot bool   `json:"screenshot"`
}

type actArgs struct {
	RunID   string               `json:"run_id"`
	Target  target               `json:"target"`
	Actions []computeruse.Action `json:"actions"`
	Blocked struct {
		Sites []string `json:"sites"`
		Apps  []string `json:"apps"`
	} `json:"blocked"`
	// Resume continues a desktop task paused because the user used the computer (the
	// server's Paused card's Continue).
	Resume bool `json:"resume"`
}

func decodeArgs(args map[string]any, v any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func failCode(code, format string, a ...any) map[string]any {
	o := node.Fail(format, a...)
	o["error_code"] = code
	return o
}

// errorCode is a browser error's code for a result: the extension's own (stale_ref,
// password_field, not_found, …), outdated_extension, or failed.
func errorCode(err error) string {
	var ce *browser.CommandError
	switch {
	case errors.As(err, &ce) && ce.Code == "extension_outdated":
		return "outdated_extension"
	case errors.As(err, &ce) && ce.Code != "":
		return ce.Code
	case errors.Is(err, browser.ErrNotConnected), errors.Is(err, browser.ErrNoSuchBrowser):
		return "not_connected"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "failed"
}

func (t *Tools) observe(ctx context.Context, a observeArgs) map[string]any {
	switch {
	case a.RunID == "":
		return failCode("invalid_argument", "run_id is required")
	case a.Target.Kind == TargetDesktop:
		return t.observeDesktop(ctx, a)
	case a.Target.Kind != TargetBrowser:
		return failCode("invalid_argument", "a target is browser or desktop, not %q", a.Target.Kind)
	case t.Browser == nil:
		return failCode("not_connected", "no browser with the Jarvis extension on this computer")
	}
	run := t.runs().Get(a.RunID)
	if run.Stopped {
		return failCode("stopped", "the task was stopped")
	}
	if a.URL != "" {
		if code, err := t.open(ctx, a.RunID, a.URL, nil); err != nil {
			return failCode(code, "%v", err)
		}
	} else if run.Window == "" {
		return failCode("invalid_argument", "the task has no window yet: give the url to open it on")
	}
	if !a.Snapshot && !a.Screenshot {
		a.Snapshot = true
	}
	run = t.runs().Get(a.RunID)
	out := map[string]any{"success": true, "target": TargetBrowser, "tab_id": run.Tab, "window_id": run.Window}
	if run.URL != "" {
		out["url"] = run.URL
	}
	if a.Snapshot {
		s, err := t.Browser.Snapshot(ctx, run.Tab, 0)
		if err != nil {
			return failCode(errorCode(err), "%v", err)
		}
		t.runs().With(a.RunID, func(r *Run) {
			r.Snap = &s
			if s.URL != "" {
				r.URL = s.URL
			}
		})
		if s.URL != "" {
			out["url"] = s.URL
		}
		if s.Title != "" {
			out["title"] = s.Title
		}
		out["snapshot"] = map[string]any{"gen": s.Gen, "elements": s.Elements}
	}
	if a.Screenshot {
		shot, g, err := t.Browser.Screenshot(ctx, run.Window)
		if err != nil {
			return failCode(errorCode(err), "%v", err)
		}
		data, err := screen.EncodeShot(&shot)
		if err != nil {
			return failCode("capture_failed", "%v", err)
		}
		g.ImageW, g.ImageH = shot.Geometry.ImageW, shot.Geometry.ImageH
		var id string
		t.runs().With(a.RunID, func(r *Run) {
			r.captures++
			id = fmt.Sprintf("cap_%d_%s", r.captures, randHex(4))
		})
		out["capture"] = map[string]any{"capture_id": id, "width": g.ImageW, "height": g.ImageH,
			"origin_x": g.OriginX, "origin_y": g.OriginY, "logical_w": g.LogicalW, "logical_h": g.LogicalH,
			"scale": g.Scale, "monitor": g.Monitor}
		out[screen.AttachmentsKey] = []any{map[string]any{"mime": "image/jpeg",
			"data": base64.StdEncoding.EncodeToString(data), "label": "screenshot of the task window"}}
	}
	return out
}

// open puts run id on rawURL: a new task window (the extension can't load an address into a
// tab), and the run's old tab closed. blocked is the batch's blocked sites (nil: none).
func (t *Tools) open(ctx context.Context, id, rawURL string, blocked []string) (code string, err error) {
	host := hostOf(rawURL)
	if host == "" {
		return "invalid_argument", fmt.Errorf("%q isn't a web address", rawURL)
	}
	if deny, why := computeruse.BuiltinDeny(computeruse.Target{Kind: TargetBrowser, Site: hostPort(rawURL)}, nil); deny {
		return "denied", errors.New(why)
	}
	if s := blockedSite(host, blocked); s != "" {
		return "blocked_site", fmt.Errorf("%s is blocked for this task (%s)", host, s)
	}
	tab, window, err := t.Browser.OpenBackground(ctx, rawURL)
	if err != nil {
		return errorCode(err), err
	}
	var old string
	t.runs().With(id, func(r *Run) {
		old = r.Tab
		r.Tab, r.Window, r.URL, r.Snap = tab, window, rawURL, nil
	})
	if old != "" && old != tab {
		// Best effort: a tab the user closed meanwhile is gone already.
		_ = t.Browser.CloseTab(ctx, old)
	}
	return "", nil
}

// Result is one action's result in computer_act's answer. NewApp is the app a desktop action
// was the task's first in (Q4); Text is what clipboard_read read.
type Result struct {
	I      int               `json:"i"`
	OK     bool              `json:"ok"`
	Code   string            `json:"code,omitempty"`
	Error  string            `json:"error,omitempty"`
	Risk   *computeruse.Risk `json:"risk,omitempty"`
	NewApp string            `json:"new_app,omitempty"`
	Text   string            `json:"text,omitempty"`
}

func (r Result) out() map[string]any {
	m := map[string]any{"i": r.I, "ok": r.OK}
	if r.Code != "" {
		m["code"] = r.Code
	}
	if r.Error != "" {
		m["error"] = r.Error
	}
	if r.Risk != nil {
		m["risk"] = *r.Risk
	}
	if r.NewApp != "" {
		m["new_app"] = r.NewApp
	}
	if r.Text != "" {
		m["text"] = r.Text
	}
	return m
}

func (t *Tools) act(ctx context.Context, a actArgs) map[string]any {
	switch {
	case a.RunID == "":
		return failCode("invalid_argument", "run_id is required")
	case a.Target.Kind != TargetBrowser && a.Target.Kind != TargetDesktop:
		return failCode("invalid_argument", "a target is browser or desktop, not %q", a.Target.Kind)
	case len(a.Actions) == 0:
		return failCode("invalid_argument", "no actions")
	case len(a.Actions) > computeruse.MaxBatch:
		return failCode("invalid_argument", "%d actions, at most %d per call", len(a.Actions), computeruse.MaxBatch)
	case a.Target.Kind == TargetDesktop:
		return t.actDesktop(ctx, a)
	case t.Browser == nil:
		return failCode("not_connected", "no browser with the Jarvis extension on this computer")
	}
	results := []map[string]any{}
	out := map[string]any{"success": true}
	for i, action := range a.Actions {
		r := t.step(ctx, a, i, action)
		results = append(results, r.out())
		if !r.OK {
			out["success"] = false
			out["stopped_at"] = i
			break
		}
	}
	out["results"] = results
	return out
}

// step runs action i of the batch, after every check.
func (t *Tools) step(ctx context.Context, a actArgs, i int, action computeruse.Action) Result {
	fail := func(code, format string, args ...any) Result {
		return Result{I: i, Code: code, Error: fmt.Sprintf(format, args...)}
	}
	run := t.runs().Get(a.RunID)
	if run.Stopped {
		return fail("stopped", "the task was stopped")
	}
	if err := action.Validate(); err != nil {
		return fail("invalid_action", "%v", err)
	}
	if run.Tab == "" {
		return fail("not_found", "the task has no window yet: observe with a url first")
	}
	// The tab's site as this agent last saw it; cc's tokens are bound to the same.
	tgt := computeruse.Target(a.Target)
	tgt.Kind = TargetBrowser
	if h := hostPort(run.URL); h != "" {
		tgt.Site = h
	}
	var el *computeruse.Element
	var snapEl *computeruse.SnapElement
	if action.Ref != "" {
		if run.Snap != nil {
			snapEl = run.Snap.Find(action.Ref)
		}
		if snapEl == nil {
			return fail("stale_ref", "%s isn't in the last snapshot: take a new one", action.Ref)
		}
		el = snapEl.Element()
	}
	if deny, why := computeruse.DenyAction(action, tgt); deny {
		return fail("denied", "%s", why)
	}
	if deny, why := computeruse.BuiltinDeny(tgt, el); deny {
		return fail("denied", "%s", why)
	}
	if s := blockedSite(hostOf(run.URL), a.Blocked.Sites); s != "" {
		return fail("blocked_site", "%s is blocked for this task (%s)", tgt.Site, s)
	}
	if (action.Kind == "set_value" || action.Kind == "type") && el != nil && el.Secure {
		return fail("password_field", "%s is a password field: Jarvis doesn't type passwords; the user enters it", action.Ref)
	}
	if r, ok := t.checkRisk(ctx, a.RunID, i, action, el, tgt); !ok {
		return r
	}

	t.showing(action.Intent)
	if code, err := t.do(ctx, a, action, run); err != nil {
		return fail(code, "%v", err)
	}
	return Result{I: i, OK: true}
}

// checkRisk classifies action i (on el, or nil, in tgt): ok when it may run, else its failed
// result. A risky one runs only with a valid approval token for exactly it.
func (t *Tools) checkRisk(ctx context.Context, runID string, i int, action computeruse.Action, el *computeruse.Element, tgt computeruse.Target) (Result, bool) {
	fail := func(code, format string, args ...any) Result {
		return Result{I: i, Code: code, Error: fmt.Sprintf(format, args...)}
	}
	risk := computeruse.Classify(action, el, tgt)
	switch risk.Level {
	case computeruse.LevelDeny:
		r := fail("denied", "%s", risk.Reason)
		r.Risk = &risk
		return r, false
	case computeruse.LevelConfirm:
		if action.Approval == "" {
			r := fail("needs_approval", "%s: the user has to approve it", risk.Reason)
			r.Risk = &risk
			return r, false
		}
		if err := t.verify(ctx, action.Approval, action.Digest(runID, tgt)); err != nil {
			r := fail("bad_approval", "%v", err)
			r.Risk = &risk
			return r, false
		}
	}
	return Result{I: i, OK: true}, true
}

func (t *Tools) verify(ctx context.Context, token, digest string) error {
	var cc Approver
	if t.CC != nil {
		cc = t.CC()
	}
	if cc == nil {
		return errors.New("not connected to jarvisd, so the approval can't be checked")
	}
	ok, err := cc.VerifyApproval(ctx, token, digest)
	if err != nil {
		return fmt.Errorf("couldn't check the approval: %w", err)
	}
	if !ok {
		return errors.New("the approval isn't valid for this action")
	}
	return nil
}

// showing shows the action's intent on the island and the tray, unless they show something
// else (a recording, a paused desktop task).
func (t *Tools) showing(intent string) {
	if t.Activity == nil {
		return
	}
	if k, _ := t.Activity.Status(); k == Idle || k == Controlling {
		t.Activity.Set(Controlling, intent)
	}
}

// browserKeys are the keypress names the extension's keys op sends.
var browserKeys = map[string]string{
	"enter": "Enter", "return": "Enter", "escape": "Escape", "esc": "Escape", "tab": "Tab",
	"up": "ArrowUp", "down": "ArrowDown", "left": "ArrowLeft", "right": "ArrowRight",
}

// do runs one checked action in the run's tab.
func (t *Tools) do(ctx context.Context, a actArgs, action computeruse.Action, run Run) (code string, err error) {
	elementAct := func(op, value string, keys []string) (string, error) {
		res, err := t.Browser.Act(ctx, run.Tab, run.Snap.Gen, action.Ref, op, value, keys)
		if err != nil {
			return errorCode(err), err
		}
		if res.URLAfter != "" {
			t.runs().With(a.RunID, func(r *Run) { r.URL = res.URLAfter })
		}
		return "", nil
	}
	switch action.Kind {
	case "press":
		return elementAct("press", "", nil)
	case "set_value", "select":
		return elementAct(action.Kind, action.Text, nil)
	case "type":
		if action.Ref != "" {
			return elementAct("set_value", action.Text, nil)
		}
	case "keypress":
		var keys []string
		for _, k := range action.Keys {
			if bk, ok := browserKeys[strings.ToLower(k)]; ok {
				keys = append(keys, bk)
			}
		}
		if action.Ref != "" && len(keys) == len(action.Keys) {
			return elementAct("keys", "", keys)
		}
	case "navigate":
		return t.open(ctx, a.RunID, action.URL, a.Blocked.Sites)
	case "wait":
		select {
		case <-time.After(time.Duration(action.MS) * time.Millisecond):
			return "", nil
		case <-ctx.Done():
			return "timeout", ctx.Err()
		}
	case "screenshot":
		return "", nil // the next computer_observe takes it
	}
	return "browser_window", fmt.Errorf("a browser task acts on page elements by ref (press, set_value, select, "+
		"keypress with a ref and Enter, Escape, Tab or arrows; navigate); %s by coordinates or at the focus is for desktop tasks", action.Kind)
}

// hostOf is rawURL's host name, lower case ("" when it has none).
func hostOf(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// hostPort is rawURL's host[:port], as cc's target names the site.
func hostPort(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return u.Host
}

// blockedSite is the entry of blocked that host is (or is a subdomain of), or "".
func blockedSite(host string, blocked []string) string {
	if host == "" {
		return ""
	}
	for _, s := range blocked {
		b := strings.ToLower(strings.TrimSpace(s))
		if strings.Contains(b, "://") {
			b = hostOf(b)
		} else if h := hostOf("//" + b); h != "" {
			b = h
		}
		b = strings.TrimPrefix(b, "*.")
		if b != "" && (host == b || strings.HasSuffix(host, "."+b)) {
			return s
		}
	}
	return ""
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
