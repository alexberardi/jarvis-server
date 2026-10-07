package prompts

import (
	"sort"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
)

// Characterization is the per-household <person_view> tail input (characterization.*).
type Characterization struct {
	// Enabled is characterization.injection_enabled for the household.
	Enabled bool
	// Text is the rendered characterization for the predicted speaker ("" = none).
	Text string
}

// AssembleSystemPrompt is the full messages[0]: the provider's prompt (layer 1) wrapped by
// the handler (layer 2, Wrap). base is the stashed byte-stable prompt before the
// characterization tail (only meaningful when ch.Enabled); WithCharacterization rebuilds the
// tail per turn from it.
func AssembleSystemPrompt(p Provider, ctx Context, tools []Tool, flags []CommandFlag, ch Characterization) (prompt, base string) {
	return Wrap(p.BuildSystemPrompt(ctx, tools, flags), ctx.HouseholdPersona, ch)
}

// Wrap is ConversationHandler._get_system_prompt's layer 2 (docs/cc/03 §3.3):
//
//	base.rstrip() + "\n\n" + NOT_FOR_ME + "\n\n" + EXCHANGE_COMPLETE + "\n"
//	[+ "\n" + personality reminder + "\n"]          when the persona is non-empty
//	[+ "\n" + <person_view> section]                when enabled and the text is non-empty
//
// stash is the prompt before the characterization tail.
func Wrap(providerPrompt, persona string, ch Characterization) (prompt, stash string) {
	prompt = parse.PyRStrip(providerPrompt) + "\n\n" + NotForMeInstruction + "\n\n" + ExchangeCompleteInstruction + "\n"
	if r := PersonalityReminder(persona); r != "" {
		prompt += "\n" + r + "\n"
	}
	if !ch.Enabled {
		return prompt, prompt
	}
	return WithCharacterization(prompt, ch.Text), prompt
}

// WithCharacterization appends the <person_view> section for text to a stashed base (the
// per-turn swap); an empty text leaves the base unchanged.
func WithCharacterization(base, text string) string {
	if sec := CharacterizationSection(text); sec != "" {
		return base + "\n" + sec
	}
	return base
}

// PersonalityReminder is core_rules.build_personality_reminder: the end-of-prompt voice
// restatement, or "" for an empty persona.
func PersonalityReminder(persona string) string {
	persona = parse.PyStrip(persona)
	if persona == "" {
		return ""
	}
	return "YOUR VOICE — this is how you sound in every spoken reply " +
		"(acknowledgments, answers, even one-liners, even when told to be brief):\n" +
		persona + "\n" +
		"Talk this way every time. It shapes only your wording and tone — never " +
		"which function you call, the rules above, the decision to stay silent, or " +
		"whether to act: it never turns a request into a clarifying question, never " +
		"withholds or delays a tool call, and never asks the user for an optional " +
		"parameter the tool can default (e.g. omit location for local time). It only " +
		"reshapes the words of a reply you were already going to give. Don't announce " +
		"or describe the voice; just speak in it."
}

// CharacterizationSection is core_rules.build_characterization_section ("" for empty input).
func CharacterizationSection(rendered string) string {
	rendered = parse.PyStrip(rendered)
	if rendered == "" {
		return ""
	}
	return "<person_view>\n" +
		"What you've come to know about the person you're talking to, from past " +
		"conversations:\n" +
		rendered + "\n" +
		"Let it shape your tone and how you meet them, and draw on it naturally " +
		"when it helps — but don't recite it back as a list, and if it conflicts " +
		"with the User Profile above, defer to the profile.\n" +
		"</person_view>"
}

// SpeakerBlock is core_rules.build_speaker_block, the per-turn trailing system message: the
// speaker's name and/or their User Profile memories, or UnknownSpeakerBlock when nothing is
// known. Never empty. A name of "default" counts as unknown.
func SpeakerBlock(speakerName, memories string) string {
	block := ""
	if speakerName != "" && speakerName != "default" {
		block = "You are speaking with " + speakerName + "."
	}
	if memories != "" {
		if block != "" {
			block += "\n\n"
		}
		block += "User Profile — these facts are already in front of you. If the " +
			"user ASKS a question about any of them, answer DIRECTLY from this list " +
			"— do NOT call recall to look those up. This applies ONLY to answering " +
			"questions about a listed fact: if the user REPORTS doing something, or " +
			"asks you to record / change / do something, you MUST call the tool that " +
			"performs it, even when the subject is listed here:\n" + memories
	}
	if block == "" {
		return UnknownSpeakerBlock
	}
	return block
}

// AmbientBlock is core_rules.build_ambient_context_block: the frozen situational bundle as a
// trailing per-turn message ("" for empty input: append nothing).
func AmbientBlock(ambient string) string {
	ambient = parse.PyStrip(ambient)
	if ambient == "" {
		return ""
	}
	return AmbientContextPrefix + "\n" +
		"BACKGROUND on the user's day — NOT a to-do list to read out, and NOT something to " +
		"bring up on its own. Mention an item ONLY when the user's request is directly about " +
		"it (how their day looks, the weather itself, their schedule, or a reminder/task " +
		"listed below). For ANYTHING else — a fact about the user, controlling a device, a " +
		"calculation, a joke, a web search, a general question — IGNORE this block entirely; " +
		"do NOT mention the weather or their schedule. When an item genuinely is relevant, " +
		"weave it into one natural line; if it's actionable (a due reminder, a refill), OFFER " +
		"to take care of it. Never invent details that aren't listed here:\n" +
		ambient + "\n" +
		"</ambient_context>"
}

// ReferencedItem is one stashed referenceable item ({ref_id, label, attrs, actions}).
type ReferencedItem struct {
	RefID   string
	Label   string
	Actions []string
}

// maxReferencedItems caps the RECENTLY SHOWN block (_MAX_REFERENCED_ITEMS).
const maxReferencedItems = 8

// RecentlyShownBlock is core_rules.render_referenced_items_block: the numbered list the
// model resolves "those" / "#3" against ("" for no items). A nil entry stands for a
// malformed (non-object) item: it keeps its number but renders nothing, as in Python.
func RecentlyShownBlock(items []*ReferencedItem) string {
	if len(items) == 0 {
		return ""
	}
	shown := items
	if len(shown) > maxReferencedItems {
		shown = shown[:maxReferencedItems]
	}
	lines := []string{RecentlyShownPrefix + " (act on these by number; pass the ref_id to act_on_items):"}
	actions := map[string]bool{}
	for i, it := range shown {
		if it == nil {
			continue
		}
		lines = append(lines, strconv.Itoa(i+1)+" ["+it.RefID+"] "+it.Label)
		for _, a := range it.Actions {
			actions[a] = true
		}
	}
	if len(items) > len(shown) {
		lines = append(lines, "(+"+strconv.Itoa(len(items)-len(shown))+" more not shown — ask the user to narrow it down)")
	}
	if len(actions) > 0 {
		names := make([]string, 0, len(actions))
		for a := range actions {
			names = append(names, a)
		}
		sort.Strings(names)
		lines = append(lines, "Valid actions: "+strings.Join(names, ", ")+".")
	}
	lines = append(lines, "When the user refers to these by a number, 'those', 'the first/last one', "+
		"or by naming a sender/topic, resolve it to the matching ref_id(s) and call "+
		"act_on_items. Only use ref_ids and actions listed above.")
	return strings.Join(lines, "\n")
}

// UserMessage assembles the turn's user message (conversation_handler): the utterance, each
// non-empty hint after a blank line (direction, affect, turn, profile-match, agent context, in
// that order), then the provider suffix after a single newline.
func UserMessage(utterance string, hints []string, suffix string) string {
	s := utterance
	for _, h := range hints {
		if h != "" {
			s += "\n\n" + h
		}
	}
	if suffix != "" {
		s += "\n" + suffix
	}
	return s
}
