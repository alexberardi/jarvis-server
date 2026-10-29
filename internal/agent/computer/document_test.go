package computer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// fakeOffice stands for office.py: it records requests, and an edit writes "edited:<src bytes>"
// to dst.
type fakeOffice struct {
	reqs []map[string]any
	fail string
}

func (f *fakeOffice) run(_ context.Context, req map[string]any) (map[string]any, error) {
	f.reqs = append(f.reqs, req)
	if f.fail != "" {
		return map[string]any{"ok": false, "error": f.fail}, nil
	}
	if ops, _ := req["ops"].([]any); len(ops) == 0 {
		return map[string]any{"ok": true, "text": []any{map[string]any{"style": "Standard", "text": "hi"}}}, nil
	}
	var src []byte
	if s, _ := req["src"].(string); s != "" {
		var err error
		if src, err = os.ReadFile(s); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(req["dst"].(string), append([]byte("edited:"), src...), 0o600); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "done": []any{"appended"}}, nil
}

func TestDocumentsAsFiles(t *testing.T) {
	tl, _, _ := testTools(t)
	fo := &fakeOffice{}
	tl.Office = fo.run
	docs := filepath.Join(tl.Home, "Documents")
	if err := errors.Join(os.MkdirAll(filepath.Join(docs, "Budget"), 0o700), os.MkdirAll(filepath.Join(tl.Home, "Downloads"), 0o700)); err != nil {
		t.Fatal(err)
	}
	files := func(args map[string]any) map[string]any { return call(t, tl, computeruse.ToolFiles, args) }
	code := func(out map[string]any, want string) {
		t.Helper()
		if out["success"] != false || out["error_code"] != want {
			t.Fatalf("want %s: %v", want, out)
		}
	}
	ops := []any{map[string]any{"op": "append", "text": "Apollo 11"}}

	// Create, anywhere under ~/Documents; then it exists.
	out := files(map[string]any{"op": "doc_edit", "path": "~/Documents/Budget/apollo.odt", "create": true, "ops": ops})
	if out["success"] != true || out["backup"] != nil {
		t.Fatalf("create: %v", out)
	}
	p := filepath.Join(docs, "Budget", "apollo.odt")
	if b, _ := os.ReadFile(p); string(b) != "edited:" {
		t.Fatalf("created %q", b)
	}
	if fo.reqs[0]["src"] != "" || fo.reqs[0]["kind"] != "text" || fo.reqs[0]["filter"] != "writer8" {
		t.Fatalf("create request %v", fo.reqs[0])
	}
	code(files(map[string]any{"op": "doc_edit", "path": "~/Documents/Budget/apollo.odt", "create": true, "ops": ops}), "exists")

	// Edit: LibreOffice gets a private copy, never the file; the original is backed up first.
	out = files(map[string]any{"op": "doc_edit", "path": "~/Documents/Budget/apollo.odt", "ops": ops})
	if out["success"] != true {
		t.Fatalf("edit: %v", out)
	}
	if src := fo.reqs[1]["src"].(string); src == p || strings.HasPrefix(src, docs) {
		t.Fatalf("office got the user's file %s", src)
	}
	if b, _ := os.ReadFile(p); string(b) != "edited:edited:" {
		t.Fatalf("edited %q", b)
	}
	backup, _ := out["backup"].(string)
	if b, _ := os.ReadFile(backup); string(b) != "edited:" || !strings.HasPrefix(backup, filepath.Join(docs, "Jarvis", "Backups")) {
		t.Fatalf("backup %s %q", backup, b)
	}

	// A failed edit changes nothing and backs nothing up.
	fo.fail = "edit 1: no text 'x' to replace"
	out = files(map[string]any{"op": "doc_edit", "path": "~/Documents/Budget/apollo.odt", "ops": ops})
	code(out, "edit_failed")
	if !strings.Contains(out["error"].(string), "no text 'x'") {
		t.Fatalf("error %v", out)
	}
	if b, _ := os.ReadFile(p); string(b) != "edited:edited:" {
		t.Fatalf("a failed edit wrote %q", b)
	}
	if es, _ := os.ReadDir(filepath.Join(docs, "Jarvis", "Backups")); len(es) != 1 {
		t.Fatalf("backups %d", len(es))
	}
	fo.fail = ""

	// Open in LibreOffice: refused.
	lock := filepath.Join(docs, "Budget", ".~lock.apollo.odt#")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code(files(map[string]any{"op": "doc_edit", "path": "~/Documents/Budget/apollo.odt", "ops": ops}), "file_open")
	os.Remove(lock)

	// Read: Documents and Downloads; the formats only.
	if out := files(map[string]any{"op": "doc_read", "path": "~/Documents/Budget/apollo.odt"}); out["success"] != true || out["text"] == nil {
		t.Fatalf("read: %v", out)
	}
	code(files(map[string]any{"op": "doc_read", "path": "~/Documents/Budget/none.odt"}), "not_found")
	code(files(map[string]any{"op": "doc_edit", "path": "~/Documents/Budget/none.odt", "ops": ops}), "not_found")
	code(files(map[string]any{"op": "doc_edit", "path": "~/Documents/notes.txt", "create": true, "ops": ops}), "invalid_argument")
	code(files(map[string]any{"op": "doc_edit", "path": "~/Documents/a.odt", "create": true}), "invalid_argument")

	// Outside ~/Documents, or climbing out: refused. Downloads is read-only.
	outside := t.TempDir()
	for _, path := range []string{filepath.Join(outside, "x.odt"), "~/x.odt", "~/Documents/../x.odt", "~/Downloads/x.odt"} {
		code(files(map[string]any{"op": "doc_edit", "path": path, "create": true, "ops": ops}), "path_refused")
	}
	code(files(map[string]any{"op": "doc_read", "path": filepath.Join(outside, "x.odt")}), "path_refused")
	if runtime.GOOS != "windows" {
		// A symlinked folder pointing out, and a symlink as the file.
		if err := os.Symlink(outside, filepath.Join(docs, "out")); err != nil {
			t.Fatal(err)
		}
		code(files(map[string]any{"op": "doc_edit", "path": "~/Documents/out/x.odt", "create": true, "ops": ops}), "path_refused")
		if err := os.WriteFile(filepath.Join(outside, "y.odt"), []byte("theirs"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "y.odt"), filepath.Join(docs, "y.odt")); err != nil {
			t.Fatal(err)
		}
		code(files(map[string]any{"op": "doc_edit", "path": "~/Documents/y.odt", "ops": ops}), "invalid_argument")
		if b, _ := os.ReadFile(filepath.Join(outside, "y.odt")); string(b) != "theirs" {
			t.Fatalf("written through a symlink: %q", b)
		}
	}
}

// TestLiveOffice runs office.py on the real LibreOffice, headless with its own profile (no
// window, nothing on the user's screen): JARVIS_OFFICE_LIVE=1.
func TestLiveOffice(t *testing.T) {
	if os.Getenv("JARVIS_OFFICE_LIVE") == "" {
		t.Skip("JARVIS_OFFICE_LIVE=1 runs a headless LibreOffice")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	tl := &Tools{Home: home}
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(home, "Documents"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"apollo.odt", "apollo.docx"} {
		out := tl.docEdit(ctx, "~/Documents/"+name, true, []any{
			map[string]any{"op": "append", "style": "Heading 1", "text": "Apollo 11"},
			map[string]any{"op": "append", "text": "Launched July 16, 1969.\nLanded July 20."},
		})
		if out["success"] != true {
			t.Fatalf("%s create: %v", name, out)
		}
		out = tl.docEdit(ctx, "~/Documents/"+name, false, []any{
			map[string]any{"op": "insert_after", "find": "Launched", "text": "Crew: Armstrong, Aldrin, Collins."},
			map[string]any{"op": "replace", "find": "Landed", "text": "Touched down"},
		})
		if out["success"] != true || out["backup"] == nil {
			t.Fatalf("%s edit: %v", name, out)
		}
		out = tl.docRead(ctx, "~/Documents/"+name)
		var got []string
		for _, p := range out["text"].([]any) {
			m := p.(map[string]any)
			got = append(got, m["style"].(string)+": "+m["text"].(string))
		}
		want := "Heading 1: Apollo 11|Standard: Launched July 16, 1969.|Standard: Crew: Armstrong, Aldrin, Collins.|Standard: Touched down July 20."
		if strings.Join(got, "|") != want {
			t.Fatalf("%s read:\n%s\nwant\n%s", name, strings.Join(got, "|"), want)
		}
	}
	for _, name := range []string{"budget.xlsx", "budget.ods"} {
		out := tl.docEdit(ctx, "~/Documents/"+name, true, []any{
			map[string]any{"op": "add_sheet", "name": "Budget"},
			map[string]any{"op": "append_row", "sheet": "Budget", "values": []any{"Item", "Cost"}},
			map[string]any{"op": "append_row", "sheet": "Budget", "values": []any{"Rent", 1200}},
			map[string]any{"op": "append_row", "sheet": "Budget", "values": []any{"Food", 400.5}},
			map[string]any{"op": "set", "sheet": "Budget", "cell": "B4", "value": "=SUM(B2:B3)"},
		})
		if out["success"] != true {
			t.Fatalf("%s create: %v", name, out)
		}
		out = tl.docEdit(ctx, "~/Documents/"+name, false, []any{map[string]any{"op": "set", "sheet": "Budget", "cell": "B3", "value": 500}})
		if out["success"] != true {
			t.Fatalf("%s edit: %v", name, out)
		}
		out = tl.docRead(ctx, "~/Documents/"+name)
		sheets := out["sheets"].([]any)
		rows := sheets[0].(map[string]any)["rows"].([]any)
		if len(sheets) != 1 || sheets[0].(map[string]any)["name"] != "Budget" || len(rows) != 4 ||
			rows[2].([]any)[1] != "500" || rows[3].([]any)[1] != "=SUM(B2:B3)" {
			t.Fatalf("%s read: %v", name, sheets)
		}
	}
	// A failed edit leaves the file as it was.
	before, _ := os.ReadFile(filepath.Join(home, "Documents", "apollo.odt"))
	out := tl.docEdit(ctx, "~/Documents/apollo.odt", false, []any{map[string]any{"op": "replace", "find": "Saturn", "text": "x"}})
	if out["success"] != false || !strings.Contains(out["error"].(string), "Saturn") {
		t.Fatalf("bad edit: %v", out)
	}
	if after, _ := os.ReadFile(filepath.Join(home, "Documents", "apollo.odt")); string(after) != string(before) {
		t.Fatal("a failed edit changed the file")
	}
	if _, err := os.Stat(filepath.Join(home, "Documents", ".~lock.apollo.odt#")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LibreOffice left a lock beside the file: %v", err)
	}
}
