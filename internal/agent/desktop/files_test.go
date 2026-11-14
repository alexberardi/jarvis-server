package desktop

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// fileTools is the tools over a fake host, with the user's folders under a temp dir: nothing
// real is ever opened.
func fileTools(t *testing.T, files map[string]os.FileMode) (*Tools, *fakeBackend, string) {
	t.Helper()
	root := t.TempDir()
	for name, mode := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
	}
	b := &fakeBackend{caps: all}
	ts := New(host.New(b), OpenPolicy{}, catalogue)
	ts.FileRoots = []string{filepath.Join(root, "Documents"), filepath.Join(root, "Downloads"), filepath.Join(root, "Desktop")}
	ts.Confirm = &dictate.Confirmer{Mode: dictate.ConfirmOff} // a program asks whatever the mode
	return ts, b, root
}

func openTarget(t *testing.T, ts *Tools, target string) map[string]any {
	t.Helper()
	out, _ := ts.Call(context.Background(), ToolOpen, map[string]any{"target": target}, node.Request{})
	return out
}

// A document found by name is opened at once, by the path found here.
func TestOpenDocumentByName(t *testing.T) {
	ts, b, root := fileTools(t, map[string]os.FileMode{
		"Documents/Budgets/Budget 2026.xlsx": 0o644,
		"Documents/notes.md":                 0o644,
		"Downloads/.hidden/budget 2026.xlsx": 0o644,
	})
	for _, said := range []string{"budget 2026", "Budget 2026.xlsx", "budget-2026", "my budget 2026 spreadsheet", "the budget 2026 file"} {
		b.opened = nil
		out := openTarget(t, ts, said)
		want := filepath.Join(root, "Documents", "Budgets", "Budget 2026.xlsx")
		if out["opened"] != "Budget 2026.xlsx" || len(b.opened) != 1 || b.opened[0] != want {
			t.Fatalf("%q: %v, opened %v", said, out, b.opened)
		}
	}
	// A name that also reads as a site is the user's file when they have one.
	b.opened = nil
	if out := openTarget(t, ts, "notes.md"); out["opened"] != "notes.md" || !strings.HasSuffix(b.opened[0], "notes.md") {
		t.Fatalf("notes.md: %v %v", out, b.opened)
	}
}

// A program, a script or a shortcut runs only after the user's yes; "no" runs nothing.
func TestRunProgramByNameAsksFirst(t *testing.T) {
	ts, b, _ := fileTools(t, map[string]os.FileMode{
		"Documents/backup.sh":    0o755,
		"Downloads/report.csv":   0o755, // a document marked executable is not just a document
		"Desktop/setup.AppImage": 0o644,
	})
	cases := map[string]string{
		"backup":         "Run backup.sh from Documents?",
		"setup.AppImage": "Run setup.AppImage from Desktop?",
	}
	if runtime.GOOS != "windows" {
		// Windows has no executable mark: there a .csv only ever opens in its app.
		cases["report.csv"] = "Run report.csv from Downloads?"
	}
	for name, question := range cases {
		b.opened = nil
		out := openTarget(t, ts, name)
		if out["status"] != "awaiting_confirmation" || !strings.HasSuffix(out["question"].(string), question[4:]) || len(b.opened) != 0 {
			t.Fatalf("%s: %v, opened %v", name, out, b.opened)
		}
		id := out["confirmation_id"].(string)
		if name == "backup" {
			if _, err := ts.Confirm.Answer(context.Background(), id, "no"); err != nil {
				t.Fatal(err)
			}
			if len(b.opened) != 0 {
				t.Fatalf("ran after a no: %v", b.opened)
			}
			continue
		}
		if _, err := ts.Confirm.Answer(context.Background(), id, "yes"); err != nil || len(b.opened) != 1 {
			t.Fatalf("%s after yes: %v, opened %v", name, err, b.opened)
		}
	}
	// With no way to ask, nothing runs.
	ts.Confirm, b.opened = nil, nil
	if out := openTarget(t, ts, "backup"); out["success"] != false || len(b.opened) != 0 {
		t.Fatalf("no confirmer: %v, opened %v", out, b.opened)
	}
}

// Several files of one name are listed back, never guessed between; a path is never taken.
func TestOpenFileNeverGuessesOrTakesAPath(t *testing.T) {
	ts, b, root := fileTools(t, map[string]os.FileMode{
		"Documents/budget.xlsx":   0o644,
		"Downloads/Budget.xlsx":   0o644,
		"Documents/Taxes/tax.pdf": 0o644,
	})
	out := openTarget(t, ts, "budget")
	if out["status"] != "ambiguous_file" || len(out["candidates"].([]string)) != 2 || len(b.opened) != 0 {
		t.Fatalf("ambiguous: %v, opened %v", out, b.opened)
	}
	for _, path := range []string{filepath.Join(root, "Documents", "Taxes", "tax.pdf"), "Taxes/tax.pdf", "../Documents/Taxes/tax.pdf", "file://" + root + "/Documents/Taxes/tax.pdf"} {
		if out := openTarget(t, ts, path); len(b.opened) != 0 || out["opened"] != nil {
			t.Fatalf("a path was opened: %q -> %v %v", path, out, b.opened)
		}
	}
	if got := findFiles(ts.FileRoots, "tax"); len(got) != 1 {
		t.Fatalf("by name: %v", got)
	}
}
