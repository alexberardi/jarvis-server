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
		{State{VoiceAvailable: true},
			`(Jarvis Automation Agent — Running) | -- | [ ] Listen for "Hey Jarvis" | -- | Quit Jarvis Automation Agent`},
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
		Open: func(u string) { rec("open " + u) },
		Quit: func() { rec("quit") },
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
	tr.clicked(IDOpen) // no URL: nothing
	tr.wait()
	tr.Update(func(s *State) { s.OpenURL = "http://jarvis.example/" })
	tr.clicked(IDOpen)
	tr.wait()
	tr.clicked(IDHeader) // the header does nothing
	tr.clicked(IDQuit)
	tr.wait()
	if strings.Join(got, ", ") != "voice on, voice off, open http://jarvis.example/, quit" {
		t.Fatalf("actions %q", got)
	}
	if len(be.shown) != 3 || !strings.Contains(be.shown[1], "[x] Listen") {
		t.Fatalf("shown %q", be.shown)
	}
	tr.Close()
	tr.Close()
	tr.clicked(IDQuit) // after Close: ignored
	tr.wait()
	if !be.closed || len(got) != 4 {
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
