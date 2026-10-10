package prompts

// ToolGates are the per-household (and per-conversation speaker) inputs that decide which
// server tools a conversation is offered (conversation_handler.py:311-363).
type ToolGates struct {
	WebSearch     bool // web_search.enabled (default off, fail-closed)
	SpeakerKnown  bool // a confidently identified speaker for this conversation (D21)
	MemoryEnabled bool // memory.enabled
	RecallEnabled bool // memory.recall_enabled
	// ChatPhotos: a mobile chat conversation whose live slot takes images, the only place
	// photos come from (docs/cc/chat-images.md §8). Gates the photo → action tools.
	ChatPhotos bool
}

// PhotoTools are the server tools that act on chat photos (offered only with ChatPhotos).
var PhotoTools = []string{"save_recipe_from_image"}

// PhotoActionsBlock is the per-turn rule while a chat has photos a photo tool can act on
// (docs/cc/chat-images.md CI8: offer, don't act). A transient block, so the byte-stable
// messages[0] and voice prompts are unchanged.
const PhotoActionsBlock = "PHOTOS: the user's photos in this chat can be handed to your tools. Use a tool on a " +
	"photo only when the user asked for that action, in this message or by accepting your offer (\"yes\", " +
	"\"sure\", \"do it\"). If they sent a photo without saying what to do with it, describe it briefly and " +
	"offer the matching action (e.g. \"Want me to save it as a recipe?\") without calling the tool. Never say " +
	"an action was done unless its tool succeeded in this turn."

// PhotoActionsGate reports whether a turn gets PhotoActionsBlock: photos are available to
// tools (attached now or kept from earlier) and a photo tool is offered.
func PhotoActionsGate(photosAvailable bool, offered map[string]bool) bool {
	if !photosAvailable {
		return false
	}
	for _, n := range PhotoTools {
		if offered[n] {
			return true
		}
	}
	return false
}

// textPathWhitelist is the legacy text-path whitelist, in offer order. answer_question is a
// disabled server tool (it only resolves if a node offers a client tool of that name);
// make_phone_call is offered even with phone off (its execute() refuses honestly).
var textPathWhitelist = []string{
	"answer_question", "make_phone_call", "run_errand", "schedule_errand", "list_scheduled_errands",
}

// ServerToolAllowed is the gate itself: web tools need web search on; remember/forget need a
// known speaker and memory on; recall additionally needs recall on. Every other tool passes.
func ServerToolAllowed(name string, g ToolGates) bool {
	switch name {
	case "deep_research", "quick_search":
		return g.WebSearch
	case "remember", "forget":
		return g.SpeakerKnown && g.MemoryEnabled
	case "recall":
		return g.SpeakerKnown && g.MemoryEnabled && g.RecallEnabled
	case "save_recipe_from_image":
		return g.ChatPhotos
	}
	return true
}

// TextServerTools is the text path's server-tool list, in the legacy order: the whitelist,
// then deep_research and quick_search, then remember, forget and recall, each behind its gate.
// Names the caller's registry does not have are skipped by the caller (legacy get_tool).
func TextServerTools(g ToolGates) []string {
	out := append([]string(nil), textPathWhitelist...)
	for _, n := range append([]string{"deep_research", "quick_search", "remember", "forget", "recall"}, PhotoTools...) {
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
