// Package computer is cc's computer-task side (docs/agent/COMPUTER_CONTROL.md, plan P3).
package computer

// SuccessCheck is how Jarvis checks a computer task's result before it reports done (plan P3
// piece 12). A taught workflow stores one (cc_taught_workflows.success). Only the shape is here
// so far: piece 12 adds Verify. Empty fields are not checked.
type SuccessCheck struct {
	URLContains string         `json:"url_contains,omitempty"`
	TextPresent []string       `json:"text_present,omitempty"`
	Download    *DownloadCheck `json:"download,omitempty"`
	File        *FileCheck     `json:"file,omitempty"`
	Screen      string         `json:"screen,omitempty"` // free text, a yes/no question about a fresh screenshot
}

// DownloadCheck: a download whose name matches NameLike, of at least MinBytes.
type DownloadCheck struct {
	NameLike string `json:"name_like"`
	MinBytes int64  `json:"min_bytes,omitempty"`
}

// FileCheck: a file at Path (under the agent's allowed folders) containing every Contains.
type FileCheck struct {
	Path     string   `json:"path"`
	Contains []string `json:"contains,omitempty"`
}
