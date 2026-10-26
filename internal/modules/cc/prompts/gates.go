package prompts

import "sort"

// ToolGates are the per-household (and per-conversation speaker) inputs that decide which
// server tools a conversation is offered (conversation_handler.py:311-363).
type ToolGates struct {
	WebSearch     bool // web_search.enabled (default off, fail-closed)
	SpeakerKnown  bool // a confidently identified speaker for this conversation (D21)
	MemoryEnabled bool // memory.enabled
	RecallEnabled bool // memory.recall_enabled
	Watches       bool // watches.enabled
	// Computers: the household has a computer (a desktop agent). Gates start_recording,
	// stop_recording, the computer control tools (enable_computer_control,
	// disable_computer_control, computer_control_status) and run_workflow, which also need a
	// known speaker (the recording is theirs; whose computers and workflows they reach depends on
	// who asks).
	Computers bool
	// Workflows: the household has a taught workflow. run_workflow also needs it: with none it
	// could only say so, and one more tool next to the recording ones makes the small live model
	// call those less.
	Workflows bool
	// Devices: the conversation's device list has devices — the node's agents payload, or the
	// one warmup builds from cc_devices when the node sends none. Gates control_device, the
	// server-side device tool, so it is offered exactly when the prompt's device sections tell
	// the model to call it (#512); warmup drops the server copy when the node offers its own.
	Devices bool
	// ChatPhotos: a mobile chat conversation whose live slot takes images, the only place
	// photos come from (docs/cc/chat-images.md §8-9). Gates every photo tool.
	ChatPhotos bool
	// PhotoTools are the registered server tools that declare an image parameter
	// (servertools.IsPhotoTool); they are offered only with ChatPhotos. Client (node) tools are
	// gated by the same rule at warmup.
	PhotoTools map[string]bool
}

// PhotoActionsBlock is the per-turn rule while a chat has photos a photo tool can act on
// (docs/cc/chat-images.md CI8: offer, don't act). A transient block, so the byte-stable
// messages[0] and voice prompts are unchanged.
const PhotoActionsBlock = "PHOTOS: the user's photos in this chat can be handed to your tools. Use a tool on a " +
	"photo only when the user asked for that action, in this message or by accepting your offer (\"yes\", " +
	"\"sure\", \"do it\"). If they sent a photo without saying what to do with it, describe it briefly and " +
	"offer the matching action (e.g. \"Want me to save it as a recipe?\") without calling the tool. Never say " +
	"an action was done unless its tool succeeded in this turn."

// PhotoOnlyHint is the turn hint of a chat message that is only photos, no words (CI8): there
// is no request to act on, so the model describes and offers.
const PhotoOnlyHint = "[photo only: the user sent a photo with no message. Describe it in a sentence or two and, " +
	"if one of your tools fits it, offer that action (e.g. \"Want me to save it as a recipe?\"). Don't call a tool " +
	"on it until they say so.]"

// PhotoActionsGate reports whether a turn gets PhotoActionsBlock: photos are available to
// tools (attached now or kept from earlier) and a photo tool (server or client) is offered.
func PhotoActionsGate(photosAvailable, photoToolOffered bool) bool {
	return photosAvailable && photoToolOffered
}

// textPathWhitelist is the legacy text-path whitelist, in offer order. answer_question is a
// disabled server tool (it only resolves if a node offers a client tool of that name);
// make_phone_call is offered even with phone off (its execute() refuses honestly).
var textPathWhitelist = []string{
	"answer_question", "make_phone_call", "run_errand", "schedule_errand", "list_scheduled_errands",
}

// ServerToolAllowed is the gate itself: web tools need web search on; remember/forget need a
// known speaker and memory on; recall additionally needs recall on; the watch tools need watches
// on and a known speaker (A8); the recording and computer control tools a computer in the
// household and a known speaker; the file tools never (errand steps only). Every other tool passes.
func ServerToolAllowed(name string, g ToolGates) bool {
	switch name {
	case "deep_research", "quick_search":
		return g.WebSearch
	case "remember", "forget":
		return g.SpeakerKnown && g.MemoryEnabled
	case "recall":
		return g.SpeakerKnown && g.MemoryEnabled && g.RecallEnabled
	case "create_watch", "list_watches", "cancel_watch":
		return g.Watches && g.SpeakerKnown
	case "start_recording", "stop_recording", "enable_computer_control", "disable_computer_control", "computer_control_status":
		return g.Computers && g.SpeakerKnown
	case "read_computer_file", "write_spreadsheet":
		return false // errand steps only: the planner offers them with computer_task
	case "run_workflow":
		return g.Computers && g.Workflows && g.SpeakerKnown
	case "control_device":
		return g.Devices
	}
	if g.PhotoTools[name] {
		return g.ChatPhotos
	}
	return true
}

// TextServerTools is the text path's server-tool list, in the legacy order: the whitelist,
// then deep_research and quick_search, then remember, forget and recall, then create_watch,
// list_watches and cancel_watch, then start_recording and stop_recording, then the computer
// control tools, run_workflow and control_device, then the photo tools
// (sorted), each behind its gate. Names the caller's registry does not have are skipped by the
// caller (legacy get_tool).
func TextServerTools(g ToolGates) []string {
	out := append([]string(nil), textPathWhitelist...)
	photo := make([]string, 0, len(g.PhotoTools))
	for n := range g.PhotoTools {
		photo = append(photo, n)
	}
	sort.Strings(photo)
	for _, n := range append([]string{"deep_research", "quick_search", "remember", "forget", "recall",
		"create_watch", "list_watches", "cancel_watch", "start_recording", "stop_recording",
		"enable_computer_control", "disable_computer_control", "computer_control_status", "run_workflow",
		"control_device"}, photo...) {
		if ServerToolAllowed(n, g) {
			out = append(out, n)
		}
	}
	return out
}

// NativeServerTools is the native path's server-tool list: every registered tool (in
// registry order) that passes the same per-household gates as the text path (D22; legacy
// offered all of them ungated).
func NativeServerTools(registered []string, g ToolGates) []string {
	var out []string
	for _, n := range registered {
		if ServerToolAllowed(n, g) {
			out = append(out, n)
		}
	}
	return out
}

// ServerToolNames picks the path's list: TextServerTools, or NativeServerTools when native.
func ServerToolNames(native bool, registered []string, g ToolGates) []string {
	if native {
		return NativeServerTools(registered, g)
	}
	return TextServerTools(g)
}
