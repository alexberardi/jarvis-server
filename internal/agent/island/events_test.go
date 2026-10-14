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
