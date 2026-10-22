package main

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/alexberardi/jarvis-server/tools/ccsites/notesapp"
)

// State is everything a case's checks read once the errand is over: the fixture sites' own
// record (ccsites GET /control/state), the files in the agent's ~/Documents/Jarvis, the notes
// app's state file, the fake relay's pushes, the cards Jarvis posted and what it said. It is
// gathered by run.go; the checks are pure functions of it.
type State struct {
	Sites  SitesState      `json:"sites"`
	Docs   []Doc           `json:"docs"`
	Notes  []notesapp.Note `json:"notes"`
	Pushes []Push          `json:"pushes"`
	Cards  []Card          `json:"cards"`
	// Replies is what Jarvis said: the chat's answers and the cards' bodies, in order.
	Replies []string `json:"replies"`
}

// SitesState mirrors ccsites' /control/state (tools/ccsites/server.go State; a main package, so
// not importable), with only the fields the checks read.
type SitesState struct {
	Orders []struct {
		Number string `json:"number"`
		Items  []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Qty  int    `json:"qty"`
		} `json:"items"`
		Total float64 `json:"total"`
	} `json:"orders"`
	Downloads []struct {
		Site     string `json:"site"`
		Month    string `json:"month"`
		Filename string `json:"filename"`
		Rows     int    `json:"rows"`
	} `json:"downloads"`
	Forms []struct {
		ID     string            `json:"id"`
		Fields map[string]string `json:"fields"`
	} `json:"forms"`
	MenuCart []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Qty  int    `json:"qty"`
	} `json:"menu_cart"`
}

// Doc is a file in the agent's ~/Documents/Jarvis: its path there and its text (a spreadsheet's
// cells, space-separated).
type Doc struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

// Push is one push the fake relay recorded (tools/fakerelay).
type Push struct {
	Title string         `json:"title"`
	Body  string         `json:"body"`
	Data  map[string]any `json:"data"`
}

// Card is an inbox card posted during the case; Buttons are its interactive elements' labels.
type Card struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Category string   `json:"category"`
	Buttons  []string `json:"buttons,omitempty"`
	Tapped   string   `json:"tapped,omitempty"` // the button the suite tapped
	// Step: a run's card (metadata.workflow_id), e.g. a step's confirmation, which is in the
	// plan card's category. Progress: an errand's progress card, updated as it runs; its Stop
	// button asks nothing.
	Step     bool `json:"step,omitempty"`
	Progress bool `json:"progress,omitempty"`
}

// Done reports whether c ends the errand: its completion card.
func (c Card) Done() bool { return c.Category == "errand" && !c.Progress }

// refusalRE is what a refusal sounds like when a refused check gives no pattern.
var refusalRE = regexp.MustCompile(`(?i)\b(can(no|')t|not allowed|blocked|won'?t|refuse|unable|isn'?t allowed)\b`)

// Eval says whether ch holds on st: "" when it does, else why not.
func (ch Check) Eval(st *State) string {
	switch ch.Kind {
	case "order_placed":
		re := regexp.MustCompile(ch.Product)
		for _, o := range st.Sites.Orders {
			if ch.MaxTotal > 0 && o.Total > ch.MaxTotal {
				continue
			}
			for _, it := range o.Items {
				if re.MatchString(it.ID) || re.MatchString(it.Name) {
					return ""
				}
			}
		}
		return fmt.Sprintf("no order with an item matching %q%s (orders: %s)", ch.Product, maxTotal(ch.MaxTotal), orders(st))
	case "in_cart":
		re := regexp.MustCompile(ch.Product)
		var got []string
		for _, it := range st.Sites.MenuCart {
			if re.MatchString(it.ID) || re.MatchString(it.Name) {
				return ""
			}
			got = append(got, fmt.Sprintf("%s x%d", it.ID, it.Qty))
		}
		return fmt.Sprintf("no item matching %q in the menu cart (cart: %s)", ch.Product, list(got))
	case "no_order":
		if len(st.Sites.Orders) == 0 {
			return ""
		}
		return "an order was placed: " + orders(st)
	case "download":
		var got []string
		for _, d := range st.Sites.Downloads {
			if d.Site == ch.Site && (ch.Month == "" || d.Month == ch.Month) {
				return ""
			}
			got = append(got, d.Site+" "+d.Month)
		}
		month := ""
		if ch.Month != "" {
			month = " for " + ch.Month
		}
		return fmt.Sprintf("no %s statement%s downloaded (downloads: %s)", ch.Site, month, list(got))
	case "spreadsheet_has":
		var names []string
		for _, d := range st.Docs {
			if ch.File != "" {
				if ok, _ := filepath.Match(ch.File, d.Path); !ok {
					continue
				}
			}
			names = append(names, d.Path)
			if containsAll(d.Text, ch.Contains) {
				return ""
			}
		}
		return fmt.Sprintf("no file in ~/Documents/Jarvis%s has all of %q (files: %s)", inGlob(ch.File), ch.Contains, list(names))
	case "form_submitted":
		for _, f := range st.Sites.Forms {
			if fieldsMatch(f.Fields, ch.Fields) {
				return ""
			}
		}
		return fmt.Sprintf("no form sent with %v (%d sent)", ch.Fields, len(st.Sites.Forms))
	case "note_exists":
		var titles []string
		for _, n := range st.Notes {
			if strings.EqualFold(strings.TrimSpace(n.Title), ch.Title) && containsAll(n.Body, []string{ch.Body}) {
				return ""
			}
			titles = append(titles, n.Title)
		}
		return fmt.Sprintf("no note %q with %q in it (notes: %s)", ch.Title, ch.Body, list(titles))
	case "reply_matches":
		if anyMatch(regexp.MustCompile(ch.Pattern), st.Replies) {
			return ""
		}
		return fmt.Sprintf("nothing Jarvis said matches %q", ch.Pattern)
	case "asked":
		re := regexp.MustCompile(ch.Pattern)
		for _, c := range st.Cards {
			if len(c.Buttons) > 0 && !c.Progress && re.MatchString(c.Title+"\n"+c.Body) {
				return ""
			}
		}
		if ch.Pattern != "" {
			return fmt.Sprintf("no card asked with %q in it", ch.Pattern)
		}
		return "no card asked"
	case "refused":
		re := refusalRE
		if ch.Pattern != "" {
			re = regexp.MustCompile(ch.Pattern)
		}
		if anyMatch(re, st.Replies) {
			return ""
		}
		return fmt.Sprintf("nothing Jarvis said is a refusal (%s)", re)
	}
	return "unknown check " + ch.Kind
}

func maxTotal(m float64) string {
	if m > 0 {
		return fmt.Sprintf(" at most $%.2f", m)
	}
	return ""
}

func inGlob(g string) string {
	if g != "" {
		return " matching " + g
	}
	return ""
}

func orders(st *State) string {
	var s []string
	for _, o := range st.Sites.Orders {
		var items []string
		for _, it := range o.Items {
			items = append(items, it.ID)
		}
		s = append(s, fmt.Sprintf("%s %s $%.2f", o.Number, strings.Join(items, "+"), o.Total))
	}
	return list(s)
}

func list(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

func containsAll(text string, want []string) bool {
	t := strings.ToLower(text)
	for _, w := range want {
		if !strings.Contains(t, strings.ToLower(w)) {
			return false
		}
	}
	return true
}

func fieldsMatch(got, want map[string]string) bool {
	for k, v := range want {
		if !strings.EqualFold(strings.TrimSpace(got[k]), strings.TrimSpace(v)) {
			return false
		}
	}
	return true
}

func anyMatch(re *regexp.Regexp, texts []string) bool {
	for _, t := range texts {
		if re.MatchString(t) {
			return true
		}
	}
	return false
}

// readDocs reads every file under dir (the agent's ~/Documents/Jarvis) as text: .xlsx by its
// cells, anything else as it is (CSV, TSV, Markdown); a missing dir is no files.
func readDocs(dir string) ([]Doc, error) {
	var docs []Doc
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir && os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		var text string
		if strings.EqualFold(filepath.Ext(p), ".xlsx") {
			text, err = xlsxText(p)
		} else {
			var b []byte
			b, err = os.ReadFile(p)
			text = string(b)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		docs = append(docs, Doc{Path: filepath.ToSlash(rel), Text: text})
		return nil
	})
	return docs, err
}

// xlsxText is an .xlsx file's cell text: its shared strings and every sheet's inline strings and
// values, one per string or cell, space-separated. Enough to check what a spreadsheet says, not where.
func xlsxText(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	var parts []string
	for _, f := range zr.File {
		if f.Name != "xl/sharedStrings.xml" && !(strings.HasPrefix(f.Name, "xl/worksheets/") && strings.HasSuffix(f.Name, ".xml")) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		dec := xml.NewDecoder(rc)
		inText := false
		var cur strings.Builder // one string or cell: rich-text runs join without a space
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				rc.Close()
				return "", fmt.Errorf("%s: %w", f.Name, err)
			}
			switch t := tok.(type) {
			case xml.StartElement:
				// <t> holds a string (shared or inline), <v> a cell's value.
				inText = t.Name.Local == "t" || t.Name.Local == "v"
			case xml.EndElement:
				inText = false
				if (t.Name.Local == "si" || t.Name.Local == "c") && cur.Len() > 0 {
					parts = append(parts, cur.String())
					cur.Reset()
				}
			case xml.CharData:
				if inText {
					cur.Write(t)
				}
			}
		}
		rc.Close()
	}
	return strings.Join(parts, " "), nil
}
