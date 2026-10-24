package main

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/tools/ccsites/notesapp"
)

// Apple's Notes.app is the desktop-mac case's fixture: the Mac test host has no headless
// session, so the case runs attended in the user's own session against the real app
// (docs/dev/computer-suite.md). The suite therefore touches as little of it as it can: it
// deletes only notes with the case's own title, and scores only the notes that appeared while
// the case ran. No other note's text ever reaches the suite.
//
// Everything here goes through AppleScript (`osascript -`, the script on stdin, the arguments
// as its `on run argv`), which needs the Automation grant for Notes of the terminal ccsuite
// runs in; startSession checks for it before a case starts.

const (
	// The field and record separators of a script's output: Notes' text has no control
	// characters, so a note's body can be sent as it is.
	macFS = "\x1f"
	macRS = "\x1e"
)

// macNoteIDsScript lists every note's id, one per record.
const macNoteIDsScript = `
on run argv
	set rs to character id 30
	set out to ""
	tell application "Notes"
		repeat with n in every note
			set out to out & ((id of n) as text) & rs
		end repeat
	end tell
	return out
end run
`

// macNotesSinceScript lists id, name and body of every note whose id is not in argv: the notes
// that appeared since the snapshot the case started from.
const macNotesSinceScript = `
on run argv
	set fs to character id 31
	set rs to character id 30
	set out to ""
	tell application "Notes"
		repeat with n in every note
			set nid to (id of n) as text
			if argv does not contain nid then
				set out to out & nid & fs & ((name of n) as text) & fs & ((body of n) as text) & rs
			end if
		end repeat
	end tell
	return out
end run
`

// macNotesDeleteScript deletes every note named as one of argv and answers "<found>/<left>":
// how many it found and how many still answer to those names (a note Notes moved to Recently
// Deleted may still be one of them, so this is for the log, not a verdict).
const macNotesDeleteScript = `
on run argv
	set theIDs to {}
	set leftOver to 0
	tell application "Notes"
		repeat with t in argv
			repeat with n in (every note whose name is (t as text))
				set end of theIDs to ((id of n) as text)
			end repeat
		end repeat
		repeat with i in theIDs
			try
				delete note id (i as text)
			on error
				try
					delete (first note whose id is (i as text))
				end try
			end try
		end repeat
		repeat with t in argv
			set leftOver to leftOver + (count of (every note whose name is (t as text)))
		end repeat
	end tell
	return ((count of theIDs) as text) & "/" & (leftOver as text)
end run
`

// macNotesActivateScript brings Notes.app to the front and answers how many windows it has.
const macNotesActivateScript = `
on run argv
	tell application "Notes"
		activate
		return (count of windows) as text
	end tell
end run
`

// osa runs an AppleScript with args as its `on run argv` and returns what it returned.
func osa(ctx context.Context, script string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "osascript", append([]string{"-"}, args...)...)
	cmd.Stdin = strings.NewReader(script)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("osascript: %w: %s", err, lastOutputLine(errs.Bytes()))
	}
	return out.String(), nil
}

// macNoteIDs is the id of every note there is now, the snapshot a case starts from.
func macNoteIDs(ctx context.Context) (map[string]bool, error) {
	out, err := osa(ctx, macNoteIDsScript)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, id := range strings.Split(out, macRS) {
		if id = strings.TrimSpace(id); id != "" {
			ids[id] = true
		}
	}
	return ids, nil
}

// macNotesSince is every note that is not in seen: what the case made.
func macNotesSince(ctx context.Context, seen map[string]bool) ([]notesapp.Note, error) {
	args := make([]string, 0, len(seen))
	for id := range seen {
		args = append(args, id)
	}
	out, err := osa(ctx, macNotesSinceScript, args...)
	if err != nil {
		return nil, err
	}
	return parseMacNotes(out), nil
}

// parseMacNotes reads macNotesSinceScript's output: id, name, body per record.
func parseMacNotes(out string) []notesapp.Note {
	var notes []notesapp.Note
	for _, rec := range strings.Split(out, macRS) {
		if strings.TrimSpace(rec) == "" {
			continue
		}
		f := strings.SplitN(rec, macFS, 3)
		if len(f) < 3 {
			continue
		}
		notes = append(notes, notesapp.Note{Title: strings.TrimSpace(f[1]), Body: macNoteText(f[2])})
	}
	return notes
}

var (
	// One line end per line: a run of closing block tags (</h1></div> around a title, say),
	// with the <br> an empty line is made of folded into it, and a <br> on its own.
	macNoteBreakRE = regexp.MustCompile(`(?i)(?:<br\s*/?>\s*)?(?:</(?:div|p|li|tr|h[1-6])>\s*)+|<br\s*/?>`)
	macNoteTagRE   = regexp.MustCompile(`(?s)<[^>]*>`)
	macNoteBlankRE = regexp.MustCompile(`\n{3,}`)
)

// macNoteText is a note's text from the HTML body Notes gives: the checks read a note the way
// it looks, not its markup. Line ends survive; a run of blank lines becomes one.
func macNoteText(body string) string {
	s := macNoteBreakRE.ReplaceAllString(body, "\n")
	s = macNoteTagRE.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, " ", " ")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = macNoteBlankRE.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// noteTitles is the titles a case's note_exists checks name: the notes the suite may delete.
func noteTitles(c Case) []string {
	var titles []string
	seen := map[string]bool{}
	for _, ch := range c.Checks {
		if ch.Kind == "note_exists" && ch.Title != "" && !seen[ch.Title] {
			seen[ch.Title] = true
			titles = append(titles, ch.Title)
		}
	}
	return titles
}

// startMacNotes puts Notes.app back as the case starts it: the case's own notes deleted, the
// app in front, and a snapshot of what was already there so the checks see only new notes.
func (e *Env) startMacNotes(ctx context.Context, c Case) error {
	titles := noteTitles(c)
	if len(titles) > 0 {
		counts, err := osa(ctx, macNotesDeleteScript, titles...)
		if err != nil {
			return fmt.Errorf("deleting the case's own notes (%s): %w", strings.Join(titles, ", "), err)
		}
		e.logf("Notes.app: %s found/left of the case's own notes (%s)", strings.TrimSpace(counts), strings.Join(titles, ", "))
	}
	ids, err := macNoteIDs(ctx)
	if err != nil {
		return err
	}
	e.desk.macSeen = ids
	deadline := time.Now().Add(20 * time.Second)
	for {
		out, err := osa(ctx, macNotesActivateScript)
		if err != nil {
			return err
		}
		if strings.TrimSpace(out) != "0" {
			e.logf("Notes.app in front, %d note(s) there before the case", len(ids))
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("Notes.app opened no window in 20 s")
		}
		time.Sleep(500 * time.Millisecond)
	}
}
