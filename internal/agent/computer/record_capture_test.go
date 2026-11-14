package computer

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/ax"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// fakeScreen is the windows on screen and the front one's elements.
type fakeScreen struct {
	windows []DesktopWindow
	tree    []computeruse.SnapElement
	treeErr error
	pid     int      // whose tree to give (0: the first window's)
	calls   []string // what the recorder asked, in order
}

func (f *fakeScreen) wire(p *DeskPlatform) {
	p.Windows = func(context.Context) ([]DesktopWindow, error) { return f.windows, nil }
	p.Elements = func(_ context.Context, pid, _ int) ([]computeruse.SnapElement, error) {
		f.calls = append(f.calls, "tree")
		if want := cmp.Or(f.pid, f.windows[0].PID); pid != want {
			return nil, fmt.Errorf("asked for pid %d, not %d", pid, want)
		}
		return f.tree, f.treeErr
	}
	at := p.ElementAt
	p.ElementAt = func(x, y float64) (*ax.Recorded, error) {
		f.calls = append(f.calls, "element")
		return at(x, y)
	}
}

func els(named ...string) []computeruse.SnapElement {
	var out []computeruse.SnapElement
	for _, n := range named {
		role, name, _ := strings.Cut(n, ":")
		out = append(out, computeruse.SnapElement{Role: role, Name: name})
	}
	return out
}

func resultLine(e DesktopEvent) string {
	var a, d []string
	for _, x := range e.Appeared {
		a = append(a, x.Role+":"+x.Name)
	}
	for _, x := range e.Disappeared {
		d = append(d, x.Role+":"+x.Name)
	}
	return fmt.Sprintf("of %d in %q +[%s] -[%s]", e.Of, e.Window.Title, strings.Join(a, " "), strings.Join(d, " "))
}

func captureEnv(t *testing.T) (*deskEnv, *fakeScreen) {
	e := newDeskEnv(t)
	scr := &fakeScreen{
		windows: []DesktopWindow{
			{App: "Notes", PID: 42, Title: "All Notes", W: 800, H: 600},
			{App: "Dock", PID: 1, Title: "Dock", Layer: 20, W: 800, H: 60},
			{App: "Files", PID: 43, Title: "budget-2026-09.xlsx", W: 800, H: 600},
		},
		tree: els("button:New Note", "list:Notes", "button:"),
	}
	scr.wire(&e.rec.p)
	return e, scr
}

func TestRecordingStartsWithTheOpenWindows(t *testing.T) {
	e, _ := captureEnv(t)
	e.rec.begin()
	evs := e.events(t)
	if len(evs) != 1 || evs[0].Kind != KindStart || evs[0].Seq != 0 {
		t.Fatalf("events = %+v", evs)
	}
	want := []StartWindow{{App: "Notes", Title: "All Notes", Focused: true}, {App: "Files", Title: "budget-2026-09.xlsx"}}
	if fmt.Sprint(evs[0].Windows) != fmt.Sprint(want) {
		t.Fatalf("windows = %+v, want %+v (panels left out, the front one focused)", evs[0].Windows, want)
	}
}

// A click's result is what the front window gained and lost, read when the next input comes
// or the recording ends; typing owes none; the actions keep their numbering.
func TestResultFollowsEachAction(t *testing.T) {
	e, scr := captureEnv(t)
	e.rec.begin()
	e.desk.at[[2]float64{10, 10}] = newNoteBtn
	e.rec.handle(DeskInput{Kind: InClick, X: 10, Y: 10, At: e.desk.tick(time.Second)})

	scr.windows[0].Title = "New Note"
	scr.tree = els("button:New Note", "text:Title", "text:Note body")
	e.desk.focus(notesBody)
	e.typeText("hi")
	e.press(KeyEnter)

	scr.tree = els("button:New Note", "text:Title", "text:Note body", "button:Done")
	e.rec.finish()

	evs := e.events(t)[1:]
	if got := summary(evs); got != `click New Note; result; input Note body "hi"; key Note body [Enter]; result` {
		t.Fatalf("events: %s", got)
	}
	if evs[0].Seq != 1 || evs[2].Seq != 2 || evs[3].Seq != 3 || evs[1].Seq != 0 {
		t.Fatalf("seqs: %d %d %d %d", evs[0].Seq, evs[1].Seq, evs[2].Seq, evs[3].Seq)
	}
	if got, want := resultLine(evs[1]), `of 1 in "New Note" +[text:Title text:Note body] -[list:Notes]`; got != want {
		t.Fatalf("click's result: %s, want %s", got, want)
	}
	if got, want := resultLine(evs[4]), `of 3 in "New Note" +[button:Done] -[]`; got != want {
		t.Fatalf("key's result: %s, want %s", got, want)
	}
}

// A result is read from the app the action was in, whatever the window list puts first (a
// Mac listed another app's window ahead of the one in use).
func TestResultIsFromTheActedOnApp(t *testing.T) {
	e, scr := captureEnv(t)
	e.rec.begin()
	files := &ax.Recorded{Role: "AXCell", Name: "budget", PID: 43, App: "com.apple.finder"}
	e.desk.at[[2]float64{5, 5}] = files
	scr.pid = 43
	scr.tree = els("cell:budget", "button:Open")
	e.rec.handle(DeskInput{Kind: InClick, X: 5, Y: 5, At: e.desk.tick(time.Second)})
	e.rec.finish()
	evs := e.events(t)
	last := evs[len(evs)-1]
	if last.Kind != KindResult || last.App != "Files" || last.Window.Title != "budget-2026-09.xlsx" {
		t.Fatalf("result read from %+v, want the Files window", last)
	}
	// The window before was another app's: its elements aren't reported as gone.
	if got, want := resultLine(last), `of 1 in "budget-2026-09.xlsx" +[cell:budget button:Open] -[]`; got != want {
		t.Fatalf("result: %s, want %s", got, want)
	}
}

// A click's own element is looked up before any tree is read: the click has already landed, and
// a read ahead of it would find the screen after it. A click followed at once by another gets
// no result of its own; the next one covers both.
func TestQuickClicksAreNotDelayedByAResult(t *testing.T) {
	e, scr := captureEnv(t)
	e.rec.begin()
	e.desk.at[[2]float64{10, 10}] = newNoteBtn
	e.rec.handle(DeskInput{Kind: InClick, X: 10, Y: 10, At: e.desk.tick(time.Second)})
	scr.calls = nil
	scr.tree = els("button:New Note", "list:Notes", "text:Title")
	e.rec.handle(DeskInput{Kind: InClick, X: 10, Y: 10, At: e.desk.tick(50 * time.Millisecond)})
	if got := strings.Join(scr.calls, " "); got != "element" {
		t.Fatalf("the second click asked %q; want only its own element", got)
	}
	e.rec.finish()
	evs := e.events(t)[1:]
	if got := summary(evs); got != "click New Note; click New Note; result" {
		t.Fatalf("events: %s", got)
	}
	if got, want := resultLine(evs[2]), `of 2 in "All Notes" +[text:Title] -[]`; got != want {
		t.Fatalf("result: %s, want %s", got, want)
	}
}

// A second element with the same role and name counts as appeared, and one of two going counts
// as disappeared.
func TestResultCountsSameNamedElements(t *testing.T) {
	e, scr := captureEnv(t)
	scr.tree = els("button:Delete", "button:Save")
	e.rec.begin()
	e.rec.handle(DeskInput{Kind: InClick, NoPoint: true, At: e.desk.tick(time.Second)})
	scr.tree = els("button:Delete", "button:Save", "button:Delete")
	e.rec.result()
	e.rec.handle(DeskInput{Kind: InClick, NoPoint: true, At: e.desk.tick(time.Second)})
	scr.tree = els("button:Delete", "button:Save")
	e.rec.result()
	evs := e.events(t)
	if got, want := resultLine(evs[2]), `of 1 in "All Notes" +[button:Delete] -[]`; got != want {
		t.Fatalf("a second Delete: %s, want %s", got, want)
	}
	if got, want := resultLine(evs[4]), `of 2 in "All Notes" +[] -[button:Delete]`; got != want {
		t.Fatalf("one Delete gone: %s, want %s", got, want)
	}
}

// A read that hit the cap can't say what disappeared: it may only have moved past the cap.
func TestCappedReadReportsNothingGone(t *testing.T) {
	e, scr := captureEnv(t)
	var rows []string
	for i := range captureTree {
		rows = append(rows, fmt.Sprintf("cell:row %d", i))
	}
	scr.tree = els(rows...)
	e.rec.begin()
	e.rec.handle(DeskInput{Kind: InClick, NoPoint: true, At: e.desk.tick(time.Second)})
	scr.tree = els(append([]string{"cell:inserted"}, rows[:captureTree-1]...)...)
	e.rec.finish()
	evs := e.events(t)
	if got, want := resultLine(evs[len(evs)-1]), `of 1 in "All Notes" +[cell:inserted] -[]`; got != want {
		t.Fatalf("result: %s, want %s", got, want)
	}
}

// A click on something with no window of its own (the Dock) reads the app that has the
// keyboard focus afterwards, not whatever the window list puts first.
func TestResultAfterAClickWithNoWindow(t *testing.T) {
	e, scr := captureEnv(t)
	e.rec.begin()
	e.desk.at[[2]float64{5, 5}] = &ax.Recorded{Role: "AXDockItem", Name: "Files", PID: 1, App: "com.apple.dock"}
	e.rec.handle(DeskInput{Kind: InClick, X: 5, Y: 5, At: e.desk.tick(time.Second)})
	e.desk.focus(&ax.Recorded{Role: "AXOutline", PID: 43, App: "com.apple.finder"})
	scr.pid = 43
	scr.tree = els("cell:budget")
	e.rec.finish()
	evs := e.events(t)
	if last := evs[len(evs)-1]; last.Kind != KindResult || last.App != "Files" {
		t.Fatalf("result read from %+v, want the Files window", last)
	}
}

// Without a tree (an app off the accessibility bus) the result still says which window is in front.
func TestResultWithoutATree(t *testing.T) {
	e, scr := captureEnv(t)
	scr.treeErr = errors.New("no accessibility")
	e.rec.begin()
	e.rec.handle(DeskInput{Kind: InClick, NoPoint: true, At: e.desk.tick(time.Second)})
	scr.windows[0].Title = "Untitled"
	e.rec.finish()
	evs := e.events(t)
	last := evs[len(evs)-1]
	if got, want := resultLine(last), `of 1 in "Untitled" +[] -[]`; got != want {
		t.Fatalf("result: %s, want %s", got, want)
	}
}

// Results are capped each and in all; the actions are always kept.
func TestResultsAreCapped(t *testing.T) {
	e, scr := captureEnv(t)
	e.rec.begin()
	long := strings.Repeat("x", 300)
	const clicks = 60 // each result is about 9 KB uncapped by the budget: twice the budget in all
	for c := range clicks {
		var tree []string
		for i := range 3 * resultElements {
			tree = append(tree, fmt.Sprintf("button:%d %d %s", c, i, long))
		}
		scr.tree = els(tree...)
		e.rec.handle(DeskInput{Kind: InClick, NoPoint: true, At: e.desk.tick(time.Second)})
		e.rec.result() // as the settle wait does
	}
	e.rec.finish()
	total, got := 0, 0
	for i, raw := range e.raw {
		ev := e.events(t)[i]
		switch ev.Kind {
		case InClick:
			got++
		case KindResult:
			total += len(raw)
			if len(ev.Appeared) > resultElements || len(ev.Disappeared) > resultElements {
				t.Fatalf("a result names %d and %d elements", len(ev.Appeared), len(ev.Disappeared))
			}
			for _, x := range ev.Appeared {
				if len([]rune(x.Name)) > resultNameMax {
					t.Fatalf("name of %d runes", len([]rune(x.Name)))
				}
			}
		}
	}
	if got != clicks {
		t.Fatalf("%d of %d clicks kept", got, clicks)
	}
	if total > resultBudget || total < resultBudget/2 {
		t.Fatalf("results take %d bytes; the budget is %d", total, resultBudget)
	}
}

// The worker reads the result once the screen has been quiet, without waiting for more input.
func TestResultAfterTheSettleWait(t *testing.T) {
	desk := newFakeDesk()
	scr := &fakeScreen{windows: []DesktopWindow{{App: "Notes", PID: 42, Title: "All Notes", W: 800, H: 600}}, tree: els("button:New Note")}
	p := desk.platform()
	scr.wire(&p)
	var send func(DeskInput)
	quiet := make(chan time.Time)
	waits := make(chan time.Duration, 4)
	out := make(chan DesktopEvent, 8)
	src := &DesktopSource{Dir: t.TempDir(), Platform: p, now: desk.now,
		Tap:   func(s func(DeskInput)) (func(), error) { send = s; return func() {}, nil },
		after: func(d time.Duration) <-chan time.Time { waits <- d; return quiet }}
	emit := func(raw []byte) {
		var e DesktopEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Error(err)
		}
		out <- e
	}
	if err := src.Start(context.Background(), "rec_1", func(b json.RawMessage) { emit(b) }); err != nil {
		t.Fatal(err)
	}
	if e := <-out; e.Kind != KindStart {
		t.Fatalf("first event %q", e.Kind)
	}
	send(DeskInput{Kind: InClick, NoPoint: true, At: desk.tick(time.Second)})
	if e := <-out; e.Kind != InClick {
		t.Fatalf("second event %q", e.Kind)
	}
	if d := <-waits; d != resultSettle {
		t.Fatalf("waits %v", d)
	}
	scr.tree = els("button:New Note", "text:Title")
	quiet <- time.Time{}
	if e := <-out; e.Kind != KindResult || e.Of != 1 || len(e.Appeared) != 1 {
		t.Fatalf("after the wait: %+v", e)
	}
	if err := src.Stop(context.Background(), "rec_1"); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-out:
		t.Fatalf("a second result for one action: %+v", e)
	default:
	}
}
