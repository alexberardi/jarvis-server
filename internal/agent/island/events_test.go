package island

import "testing"

func TestFourCC(t *testing.T) {
	if got := fourCC("aevt"); got != 0x61657674 {
		t.Fatalf("fourCC(aevt) = %#x", got)
	}
	if keyDirectObject != 0x2d2d2d2d {
		t.Fatalf("keyDirectObject = %#x", keyDirectObject)
	}
}

func TestAppEventsDispatch(t *testing.T) {
	got := make(chan string, 1)
	ev := AppEvents{Reopen: func() { got <- "reopen" }, OpenURL: func(u string) { got <- "url " + u }}
	for _, c := range []struct {
		class, id uint32
		url, want string
	}{
		{aeCoreClass, aeReopen, "", "reopen"},
		{aeInternetClass, aeGetURL, "jarvis-agent://start", "url jarvis-agent://start"},
	} {
		if !ev.dispatch(c.class, c.id, c.url) {
			t.Fatalf("%x/%x not handled", c.class, c.id)
		}
		if g := <-got; g != c.want {
			t.Fatalf("got %q, want %q", g, c.want)
		}
	}
	// Someone else's event (open documents) is not ours, and nil handlers are fine.
	if ev.dispatch(aeCoreClass, fourCC("odoc"), "") {
		t.Fatal("odoc handled")
	}
	if !(AppEvents{}).dispatch(aeCoreClass, aeReopen, "") {
		t.Fatal("reopen without a handler should still count as ours")
	}
}

// TestStopHotkey: ⌃⌥⎋ is Escape with control and option, as Carbon has them, and pressing it
// calls StopHotkey on a goroutine (never on the main thread); none set is fine.
func TestStopHotkey(t *testing.T) {
	if stopKeyCode != 53 || stopModifiers != 0x1000|0x0800 {
		t.Fatalf("the stop hotkey is %d/%#x, want Escape (53) with control|option (0x1800)", stopKeyCode, stopModifiers)
	}
	if hotkeySig != 0x6a617276 { // "jarv"
		t.Fatalf("hotkeySig = %#x", hotkeySig)
	}
	stopped := make(chan struct{})
	AppEvents{StopHotkey: func() { close(stopped) }}.hotkey()
	<-stopped
	AppEvents{}.hotkey()
}

// TestStopHotkeyOnlyOnceRegistered: the tooltip names the stop hotkey only once it works.
func TestStopHotkeyOnlyOnceRegistered(t *testing.T) {
	defer stopHotkeyOn.Store(stopHotkeyOn.Load())
	stopHotkeyOn.Store(false)
	if got := StopHotkey(); got != "" {
		t.Fatalf("unregistered stop hotkey is %q, want none", got)
	}
	stopHotkeyOn.Store(true)
	if got := StopHotkey(); got != StopHotkeyLabel {
		t.Fatalf("registered stop hotkey is %q, want %q", got, StopHotkeyLabel)
	}
}
