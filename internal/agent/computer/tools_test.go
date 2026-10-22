package computer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// fakeBrowser is the extension: one page per tab, acts recorded.
type fakeBrowser struct {
	url     string
	snap    computeruse.Snapshot
	acts    []string // "op ref value"
	actErr  map[string]error
	opened  []string
	closed  []string
	onAct   func()
	n       int
	shotErr error
	text    string // the page's text (read_page)
	textErr error
	// loading is how many snapshots after an open see the new tab still on about:blank, which
	// the browser doesn't let extensions script.
	loading int
	snaps   int
}

func (b *fakeBrowser) PageText(_ context.Context, tab string, max int) (string, error) {
	if b.textErr != nil {
		return "", b.textErr
	}
	if r := []rune(b.text); len(r) > max {
		return string(r[:max]), nil
	}
	return b.text, nil
}

func (b *fakeBrowser) OpenBackground(_ context.Context, u string) (string, string, error) {
	b.n++
	b.opened = append(b.opened, u)
	b.url = u
	return "firefox:" + string(rune('0'+b.n)), "firefox:" + string(rune('0'+b.n)) + "0", nil
}

func (b *fakeBrowser) Snapshot(_ context.Context, tab string, _ int) (computeruse.Snapshot, error) {
	b.snaps++
	if b.loading > 0 {
		b.loading--
		return computeruse.Snapshot{}, &browser.CommandError{Code: "restricted_page",
			Message: "the browser doesn't let extensions read or control this page (about:blank)"}
	}
	s := b.snap
	s.URL = b.url
	return s, nil
}

func (b *fakeBrowser) Act(_ context.Context, tab string, gen int, ref, op, value string, keys []string) (browser.ActResult, error) {
	if b.onAct != nil {
		b.onAct()
	}
	b.acts = append(b.acts, strings.Join(strings.Fields(op+" "+ref+" "+value+" "+strings.Join(keys, "+")), " "))
	if err := b.actErr[ref]; err != nil {
		return browser.ActResult{}, err
	}
	return browser.ActResult{OK: true, URLAfter: b.url}, nil
}

func (b *fakeBrowser) Screenshot(_ context.Context, window string) (screen.Shot, computeruse.Geometry, error) {
	if b.shotErr != nil {
		return screen.Shot{}, computeruse.Geometry{}, b.shotErr
	}
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	g := computeruse.Geometry{LogicalW: 100, LogicalH: 50, Scale: 2, ImageW: 200, ImageH: 100}
	return screen.Shot{Image: img, App: "Firefox", Geometry: g}, g, nil
}

func (b *fakeBrowser) CloseTab(_ context.Context, tab string) error {
	b.closed = append(b.closed, tab)
	return nil
}

// fakeCC answers approvals: valid only for the token minted for that digest, once.
type fakeCC struct {
	tokens map[string]string // token → digest
	asked  int
}

func (c *fakeCC) VerifyApproval(_ context.Context, token, digest string) (bool, error) {
	c.asked++
	d, ok := c.tokens[token]
	if !ok || d != digest {
		return false, nil
	}
	delete(c.tokens, token)
	return true, nil
}

const store = "https://shop.example.com/cart"

func testTools(t *testing.T) (*Tools, *fakeBrowser, *fakeCC) {
	t.Helper()
	b := &fakeBrowser{snap: computeruse.Snapshot{Gen: 3, Elements: []computeruse.SnapElement{
		{Ref: "e1", Role: "textbox", Name: "Search"},
		{Ref: "e2", Role: "button", Name: "Add to cart"},
		{Ref: "e3", Role: "button", Name: "Place order"},
		{Ref: "e4", Role: "textbox", Name: "Password", Type: "password"},
	}}}
	cc := &fakeCC{tokens: map[string]string{}}
	sw := NewSwitch(true, nil)
	tl := &Tools{Browser: b, CC: func() Approver { return cc }, Switch: sw, Home: t.TempDir()}
	return tl, b, cc
}

func call(t *testing.T, tl *Tools, name string, args any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	out, ok := tl.Call(context.Background(), name, m, node.Request{})
	if !ok {
		t.Fatalf("%s not handled", name)
	}
	return out
}

var browserTarget = map[string]any{"kind": "browser"}

func observe(t *testing.T, tl *Tools, runID string) map[string]any {
	t.Helper()
	out := call(t, tl, computeruse.ToolObserve, map[string]any{"run_id": runID, "target": browserTarget, "url": store, "snapshot": true})
	if out["success"] != true {
		t.Fatalf("observe: %v", out)
	}
	return out
}

func act(t *testing.T, tl *Tools, runID string, actions []computeruse.Action, blocked ...string) (results []map[string]any, out map[string]any) {
	t.Helper()
	args := map[string]any{"run_id": runID, "target": browserTarget, "actions": actions}
	if len(blocked) > 0 {
		args["blocked"] = map[string]any{"sites": blocked}
	}
	out = call(t, tl, computeruse.ToolAct, args)
	rs, _ := out["results"].([]map[string]any)
	return rs, out
}

func press(ref, intent string) computeruse.Action {
	return computeruse.Action{Kind: "press", Ref: ref, Intent: intent}
}

func codes(rs []map[string]any) []string {
	var out []string
	for _, r := range rs {
		c, _ := r["code"].(string)
		if r["ok"] == true {
			c = "ok"
		}
		out = append(out, c)
	}
	return out
}

func wantCodes(t *testing.T, rs []map[string]any, want ...string) {
	t.Helper()
	if got := strings.Join(codes(rs), ","); got != strings.Join(want, ",") {
		t.Fatalf("results %s, want %s (%v)", got, strings.Join(want, ","), rs)
	}
}

// TestObserveWaitsForNewWindow: a task window's tab is about:blank for its first moments, which
// the extension can't snapshot (#418: a replay failed at once with "doesn't let extensions read
// or control this page (about:blank)"). observe waits for the page instead.
func TestObserveWaitsForNewWindow(t *testing.T) {
	defer func(p time.Duration) { settlePoll = p }(settlePoll)
	settlePoll = time.Millisecond
	tl, b, _ := testTools(t)
	b.loading = 3
	out := call(t, tl, computeruse.ToolObserve, map[string]any{"run_id": "cr_1", "target": browserTarget, "url": store, "snapshot": true})
	if out["success"] != true || out["url"] != store || b.snaps != 4 {
		t.Fatalf("observe %v after %d snapshots", out, b.snaps)
	}
	// A page that stays restricted fails once the wait is over.
	defer func(d time.Duration) { settleFor = d }(settleFor)
	settleFor = 20 * time.Millisecond
	b.loading = 1 << 30
	out = call(t, tl, computeruse.ToolObserve, map[string]any{"run_id": "cr_2", "target": browserTarget, "url": store, "snapshot": true})
	if out["success"] == true || out["error_code"] != "restricted_page" {
		t.Fatalf("observe %v", out)
	}
	// In a window already open, a restricted page is the page: no wait.
	b.loading, b.snaps = 1, 0
	out = call(t, tl, computeruse.ToolObserve, map[string]any{"run_id": "cr_1", "target": browserTarget, "snapshot": true})
	if out["error_code"] != "restricted_page" || b.snaps != 1 {
		t.Fatalf("observe %v after %d snapshots", out, b.snaps)
	}
}

func TestObserveOpensTaskWindow(t *testing.T) {
	tl, b, _ := testTools(t)
	out := call(t, tl, computeruse.ToolObserve, map[string]any{"run_id": "cr_1", "target": browserTarget, "url": store, "snapshot": true, "screenshot": true})
	if out["success"] != true {
		t.Fatalf("observe: %v", out)
	}
	if len(b.opened) != 1 || b.opened[0] != store {
		t.Fatalf("opened %v, want the store once", b.opened)
	}
	snap := out["snapshot"].(map[string]any)
	if snap["gen"] != 3 || len(snap["elements"].([]computeruse.SnapElement)) != 4 {
		t.Fatalf("snapshot %v", snap)
	}
	capt := out["capture"].(map[string]any)
	if capt["width"] != 200 || capt["logical_w"] != 100 || capt["capture_id"] == "" {
		t.Fatalf("capture %v", capt)
	}
	imgs := out[screen.AttachmentsKey].([]any)
	if len(imgs) != 1 || imgs[0].(map[string]any)["mime"] != "image/jpeg" {
		t.Fatalf("attachments %v", imgs)
	}
	if out["url"] != store || out["window_id"] != "firefox:10" {
		t.Fatalf("url %v window %v", out["url"], out["window_id"])
	}
	// The second observe reuses the window.
	out = call(t, tl, computeruse.ToolObserve, map[string]any{"run_id": "cr_1", "target": browserTarget, "snapshot": true})
	if out["success"] != true || len(b.opened) != 1 {
		t.Fatalf("second observe %v, opened %v", out, b.opened)
	}
	// The snapshot comes with the page's text (prices, ratings: not elements), capped; a page
	// whose text can't be read is still observed.
	b.text = "Pulse Active 2 Smart Watch $249 ★ 4.8 " + strings.Repeat("x", 5000)
	out = call(t, tl, computeruse.ToolObserve, map[string]any{"run_id": "cr_1", "target": browserTarget, "snapshot": true})
	if txt, _ := out["text"].(string); !strings.HasPrefix(txt, "Pulse Active 2 Smart Watch $249") || len([]rune(txt)) != pageTextChars {
		t.Fatalf("text %d chars: %.60q", len([]rune(txt)), txt)
	}
	b.textErr = errors.New("no readable text")
	out = call(t, tl, computeruse.ToolObserve, map[string]any{"run_id": "cr_1", "target": browserTarget, "snapshot": true})
	if out["success"] != true || out["text"] != nil {
		t.Fatalf("observe without text: %v", out)
	}
	b.text, b.textErr = "", nil
	// A new run needs an address.
	out = call(t, tl, computeruse.ToolObserve, map[string]any{"run_id": "cr_2", "target": browserTarget, "snapshot": true})
	if out["success"] != false || !strings.Contains(out["error"].(string), "url") {
		t.Fatalf("observe without a window or url: %v", out)
	}
}

func TestActStopsAtFirstFailure(t *testing.T) {
	tl, b, _ := testTools(t)
	b.actErr = map[string]error{"e2": &browser.CommandError{Code: "stale_ref", Message: "take a new snapshot"}}
	observe(t, tl, "cr_1")
	rs, out := act(t, tl, "cr_1", []computeruse.Action{
		{Kind: "set_value", Ref: "e1", Text: "blue mug", Intent: "search for the mug"},
		press("e2", "add it"),
		press("e1", "never runs"),
	})
	wantCodes(t, rs, "ok", "stale_ref")
	if out["stopped_at"] != 1 || out["success"] != false {
		t.Fatalf("out %v, want stopped_at 1", out)
	}
	if len(b.acts) != 2 {
		t.Fatalf("acts %v, want two", b.acts)
	}
}

func TestRiskyNeedsApproval(t *testing.T) {
	tl, b, cc := testTools(t)
	observe(t, tl, "cr_1")
	order := press("e3", "place the order")
	rs, out := act(t, tl, "cr_1", []computeruse.Action{press("e2", "add it"), order, press("e2", "after")})
	wantCodes(t, rs, "ok", "needs_approval")
	if out["stopped_at"] != 1 || len(b.acts) != 1 {
		t.Fatalf("out %v acts %v", out, b.acts)
	}
	risk := rs[1]["risk"].(computeruse.Risk)
	if risk.Level != computeruse.LevelConfirm || risk.Category != computeruse.CatPay {
		t.Fatalf("risk %+v", risk)
	}
	// With cc's token for this action on this site, it runs.
	cc.tokens["tok"] = order.Digest("cr_1", computeruse.Target{Kind: "browser", Site: "shop.example.com"})
	order.Approval = "tok"
	rs, _ = act(t, tl, "cr_1", []computeruse.Action{order})
	wantCodes(t, rs, "ok")
	if b.acts[len(b.acts)-1] != "press e3" {
		t.Fatalf("acts %v", b.acts)
	}
}

func TestBadApprovalRefused(t *testing.T) {
	tl, b, cc := testTools(t)
	observe(t, tl, "cr_1")
	order := press("e3", "place the order")
	// A token minted for another run, and a made-up one.
	cc.tokens["other"] = order.Digest("cr_2", computeruse.Target{Kind: "browser", Site: "shop.example.com"})
	for _, tok := range []string{"other", "forged"} {
		order.Approval = tok
		rs, _ := act(t, tl, "cr_1", []computeruse.Action{order})
		wantCodes(t, rs, "bad_approval")
	}
	if len(b.acts) != 0 || cc.asked != 2 {
		t.Fatalf("acts %v, asked %d", b.acts, cc.asked)
	}
	// Without a connection to jarvisd nothing risky runs either.
	tl.CC = func() Approver { return nil }
	cc.tokens["tok"] = order.Digest("cr_1", computeruse.Target{Kind: "browser", Site: "shop.example.com"})
	order.Approval = "tok"
	rs, _ := act(t, tl, "cr_1", []computeruse.Action{order})
	wantCodes(t, rs, "bad_approval")
	if len(b.acts) != 0 {
		t.Fatalf("acts %v", b.acts)
	}
}

func TestBlockedSite(t *testing.T) {
	tl, b, _ := testTools(t)
	observe(t, tl, "cr_1")
	for _, blocked := range []string{"example.com", "SHOP.example.com", "https://shop.example.com/"} {
		rs, _ := act(t, tl, "cr_1", []computeruse.Action{press("e2", "add it")}, blocked)
		wantCodes(t, rs, "blocked_site")
	}
	// Navigating to a blocked site is refused too.
	rs, _ := act(t, tl, "cr_1", []computeruse.Action{{Kind: "navigate", URL: "https://bank.example.org/", Intent: "go"}}, "bank.example.org")
	wantCodes(t, rs, "blocked_site")
	if len(b.acts) != 0 || len(b.opened) != 1 {
		t.Fatalf("acts %v opened %v", b.acts, b.opened)
	}
	// Another site is fine.
	rs, _ = act(t, tl, "cr_1", []computeruse.Action{press("e2", "add it")}, "notexample.com")
	wantCodes(t, rs, "ok")
}

func TestJarvisServerDenied(t *testing.T) {
	tl, b, _ := testTools(t)
	computeruse.SetServerOrigins("https://jarvis.home.example:7700")
	t.Cleanup(func() { computeruse.SetServerOrigins() })
	out := call(t, tl, computeruse.ToolObserve, map[string]any{"run_id": "cr_1", "target": browserTarget, "url": "https://jarvis.home.example:7700/admin", "snapshot": true})
	if out["success"] != false || out["error_code"] != "denied" || len(b.opened) != 0 {
		t.Fatalf("observe of the Jarvis server: %v, opened %v", out, b.opened)
	}
	observe(t, tl, "cr_1")
	rs, _ := act(t, tl, "cr_1", []computeruse.Action{{Kind: "navigate", URL: "https://jarvis.home.example:7700/", Intent: "go"}})
	wantCodes(t, rs, "denied")
}

func TestPasswordFieldRefused(t *testing.T) {
	tl, b, cc := testTools(t)
	observe(t, tl, "cr_1")
	for _, a := range []computeruse.Action{
		{Kind: "set_value", Ref: "e4", Text: "hunter2", Intent: "type the password"},
		{Kind: "type", Ref: "e4", Text: "hunter2", Intent: "type the password"},
	} {
		// Not even with an approval.
		cc.tokens["tok"] = a.Digest("cr_1", computeruse.Target{Kind: "browser", Site: "shop.example.com"})
		a.Approval = "tok"
		rs, _ := act(t, tl, "cr_1", []computeruse.Action{a})
		wantCodes(t, rs, "password_field")
	}
	if len(b.acts) != 0 {
		t.Fatalf("acts %v", b.acts)
	}
	// The extension's own refusal surfaces with its code.
	b.snap.Elements[0].Type = ""
	b.actErr = map[string]error{"e1": &browser.CommandError{Code: "password_field", Message: "that is a password field"}}
	rs, _ := act(t, tl, "cr_1", []computeruse.Action{{Kind: "set_value", Ref: "e1", Text: "x", Intent: "type"}})
	wantCodes(t, rs, "password_field")
}

func TestCoordinatesOnBrowserRefused(t *testing.T) {
	tl, b, _ := testTools(t)
	observe(t, tl, "cr_1")
	x, y := 10, 20
	for _, a := range []computeruse.Action{
		{Kind: "click", X: &x, Y: &y, Intent: "click"},
		{Kind: "double_click", X: &x, Y: &y, Intent: "click"},
		{Kind: "move", X: &x, Y: &y, Intent: "move"},
		{Kind: "drag", Path: [][2]int{{1, 2}, {3, 4}}, Intent: "drag"},
		{Kind: "scroll", DY: 3, Intent: "scroll"},
		{Kind: "zoom", Region: &[4]int{0, 0, 10, 10}, Intent: "zoom"},
		{Kind: "type", Text: "hello", Intent: "type where the focus is"},
		{Kind: "keypress", Keys: []string{"ctrl", "s"}, Intent: "save"},
	} {
		rs, _ := act(t, tl, "cr_1", []computeruse.Action{a})
		wantCodes(t, rs, "browser_window")
	}
	if len(b.acts) != 0 {
		t.Fatalf("acts %v", b.acts)
	}
	// Enter on an element is the extension's keys op.
	rs, _ := act(t, tl, "cr_1", []computeruse.Action{{Kind: "keypress", Ref: "e1", Keys: []string{"enter"}, Intent: "search"}})
	wantCodes(t, rs, "ok")
	if b.acts[0] != "keys e1 Enter" {
		t.Fatalf("acts %v", b.acts)
	}
}

func TestNavigateReplacesTaskWindow(t *testing.T) {
	tl, b, _ := testTools(t)
	observe(t, tl, "cr_1")
	rs, _ := act(t, tl, "cr_1", []computeruse.Action{{Kind: "navigate", URL: "https://other.example.net/x", Intent: "go"}, press("e2", "stale")})
	// The old snapshot's refs are gone with the old page.
	wantCodes(t, rs, "ok", "stale_ref")
	if len(b.opened) != 2 || len(b.closed) != 1 || b.closed[0] != "firefox:1" {
		t.Fatalf("opened %v closed %v", b.opened, b.closed)
	}
	if r := tl.runs().Get("cr_1"); r.Tab != "firefox:2" || r.URL != "https://other.example.net/x" {
		t.Fatalf("run %+v", r)
	}
}

func TestStopFlag(t *testing.T) {
	tl, b, _ := testTools(t)
	observe(t, tl, "cr_1")
	// computer_stop between two actions of a batch: the first finishes, the second is refused.
	b.onAct = func() {
		out := call(t, tl, computeruse.ToolStop, map[string]any{"run_id": "cr_1"})
		if out["success"] != true {
			t.Errorf("stop: %v", out)
		}
	}
	rs, out := act(t, tl, "cr_1", []computeruse.Action{press("e2", "add it"), press("e2", "again")})
	wantCodes(t, rs, "ok", "stopped")
	if out["stopped_at"] != 1 || len(b.acts) != 1 {
		t.Fatalf("out %v acts %v", out, b.acts)
	}
	out = call(t, tl, computeruse.ToolObserve, map[string]any{"run_id": "cr_1", "target": browserTarget, "snapshot": true})
	if out["success"] != false || out["error_code"] != "stopped" {
		t.Fatalf("observe after stop: %v", out)
	}
	// The tray's Stop (Activity.Stop) stops every run.
	act2 := &Activity{}
	tl2, b2, _ := testTools(t)
	tl2.Activity = act2
	observe(t, tl2, "cr_9")
	act2.Stop()
	rs, _ = act(t, tl2, "cr_9", []computeruse.Action{press("e2", "add it")})
	wantCodes(t, rs, "stopped")
	if len(b2.acts) != 0 {
		t.Fatalf("acts %v", b2.acts)
	}
}

func TestActShowsControlling(t *testing.T) {
	tl, _, _ := testTools(t)
	a := &Activity{}
	tl.Activity = a
	observe(t, tl, "cr_1")
	act(t, tl, "cr_1", []computeruse.Action{press("e2", "Adding the mug to the cart")})
	if k, text := a.Status(); k != Controlling || text != "Adding the mug to the cart" {
		t.Fatalf("activity %s %q", k, text)
	}
	call(t, tl, computeruse.ToolStop, map[string]any{"run_id": "cr_1"})
	if k, _ := a.Status(); k != Idle {
		t.Fatalf("activity after stop %s", k)
	}
}

func TestSwitchOffRefusesComputerTools(t *testing.T) {
	tl, b, _ := testTools(t)
	if err := tl.Switch.Set(false); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{computeruse.ToolObserve, map[string]any{"run_id": "cr_1", "target": browserTarget, "url": store}},
		{computeruse.ToolAct, map[string]any{"run_id": "cr_1", "target": browserTarget, "actions": []any{map[string]any{"kind": "press", "ref": "e1", "intent": "x"}}}},
		{computeruse.ToolFiles, map[string]any{"op": "list", "path": "~/Downloads"}},
	} {
		out := call(t, tl, c.name, c.args)
		if out["success"] != false || out["error_code"] != "computer_control_off" {
			t.Fatalf("%s with the switch off: %v", c.name, out)
		}
	}
	if len(b.opened) != 0 || len(b.acts) != 0 {
		t.Fatalf("opened %v acts %v", b.opened, b.acts)
	}
	// Stopping is always allowed.
	if out := call(t, tl, computeruse.ToolStop, map[string]any{"run_id": "cr_1"}); out["success"] != true {
		t.Fatalf("stop: %v", out)
	}
}

func TestInvalidActionRefused(t *testing.T) {
	tl, b, _ := testTools(t)
	observe(t, tl, "cr_1")
	rs, _ := act(t, tl, "cr_1", []computeruse.Action{{Kind: "press", Ref: "e2"}}) // no intent
	wantCodes(t, rs, "invalid_action")
	if len(b.acts) != 0 {
		t.Fatalf("acts %v", b.acts)
	}
}

func TestFilesPathRules(t *testing.T) {
	tl, _, _ := testTools(t)
	home := tl.Home
	dl := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(dl, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dl, "statement.csv"), []byte("date,amount\n2026-10-01,12.50\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dl, "receipt.pdf"), []byte("%PDF-1.4 fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := func(args map[string]any) map[string]any { return call(t, tl, computeruse.ToolFiles, args) }
	ok := func(out map[string]any) map[string]any {
		t.Helper()
		if out["success"] != true {
			t.Fatalf("want success: %v", out)
		}
		return out
	}
	refused := func(out map[string]any) {
		t.Helper()
		if out["success"] != false || out["error_code"] != "path_refused" {
			t.Fatalf("want path_refused: %v", out)
		}
	}

	// Downloads: read ok, as text for a CSV; a PDF is its size and type.
	out := ok(files(map[string]any{"op": "read", "path": "~/Downloads/statement.csv"}))
	if out["text"] != "date,amount\n2026-10-01,12.50\n" {
		t.Fatalf("read %v", out)
	}
	out = ok(files(map[string]any{"op": "read", "path": filepath.Join(dl, "receipt.pdf")}))
	if out["text"] != nil || out["bytes"] != int64(13) || out["mime"] != "application/pdf" {
		t.Fatalf("read pdf %v", out)
	}
	out = ok(files(map[string]any{"op": "list", "path": "~/Downloads"}))
	if n := len(out["entries"].([]map[string]any)); n != 2 {
		t.Fatalf("list %v", out)
	}

	// Outside the two folders, or climbing out of them.
	refused(files(map[string]any{"op": "read", "path": "/etc/passwd"}))
	refused(files(map[string]any{"op": "read", "path": filepath.Join(outside, "secret.txt")}))
	refused(files(map[string]any{"op": "read", "path": "~/Downloads/../.ssh/id_ed25519"}))
	refused(files(map[string]any{"op": "list", "path": "~"}))

	// A symlink in Downloads that points out of it.
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dl, "link.txt")); err != nil {
			t.Fatal(err)
		}
		refused(files(map[string]any{"op": "read", "path": "~/Downloads/link.txt"}))
	}

	// Write: only under ~/Documents/Jarvis, created 0700, files 0600.
	data := base64.StdEncoding.EncodeToString([]byte("a,b\n1,2\n"))
	refused(files(map[string]any{"op": "write", "path": "~/Downloads/out.csv", "content_b64": data}))
	refused(files(map[string]any{"op": "write", "path": filepath.Join(outside, "out.csv"), "content_b64": data}))
	out = ok(files(map[string]any{"op": "write", "path": "~/Documents/Jarvis/budget.csv", "content_b64": data}))
	jd := filepath.Join(home, "Documents", "Jarvis")
	got, err := os.ReadFile(filepath.Join(jd, "budget.csv"))
	if err != nil || string(got) != "a,b\n1,2\n" {
		t.Fatalf("written %q %v", got, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(jd, "budget.csv")); fi.Mode().Perm() != 0o600 {
			t.Fatalf("file mode %v", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(jd); fi.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v", fi.Mode().Perm())
		}
		// A symlink in the Jarvis folder doesn't let a write out.
		if err := os.Symlink(outside, filepath.Join(jd, "out")); err != nil {
			t.Fatal(err)
		}
		refused(files(map[string]any{"op": "write", "path": "~/Documents/Jarvis/out/x.csv", "content_b64": data}))
		if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(jd, "s.txt")); err != nil {
			t.Fatal(err)
		}
		refused(files(map[string]any{"op": "write", "path": "~/Documents/Jarvis/s.txt", "content_b64": data}))
		if b, _ := os.ReadFile(filepath.Join(outside, "secret.txt")); string(b) != "secret" {
			t.Fatalf("the file outside was overwritten: %q", b)
		}
	}
	// Too big.
	big := base64.StdEncoding.EncodeToString(make([]byte, maxWrite+1))
	out = files(map[string]any{"op": "write", "path": "~/Documents/Jarvis/big.bin", "content_b64": big})
	if out["success"] != false {
		t.Fatalf("big write: %v", out)
	}
}

func TestUnknownToolNotHandled(t *testing.T) {
	tl, _, _ := testTools(t)
	if _, ok := tl.Call(context.Background(), "browser_open", nil, node.Request{}); ok {
		t.Fatal("handled browser_open")
	}
	names := map[string]bool{}
	for _, d := range tl.Available(context.Background()) {
		names[d.Name] = true
	}
	for _, n := range []string{computeruse.ToolObserve, computeruse.ToolAct, computeruse.ToolFiles, computeruse.ToolStop} {
		if !names[n] {
			t.Fatalf("%s not offered: %v", n, names)
		}
	}
}

// set_value or select on a checkbox, radio button or switch sets its state (the extension's
// check op), where a press would toggle it whatever it was.
func TestSetValueChecksABox(t *testing.T) {
	tl, b, _ := testTools(t)
	b.snap.Elements = append(b.snap.Elements,
		computeruse.SnapElement{Ref: "e5", Role: "checkbox", Name: "Send me the monthly newsletter"},
		computeruse.SnapElement{Ref: "e6", Role: "radio", Name: "standard"})
	observe(t, tl, "cr_1")
	rs, _ := act(t, tl, "cr_1", []computeruse.Action{
		{Kind: "set_value", Ref: "e5", Text: "unchecked", Intent: "no newsletter"},
		{Kind: "select", Ref: "e6", Text: "checked", Intent: "the standard plot"},
		{Kind: "set_value", Ref: "e1", Text: "mug", Intent: "search"},
	})
	wantCodes(t, rs, "ok", "ok", "ok")
	if strings.Join(b.acts, "|") != "check e5 unchecked|check e6 checked|set_value e1 mug" {
		t.Fatalf("acts %v", b.acts)
	}
}
