package computeruse

import "testing"

func TestIsInternalTool(t *testing.T) {
	for _, n := range []string{ToolObserve, ToolAct, ToolFiles, ToolStop, ToolRecordStart, ToolRecordStop, "computer_future"} {
		if !IsInternalTool(n) {
			t.Errorf("IsInternalTool(%q) = false", n)
		}
	}
	for _, n := range []string{"", "computer", "browser_open", "get_weather", "my_computer_tool"} {
		if IsInternalTool(n) {
			t.Errorf("IsInternalTool(%q) = true", n)
		}
	}
}
