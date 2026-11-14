package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Recording (blueprint P3 piece 28): while a recording runs, the extension reports what the user
// does in the browser (clicks, field changes, submits, Enter/Escape/Tab, navigations, downloads)
// as "record" events. The hub hands them to the sink the recorder registered. Secret fields'
// values never travel: the extension sends masked: true instead, and the agent drops a value it
// sent anyway (secretRecorded).

// RecordEvent is one recorded user action.
type RecordEvent struct {
	RecordingID string `json:"recording_id"`
	Seq         int64  `json:"seq"`
	TS          int64  `json:"ts"` // ms since the epoch, the page's clock
	// Kind: click, input, change, submit, key, navigate, download, cannot_watch (a page it can't
	// script); and, with no seq, start (Tabs: what was open when the recording began) and result
	// (Of: the seq of the click, submit or key it follows; the page's URL and Title afterwards and
	// the controls that Appeared and Disappeared).
	Kind        string          `json:"kind"`
	URL         string          `json:"url,omitempty"`
	Tab         int64           `json:"tab,omitempty"`
	Key         string          `json:"key,omitempty"` // kind key: Enter, Escape or Tab
	Element     *RecordElement  `json:"element,omitempty"`
	Value       string          `json:"value,omitempty"`
	Masked      bool            `json:"masked,omitempty"`
	Mime        string          `json:"mime,omitempty"`    // kind download
	Browser     string          `json:"browser,omitempty"` // the hub's key for the browser that sent it
	Tabs        []RecordTab     `json:"tabs,omitempty"`
	Of          int64           `json:"of,omitempty"`
	Title       string          `json:"title,omitempty"`
	Appeared    []ResultControl `json:"appeared,omitempty"`
	Disappeared []ResultControl `json:"disappeared,omitempty"`
}

// RecordTab is one page open when a recording began; SignedIn: it showed a way to sign out.
type RecordTab struct {
	URL      string `json:"url,omitempty"`
	Title    string `json:"title,omitempty"`
	Active   bool   `json:"active,omitempty"`
	SignedIn bool   `json:"signed_in,omitempty"`
}

// ResultControl is a control an action made appear or disappear: its role and name, never a value.
type ResultControl struct {
	Role string `json:"role,omitempty"`
	Name string `json:"name,omitempty"`
}

// What the agent keeps of a start or a result, whatever the extension sent (its own caps:
// REC_START_TABS, RESULT_ELEMENTS, RESULT_NAME_MAX).
const (
	recordStartTabs      = 30
	recordResultControls = 40
	recordResultName     = 80
	recordTitleMax       = 120
)

func clipControls(cs []ResultControl) []ResultControl {
	cs = cs[:min(len(cs), recordResultControls)]
	for i := range cs {
		cs[i] = ResultControl{Role: clipRunes(cs[i].Role, 40), Name: clipRunes(cs[i].Name, recordResultName)}
	}
	return cs
}

// RecordElement is the element an event happened on, as the snapshot names it.
type RecordElement struct {
	Role         string `json:"role,omitempty"`
	Name         string `json:"name,omitempty"`
	Tag          string `json:"tag,omitempty"`
	Type         string `json:"type,omitempty"`
	Autocomplete string `json:"autocomplete,omitempty"`
	Selector     string `json:"selector,omitempty"` // id › data-testid › name › aria path
	Frame        int    `json:"frame,omitempty"`
}

// recordValueMax is the extension's REC_VALUE_MAX.
const recordValueMax = 500

var recordingIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// secretLabel is the extension's REC_SECRET_LABEL: a card, CVV, SSN, PIN or security-code field.
var secretLabel = regexp.MustCompile(`(?i)card|cvv|cvc|csc|ssn|social security|security code|(^|[^a-z])pin([^a-z]|$)`)

var camelBreak = regexp.MustCompile(`([a-z])([A-Z])`)

// secretRecorded is the extension's recSecret: a password, hidden, card, one-time-code or
// password-manager field, or one whose label names a card, CVV, SSN or PIN.
func secretRecorded(e RecordEvent) bool {
	if e.Masked {
		return true
	}
	el := e.Element
	if el == nil {
		return false
	}
	if t := strings.ToLower(el.Type); t == "password" || t == "hidden" {
		return true
	}
	for _, a := range strings.Fields(strings.ToLower(el.Autocomplete)) {
		if strings.HasPrefix(a, "cc-") || a == "one-time-code" || a == "current-password" || a == "new-password" {
			return true
		}
	}
	return secretLabel.MatchString(camelBreak.ReplaceAllString(el.Name+" "+el.Selector, "$1 $2"))
}

// SetRecordSink registers the function record events go to (nil: drop them). It is called on
// the browser's connection goroutine, in the order the extension sent them; keep it quick.
func (h *Hub) SetRecordSink(sink func(RecordEvent)) {
	h.mu.Lock()
	h.recordSink = sink
	h.mu.Unlock()
}

// deliverRecord decodes a record event line and hands it to the sink, masked.
func (h *Hub) deliverRecord(line []byte, browserKey string) {
	h.mu.Lock()
	sink := h.recordSink
	h.mu.Unlock()
	if sink == nil {
		return
	}
	var e RecordEvent
	if err := json.Unmarshal(line, &e); err != nil {
		h.Log.Warn("browser: bad record event", "err", err)
		return
	}
	e.Browser = browserKey
	if (e.Value != "" || e.Masked) && secretRecorded(e) { // a click on "Add card" carries no value to drop
		e.Value, e.Masked = "", true
	}
	e.Value = clipRunes(e.Value, recordValueMax)
	if e.Element != nil {
		e.Element.Name = clipRunes(e.Element.Name, snapshotNameMax)
	}
	e.Title = clipRunes(e.Title, recordTitleMax)
	e.Appeared, e.Disappeared = clipControls(e.Appeared), clipControls(e.Disappeared)
	e.Tabs = e.Tabs[:min(len(e.Tabs), recordStartTabs)]
	for i := range e.Tabs {
		e.Tabs[i].Title = clipRunes(e.Tabs[i].Title, recordTitleMax)
	}
	sink(e)
}

// RecordStart starts recording id in the default browser: every tab and frame reports the
// user's actions as record events until RecordStop.
func (t *Tools) RecordStart(ctx context.Context, id string) error {
	if !recordingIDPattern.MatchString(id) {
		return fmt.Errorf("%w: recording_id must be 1-64 letters, digits, _ or -", errBadArgs)
	}
	_, err := t.call(ctx, "record_start", map[string]any{}, map[string]any{"recording_id": id})
	return err
}

// RecordStop stops recording id and returns how many events the extension sent.
func (t *Tools) RecordStop(ctx context.Context, id string) (int, error) {
	if !recordingIDPattern.MatchString(id) {
		return 0, fmt.Errorf("%w: recording_id must be 1-64 letters, digits, _ or -", errBadArgs)
	}
	res, err := t.call(ctx, "record_stop", map[string]any{}, map[string]any{"recording_id": id})
	if err != nil {
		return 0, err
	}
	n, _ := res["events"].(float64)
	return int(n), nil
}
