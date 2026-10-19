package cc

import (
	"context"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Computer control and recording by voice (docs/agent/COMPUTER-CONTROL.md C11): "enable computer
// control", "disable computer control", "is computer control on?", and the recording tools'
// targeting. Each acts on the computer you're talking to; from another node (a Pi, the phone) on
// the computer you name, or your only one, and asks which when you have several. Turning the
// switch on or off goes through the agent (computer_switch, the menu's own path: agent.json, the
// tray, the heartbeat), so the agent stays what enforces it (C8).

const (
	toolEnableComputerControl  = "enable_computer_control"
	toolDisableComputerControl = "disable_computer_control"
	toolComputerControlStatus  = "computer_control_status"
)

// voiceComputer is one of the household's computers as a voice tool sees it.
type voiceComputer struct {
	row  *nodeRow
	name string // the registered name; "" when it has none
}

// label is how Jarvis names it: "the MacBook", "Alex's iMac", "this computer".
func (c voiceComputer) label() string {
	switch n := c.name; {
	case n == "":
		return "this computer"
	case strings.Contains(n, "'s ") || strings.Contains(n, "’s ") || strings.HasPrefix(strings.ToLower(n), "the "):
		return n
	default:
		return "the " + n
	}
}

// householdComputers are the household's active computers (desktop agents) with their names.
func (m *Module) householdComputers(ctx context.Context, hh string) ([]voiceComputer, error) {
	if hh == "" {
		return nil, nil
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+nodeCols+` FROM cc_nodes WHERE household_id = ? AND is_active ORDER BY node_id`, hh)
	if err != nil {
		return nil, err
	}
	var nodes []*nodeRow
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if n.desktop() {
			nodes = append(nodes, n)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var names map[string]string
	if len(nodes) > 0 {
		names = m.nodeNames(ctx, nodes...)
	}
	out := make([]voiceComputer, len(nodes))
	for i, n := range nodes {
		name := names[n.nodeID]
		if name == n.nodeID {
			name = ""
		}
		out[i] = voiceComputer{row: n, name: name}
	}
	return out, nil
}

// computerScope is which computers a voice tool may reach from another node.
type computerScope int

const (
	// ownComputers: the speaker's own (the member who signed in there). Turning control on and
	// recording reach only these from elsewhere.
	ownComputers computerScope = iota
	// anyComputer: every computer in the household. Turning control off works from anywhere.
	anyComputer
)

// genericComputerNames are what the model passes when the user named no computer.
var genericComputerNames = map[string]bool{"": true, "this": true, "here": true, "computer": true, "thiscomputer": true,
	"thecomputer": true, "mycomputer": true, "thismachine": true, "mymachine": true, "thislaptop": true, "mylaptop": true}

// computerPick is what pickComputer found: the computers in reach, the one to act on, or a
// refusal to say instead.
type computerPick struct {
	here     *voiceComputer  // the computer the turn comes from
	reach    []voiceComputer // the computers in scope
	target   *voiceComputer
	refusal  *pyjson.Object
	hasNamed bool
}

// pickComputer is the computer a voice tool acts on: the one named (among those in scope), else
// the one the turn comes from, else the only one in scope (of those prefer keeps, when it keeps
// any); several: a refusal asking which, none: one saying why.
func (m *Module) pickComputer(ctx context.Context, turn servertools.Turn, named string, scope computerScope, prefer func(*nodeRow) bool) (computerPick, error) {
	var p computerPick
	all, err := m.householdComputers(ctx, turn.HouseholdID)
	if err != nil {
		return p, err
	}
	for i, c := range all {
		if turn.NodeID != "" && c.row.nodeID == turn.NodeID {
			p.here = &all[i]
		}
		if scope == anyComputer || (p.here != nil && p.here.row == c.row) ||
			(c.row.ownerUserID.Valid && c.row.ownerUserID.Int64 == turn.Speaker.UserID) {
			p.reach = append(p.reach, c)
		}
	}
	key := compactName(strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(named)), "the "), "my "))
	if !genericComputerNames[key] {
		p.hasNamed = true
		var hits []voiceComputer
		for _, c := range p.reach {
			cn := compactName(c.name)
			if (cn != "" && (strings.Contains(cn, key) || strings.Contains(key, cn))) || compactName(c.row.nodeID) == key {
				hits = append(hits, c)
			}
		}
		switch {
		case len(hits) == 1:
			p.target = &hits[0]
		case len(hits) > 1:
			p.refusal = askWhichComputer("Which one", hits)
		case len(p.reach) == 0:
			p.refusal = noComputer(all)
		default:
			p.refusal = askWhichComputer(`I don't know a computer called "`+strings.TrimSpace(named)+`". Which one`, p.reach)
		}
		return p, nil
	}
	if p.here != nil {
		p.target = p.here
		return p, nil
	}
	cands := p.reach
	if prefer != nil {
		var kept []voiceComputer
		for _, c := range cands {
			if prefer(c.row) {
				kept = append(kept, c)
			}
		}
		if len(kept) > 0 {
			cands = kept
		}
	}
	switch len(cands) {
	case 0:
		p.refusal = noComputer(all)
	case 1:
		p.target = &cands[0]
	default:
		p.refusal = askWhichComputer("Which computer", cands)
	}
	return p, nil
}

// askWhichComputer is the refusal asking which of cs: "Which computer: the MacBook or the iMac?"
func askWhichComputer(lead string, cs []voiceComputer) *pyjson.Object {
	labels := make([]string, len(cs))
	for i, c := range cs {
		labels[i] = c.label()
	}
	return recordingRefusal("which_computer", lead+": "+orList(labels)+"?")
}

// noComputer is the refusal when no computer is in reach: none set up, or none of the speaker's.
func noComputer(all []voiceComputer) *pyjson.Object {
	if len(all) == 0 {
		return recordingRefusal("no_computer", `That needs the Jarvis Automation agent on a computer: set it up there, then ask me again.`)
	}
	return recordingRefusal("no_computer", "I can only do that on your own computer. Ask me there, or sign in to Jarvis Automation on it.")
}

// orList joins "a", "a or b", "a, b or c".
func orList(xs []string) string {
	return joinLast(xs, " or ")
}

func joinLast(xs []string, last string) string {
	if len(xs) <= 1 {
		return strings.Join(xs, "")
	}
	return strings.Join(xs[:len(xs)-1], ", ") + last + xs[len(xs)-1]
}

func computerProp() *pyjson.Object {
	return strProp("Which computer, by its name, only when the user named one (e.g. 'the MacBook'); leave it empty for the computer they're at or their only one")
}

// setComputerControl turns target's switch on or off through its agent and says what it did.
func (m *Module) setComputerControl(ctx context.Context, target voiceComputer, on bool, uid int64) *pyjson.Object {
	state := map[bool]string{true: "on", false: "off"}[on]
	already := servertools.Obj("status", state, "message", "Computer control is already "+state+" for "+target.label()+".")
	if on && target.row.computerOn() {
		return already
	}
	// Off is the safe direction, so it is asked of the agent even when the heartbeat says off.
	out := m.agentRecordingCall(ctx, target.row.nodeID, computeruse.ToolSwitch, map[string]any{"on": on}, uid)
	if !dispatchOK(out) {
		if !on && !target.row.computerOn() {
			return already
		}
		return recordingRefusal(agentCode(out, "switch_failed"),
			"I couldn't turn computer control "+state+" for "+target.label()+": "+agentError(out)+".")
	}
	m.deps.Log.Info("cc: computer control turned "+state+" by voice", "node", target.row.nodeID, "user", uid)
	return servertools.Obj("status", state, "message", "Computer control is "+state+" for "+target.label()+".")
}

type enableComputerControlTool struct{ m *Module }

func (*enableComputerControlTool) Name() string { return toolEnableComputerControl }

func (*enableComputerControlTool) Definition() *pyjson.Object {
	return recordingToolDefinition(toolEnableComputerControl,
		"ONLY when the user says the words 'computer control' ('enable computer control', 'turn on computer control'): "+
			"turns on the 'Computer control' switch that lets Jarvis use their computer's mouse, keyboard and browser. "+
			"Never for 'turn on the computer' (powering a computer on, which Jarvis can't do), dark mode or any other setting.",
		servertools.Obj("computer", computerProp()))
}

func (t *enableComputerControlTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	if e := toolSpeakerError(turn); e != nil {
		return e, nil
	}
	// C11: never from something the user said that wasn't asking for it ("turn on the computer").
	if u := strings.ToLower(turn.Utterance); u != "" && !strings.Contains(u, "control") {
		return recordingRefusal("not_asked", `Computer control is only turned on when you ask for it by name: "enable computer control". `+
			"I can't power a computer on."), nil
	}
	p, err := t.m.pickComputer(ctx, turn, argText(call, "computer"), ownComputers, nil)
	if err != nil || p.refusal != nil {
		return p.refusal, err
	}
	return t.m.setComputerControl(ctx, *p.target, true, turn.Speaker.UserID), nil
}

type disableComputerControlTool struct{ m *Module }

func (*disableComputerControlTool) Name() string { return toolDisableComputerControl }

func (*disableComputerControlTool) Definition() *pyjson.Object {
	return recordingToolDefinition(toolDisableComputerControl,
		"Turn off computer control on a computer: Jarvis stops using it, and a task running there stops. Use when "+
			"the user says 'disable computer control', 'turn off computer control'. Works from any device.",
		servertools.Obj("computer", computerProp()))
}

func (t *disableComputerControlTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	if e := toolSpeakerError(turn); e != nil {
		return e, nil
	}
	p, err := t.m.pickComputer(ctx, turn, argText(call, "computer"), anyComputer, (*nodeRow).computerOn)
	if err != nil {
		return nil, err
	}
	if p.refusal != nil && !p.hasNamed && p.here == nil && len(p.reach) > 0 && !anyOn(p.reach) {
		return servertools.Obj("status", "off", "message", "Computer control is already off on every computer."), nil
	}
	if p.refusal != nil {
		return p.refusal, nil
	}
	return t.m.setComputerControl(ctx, *p.target, false, turn.Speaker.UserID), nil
}

func anyOn(cs []voiceComputer) bool {
	for _, c := range cs {
		if c.row.computerOn() {
			return true
		}
	}
	return false
}

type computerControlStatusTool struct{ m *Module }

func (*computerControlStatusTool) Name() string { return toolComputerControlStatus }

func (*computerControlStatusTool) Definition() *pyjson.Object {
	return recordingToolDefinition(toolComputerControlStatus,
		"Say whether computer control is on: 'is computer control on?', 'can you use my computer?'.",
		servertools.Obj("computer", computerProp()))
}

func (t *computerControlStatusTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	if e := toolSpeakerError(turn); e != nil {
		return e, nil
	}
	p, err := t.m.pickComputer(ctx, turn, argText(call, "computer"), ownComputers, nil)
	if err != nil {
		return nil, err
	}
	cs := p.reach
	switch {
	case p.target != nil:
		cs = []voiceComputer{*p.target}
	case p.hasNamed || len(cs) == 0:
		return p.refusal, nil
	}
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = map[bool]string{true: "on", false: "off"}[c.row.computerOn()] + " for " + c.label()
	}
	return servertools.Obj("status", "ok", "message", "Computer control is "+joinLast(parts, " and ")+"."), nil
}
