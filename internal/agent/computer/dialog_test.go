package computer

import "testing"

// A task follows its app's own dialog: LibreOffice's Save dialog is a window of Writer's
// process under another app name ("soffice"). Found live 2026-11-13: every look picked and
// focused Writer's main window again, raising it over the dialog, so the task never saw the
// dialog and pressed Ctrl+S round after round.
func TestFindWindowFollowsTheAppsFocusedDialog(t *testing.T) {
	writer := DesktopWindow{ID: "1", App: "libreoffice-writer", PID: 40, Title: "Untitled 1 — LibreOffice Writer", W: 1280, H: 1440}
	dialog := DesktopWindow{ID: "2", App: "soffice", PID: 40, Title: "Save", W: 900, H: 600, Focused: true}
	other := DesktopWindow{ID: "3", App: "org.gnome.TextEditor", PID: 77, Title: "notes", W: 800, H: 600}

	if w, ok := findWindow([]DesktopWindow{dialog, writer, other}, "LibreOffice Writer"); !ok || w.ID != "2" {
		t.Fatalf("with its dialog focused: %+v", w)
	}
	// The dialog is gone: the main window again.
	writer.Focused = true
	if w, ok := findWindow([]DesktopWindow{writer, other}, "LibreOffice Writer"); !ok || w.ID != "1" {
		t.Fatalf("no dialog: %+v", w)
	}
	// Another app has the focus: never that one, whatever it is.
	writer.Focused, other.Focused = false, true
	if w, ok := findWindow([]DesktopWindow{other, writer}, "LibreOffice Writer"); !ok || w.ID != "1" {
		t.Fatalf("another app focused: %+v", w)
	}
	// No pid known (a backend that can't tell): windows aren't tied together by 0.
	a := DesktopWindow{ID: "4", App: "notes", W: 500, H: 500}
	b := DesktopWindow{ID: "5", App: "soffice", W: 500, H: 500, Focused: true}
	if w, _ := findWindow([]DesktopWindow{b, a}, "notes"); w.ID != "4" {
		t.Fatalf("pid 0: %+v", w)
	}
}
