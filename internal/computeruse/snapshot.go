package computeruse

import "strings"

// Snapshot is a page's (or a window's) interactive elements, each with a ref an action can name
// (Action.Ref). Gen identifies the snapshot: a ref is only good for the Gen it came with. Names
// and values are untrusted page content.
type Snapshot struct {
	Gen      int           `json:"gen"`
	URL      string        `json:"url,omitempty"`
	Title    string        `json:"title,omitempty"`
	Elements []SnapElement `json:"elements"`
}

// SnapElement is one element of a Snapshot. Value is left out for password, card and one-time-code
// fields (and for an AXSecureTextField or an AT-SPI passwordtext). State holds any of checked,
// disabled, expanded, selected, required. Frame is the browser frame the element is in (0: the top
// document). Bounds is [x, y, width, height] in global screen points, when known (desktop windows).
//
// Context, Position and Target are the element layer's observations (docs/agent/
// ELEMENT-LAYER-WORK.md §2), each optional: nearby visible text (≤ ContextMax code points, never a
// field's value), where the element sits in its viewport or window (ValidPosition; none when off
// screen), and a link's destination URL. Like names, they are untrusted page or app content.
type SnapElement struct {
	Ref          string   `json:"ref"`
	Role         string   `json:"role,omitempty"`
	Name         string   `json:"name,omitempty"`
	Type         string   `json:"type,omitempty"`
	Autocomplete string   `json:"autocomplete,omitempty"`
	Value        string   `json:"value,omitempty"`
	State        []string `json:"state,omitempty"`
	Frame        int      `json:"frame,omitempty"`
	Bounds       *[4]int  `json:"bounds,omitempty"`
	Context      string   `json:"context,omitempty"`
	Position     string   `json:"position,omitempty"`
	Target       string   `json:"target,omitempty"`
}

const (
	ContextMax = 200  // code points of an element's Context
	TargetMax  = 2048 // bytes of a Target kept whole; a longer one is dropped, never cut
)

// PositionIn is where an element's centre sits in its window, both given as screen bounds
// (x, y, width, height): one of the nine labels, or "" when either has no size or the centre is
// outside the window. For sources that know bounds (AT-SPI now; Windows UIA and macOS AX later).
func PositionIn(el, win [4]int) string {
	if el[2] <= 0 || el[3] <= 0 || win[2] <= 0 || win[3] <= 0 {
		return ""
	}
	fx := (float64(el[0]-win[0]) + float64(el[2])/2) / float64(win[2])
	fy := (float64(el[1]-win[1]) + float64(el[3])/2) / float64(win[3])
	if fx < 0 || fx > 1 || fy < 0 || fy > 1 {
		return ""
	}
	h, v := "center", "middle"
	switch {
	case fx < 1.0/3:
		h = "left"
	case fx > 2.0/3:
		h = "right"
	}
	switch {
	case fy < 1.0/3:
		v = "top"
	case fy > 2.0/3:
		v = "bottom"
	}
	return v + "-" + h
}

// ValidPosition reports one of the nine position labels: top, middle or bottom, a hyphen, then
// left, center or right.
func ValidPosition(p string) bool {
	v, h, ok := strings.Cut(p, "-")
	return ok && (v == "top" || v == "middle" || v == "bottom") && (h == "left" || h == "center" || h == "right")
}

// Element is what Classify and BuiltinDeny read about e.
func (e SnapElement) Element() *Element {
	return &Element{Role: e.Role, Name: e.Name, Type: e.Type, Autocomplete: e.Autocomplete,
		Secure: strings.EqualFold(e.Type, "password") || e.Role == "AXSecureTextField" || e.Role == "passwordtext"}
}

// Find returns the element with ref, or nil.
func (s Snapshot) Find(ref string) *SnapElement {
	for i := range s.Elements {
		if s.Elements[i].Ref == ref {
			return &s.Elements[i]
		}
	}
	return nil
}
