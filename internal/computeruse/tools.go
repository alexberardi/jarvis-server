package computeruse

import "strings"

// The agent node's computer-control tools (blueprint Interfaces §3). They are internal: cc's
// computer-task loop calls them, and IsInternalTool keeps them out of live tools and the planner menu.
const (
	ToolObserve     = "computer_observe"
	ToolAct         = "computer_act"
	ToolFiles       = "computer_files"
	ToolStop        = "computer_stop"
	ToolRecordStart = "computer_record_start"
	ToolRecordStop  = "computer_record_stop"
	// ToolRecordDelete deletes the agent's local copy of a recording (piece 29 adds it to the agent).
	ToolRecordDelete = "computer_record_delete"

	toolPrefix = "computer_"
)

// IsInternalTool is whether name is one of the computer-control tools (any "computer_" name, so a
// newer agent's tools are hidden from an older server too).
func IsInternalTool(name string) bool { return strings.HasPrefix(name, toolPrefix) }
