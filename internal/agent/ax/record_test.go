package ax

import "testing"

// A recorded element's path runs from its window down, with AXIdentifier where set, stops at
// the application and releases every parent it fetched.
func TestRecordedPath(t *testing.T) {
	var tr tree
	app := tr.n("AXApplication", nil)
	win := tr.n("AXWindow", map[string]string{"AXTitle": "Groceries"})
	split := tr.n("AXSplitGroup", map[string]string{"AXIdentifier": "main/split"})
	area := tr.n("AXTextArea", map[string]string{"AXDescription": "Note body"})
	parents := map[*fakeNode]*fakeNode{area: split, split: win, win: app}
	fetched := 0
	parent := func(n axNode) axNode {
		if p := parents[n.(*fakeNode)]; p != nil {
			fetched++
			return p
		}
		return nil
	}
	got := recordedOf(area, parent)
	if got.Role != "AXTextArea" || got.Name != "Note body" || got.Secure {
		t.Fatalf("identity = %+v", got)
	}
	if want := "AXWindow/AXSplitGroup#main_split/AXTextArea"; got.Path != want {
		t.Fatalf("path = %q, want %q", got.Path, want)
	}
	if tr.released != fetched || fetched != 3 {
		t.Fatalf("released %d of %d parents fetched", tr.released, fetched)
	}
}

func TestRecordedSecureField(t *testing.T) {
	var tr tree
	pw := tr.n("AXTextField", map[string]string{"AXSubrole": "AXSecureTextField", "AXTitle": "Password", "AXValue": "hunter2"})
	got := recordedOf(pw, func(axNode) axNode { return nil })
	if got.Role != "AXSecureTextField" || !got.Secure || got.Path != "AXSecureTextField" {
		t.Fatalf("secure field = %+v", got)
	}
}
