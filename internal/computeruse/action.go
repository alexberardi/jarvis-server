// Package computeruse is the one place that defines computer-control actions, their risk, the
// built-in deny list, coordinate adapters and approval digests. Both cc (which plans and approves
// actions) and the agent (which executes them) import it, so the two sides cannot drift.
//
// See docs/agent/COMPUTER-CONTROL.md §Safety for the policy these lists implement.
package computeruse

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Limits on a single action and on a batch (one computer_act call).
const (
	MaxIntent = 200  // runes
	MaxText   = 2000 // runes, type / set_value / select
	MaxBatch  = 12   // actions per computer_act
	MaxWaitMS = 5000 // wait
)

// Action is one step the model asks the executor to take. Coordinates are image pixels of the
// capture CaptureID (after the model's Adapter); the executor maps them to the screen with
// Geometry.ToGlobal.
type Action struct {
	Kind      string   `json:"kind"`             // see Kinds
	Intent    string   `json:"intent"`           // one line, required, ≤ MaxIntent runes
	X         *int     `json:"x,omitempty"`      // image pixels of capture CaptureID
	Y         *int     `json:"y,omitempty"`      //
	Path      [][2]int `json:"path,omitempty"`   // drag
	DX        int      `json:"dx,omitempty"`     // scroll
	DY        int      `json:"dy,omitempty"`     //
	Button    string   `json:"button,omitempty"` // left right middle
	Keys      []string `json:"keys,omitempty"`   // keypress: ["ctrl","s"], names from KeyNames
	Text      string   `json:"text,omitempty"`   // type / set_value / select (≤ MaxText)
	Ref       string   `json:"ref,omitempty"`    // press set_value select: snapshot ref "e12"
	URL       string   `json:"url,omitempty"`    // navigate (http/https)
	MS        int      `json:"ms,omitempty"`     // wait (≤ MaxWaitMS)
	Region    *[4]int  `json:"region,omitempty"` // zoom
	CaptureID string   `json:"capture_id,omitempty"`
	Approval  string   `json:"approval,omitempty"` // token minted by cc for this action
}

// Target is what a run acts on: a browser tab (Site is its host[:port], the port only when the
// URL names one) or a desktop app (App is its bundle id, app id or name).
type Target struct {
	Kind     string `json:"kind"` // browser | desktop
	Site     string `json:"site,omitempty"`
	App      string `json:"app,omitempty"`
	WindowID string `json:"window_id,omitempty"`
	TabID    string `json:"tab_id,omitempty"`
}

// Kinds are the valid Action.Kind values.
var Kinds = map[string]bool{
	"click": true, "double_click": true, "triple_click": true, "drag": true, "move": true,
	"scroll": true, "keypress": true, "type": true, "wait": true, "screenshot": true,
	"zoom": true, "press": true, "set_value": true, "select": true, "navigate": true,
}

// KeyNames are the valid keypress names, lower case (Validate compares case-insensitively).
var KeyNames = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range []string{
		// modifiers
		"ctrl", "alt", "shift", "meta", "cmd", "super",
		// named keys
		"enter", "return", "tab", "escape", "esc", "space", "backspace", "delete", "insert",
		"home", "end", "pageup", "pagedown", "up", "down", "left", "right",
		// punctuation
		"-", "=", ",", ".", "/", ";", "'", "[", "]", `\`, "`",
	} {
		m[k] = true
	}
	for c := 'a'; c <= 'z'; c++ {
		m[string(c)] = true
	}
	for c := '0'; c <= '9'; c++ {
		m[string(c)] = true
	}
	for i := 1; i <= 12; i++ {
		m[fmt.Sprintf("f%d", i)] = true
	}
	return m
}()

// Validate checks one action against the schema and its limits.
func (a Action) Validate() error {
	if !Kinds[a.Kind] {
		return fmt.Errorf("unknown kind %q", a.Kind)
	}
	if strings.TrimSpace(a.Intent) == "" {
		return errors.New("intent is required")
	}
	if utf8.RuneCountInString(a.Intent) > MaxIntent {
		return fmt.Errorf("intent is longer than %d characters", MaxIntent)
	}
	if strings.ContainsAny(a.Intent, "\r\n") {
		return errors.New("intent must be one line")
	}
	if utf8.RuneCountInString(a.Text) > MaxText {
		return fmt.Errorf("text is longer than %d characters", MaxText)
	}
	switch a.Button {
	case "", "left", "right", "middle":
	default:
		return fmt.Errorf("unknown button %q", a.Button)
	}
	if (a.X == nil) != (a.Y == nil) {
		return errors.New("x and y go together")
	}
	if a.X != nil && (*a.X < 0 || *a.Y < 0) {
		return errors.New("x and y must not be negative")
	}

	switch a.Kind {
	case "click", "double_click", "triple_click", "move":
		if a.X == nil {
			return fmt.Errorf("%s needs x and y", a.Kind)
		}
	case "drag":
		if len(a.Path) < 2 {
			return errors.New("drag needs a path of at least two points")
		}
		for _, p := range a.Path {
			if p[0] < 0 || p[1] < 0 {
				return errors.New("drag path must not be negative")
			}
		}
	case "scroll":
		if a.DX == 0 && a.DY == 0 {
			return errors.New("scroll needs dx or dy")
		}
	case "keypress":
		if len(a.Keys) == 0 {
			return errors.New("keypress needs keys")
		}
		for _, k := range a.Keys {
			if !KeyNames[strings.ToLower(k)] {
				return fmt.Errorf("unknown key %q", k)
			}
		}
	case "type":
		if a.Text == "" {
			return errors.New("type needs text")
		}
	case "press", "set_value", "select":
		if a.Ref == "" {
			return fmt.Errorf("%s needs a ref", a.Kind)
		}
	case "navigate":
		u, err := url.Parse(a.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("navigate needs an http or https URL, got %q", a.URL)
		}
	case "wait":
		if a.MS <= 0 || a.MS > MaxWaitMS {
			return fmt.Errorf("wait must be 1 to %d ms", MaxWaitMS)
		}
	case "zoom":
		// Only the shape is checked: whether the region is x,y,w,h or two corners is the
		// executor's, and both have non-negative values and a positive third and fourth.
		if a.Region == nil {
			return errors.New("zoom needs a region")
		}
		r := *a.Region
		if r[0] < 0 || r[1] < 0 || r[2] <= 0 || r[3] <= 0 {
			return errors.New("zoom region is empty")
		}
	}
	return nil
}

// ValidateBatch checks one computer_act call: 1 to MaxBatch actions, each valid.
func ValidateBatch(actions []Action) error {
	if len(actions) == 0 {
		return errors.New("no actions")
	}
	if len(actions) > MaxBatch {
		return fmt.Errorf("%d actions, at most %d per call", len(actions), MaxBatch)
	}
	for i, a := range actions {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("action %d: %w", i, err)
		}
	}
	return nil
}

// Digest is what an approval token is bound to: the sha256 hex of the canonical JSON of the action
// without its approval, plus the run id and the target's site and app. encoding/json writes struct
// fields in declaration order and the struct has no maps, so the same action always marshals to
// the same bytes on both sides (cc mints, the agent verifies).
func (a Action) Digest(runID string, t Target) string {
	a.Approval = ""
	b, err := json.Marshal(struct {
		RunID  string `json:"run_id"`
		Site   string `json:"site"`
		App    string `json:"app"`
		Action Action `json:"action"`
	}{runID, t.Site, t.App, a})
	if err != nil {
		// Unreachable: every field is a plain value.
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
