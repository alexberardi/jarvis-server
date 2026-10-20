package prompts

import (
	"slices"
	"testing"
)

// read_computer_file and write_spreadsheet are errand steps only: no conversation is offered them,
// whatever the household has.
func TestFileToolsNeverInAConversation(t *testing.T) {
	all := ToolGates{WebSearch: true, SpeakerKnown: true, MemoryEnabled: true, RecallEnabled: true, Watches: true, Computers: true}
	for _, n := range []string{"read_computer_file", "write_spreadsheet"} {
		if ServerToolAllowed(n, all) || slices.Contains(NativeServerTools([]string{n}, all), n) || slices.Contains(TextServerTools(all), n) {
			t.Errorf("%s offered in a conversation", n)
		}
	}
	if !ServerToolAllowed("start_recording", all) {
		t.Error("start_recording not offered with a computer")
	}
}
