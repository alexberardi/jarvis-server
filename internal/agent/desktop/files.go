package desktop

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/host"
)

// Opening a file by name (docs/decisions/2026-11-13-programs-open-after-a-confirmation.md):
// desktop_open, given a name that is no installed app and no site, looks for a file of that
// name in the user's Documents, Downloads and Desktop. The model names the file; it never
// supplies a path, so a page or a document can't point this at an arbitrary location. Exactly
// one match is opened: a document at once, a program, script or shortcut only after the user
// says yes to a question that names it and its folder. None is "not found"; several are listed
// back, never guessed between.

const (
	fileSearchDepth   = 4     // folders below a root that are looked in
	fileSearchEntries = 50000 // entries looked at before the search gives up
	fileMatches       = 6     // matches kept: more than one is already a question
)

// documentExts are the files that open in an app and run nothing: opened without asking.
// Anything else (a script, a program, a shortcut, a type this list doesn't know) asks first.
var documentExts = map[string]bool{
	".txt": true, ".md": true, ".rtf": true, ".pdf": true, ".csv": true, ".tsv": true, ".json": true, ".xml": true,
	".yaml": true, ".yml": true, ".log": true,
	".doc": true, ".docx": true, ".odt": true, ".pages": true,
	".xls": true, ".xlsx": true, ".ods": true, ".numbers": true,
	".ppt": true, ".pptx": true, ".odp": true, ".key": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true, ".tiff": true, ".heic": true,
	".mp3": true, ".wav": true, ".flac": true, ".m4a": true, ".ogg": true,
	".mp4": true, ".mov": true, ".mkv": true, ".webm": true, ".avi": true,
	".epub": true, ".zip": true,
}

// isDocument reports a file that is opened without asking: a known document type that isn't
// marked executable.
func isDocument(path string, mode fs.FileMode) bool {
	return documentExts[strings.ToLower(filepath.Ext(path))] && mode.IsRegular() && mode.Perm()&0o111 == 0
}

// fileRoots are the folders a name is looked for in: the user's Documents, Downloads and Desktop.
func (t *Tools) fileRoots() []string {
	if t.FileRoots != nil {
		return t.FileRoots
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(home, "Documents"), filepath.Join(home, "Downloads"), filepath.Join(home, "Desktop")}
}

// fileKey is a name as it is compared: lower case, with spaces, dashes and underscores dropped
// ("Budget 2026.xlsx" is said "budget 2026").
func fileKey(s string) string {
	return strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.ToLower(strings.TrimSpace(s)))
}

// kindWords are words people add to a file's name when they say it.
var kindWords = regexp.MustCompile(`\s(my|the|a|file|document|doc|spreadsheet|sheet|workbook|pdf|script|program|photo|picture|image|presentation|slides)\b`)

type foundFile struct {
	path string
	mode fs.FileMode
}

// findFiles are the files under roots named name, with or without its extension. A name with a
// path separator finds nothing: this takes names, never paths. Hidden files and folders are
// skipped.
func findFiles(roots []string, name string) []foundFile {
	if name = strings.TrimSpace(name); name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return nil
	}
	// "my budget spreadsheet" is the file "budget": the kind of file as said is not in its name.
	want, bare := fileKey(name), fileKey(kindWords.ReplaceAllString(" "+strings.ToLower(name)+" ", " "))
	var out []foundFile
	seen := 0
	for _, root := range roots {
		base := strings.Count(filepath.Clean(root), string(filepath.Separator))
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable: skip it
			}
			if seen++; seen > fileSearchEntries || len(out) >= fileMatches {
				return filepath.SkipAll
			}
			if path != root && strings.HasPrefix(d.Name(), ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if strings.Count(path, string(filepath.Separator))-base >= fileSearchDepth {
					return filepath.SkipDir
				}
				return nil
			}
			n := d.Name()
			full, stem := fileKey(n), fileKey(strings.TrimSuffix(n, filepath.Ext(n)))
			if full != want && stem != want && (bare == "" || stem != bare) {
				return nil
			}
			if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
				out = append(out, foundFile{path, info.Mode()})
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// where is a file's folder as the user knows it: from their home folder ("Documents/Budgets").
func where(path string) string {
	dir := filepath.Dir(path)
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, dir); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return filepath.Base(dir)
}

// openFile opens the one file named name: a document at once, anything else after the user's
// yes. handled is false when no file has that name (the caller reports the name as not found).
func (t *Tools) openFile(ctx context.Context, name string) (out map[string]any, handled bool, err error) {
	found := findFiles(t.fileRoots(), name)
	switch len(found) {
	case 0:
		return nil, false, nil
	case 1:
	default:
		var list []string
		for _, f := range found {
			list = append(list, filepath.Base(f.path)+" in "+where(f.path))
		}
		return map[string]any{"success": false, "status": "ambiguous_file", "candidates": list,
			"error": fmt.Sprintf("Nothing was opened: more than one file is called %q (%s). Ask the user which one.", name, strings.Join(list, "; "))}, true, nil
	}
	f := found[0]
	base, folder := filepath.Base(f.path), where(f.path)
	open := func(ctx context.Context) (map[string]any, error) {
		// The path is the one found here, never the model's.
		if err := t.Host.Open(ctx, f.path); err != nil {
			return nil, err
		}
		return map[string]any{"opened": base, "folder": folder, "note": "requested; launch is not confirmed"}, nil
	}
	if isDocument(f.path, f.mode) {
		out, err := open(ctx)
		return out, true, err
	}
	// A program, a script, a shortcut, or a type that isn't known to be a plain document: it
	// runs only on the user's yes, whatever the dictation confirm mode is.
	if t.Confirm == nil {
		return nil, true, host.Errorf(host.ErrInvalidTarget, "%s in %s is a program or script: it can only be run after you confirm, and this agent has no way to ask", base, folder)
	}
	return t.Confirm.Ask(fmt.Sprintf("Run %s from %s?", base, folder), open), true, nil
}
