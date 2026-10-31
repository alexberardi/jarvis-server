package computer

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Documents (computer_files doc_read and doc_edit, ELEMENT-LAYER-WORK.md C27): a text document or
// spreadsheet read, created or edited as a file through a headless LibreOffice (office.py), not
// typed through its window. LibreOffice works on a private copy; the result replaces the file
// only when every edit applied, after the original is copied to the backups folder.

const (
	documentsDir = "Documents"
	backupsDir   = "Documents/Jarvis/Backups"
	officeWait   = 2 * time.Minute
)

//go:embed office.py
var officeScript []byte

// docFormat is how LibreOffice handles one file type: a text document or a sheet, saved with filter.
type docFormat struct{ kind, filter string }

var docFormats = map[string]docFormat{
	".odt":  {"text", "writer8"},
	".docx": {"text", "MS Word 2007 XML"},
	".ods":  {"sheet", "calc8"},
	".xlsx": {"sheet", "Calc MS Excel 2007 XML"},
}

// Office runs office.py with req (its src, dst, kind, filter and ops; the runner adds soffice,
// profile and pipe) and returns its answer.
type Office func(ctx context.Context, req map[string]any) (map[string]any, error)

var errDocPath = errors.New("documents are read in ~/Documents and Downloads, and created or edited only in ~/Documents")

// officeMu: one LibreOffice at a time (they share a profile).
var officeMu sync.Mutex

func (t *Tools) office() Office {
	if t.Office != nil {
		return t.Office
	}
	return nativeOffice
}

// docPath is path resolved for a document op: its format, the real path (the folder's
// symlinks resolved; the file itself never a symlink) and whether it exists.
func (t *Tools) docPath(path string, write bool) (f docFormat, p string, exists bool, err error) {
	p, err = t.clean(path)
	if err != nil {
		return f, "", false, err
	}
	f, ok := docFormats[strings.ToLower(filepath.Ext(p))]
	if !ok {
		return f, "", false, fmt.Errorf("%s isn't a document: .odt, .docx, .ods or .xlsx", filepath.Base(p))
	}
	home, err := t.home()
	if err != nil {
		return f, "", false, err
	}
	roots := []string{filepath.Join(home, documentsDir)}
	if !write {
		read, _, err := t.roots()
		if err != nil {
			return f, "", false, err
		}
		roots = append(roots, read...)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		return f, "", false, fmt.Errorf("no folder %s", filepath.Dir(path))
	}
	in := false
	for _, r := range roots {
		if real, err := filepath.EvalSymlinks(r); err == nil && within(dir, real) {
			in = true
		}
	}
	if !in {
		return f, "", false, errDocPath
	}
	p = filepath.Join(dir, filepath.Base(p))
	fi, err := os.Lstat(p)
	switch {
	case err == nil && !fi.Mode().IsRegular():
		return f, "", false, fmt.Errorf("%s isn't a plain file", path)
	case err == nil:
		exists = true
	case !errors.Is(err, os.ErrNotExist):
		return f, "", false, err
	}
	return f, p, exists, nil
}

func docFail(err error) map[string]any {
	if errors.Is(err, errDocPath) || errors.Is(err, errPath) {
		return failCode("path_refused", "%v", err)
	}
	return failCode("invalid_argument", "%v", err)
}

// docRead is a document's content: a text document's paragraphs ({style, text}) or a
// spreadsheet's sheets ({name, rows}; formulas as written, "=SUM(B2:B3)").
func (t *Tools) docRead(ctx context.Context, path string) map[string]any {
	f, p, exists, err := t.docPath(path, false)
	if err != nil {
		return docFail(err)
	}
	if !exists {
		return failCode("not_found", "no file %s", path)
	}
	out, err := t.runOffice(ctx, p, "", f, nil)
	if err != nil {
		return failCode("failed", "couldn't read %s: %v", filepath.Base(p), err)
	}
	res := map[string]any{"success": true, "path": p, "kind": f.kind}
	for _, k := range []string{"text", "sheets"} {
		if v, ok := out[k]; ok {
			res[k] = v
		}
	}
	return res
}

// docEdit applies ops to the document at path, or makes it (create: it must not exist yet),
// starting from a copy of from when given: a readable document of the same kind (a spreadsheet
// someone emailed, totalled into a new file), left untouched.
func (t *Tools) docEdit(ctx context.Context, path string, create bool, from string, ops []any) map[string]any {
	if len(ops) == 0 {
		return failCode("invalid_argument", "ops: no edits")
	}
	f, p, exists, err := t.docPath(path, true)
	if err != nil {
		return docFail(err)
	}
	switch {
	case create && exists:
		return failCode("exists", "%s already exists: edit it, or pick another name", filepath.Base(p))
	case !create && !exists:
		return failCode("not_found", "no file %s", path)
	case from != "" && !create:
		return failCode("invalid_argument", "from: only when creating a document")
	}
	src := p
	if from != "" {
		ff, fp, fexists, err := t.docPath(from, false)
		if err != nil {
			return docFail(err)
		}
		if !fexists {
			return failCode("not_found", "no file %s", from)
		}
		if ff.kind != f.kind {
			return failCode("invalid_argument", "from: %s isn't a %s", filepath.Base(fp), map[string]string{"sheet": "spreadsheet", "text": "text document"}[f.kind])
		}
		src = fp
	}
	// shortcut: a file open in LibreOffice is refused, not merged; handle it when editing open documents is in scope.
	if _, err := os.Lstat(filepath.Join(filepath.Dir(p), ".~lock."+filepath.Base(p)+"#")); err == nil {
		return failCode("file_open", "%s is open in LibreOffice: close it first", filepath.Base(p))
	}
	tmp, err := os.MkdirTemp("", "jarvis-doc-")
	if err != nil {
		return failCode("failed", "%v", err)
	}
	defer os.RemoveAll(tmp)
	dst := filepath.Join(tmp, "out"+filepath.Ext(p))
	out, err := t.runOffice(ctx, src, dst, f, ops)
	if err != nil {
		return failCode("edit_failed", "nothing was changed: %v", err)
	}
	res := map[string]any{"success": true, "path": p, "done": out["done"]}
	mode := os.FileMode(0o600)
	if exists {
		fi, err := os.Stat(p)
		if err != nil {
			return failCode("failed", "%v", err)
		}
		mode = fi.Mode().Perm()
		b, err := t.backup(p)
		if err != nil {
			return failCode("failed", "nothing was changed: couldn't back up %s: %v", filepath.Base(p), err)
		}
		res["backup"] = b
	}
	if err := replaceFile(dst, p, mode); err != nil {
		return failCode("failed", "%v", err)
	}
	return res
}

// runOffice runs office.py on a private copy of src (none: a new document), saving to dst
// when it edits.
func (t *Tools) runOffice(ctx context.Context, src, dst string, f docFormat, ops []any) (map[string]any, error) {
	req := map[string]any{"src": "", "dst": dst, "kind": f.kind, "filter": f.filter, "ops": ops}
	if src != "" {
		if _, err := os.Stat(src); err == nil {
			tmp, err := os.MkdirTemp("", "jarvis-doc-src-")
			if err != nil {
				return nil, err
			}
			defer os.RemoveAll(tmp)
			// The copy, so LibreOffice's lock file and any half-write land in our folder, never
			// beside the user's file.
			c := filepath.Join(tmp, "in"+filepath.Ext(src))
			if err := copyFile(src, c, 0o600); err != nil {
				return nil, err
			}
			req["src"] = c
		}
	}
	ctx, cancel := context.WithTimeout(ctx, officeWait)
	defer cancel()
	officeMu.Lock()
	defer officeMu.Unlock()
	out, err := t.office()(ctx, req)
	if err != nil {
		return nil, err
	}
	if out["ok"] != true {
		return nil, fmt.Errorf("%v", out["error"])
	}
	return out, nil
}

// backup copies p into the backups folder, stamped with the time: an edit is one copy to undo.
func (t *Tools) backup(p string) (string, error) {
	home, err := t.home()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, filepath.FromSlash(backupsDir))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	ext := filepath.Ext(p)
	b := filepath.Join(dir, strings.TrimSuffix(filepath.Base(p), ext)+"-"+time.Now().Format("20060102-150405.000")+ext)
	return b, copyFile(p, b, 0o600)
}

// replaceFile puts src at dst through a temporary file beside dst, so dst is never half-written.
func replaceFile(src, dst string, mode os.FileMode) error {
	tmp := filepath.Join(filepath.Dir(dst), ".jarvis-"+filepath.Base(dst)+".tmp")
	if err := copyFile(src, tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// decodeOffice is office.py's JSON answer.
func decodeOffice(b []byte) (map[string]any, error) {
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("LibreOffice script: %v (%.200s)", err, b)
	}
	return out, nil
}
