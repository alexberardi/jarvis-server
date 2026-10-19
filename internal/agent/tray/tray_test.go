package tray

import (
	"strings"
	"sync"
	"testing"
)

func labels(items []Item) string {
	var out []string
	for _, it := range items {
		switch {
		case it.Separator:
			out = append(out, "--")
		case it.Toggle && it.Checked:
			out = append(out, "[x] "+it.Label)
		case it.Toggle:
			out = append(out, "[ ] "+it.Label)
		case !it.Enabled:
			out = append(out, "("+it.Label+")")
		default:
			out = append(out, it.Label)
		}
	}
	return strings.Join(out, " | ")
}

func TestItems(t *testing.T) {
	for _, c := range []struct {
		s    State
		want string
	}{
		{State{Status: "Running", VoiceAvailable: true, Voice: true, OpenURL: "http://x/"},
			`(Jarvis Automation Agent — Running) | -- | [x] Listen for "Hey Jarvis" | Open Jarvis Automation | -- | Quit Jarvis Automation Agent`},
		{State{Status: "Running", VoiceAvailable: true, Voice: true, OpenURL: "http://x/", Setup: true},
			`(Jarvis Automation Agent — Running) | -- | [x] Listen for "Hey Jarvis" | Open Jarvis Automation | Set up / Settings… | -- | Quit Jarvis Automation Agent`},
		{State{Status: "Running", Setup: true}, `(Jarvis Automation Agent — Running) | -- | Set up / Settings… | -- | Quit Jarvis Automation Agent`},
		{State{Status: "Running", Setup: true, SignOut: true},
			`(Jarvis Automation Agent — Running) | -- | Set up / Settings… | Sign out of Jarvis Automation | -- | Quit Jarvis Automation Agent`},
		{State{SignOut: true}, `(Jarvis Automation Agent — Running) | -- | Sign out of Jarvis Automation | -- | Quit Jarvis Automation Agent`},
		{State{VoiceAvailable: true},
			`(Jarvis Automation Agent — Running) | -- | [ ] Listen for "Hey Jarvis" | -- | Quit Jarvis Automation Agent`},
		// The self-update's line sits under the header.
		{State{Status: "Running", Update: "Updated to v0.2.0"},
			`(Jarvis Automation Agent — Running) | (Updated to v0.2.0) | -- | Quit Jarvis Automation Agent`},
		// While Jarvis controls the computer (or records), the menu says so and can stop it.
		{State{Status: "Running", VoiceAvailable: true, Voice: true, Activity: ActivityControlling},
			`(Jarvis Automation Agent — Running) | (Jarvis is controlling this computer) | Stop Jarvis | -- | [x] Listen for "Hey Jarvis" | -- | Quit Jarvis Automation Agent`},
		{State{Status: "Running", Activity: ActivityPaused, Update: "Updated to v0.2.0"},
			`(Jarvis Automation Agent — Running) | (Paused) | Stop Jarvis | (Updated to v0.2.0) | -- | Quit Jarvis Automation Agent`},
		{State{Status: "Running", Activity: ActivityRecording},
			`(Jarvis Automation Agent — Running) | (Recording) | Stop Jarvis | -- | Quit Jarvis Automation Agent`},
		// Computer control: a toggle right after the voice one, off unless turned on.
		{State{Status: "Running", VoiceAvailable: true, Voice: true, ComputerAvailable: true, OpenURL: "http://x/"},
			`(Jarvis Automation Agent — Running) | -- | [x] Listen for "Hey Jarvis" | [ ] Computer control | Open Jarvis Automation | -- | Quit Jarvis Automation Agent`},
		{State{Status: "Running", ComputerAvailable: true, Computer: true},
			`(Jarvis Automation Agent — Running) | -- | [x] Computer control | -- | Quit Jarvis Automation Agent`},
		// Recording: "Record a macro…" right below Computer control whenever nothing is going
		// on, also with computer control off (recording isn't gated by it), and "Stop
		// recording" in the same place while recording.
		{State{Status: "Running", VoiceAvailable: true, Voice: true, ComputerAvailable: true, Computer: true, Record: true, OpenURL: "http://x/"},
			`(Jarvis Automation Agent — Running) | -- | [x] Listen for "Hey Jarvis" | [x] Computer control | Record a macro… | Open Jarvis Automation | -- | Quit Jarvis Automation Agent`},
		{State{Status: "Running", ComputerAvailable: true, Record: true},
			`(Jarvis Automation Agent — Running) | -- | [ ] Computer control | Record a macro… | -- | Quit Jarvis Automation Agent`},
		{State{Status: "Running", ComputerAvailable: true, Computer: true, Record: true, Activity: ActivityControlling},
			`(Jarvis Automation Agent — Running) | (Jarvis is controlling this computer) | Stop Jarvis | -- | [x] Computer control | -- | Quit Jarvis Automation Agent`},
		{State{Status: "Running", ComputerAvailable: true, Record: true, Activity: ActivityRecording},
			`(Jarvis Automation Agent — Running) | (Recording) | Stop Jarvis | -- | [ ] Computer control | Stop recording | -- | Quit Jarvis Automation Agent`},
		// Windows (no voice yet), server URL unknown: only Quit.
		{State{Status: "Running"}, `(Jarvis Automation Agent — Running) | -- | Quit Jarvis Automation Agent`},
	} {
		if got := labels(Items(c.s)); got != c.want {
			t.Errorf("%+v:\n got %s\nwant %s", c.s, got, c.want)
		}
	}
}

// fakeBackend records what the tray showed.
type fakeBackend struct {
	mu     sync.Mutex
	shown  []string
	closed bool
}

func (f *fakeBackend) update(items []Item, tip string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shown = append(f.shown, labels(items))
	return nil
}

func (f *fakeBackend) close() { f.mu.Lock(); f.closed = true; f.mu.Unlock() }

func TestClicksRunActions(t *testing.T) {
	var mu sync.Mutex
	var got []string
	rec := func(s string) { mu.Lock(); got = append(got, s); mu.Unlock() }
	tr := newTray(nil, Actions{
		SetVoice: func(on bool) {
			if on {
				rec("voice on")
			} else {
				rec("voice off")
			}
		},
		Open:     func(u string) { rec("open " + u) },
		Setup:    func() { rec("setup") },
		Quit:     func() { rec("quit") },
		SignOut:  func() { rec("sign out") },
		StopTask: func() { rec("stop task") },
		Record: func(start bool) {
			if start {
				rec("record start")
			} else {
				rec("record stop")
			}
		},
		SetComputer: func(on bool) {
			if on {
				rec("computer on")
			} else {
				rec("computer off")
			}
		},
	}, State{VoiceAvailable: true})
	be := &fakeBackend{}
	if err := tr.attach(be); err != nil {
		t.Fatal(err)
	}
	tr.clicked(IDVoice) // unchecked: turn it on
	tr.wait()
	tr.Update(func(s *State) { s.Voice = true })
	tr.clicked(IDVoice) // checked: turn it off
	tr.wait()
	tr.clicked(IDComputer) // not shown: nothing
	tr.wait()
	tr.Update(func(s *State) { s.ComputerAvailable = true })
	tr.clicked(IDComputer) // unchecked: turn it on
	tr.wait()
	tr.Update(func(s *State) { s.Computer = true })
	tr.clicked(IDComputer) // checked: turn it off
	tr.wait()
	tr.clicked(IDOpen) // no URL: nothing
	tr.wait()
	tr.Update(func(s *State) { s.OpenURL = "http://jarvis.example/" })
	tr.clicked(IDOpen)
	tr.wait()
	tr.clicked(IDSetup) // not shown: nothing
	tr.wait()
	tr.Update(func(s *State) { s.Setup = true })
	tr.clicked(IDSetup)
	tr.wait()
	tr.clicked(IDSignOut) // not shown: nothing
	tr.wait()
	tr.Update(func(s *State) { s.SignOut = true })
	tr.clicked(IDSignOut)
	tr.wait()
	tr.clicked(IDStopTask) // nothing going on: nothing
	tr.wait()
	tr.Update(func(s *State) { s.Activity = ActivityControlling })
	tr.clicked(IDStopTask)
	tr.wait()
	tr.Update(func(s *State) { s.Activity = "" })
	tr.clicked(IDRecord) // not shown: nothing
	tr.wait()
	tr.Update(func(s *State) { s.Record, s.ComputerAvailable, s.Computer = true, true, false })
	tr.clicked(IDRecord) // Record a macro…, with computer control off
	tr.wait()
	tr.Update(func(s *State) { s.Activity = ActivityRecording })
	tr.clicked(IDRecord) // Stop recording
	tr.wait()
	tr.Update(func(s *State) { s.Activity, s.Record = "", false })
	tr.clicked(IDHeader) // the header does nothing
	tr.clicked(IDActivity)
	tr.clicked(IDQuit)
	tr.wait()
	if strings.Join(got, ", ") != "voice on, voice off, computer on, computer off, open http://jarvis.example/, setup, sign out, stop task, record start, record stop, quit" {
		t.Fatalf("actions %q", got)
	}
	if len(be.shown) != 12 || !strings.Contains(be.shown[7], "Stop Jarvis") || strings.Contains(be.shown[8], "Stop Jarvis") || !strings.Contains(be.shown[1], "[x] Listen") ||
		!strings.Contains(be.shown[2], "[ ] Computer control") || !strings.Contains(be.shown[3], "[x] Computer control") || !strings.Contains(be.shown[5], "Set up / Settings…") ||
		!strings.Contains(be.shown[6], "Sign out of Jarvis Automation") {
		t.Fatalf("shown %q", be.shown)
	}
	tr.Close()
	tr.Close()
	tr.clicked(IDQuit) // after Close: ignored
	tr.wait()
	if !be.closed || len(got) != 11 {
		t.Fatalf("closed %v, actions %q", be.closed, got)
	}
}

func TestNilTray(t *testing.T) {
	var tr *Tray
	tr.Update(func(s *State) { s.Voice = true })
	tr.Close()
	if tr.State() != (State{}) {
		t.Fatal("nil tray has a state")
	}
}

func TestIcons(t *testing.T) {
	for _, n := range []int{16, 22, 32, 64} {
		img := Color(n)
		if img.Bounds().Dx() != n || img.Bounds().Dy() != n {
			t.Fatalf("Color(%d) is %v", n, img.Bounds())
		}
		// The shield is centred and opaque in the middle.
		if c := img.NRGBAAt(n/2, n/2); c.A < 200 {
			t.Fatalf("Color(%d) centre %v", n, c)
		}
	}
	tpl := Template(36)
	b := tpl.Bounds()
	if b.Dy() != 36 || b.Dx() < 20 || b.Dx() > 36 {
		t.Fatalf("Template(36) is %v", b)
	}
	// Monochrome: every pixel black, only alpha varies; the shield's edge is opaque and the
	// figure in the middle cut out.
	var opaque, clear int
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := tpl.NRGBAAt(x, y)
			if c.R != 0 || c.G != 0 || c.B != 0 {
				t.Fatalf("template pixel %v isn't black", c)
			}
			switch {
			case c.A > 200:
				opaque++
			case c.A < 40:
				clear++
			}
		}
	}
	if opaque < b.Dx()*b.Dy()/5 || clear < b.Dx()*b.Dy()/5 {
		t.Fatalf("template: %d opaque, %d clear of %d", opaque, clear, b.Dx()*b.Dy())
	}
	if len(PNG(tpl)) < 100 {
		t.Fatal("template PNG too small")
	}
}

// TestTooltipAndIconFollowTheActivity: the tooltip names what Jarvis is doing, and the icon
// gets its badge (Color's active variant) while it does it.
func TestTooltipAndIconFollowTheActivity(t *testing.T) {
	if got := Tooltip(State{Status: "Running"}); got != "Jarvis Automation Agent — Running" {
		t.Fatalf("idle tooltip %q", got)
	}
	if got := Tooltip(State{Status: "Running", Activity: ActivityControlling}); got != "Jarvis is controlling this computer" {
		t.Fatalf("controlling tooltip %q", got)
	}
	if got := Tooltip(State{Activity: ActivityControlling, StopHotkey: "⌃⌥⎋"}); got != "Jarvis is controlling this computer (⌃⌥⎋ stops it)" {
		t.Fatalf("tooltip with the stop hotkey %q", got)
	}
	if got := Tooltip(State{Status: "Running", StopHotkey: "⌃⌥⎋"}); got != "Jarvis Automation Agent — Running" {
		t.Fatalf("idle tooltip with the stop hotkey %q", got)
	}
	if Active(Items(State{})) || !Active(Items(State{Activity: ActivityRecording})) {
		t.Fatal("Active should follow the Stop item")
	}
	for _, n := range []int{16, 22, 32, 64} {
		plain, act := Color(n), ColorActive(n)
		if act.Bounds() != plain.Bounds() {
			t.Fatalf("ColorActive(%d) is %v", n, act.Bounds())
		}
		// The badge sits in the bottom-right corner, in the activity colour; the rest is the
		// plain shield.
		c := act.NRGBAAt(n-1-n/8, n-1-n/8)
		if c.A < 200 || c.R < 200 || c.B > 80 {
			t.Fatalf("ColorActive(%d): no badge at the corner (%v)", n, c)
		}
		if act.NRGBAAt(n/2, n/3) != plain.NRGBAAt(n/2, n/3) {
			t.Fatalf("ColorActive(%d) changed the shield", n)
		}
	}
}
