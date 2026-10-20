package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/control"
)

// The teach cases' setup.demonstrate (docs/dev/computer-suite.md): before the case's message the
// suite plays a scripted demonstration in the suite browser while the agent records it, as the
// user does after "Record a macro…", waits for jarvisd to learn a workflow from the recording and
// saves it as-is (the user's Review, then Save). The case then replays it.

// demoStep is one thing the demonstration does: open a URL (under the sites' base), click the
// element of tag Tag whose text is Text, or wait for the sites to record a download of Download
// ("site/month").
type demoStep struct {
	Open     string
	Tag      string
	Text     string
	Download string
}

// demonstrations are the scripts setup.demonstrate names.
var demonstrations = map[string][]demoStep{
	// The bank statement of 2026-09, from the statements list.
	"bank_download": {
		{Open: "/bank/"},
		{Tag: "a", Text: "Statement 2026-09"},
		{Tag: "button", Text: "Download CSV"},
		{Download: "bank/2026-09"},
	},
}

func demonstrationNames() []string {
	names := make([]string, 0, len(demonstrations))
	for n := range demonstrations {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// cdpCaller is the browser's DevTools: the pipe, or a fake in the tests.
type cdpCaller interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
	CallSession(ctx context.Context, session, method string, params any) (json.RawMessage, error)
}

// teacher plays a demonstration and saves what jarvisd learns from it.
type teacher struct {
	cdp cdpCaller
	// record asks the agent's control socket: control.ComputerRecordStart or ComputerRecordStop.
	record func(cmd string) (string, error)
	// api is a JSON call to cc's API as the seeded superuser (path from /api/v0).
	api       func(ctx context.Context, method, path string, body, out any) error
	sitesRaw  func(ctx context.Context) ([]byte, error)
	sites     string // the sites' base URL
	household string
	clk       clock
	// settle is how long the recorder needs after a page loads before it sees what happens in it
	// (the extension installs it once the tab reports complete).
	settle time.Duration
}

// taught is what a demonstration left in jarvisd, for cleanup.
type taught struct {
	Recording string
	Workflow  string
	Saved     map[string]any // the workflow as learnt, saved as-is
}

// Demonstrate plays the demonstration name, waits for the workflow learnt from it and saves it;
// what it left in jarvisd is in t even when it fails, for forget.
func (tc *teacher) Demonstrate(ctx context.Context, name string, t *taught) error {
	steps, ok := demonstrations[name]
	if !ok {
		return fmt.Errorf("no demonstration %q", name)
	}
	raw, err := tc.cdp.Call(ctx, "Target.createTarget", map[string]any{"url": "about:blank"})
	if err != nil {
		return err
	}
	var target struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(raw, &target); err != nil {
		return err
	}
	defer tc.cdp.Call(context.WithoutCancel(ctx), "Target.closeTarget", map[string]any{"targetId": target.TargetID})
	if raw, err = tc.cdp.Call(ctx, "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}); err != nil {
		return err
	}
	var att struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &att); err != nil {
		return err
	}
	page := &demoPage{cdp: tc.cdp, session: att.SessionID, clk: tc.clk, settle: tc.settle}
	if _, err := page.call(ctx, "Page.bringToFront", nil); err != nil {
		return err
	}

	id, err := tc.record(control.ComputerRecordStart)
	if err != nil {
		return fmt.Errorf("record start: %w", err)
	}
	t.Recording = strings.TrimSpace(id)
	for i, s := range steps {
		var err error
		switch {
		case s.Open != "":
			err = page.open(ctx, strings.TrimRight(tc.sites, "/")+s.Open)
		case s.Text != "":
			err = page.click(ctx, s.Tag, s.Text)
		case s.Download != "":
			err = tc.waitDownload(ctx, s.Download)
		}
		if err != nil {
			tc.record(control.ComputerRecordStop)
			return fmt.Errorf("step %d: %w", i+1, err)
		}
	}
	if _, err := tc.record(control.ComputerRecordStop); err != nil {
		return fmt.Errorf("record stop: %w", err)
	}

	// The learn job (cc.workflow.learn) stores the workflow under the recording's id.
	var wf map[string]any
	for wf == nil {
		var list struct {
			Workflows []map[string]any `json:"workflows"`
		}
		if err := tc.api(ctx, "GET", "/workflows?household_id="+tc.household, nil, &list); err != nil {
			return err
		}
		for _, w := range list.Workflows {
			if w["recording_id"] == t.Recording {
				wf = w
			}
		}
		if wf == nil && !tc.clk.wait(ctx, 2*time.Second) {
			return fmt.Errorf("no workflow learnt from recording %s: %w", t.Recording, ctx.Err())
		}
	}
	t.Workflow, _ = wf["id"].(string)
	t.Saved = wf
	// Saved as-is: the editable fields, unchanged.
	put := map[string]any{}
	for _, k := range []string{"name", "triggers", "params", "steps", "success", "shared"} {
		put[k] = wf[k]
	}
	return tc.api(ctx, "PUT", "/workflows/"+t.Workflow, put, nil)
}

// forget deletes what a demonstration left: the next run teaches its own (two would make the
// replay ask which one, and a replay heals the one it ran).
func (tc *teacher) forget(ctx context.Context, t taught) error {
	var errs []error
	if t.Workflow != "" {
		errs = append(errs, tc.api(ctx, "DELETE", "/workflows/"+t.Workflow, nil, nil))
	}
	if t.Recording != "" {
		errs = append(errs, tc.api(ctx, "DELETE", "/recordings/"+t.Recording, nil, nil))
	}
	return errors.Join(errs...)
}

// waitDownload waits for the sites to record a download of want ("site/month").
func (tc *teacher) waitDownload(ctx context.Context, want string) error {
	site, month, _ := strings.Cut(want, "/")
	for {
		raw, err := tc.sitesRaw(ctx)
		if err == nil {
			var st SitesState
			if json.Unmarshal(raw, &st) == nil {
				for _, d := range st.Downloads {
					if d.Site == site && d.Month == month {
						return nil
					}
				}
			}
		}
		if !tc.clk.wait(ctx, 500*time.Millisecond) {
			return fmt.Errorf("no download of %s: %w", want, ctx.Err())
		}
	}
}

// demoPage is the demonstration's tab, through its DevTools session. Clicks are real input
// events (Input.dispatchMouseEvent): the recorder ignores untrusted ones, such as el.click().
type demoPage struct {
	cdp     cdpCaller
	session string
	clk     clock
	settle  time.Duration
}

func (p *demoPage) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return p.cdp.CallSession(ctx, p.session, method, params)
}

// eval is the value of a JavaScript expression in the page.
func (p *demoPage) eval(ctx context.Context, expr string, out any) error {
	raw, err := p.call(ctx, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true})
	if err != nil {
		return err
	}
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if r.ExceptionDetails != nil {
		return fmt.Errorf("%s: %s", expr, r.ExceptionDetails.Text)
	}
	return json.Unmarshal(r.Result.Value, out)
}

// loaded waits until the page is a loaded page other than from (its URL before), then for the
// recorder to be in it.
func (p *demoPage) loaded(ctx context.Context, from string) error {
	for {
		var at struct{ URL, State string }
		if err := p.eval(ctx, `({URL: location.href, State: document.readyState})`, &at); err == nil &&
			at.URL != from && at.State == "complete" {
			p.clk.wait(ctx, p.settle)
			return nil
		}
		if !p.clk.wait(ctx, 250*time.Millisecond) {
			return fmt.Errorf("the page did not load: %w", ctx.Err())
		}
	}
}

func (p *demoPage) href(ctx context.Context) string {
	var u string
	p.eval(ctx, `location.href`, &u)
	return u
}

// open goes to url, as the user typing it.
func (p *demoPage) open(ctx context.Context, url string) error {
	from := p.href(ctx)
	if _, err := p.call(ctx, "Page.navigate", map[string]any{"url": url}); err != nil {
		return err
	}
	return p.loaded(ctx, from)
}

// click presses the left button on the middle of the tag element whose text is text, and, when
// that leaves the page, waits for the next one.
func (p *demoPage) click(ctx context.Context, tag, text string) error {
	from := p.href(ctx)
	q, _ := json.Marshal(text)
	var at *struct{ X, Y float64 }
	expr := fmt.Sprintf(`(() => {
  const el = [...document.querySelectorAll(%q)].find((e) => e.textContent.trim() === %s);
  if (!el) return null;
  el.scrollIntoView({block: "center"});
  const r = el.getBoundingClientRect();
  return {X: r.x + r.width / 2, Y: r.y + r.height / 2};
})()`, tag, q)
	if err := p.eval(ctx, expr, &at); err != nil {
		return err
	}
	if at == nil {
		return fmt.Errorf("no %s %q on %s", tag, text, from)
	}
	for _, ev := range []map[string]any{
		{"type": "mouseMoved", "x": at.X, "y": at.Y},
		{"type": "mousePressed", "x": at.X, "y": at.Y, "button": "left", "buttons": 1, "clickCount": 1},
		{"type": "mouseReleased", "x": at.X, "y": at.Y, "button": "left", "buttons": 0, "clickCount": 1},
	} {
		if _, err := p.call(ctx, "Input.dispatchMouseEvent", ev); err != nil {
			return err
		}
	}
	if tag != "a" {
		p.clk.wait(ctx, p.settle)
		return nil
	}
	return p.loaded(ctx, from)
}

// teacher is the Env's: its browser, the agent's control socket and cc's API.
func (e *Env) teacher() (*teacher, error) {
	if e.desk == nil || e.desk.cdp == nil {
		return nil, errors.New("no browser to demonstrate in")
	}
	ctl := filepath.Join(e.desk.rt, "control.sock")
	return &teacher{
		cdp: e.desk.cdp,
		record: func(cmd string) (string, error) {
			r, err := control.Ask(func() (net.Conn, error) { return net.Dial("unix", ctl) }, cmd)
			return r.Answer, err
		},
		api: func(ctx context.Context, method, path string, body, out any) error {
			return e.api(ctx, method, e.Sbx["SBX_CC"]+"/api/v0"+path, body, out)
		},
		sitesRaw: e.sitesRaw, sites: e.Sites, household: e.Household, clk: realClock, settle: 1500 * time.Millisecond,
	}, nil
}
