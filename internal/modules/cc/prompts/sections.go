package prompts

import (
	"sort"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Legacy node_context defaults (build_context_header) for a node row without the value.
const (
	DefaultRoom      = "unknown"
	DefaultVoiceMode = "brief"
)

// Context is the typed node_context the prompt reads (docs/cc/03 §4). Every field is
// household- or node-level: per-turn and per-speaker data never enters messages[0] (03 §7.1).
type Context struct {
	// Room and VoiceMode render verbatim ("" stays ""); a caller whose node row lacks them
	// passes DefaultRoom / DefaultVoiceMode, as the legacy dict lookup defaulted.
	Room      string
	VoiceMode string
	// HouseholdPersona is persona.household_prompt (DefaultPersona unless the household
	// changed it; "" means no persona).
	HouseholdPersona string
	// DateKeys is the DT_KEYS vocabulary (dates.Vocabulary in production); empty omits the line.
	DateKeys []string
	// Agents is the node's "agents" payload ({"home_assistant"|"device_agent": {…}}), kept as
	// an ordered object because floors and light groups render in payload order.
	Agents *pyjson.Object
	// RoomHierarchy is the household's rooms with parent links.
	RoomHierarchy []Room
}

// Room is one room_hierarchy row; ParentRoomID "" means top level.
type Room struct {
	ID           string
	Name         string
	ParentRoomID string
}

// CommandFlag is a client command's routing flags (voice_command_helpers available
// command flags). Only AllowDirectAnswer == true counts as "direct answer allowed": the SDK
// default and a lost (None) flag both mean must-call.
type CommandFlag struct {
	CommandName       string
	AllowDirectAnswer bool
}

// identityHeader is IJarvisPromptProvider.build_context_header.
func identityHeader(ctx Context) string {
	h := "You are Jarvis, a function calling voice assistant.\nContext: room=" + ctx.Room + ", style=" + ctx.VoiceMode
	if p := PersonalityBlock(ctx.HouseholdPersona); p != "" {
		h += "\n\n" + p
	}
	return h
}

// PersonalityBlock is core_rules.build_personality_block: the fenced household voice, or ""
// for an empty persona.
func PersonalityBlock(persona string) string {
	persona = parse.PyStrip(persona)
	if persona == "" {
		return ""
	}
	return "<personality>\n" + PersonaFrame + "\n" + persona + "\n</personality>"
}

// dtKeysLine is the Qwen DT_KEYS section (identical in the three Qwen providers).
func dtKeysLine(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return "\nDT_KEYS: " + strings.Join(keys, "|") + "\n" +
		"CRITICAL — resolved_datetimes: You MUST use the symbolic key strings from DT_KEYS " +
		"(e.g., \"today\", \"tomorrow\", \"this_weekend\", \"last_weekend\"). " +
		"NEVER resolve dates to ISO timestamps like \"2026-03-07T05:00:00Z\" — the server handles resolution. " +
		"If the user omits a date, pass [\"today\"].\n"
}

// toolGuidanceSection is context_builders.build_tool_guidance_section: each tool's stripped
// top-level included_system_prompt_text, de-duplicated in first-seen order.
func toolGuidanceSection(tools []Tool) string {
	var hints []string
	seen := map[string]bool{}
	for _, t := range tools {
		v := get(t, "included_system_prompt_text")
		if !truthy(v) {
			continue
		}
		text := parse.PyStrip(pyStr(v))
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		hints = append(hints, "- "+text)
	}
	if len(hints) == 0 {
		return ""
	}
	return "\nTool Guidance:\n" + strings.Join(hints, "\n") + "\n"
}

// directAnswerSection is context_builders.build_direct_answer_section.
func directAnswerSection(flags []CommandFlag) string {
	var must, direct []string
	for _, f := range flags {
		if f.CommandName == "" {
			continue
		}
		if f.AllowDirectAnswer {
			direct = append(direct, f.CommandName)
		} else {
			must = append(must, f.CommandName)
		}
	}
	if len(must) == 0 && len(direct) == 0 {
		return ""
	}
	s := "\nDirect Answer Policy:\n"
	if len(must) > 0 {
		s += "- MUST call tools for: " + strings.Join(sortedSet(must), ", ") + "\n"
	}
	if len(direct) > 0 {
		s += "- Direct answers allowed for: " + strings.Join(sortedSet(direct), ", ") + "\n"
	}
	s += "- BE BRIEF. Your reply is read aloud. Answer in ONE short sentence and STOP. " +
		"Give exactly what was asked — do NOT add tips, extra facts, safety advice, or " +
		"\"let me know if…\" offers unless the user explicitly asks for more. Warm tone " +
		"is fine; padding is not. E.g. \"what's the weather?\" -> \"72 and cloudy.\" — " +
		"not a paragraph.\n"
	return s
}

func sortedSet(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// deviceAgentData is context_builders.device_agent_data: home_assistant wins when present
// (even empty), else the built-in device_agent.
func deviceAgentData(agents *pyjson.Object) *pyjson.Object {
	if _, ok := agents.Get("home_assistant"); ok {
		return getObj(agents, "home_assistant")
	}
	return getObj(agents, "device_agent")
}

// list returns o[k] as a list (nil otherwise).
func list(o *pyjson.Object, k string) []any {
	v, _ := get(o, k).([]any)
	return v
}

func asObj(v any) *pyjson.Object {
	o, _ := v.(*pyjson.Object)
	return o
}

// stateIs reports d.get("state") == s.
func stateIs(d *pyjson.Object, s string) bool {
	v, ok := get(d, "state").(string)
	return ok && v == s
}

// summaryDomains are the seven domains the compact summary labels, in order.
var summaryDomains = []struct{ domain, label string }{
	{"switch", "switches"},
	{"lock", "locks"},
	{"cover", "covers"},
	{"climate", "climate"},
	{"fan", "fans"},
	{"media_player", "media players"},
	{"scene", "scenes"},
}

// agentContextSummary is context_builders.build_agent_context_summary: the room-grouped full
// list for ≤ 20 devices, else a compact per-domain summary plus floors and room hierarchy.
func agentContextSummary(ctx Context) string {
	if ctx.Agents == nil || ctx.Agents.Len() == 0 {
		return ""
	}
	ha := deviceAgentData(ctx.Agents)
	if ha == nil || ha.Len() == 0 {
		return ""
	}
	lightControls := getObj(ha, "light_controls")
	deviceControls := getObj(ha, "device_controls")

	groupIDs := map[string]bool{}
	nLightControls := 0
	if lightControls != nil {
		nLightControls = lightControls.Len()
		for _, k := range lightControls.Keys() {
			groupIDs[pyjson.Repr(get(asObj(get(lightControls, k)), "entity_id"))] = true
		}
	}
	individual := 0
	for _, d := range list(deviceControls, "light") {
		do := asObj(d)
		if !groupIDs[pyjson.Repr(get(do, "entity_id"))] && !stateIs(do, "unavailable") {
			individual++
		}
	}
	var counts []string
	if n := nLightControls + individual; n > 0 {
		counts = append(counts, strconv.Itoa(n)+" lights")
	}
	for _, sd := range summaryDomains {
		n := 0
		for _, d := range list(deviceControls, sd.domain) {
			if !stateIs(asObj(d), "unavailable") {
				n++
			}
		}
		if n > 0 {
			counts = append(counts, strconv.Itoa(n)+" "+sd.label)
		}
	}
	if len(counts) == 0 {
		return ""
	}
	total := nLightControls
	if deviceControls != nil {
		for _, dom := range deviceControls.Keys() {
			for _, d := range list(deviceControls, dom) {
				if !stateIs(asObj(d), "unavailable") {
					total++
				}
			}
		}
	}
	if total <= 20 {
		return agentContextByRoom(ctx, ha)
	}

	s := "\nHome Assistant: " + strings.Join(counts, ", ")
	if f := floorParts(ha); f != "" {
		s += "\nFloors: " + f
	}
	s += "\nCall control_device to control devices. Call get_device_status to check device state."
	if ctx.Room != "" {
		s += "\nWhen the user doesn't specify a room, prefer devices in '" + ctx.Room + "' (the current room)."
	}
	s += "\n"
	return s + roomHierarchySection(ctx.RoomHierarchy)
}

// floorParts renders "Floor (Area, Area), …" from ha["floors"] ("" when absent or empty).
func floorParts(ha *pyjson.Object) string {
	floors := getObj(ha, "floors")
	if floors == nil || floors.Len() == 0 {
		return ""
	}
	parts := make([]string, 0, floors.Len())
	for _, name := range floors.Keys() {
		var areas []string
		for _, a := range list(floors, name) {
			areas = append(areas, pyStr(a))
		}
		parts = append(parts, name+" ("+strings.Join(areas, ", ")+")")
	}
	return strings.Join(parts, ", ")
}

type deviceEntry struct{ entityID, name, state string }

// agentContextByRoom is context_builders.build_agent_context_by_room.
func agentContextByRoom(ctx Context, ha *pyjson.Object) string {
	var order []string
	rooms := map[string][]deviceEntry{}
	add := func(room string, e deviceEntry) {
		if _, ok := rooms[room]; !ok {
			order = append(order, room)
		}
		rooms[room] = append(rooms[room], e)
	}

	groupIDs := map[string]bool{}
	if lc := getObj(ha, "light_controls"); lc != nil {
		for _, name := range lc.Keys() {
			info := asObj(get(lc, name))
			id := getStr(info, "entity_id", "")
			groupIDs[id] = true
			add("Room Groups", deviceEntry{id, name, getStr(info, "state", "unknown")})
		}
	}
	if dc := getObj(ha, "device_controls"); dc != nil {
		for _, dom := range dc.Keys() {
			for _, d := range list(dc, dom) {
				dev := asObj(d)
				id := getStr(dev, "entity_id", "")
				if stateIs(dev, "unavailable") || groupIDs[id] {
					continue
				}
				add(getStr(dev, "area", "Unassigned"), deviceEntry{id, getStr(dev, "name", ""), getStr(dev, "state", "unknown")})
			}
		}
	}
	if len(order) == 0 {
		return ""
	}

	s := "\nHome Assistant Devices:\n"
	if f := floorParts(ha); f != "" {
		s += "Floors: " + f + "\n"
		s += "Floor commands (e.g., 'turn off lights downstairs') → " +
			"call control_device for EACH device in EVERY area on that floor.\n"
	}
	if groups, ok := rooms["Room Groups"]; ok {
		s += "\nRoom Groups (control all lights in a room):\n"
		for _, d := range groups {
			s += "- " + d.entityID + ": " + d.name + " (" + d.state + ")\n"
		}
	}
	var rest []string
	for _, r := range order {
		if r != "Room Groups" {
			rest = append(rest, r)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool {
		ui, uj := rest[i] == "Unassigned", rest[j] == "Unassigned"
		if ui != uj {
			return !ui
		}
		return rest[i] < rest[j]
	})
	for _, r := range rest {
		s += "\n" + r + ":\n"
		for _, d := range rooms[r] {
			s += "- " + d.entityID + ": " + d.name + " (" + d.state + ")\n"
		}
	}
	s += "\nUse control_device to control devices. "
	s += "Use get_device_status to check state. "
	s += "Copy entity_id EXACTLY as shown above."
	if ctx.Room != "" {
		s += "\nWhen the user doesn't specify a room, prefer devices in '" + ctx.Room + "' (the current room)."
	}
	return s + "\n"
}

// roomHierarchySection is context_builders.build_room_hierarchy_section.
func roomHierarchySection(rooms []Room) string {
	if len(rooms) == 0 {
		return ""
	}
	names := map[string]string{}
	for _, r := range rooms {
		names[r.ID] = r.Name
	}
	var parents []string
	children := map[string][]string{}
	for _, r := range rooms {
		if r.ParentRoomID == "" {
			continue
		}
		if _, ok := names[r.ParentRoomID]; !ok {
			continue
		}
		if _, ok := children[r.ParentRoomID]; !ok {
			parents = append(parents, r.ParentRoomID)
		}
		children[r.ParentRoomID] = append(children[r.ParentRoomID], r.Name)
	}
	if len(parents) == 0 {
		return ""
	}
	s := "\nRoom Hierarchy:\n"
	for _, p := range parents {
		s += names[p] + " → " + strings.Join(children[p], ", ") + "\n"
	}
	return s + "When user references a room with sub-rooms, target ALL devices " +
		"in that room AND its sub-rooms.\n"
}
