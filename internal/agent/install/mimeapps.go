package install

import (
	"errors"
	"os"
	"strings"
)

const defaultApps = "[Default Applications]"

// setMimeDefault makes desktop the default handler for mime in a mimeapps.list (XDG MIME
// Applications spec), keeping everything else in the file.
func setMimeDefault(path, mime, desktop string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lines := splitLines(string(b))
	out, inSec, found, done := []string{}, false, false, false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			if inSec && !done {
				out = append(out, mime+"="+desktop+";")
				done = true
			}
			inSec = t == defaultApps
			found = found || inSec
		} else if inSec && keyOf(t) == mime {
			if !done {
				out = append(out, mime+"="+desktop+";")
				done = true
			}
			continue
		}
		out = append(out, l)
	}
	if !found {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, defaultApps)
	}
	if !done {
		out = append(out, mime+"="+desktop+";")
	}
	return writeFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o644)
}

// unsetMimeDefault removes mime's default when it is desktop. A file left with no entries at
// all is removed.
func unsetMimeDefault(path, mime, desktop string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var out []string
	inSec, changed, entries := false, false, 0
	for _, l := range splitLines(string(b)) {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			inSec = t == defaultApps
		} else if k := keyOf(t); k != "" {
			if inSec && k == mime && strings.TrimSuffix(strings.TrimSpace(t[strings.Index(t, "=")+1:]), ";") == desktop {
				changed = true
				continue
			}
			entries++
		}
		out = append(out, l)
	}
	if !changed {
		return nil
	}
	if entries == 0 {
		return os.Remove(path)
	}
	return writeFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o644)
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// keyOf is a "key=value" line's key ("" for anything else).
func keyOf(line string) string {
	if strings.HasPrefix(line, "#") {
		return ""
	}
	k, _, ok := strings.Cut(line, "=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(k)
}
