package browser

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestHubCallRoundTrip(t *testing.T) {
	h, path := startHub(t)
	if h.Connected() {
		t.Fatal("connected before any browser")
	}
	if _, _, err := h.Call(context.Background(), 0, "list_tabs", nil); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("no browser: %v", err)
	}
	f := connectExt(t, h, path, "Firefox", true, func(m Message) Message {
		return ok(map[string]any{"echo": m.Cmd, "args": m.Args})
	})
	res, info, err := h.Call(context.Background(), 0, "list_tabs", map[string]any{"query": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if res["echo"] != "list_tabs" || info.Browser != "Firefox" || info.ExtensionVersion != "0.1.0" {
		t.Fatalf("res %v info %+v", res, info)
	}
	if got := f.received(); len(got) != 1 || got[0].Type != "command" || got[0].Args["query"] != "x" {
		t.Fatalf("extension got %+v", got)
	}
	if bs := h.Browsers(); len(bs) != 1 || bs[0].Origin != "chrome-extension://"+ChromeDevExtensionID+"/" {
		t.Fatalf("browsers %+v", bs)
	}
}

func TestHubCommandError(t *testing.T) {
	h, path := startHub(t)
	connectExt(t, h, path, "Chrome", true, func(Message) Message {
		return Message{OK: false, Code: "tab_not_found", Error: "no tab 9"}
	})
	_, _, err := h.Call(context.Background(), 0, "close_tab", map[string]any{"tab_id": 9})
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Code != "tab_not_found" || ce.Message != "no tab 9" {
		t.Fatalf("err %v", err)
	}
}

func TestHubTimeoutAndDisconnect(t *testing.T) {
	h, path := startHub(t)
	f := connectExt(t, h, path, "Firefox", true, nil) // never answers
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, _, err := h.Call(ctx, 0, "read_page", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	// A pending call fails as soon as the browser goes away, not at its deadline.
	errc := make(chan error, 1)
	go func() {
		_, _, err := h.Call(context.Background(), 0, "read_page", nil)
		errc <- err
	}()
	waitFor(t, func() bool { return len(f.received()) == 2 })
	_ = f.c.Close()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrDisconnected) {
			t.Fatalf("after disconnect: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending call not failed on disconnect")
	}
	waitFor(t, func() bool { return !h.Connected() })
}

func TestHubLateResultIgnored(t *testing.T) {
	h, path := startHub(t)
	f := connectExt(t, h, path, "Firefox", true, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, _, err := h.Call(ctx, 0, "list_tabs", nil)
	cancel()
	if err == nil {
		t.Fatal("expected a timeout")
	}
	// The answer arrives after the caller gave up: dropped, and the next call still works.
	waitFor(t, func() bool { return len(f.received()) == 1 }) // the command reached the fake
	f.send(Message{Type: "result", ID: f.received()[0].ID, OK: true, Result: map[string]any{"late": true}})
	go func() {
		for len(f.received()) < 2 { // the test's own Call below bounds this
			time.Sleep(5 * time.Millisecond)
		}
		f.send(Message{Type: "result", ID: f.received()[1].ID, OK: true, Result: map[string]any{"fresh": true}})
	}()
	res, _, err := h.Call(context.Background(), 0, "list_tabs", nil)
	if err != nil || res["fresh"] != true {
		t.Fatalf("res %v err %v", res, err)
	}
}

func TestHubDefaultFollowsFocus(t *testing.T) {
	h, path := startHub(t)
	now := time.Unix(1000, 0)
	h.Now = func() time.Time { return now }
	ff := connectExt(t, h, path, "Firefox", false, nil)
	now = now.Add(time.Second)
	connectExt(t, h, path, "Chrome", false, nil)
	// Nobody focused, nobody ever was: the newest connection.
	if b, _ := h.Default(); b.Browser != "Chrome" {
		t.Fatalf("default %q, want the newest (Chrome)", b.Browser)
	}
	now = now.Add(time.Second)
	ff.send(Message{Type: "event", Event: "focus", Focused: true})
	waitFor(t, func() bool { b, _ := h.Default(); return b.Browser == "Firefox" })
	// Firefox loses focus to another app: still the one used last.
	now = now.Add(time.Second)
	ff.send(Message{Type: "event", Event: "focus", Focused: false})
	waitFor(t, func() bool {
		for _, b := range h.Browsers() {
			if b.Browser == "Firefox" && !b.Focused {
				return true
			}
		}
		return false
	})
	if b, _ := h.Default(); b.Browser != "Firefox" {
		t.Fatalf("default %q, want the last focused (Firefox)", b.Browser)
	}
}

func TestHubIgnoresNonHostsAndUnreadyHosts(t *testing.T) {
	h, path := startHub(t)
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// A host that never sent the extension's hello isn't a browser yet.
	_, _ = c.Write([]byte(`{"type":"host"}` + "\n"))
	time.Sleep(50 * time.Millisecond)
	if h.Connected() {
		t.Fatal("a host without hello counts as connected")
	}
	// Garbage first lines are dropped.
	c2, _ := Dial(path)
	defer c2.Close()
	_, _ = c2.Write([]byte("not json\n"))
	buf := make([]byte, 1)
	_ = c2.SetReadDeadline(time.Now().Add(10 * time.Second)) // a hang guard
	if _, err := c2.Read(buf); err == nil {
		t.Fatal("garbage connection kept open")
	}
}

func TestBrowserKeys(t *testing.T) {
	h, path := startHub(t)
	connectExt(t, h, path, "Google Chrome", false, nil)
	second := connectExt(t, h, path, "Google Chrome", false, nil) // another profile
	connectExt(t, h, path, "Microsoft Edge", false, nil)
	connectExt(t, h, path, "", false, nil)
	var keys []string
	for _, b := range h.Browsers() {
		keys = append(keys, b.Key)
	}
	if got := strings.Join(keys, ","); got != "chrome,chrome2,edge,browser" {
		t.Fatalf("keys %s", got)
	}
	// Keys are freed on disconnect and reused, so a reconnecting browser keeps its name.
	_ = second.c.Close()
	waitFor(t, func() bool { return len(h.Browsers()) == 3 })
	connectExt(t, h, path, "Google Chrome", false, nil)
	if b, err := h.Lookup("chrome2"); err != nil || b.Browser != "Google Chrome" {
		t.Fatalf("lookup chrome2: %+v %v", b, err)
	}
	if _, err := h.Lookup("safari"); !errors.Is(err, ErrNoSuchBrowser) {
		t.Fatalf("lookup unknown: %v", err)
	}
}

func TestQueryStatus(t *testing.T) {
	h, path := startHub(t)
	connectExt(t, h, path, "Firefox", true, nil)
	st, err := QueryStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Browsers) != 1 || st.Browsers[0].Browser != "Firefox" || !st.Browsers[0].Focused {
		t.Fatalf("status %+v", st)
	}
	// The status connection is not a browser.
	if n := len(h.Browsers()); n != 1 {
		t.Fatalf("%d browsers after status", n)
	}
}

func TestWaitConnected(t *testing.T) {
	h, path := startHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	if h.WaitConnected(ctx) {
		t.Fatal("WaitConnected true with no browser")
	}
	cancel()
	done := make(chan bool, 1)
	go func() { done <- h.WaitConnected(context.Background()) }()
	connectExt(t, h, path, "Firefox", true, nil)
	select {
	case got := <-done:
		if !got {
			t.Fatal("WaitConnected false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitConnected didn't wake on connect")
	}
}

// Website access (the extension's <all_urls> host permission, which Firefox lets the user turn
// off) comes in hello and in "access" events; a change wakes Changed, so the heartbeat reports
// it. An extension that never says (before 0.3.2) leaves it unknown (nil), not off.
// slowHandler is a log handler that takes its time, as a loaded machine's might.
type slowHandler struct{ slog.Handler }

func (slowHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h slowHandler) Handle(ctx context.Context, r slog.Record) error {
	time.Sleep(200 * time.Millisecond)
	return h.Handler.Handle(ctx, r)
}

// A change that has been seen (a browser in Browsers) has already fired Changed: a Changed
// taken after it waits for the next change, however slow the log line between them is. (It
// didn't: TestHubWebsiteAccess saw the Chrome connect's Changed as Firefox's access event.)
func TestHubChangedFiresWithTheChange(t *testing.T) {
	h, path := startHubLog(t, slog.New(slowHandler{slog.DiscardHandler}))
	connectExt(t, h, path, "Chrome", false, nil)
	select {
	case <-h.Changed():
		t.Fatal("Changed fired after the connect was seen, with nothing changed since")
	case <-time.After(500 * time.Millisecond):
	}
}

func TestHubWebsiteAccess(t *testing.T) {
	h, path := startHub(t)
	off := false
	ff := connectExtHello(t, h, path, Message{Type: "hello", Protocol: Protocol, Browser: "Firefox", WebsiteAccess: &off}, nil)
	access := func(browser string) *bool {
		for _, b := range h.Browsers() {
			if b.Browser == browser {
				return b.WebsiteAccess
			}
		}
		t.Fatalf("%s not connected", browser)
		return nil
	}
	if a := access("Firefox"); a == nil || *a {
		t.Fatalf("after hello: website access %s, want off", accessText(a))
	}
	connectExt(t, h, path, "Chrome", false, nil)
	if a := access("Chrome"); a != nil {
		t.Fatalf("an old extension's website access is %v, want unknown", *a)
	}
	// The agent's own (the heartbeat's): off while any browser that says has it off.
	if a := h.WebsiteAccess(); a == nil || *a {
		t.Fatalf("hub website access %s, want off", accessText(a))
	}

	changed := h.Changed()
	on := true
	ff.send(Message{Type: "event", Event: "access", WebsiteAccess: &on})
	select {
	case <-changed:
	case <-time.After(10 * time.Second):
		t.Fatal("Changed didn't fire on an access event")
	}
	if a := access("Firefox"); a == nil || !*a {
		t.Fatalf("after the access event: %s, want on", accessText(a))
	}
	if a := h.WebsiteAccess(); a == nil || !*a {
		t.Fatalf("hub website access %s, want on", accessText(a))
	}

	changed = h.Changed()
	ff.send(Message{Type: "event", Event: "access", WebsiteAccess: &off})
	<-changed
	if a := access("Firefox"); a == nil || *a {
		t.Fatalf("after turning it off: %s, want off", accessText(a))
	}

	// The status connection (jarvis-agent browser status) carries it.
	st, err := QueryStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(st)
	if !strings.Contains(string(b), `"website_access":false`) {
		t.Fatalf("status reply %s has no website_access", b)
	}
}
