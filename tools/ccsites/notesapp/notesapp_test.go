package notesapp

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNotesState(t *testing.T) {
	got, err := NotesState(filepath.Join("testdata", "notes.json"))
	if err != nil {
		t.Fatalf("NotesState: %v", err)
	}
	want := []Note{
		{Title: "Groceries", Body: "milk\neggs\nbread"},
		{Title: "Call", Body: "dentist, Tuesday"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NotesState = %#v, want %#v", got, want)
	}

	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{"notes": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := NotesState(empty); err != nil || len(got) != 0 {
		t.Errorf("empty list: got %v, %v; want [], nil", got, err)
	}

	if _, err := NotesState(filepath.Join(dir, "missing.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: err = %v, want fs.ErrNotExist", err)
	}

	for name, body := range map[string]string{
		"invalid.json":   `{"notes": [`,
		"nolist.json":    `{"other": 1}`,
		"wrongtype.json": `{"notes": "x"}`,
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := NotesState(p); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
