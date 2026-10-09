package legacyimport

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// TableReport counts one jarvisd table's rows.
type TableReport struct {
	Table    string // jarvisd table
	From     string // legacy db.table
	Read     int
	Imported int
	Skipped  map[string]int // reason → rows
	Failed   int            // rows jarvisd's constraints rejected
	Fixed    map[string]int // fix-up → rows
}

func (t *TableReport) skip(reason string) {
	if t.Skipped == nil {
		t.Skipped = map[string]int{}
	}
	t.Skipped[reason]++
}

func (t *TableReport) fix(what string) {
	if t.Fixed == nil {
		t.Fixed = map[string]int{}
	}
	t.Fixed[what]++
}

// Report is what the import did (or, in a dry run, would do). It holds no secrets and no
// emails except masked ones; Log (for the 0600 log file) names rows by id.
type Report struct {
	Source    string
	Apply     bool
	Committed bool
	Heads     map[string]string // db key → alembic head ("" absent)
	Tables    []*TableReport
	Notes     []string
	// InactiveNodes are imported node registrations with is_active = 0: they can't log in.
	InactiveNodes []string
	// Superusers are the imported superusers (id + masked email).
	Superusers []string
	Errors     []string // row-level failures
	Refusals   []string // why nothing was (or would be) written
	Log        []string
}

func (r *Report) table(name, from string) *TableReport {
	t := &TableReport{Table: name, From: from}
	r.Tables = append(r.Tables, t)
	return t
}

func (r *Report) notef(format string, a ...any) { r.Notes = append(r.Notes, fmt.Sprintf(format, a...)) }
func (r *Report) logf(format string, a ...any)  { r.Log = append(r.Log, fmt.Sprintf(format, a...)) }

// OK is true when nothing refused the import.
func (r *Report) OK() bool { return len(r.Refusals) == 0 }

// Write prints the report.
func (r *Report) Write(w io.Writer) {
	mode := "DRY RUN (nothing written; --apply to import)"
	if r.Apply {
		mode = "APPLY"
	}
	fmt.Fprintf(w, "import-legacy: %s\nsource: %s\n", mode, r.Source)
	if len(r.Heads) > 0 {
		var hs []string
		for _, d := range LegacyDBs {
			if h, ok := r.Heads[d.Key]; ok {
				if h == "" {
					h = "absent"
				}
				hs = append(hs, d.Key+"="+h)
			}
		}
		fmt.Fprintf(w, "legacy heads: %s\n", strings.Join(hs, " "))
	}
	if len(r.Tables) > 0 {
		fmt.Fprintf(w, "\n%-28s %-36s %6s %8s %7s %6s\n", "JARVISD TABLE", "FROM", "READ", "IMPORTED", "SKIPPED", "FAILED")
		for _, t := range r.Tables {
			sk := 0
			for _, n := range t.Skipped {
				sk += n
			}
			fmt.Fprintf(w, "%-28s %-36s %6d %8d %7d %6d\n", t.Table, t.From, t.Read, t.Imported, sk, t.Failed)
			for _, k := range sortedKeys(t.Skipped) {
				fmt.Fprintf(w, "    skipped %d: %s\n", t.Skipped[k], k)
			}
			for _, k := range sortedKeys(t.Fixed) {
				fmt.Fprintf(w, "    fixed %d: %s\n", t.Fixed[k], k)
			}
		}
	}
	if len(r.Superusers) > 0 {
		fmt.Fprintf(w, "\nsuperusers: %s\n", strings.Join(r.Superusers, ", "))
	}
	if len(r.InactiveNodes) > 0 {
		fmt.Fprintf(w, "inactive nodes (imported inactive, can't log in until activated): %s\n", strings.Join(r.InactiveNodes, ", "))
	}
	if len(r.Notes) > 0 {
		fmt.Fprintln(w, "\nnotes:")
		for _, n := range r.Notes {
			fmt.Fprintf(w, "  - %s\n", n)
		}
	}
	if len(r.Errors) > 0 {
		fmt.Fprintf(w, "\nrows jarvisd rejects (%d):\n", len(r.Errors))
		for _, e := range r.Errors {
			fmt.Fprintf(w, "  - %s\n", e)
		}
	}
	if len(r.Refusals) > 0 {
		fmt.Fprintln(w, "\nREFUSED:")
		for _, e := range r.Refusals {
			fmt.Fprintf(w, "  - %s\n", e)
		}
		return
	}
	switch {
	case r.Committed:
		fmt.Fprintln(w, "\nimported: committed in one transaction.")
	case !r.Apply:
		fmt.Fprintln(w, "\ndry run passed: every row fits jarvisd's schema. Re-run with --apply to import.")
	}
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
