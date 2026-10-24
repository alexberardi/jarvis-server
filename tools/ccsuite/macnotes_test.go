package main

import (
	"reflect"
	"testing"

	"github.com/alexberardi/jarvis-server/tools/ccsites/notesapp"
)

// A note's text is what the note looks like, not the HTML Notes.app hands out: the checks read
// title and body as the user would.
func TestMacNoteText(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"<div><h1>Groceries</h1></div><div>milk</div><div>eggs</div><div>bread</div>",
			"Groceries\nmilk\neggs\nbread"},
		{"<div>Groceries<br>milk<br/>eggs</div>", "Groceries\nmilk\neggs"},
		{"<div>Tea &amp; biscuits &lt;half&gt;</div>", "Tea & biscuits <half>"},
		{"<div>milk</div><div><br></div><div>eggs</div>", "milk\n\neggs"},
		{"<div>milk</div><div><br></div><div><br></div><div><br></div><div>eggs</div>", "milk\n\neggs"},
		{"<div>a b</div>", "a b"},
		{"plain", "plain"},
		{"", ""},
	} {
		if got := macNoteText(c.body); got != c.want {
			t.Errorf("macNoteText(%q) = %q, want %q", c.body, got, c.want)
		}
	}
}

// The AppleScript output is one record per note, id, name and body separated by control
// characters, so a body's own newlines survive.
func TestParseMacNotes(t *testing.T) {
	out := "x-coredata://A/ICNote/p1" + macFS + "Groceries " + macFS + "<div>Groceries</div><div>milk</div>" + macRS +
		"x-coredata://A/ICNote/p2" + macFS + "Plan" + macFS + "<div>line one</div><div>line two</div>" + macRS + "\n"
	want := []notesapp.Note{
		{Title: "Groceries", Body: "Groceries\nmilk"},
		{Title: "Plan", Body: "line one\nline two"},
	}
	if got := parseMacNotes(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseMacNotes = %+v, want %+v", got, want)
	}
	if got := parseMacNotes("\n"); got != nil {
		t.Fatalf("no notes: %+v", got)
	}
	// A record the script could not fill in is left out rather than scored as an empty note.
	if got := parseMacNotes("id-only" + macRS); got != nil {
		t.Fatalf("short record: %+v", got)
	}
}

// Only the titles the case's own checks name may be deleted from the Mac's Notes.
func TestNoteTitles(t *testing.T) {
	c := Case{Checks: []Check{
		{Kind: "note_exists", Title: "Groceries", Body: "eggs"},
		{Kind: "note_exists", Title: "Groceries", Body: "milk"},
		{Kind: "reply_matches", Pattern: "(?i)groceries"},
		{Kind: "note_exists", Title: "Packing"},
	}}
	if got, want := noteTitles(c), []string{"Groceries", "Packing"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("noteTitles = %v, want %v", got, want)
	}
	if got := noteTitles(Case{Checks: []Check{{Kind: "reply_matches", Pattern: "x"}}}); got != nil {
		t.Fatalf("a case with no note check names no title: %v", got)
	}
}
