package computeruse

// Snapshot is the element list an observe returns: from the extension (a page) or the
// accessibility tree (a desktop window). Refs are only valid for actions within the same Gen; a
// later snapshot replaces them.
type Snapshot struct {
	Gen      int               `json:"gen"`
	Elements []SnapshotElement `json:"elements"`
}

// SnapshotElement is one element of a Snapshot. Bounds is [x, y, width, height] in global screen
// points when known. Value is never set for a password or secure text field.
type SnapshotElement struct {
	Ref    string   `json:"ref"`
	Role   string   `json:"role"`
	Name   string   `json:"name,omitempty"`
	Value  string   `json:"value,omitempty"`
	State  []string `json:"state,omitempty"`
	Bounds *[4]int  `json:"bounds,omitempty"`
}
