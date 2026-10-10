// Package apps is the catalogue of installed applications the agent may launch, and the
// resolution of a spoken name ("Steam", "spotify") to exactly one of them.
//
// Policy: "anything in your apps folder can run". By default the catalogue is every application
// in the platform's app locations; a user-edited config file can add glob patterns and deny
// entries. The model only ever supplies a name. It never supplies a path or arguments: what
// gets launched is always a catalogue entry's own path.
//
// Every scanner is plain file walking over injectable roots, so the whole package runs and is
// tested on every OS with fake app directories.
package apps

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Kind is what a catalogue entry is, which decides how it is scanned and launched.
type Kind string

const (
	MacApp      Kind = "app"     // a macOS .app bundle (a directory)
	DesktopFile Kind = "desktop" // a freedesktop .desktop entry
	Shortcut    Kind = "lnk"     // a Windows Start Menu shortcut
)

// App is one launchable application.
type App struct {
	Name string `json:"name"`
	Path string `json:"path"` // what gets launched; comes from the scan, never from the model
	Kind Kind   `json:"-"`
	ID   string `json:"-"` // desktop-file id (e.g. "steam"), else ""
}

// Source is one glob of app paths. "**" matches any number of directories (including none),
// "*" and "?" match within one path element. Matching is case-insensitive.
type Source struct {
	Pattern string
	Kind    Kind
}

// maxDepth bounds a "**" walk, so a pattern rooted at a big tree can't scan the whole disk.
const maxDepth = 6

// Catalog lists installed apps from Sources, minus Deny. It is computed per call: apps get
// installed and removed while the agent runs, and the config file may be edited.
type Catalog struct {
	Sources []Source
	Deny    []string // globs matched against the app name or its path, case-insensitive
}

// globRegexp converts a glob to an anchored, case-insensitive regexp over slash paths.
func globRegexp(glob string) (*regexp.Regexp, error) {
	g := filepath.ToSlash(glob)
	var b strings.Builder
	b.WriteString("(?i)^")
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch {
		case c == '*' && i+1 < len(g) && g[i+1] == '*':
			// "**/" matches zero or more whole directories; a trailing "**" matches anything.
			if i+2 < len(g) && g[i+2] == '/' {
				b.WriteString("(?:.*/)?")
				i += 2
			} else {
				b.WriteString(".*")
				i++
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// staticRoot is the longest leading part of a glob with no wildcard: where the walk starts.
func staticRoot(glob string) string {
	g := filepath.ToSlash(glob)
	cut := strings.IndexAny(g, "*?[")
	if cut < 0 {
		return filepath.FromSlash(g)
	}
	dir := g[:cut]
	if i := strings.LastIndex(dir, "/"); i >= 0 {
		dir = dir[:i]
	} else {
		dir = "."
	}
	if dir == "" {
		dir = "/"
	}
	return filepath.FromSlash(dir)
}

// scan walks one source and returns matching paths in walk order.
func scan(src Source) ([]string, error) {
	re, err := globRegexp(src.Pattern)
	if err != nil {
		return nil, fmt.Errorf("bad pattern %q: %w", src.Pattern, err)
	}
	root := staticRoot(src.Pattern)
	base := strings.Count(filepath.ToSlash(filepath.Clean(root)), "/")
	var out []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return fs.SkipDir // a missing root is just an empty source
			}
			return nil // unreadable subtrees are skipped, not fatal
		}
		slash := filepath.ToSlash(p)
		if d.IsDir() && p != root {
			if strings.Count(slash, "/")-base > maxDepth {
				return fs.SkipDir
			}
		}
		matched := re.MatchString(slash)
		if matched && (src.Kind == MacApp) == d.IsDir() {
			out = append(out, p)
		}
		// Never descend into an app bundle: they nest helper .apps that aren't user apps.
		if d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".app") && p != root {
			return fs.SkipDir
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return out, err
}

// List returns the catalogue, sorted by name. The first source that yields a name wins, so
// earlier sources (and, for desktop files, earlier XDG dirs) take precedence.
func (c Catalog) List() ([]App, error) {
	var deny []*regexp.Regexp
	for _, d := range c.Deny {
		re, err := globRegexp(d)
		if err != nil {
			return nil, fmt.Errorf("bad deny pattern %q: %w", d, err)
		}
		deny = append(deny, re)
	}
	denied := func(a App) bool {
		for _, re := range deny {
			if re.MatchString(a.Name) || re.MatchString(filepath.ToSlash(a.Path)) {
				return true
			}
		}
		return false
	}
	seenName := map[string]bool{}
	seenID := map[string]bool{}
	var out []App
	for _, src := range c.Sources {
		paths, err := scan(src)
		if err != nil {
			return nil, err
		}
		for _, p := range paths {
			a, ok := entry(src, p)
			// A desktop id seen in an earlier XDG dir shadows later copies, including when the
			// earlier copy is hidden: that is how a user hides a system app.
			if a.ID != "" {
				if seenID[a.ID] {
					continue
				}
				seenID[a.ID] = true
			}
			if !ok || denied(a) {
				continue
			}
			k := norm(a.Name)
			if k == "" || seenName[k] {
				continue
			}
			seenName[k] = true
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// entry turns a scanned path into an App, or false if it isn't a launchable, visible app.
func entry(src Source, p string) (App, bool) {
	base := filepath.Base(p)
	switch src.Kind {
	case MacApp:
		return App{Name: strings.TrimSuffix(base, filepath.Ext(base)), Path: p, Kind: MacApp}, true
	case Shortcut:
		name := strings.TrimSuffix(base, filepath.Ext(base))
		return App{Name: name, Path: p, Kind: Shortcut}, true
	case DesktopFile:
		// The desktop-file id is the path below the applications dir, "/" turned into "-".
		id := strings.TrimSuffix(base, ".desktop")
		if rel, err := filepath.Rel(staticRoot(src.Pattern), p); err == nil {
			id = strings.ReplaceAll(filepath.ToSlash(strings.TrimSuffix(rel, ".desktop")), "/", "-")
		}
		de, err := parseDesktop(p)
		if err != nil || !de.launchable() {
			return App{Kind: DesktopFile, ID: id}, false
		}
		name := de.name
		if name == "" {
			name = id
		}
		return App{Name: name, Path: p, Kind: DesktopFile, ID: id}, true
	}
	return App{}, false
}

type desktopEntry struct {
	name, typ, exec   string
	noDisplay, hidden bool
}

func (d desktopEntry) launchable() bool {
	return (d.typ == "" || d.typ == "Application") && d.exec != "" && !d.noDisplay && !d.hidden
}

// parseDesktop reads the [Desktop Entry] group: the unlocalised Name, Type, Exec, NoDisplay
// and Hidden. Localised keys (Name[de]) are ignored.
func parseDesktop(p string) (desktopEntry, error) {
	f, err := os.Open(p)
	if err != nil {
		return desktopEntry{}, err
	}
	defer f.Close()
	var d desktopEntry
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			in = line == "[Desktop Entry]"
			continue
		}
		if !in || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "Name":
			d.name = v
		case "Type":
			d.typ = v
		case "Exec":
			d.exec = v
		case "NoDisplay":
			d.noDisplay = strings.EqualFold(v, "true")
		case "Hidden":
			d.hidden = strings.EqualFold(v, "true")
		}
	}
	return d, sc.Err()
}
