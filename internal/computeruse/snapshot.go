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
// fields. State holds any of checked, disabled, expanded, selected, required. Frame is the
// browser frame the element is in (0: the top document).
type SnapElement struct {
	Ref          string   `json:"ref"`
	Role         string   `json:"role,omitempty"`
	Name         string   `json:"name,omitempty"`
	Type         string   `json:"type,omitempty"`
	Autocomplete string   `json:"autocomplete,omitempty"`
	Value        string   `json:"value,omitempty"`
	State        []string `json:"state,omitempty"`
	Frame        int      `json:"frame,omitempty"`
}

// Element is what Classify and BuiltinDeny read about e.
func (e SnapElement) Element() *Element {
	return &Element{Role: e.Role, Name: e.Name, Type: e.Type, Autocomplete: e.Autocomplete,
		Secure: strings.EqualFold(e.Type, "password")}
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
