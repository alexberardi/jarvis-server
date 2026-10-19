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
	RecordingID string         `json:"recording_id"`
	Seq         int64          `json:"seq"`
	TS          int64          `json:"ts"`   // ms since the epoch, the page's clock
	Kind        string         `json:"kind"` // click, input, change, submit, key, navigate, download
	URL         string         `json:"url,omitempty"`
	Tab         int64          `json:"tab,omitempty"`
	Key         string         `json:"key,omitempty"` // kind key: Enter, Escape or Tab
	Element     *RecordElement `json:"element,omitempty"`
	Value       string         `json:"value,omitempty"`
	Masked      bool           `json:"masked,omitempty"`
	Mime        string         `json:"mime,omitempty"`    // kind download
	Browser     string         `json:"browser,omitempty"` // the hub's key for the browser that sent it
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
