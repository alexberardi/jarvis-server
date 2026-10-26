package cc

import (
	"context"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Computer control's switch (docs/agent/COMPUTER-CONTROL.md C11): each computer opts in with
// "Computer control" in its agent's menu, off by default. The agent reports it in the heartbeat
// as the computer_control capability ("on" / "off"; an older agent reports nothing, which is
// off) and enforces it itself (C8). Here, computer tools and computer-task errands only target
// computers with it on; with none on, a request that needs one gets "computer control is off"
// and where to turn it on, never a fallback.

// capComputerControl is the heartbeat capability; capOn its value when on.
const (
	capComputerControl = "computer_control"
	capOn              = "on"
)

// computerNode is one of the household's computers (a desktop agent).
type computerNode struct{ id, name string }

// computerControl is a household's computers by their switch.
type computerControl struct{ on, off []computerNode }

// computerOn reports whether the node is a computer whose switch is on.
func (n *nodeRow) computerOn() bool {
	return n != nil && n.desktop() && n.caps()[capComputerControl] == capOn
}

// computerControl lists the household's active computers by their switch.
func (m *Module) computerControl(ctx context.Context, hh string) (computerControl, error) {
	var out computerControl
	all, err := m.householdComputers(ctx, hh)
	if err != nil {
		return out, err
	}
	for _, c := range all {
		n := computerNode{id: c.row.nodeID, name: c.name}
		if c.row.computerOn() {
			out.on = append(out.on, n)
		} else {
			out.off = append(out.off, n)
		}
	}
	return out, nil
}

// targets are the node ids a computer task may run on: the computers with the switch on.
func (c computerControl) targets() []string {
	ids := make([]string, len(c.on))
	for i, n := range c.on {
		ids[i] = n.id
	}
	return ids
}

// offMessage says computer control is off and where to turn it on.
func (c computerControl) offMessage() string {
	if len(c.off) == 0 {
		return `Computer control needs the Jarvis Automation agent on a computer: set it up there, then turn on "Computer control" in its menu.`
	}
	var names []string
	for _, n := range c.off {
		if n.name != "" {
			names = append(names, n.name)
		}
	}
	where := "your computer"
	if len(names) > 0 {
		where = strings.Join(names, " or ")
	}
	return `Computer control is off. Turn it on with "Computer control" in the Jarvis Automation menu on ` + where + "."
}

// withoutComputerTools drops a node's tools and commands whose name drop reports: its
// computer-control tools (computeruse.IsInternalTool), offered only for a computer whose switch is
// on, or the switch and recorder tools (computeruse.IsServerOnlyTool), never offered.
func withoutComputerTools(tools, commands []*pyjson.Object, drop func(string) bool) ([]*pyjson.Object, []*pyjson.Object) {
	var keptTools, keptCmds []*pyjson.Object
	for _, t := range tools {
		if !drop(toolName(t)) {
			keptTools = append(keptTools, t)
		}
	}
	for _, c := range commands {
		if !drop(toolName2(c)) {
			keptCmds = append(keptCmds, c)
		}
	}
	return keptTools, keptCmds
}

// computerControlOff is offMessage when a request for goal needs a computer and no computer in
// the household has the switch on, else "". With phone calls on, the errand planner may do it by
// phone instead (make_phone_call: "order a pie from Tony's"), so it is let through to the planner.
// A failed lookup is logged and lets it through: the agent refuses on its own (C8).
func (m *Module) computerControlOff(ctx context.Context, hh, goal string) string {
	if !needsComputer(goal) || m.phoneCallsEnabled(ctx, hh) {
		return ""
	}
	cc, err := m.computerControl(ctx, hh)
	if err != nil {
		m.deps.Log.Warn("cc: computer control lookup", "household", hh, "err", err)
		return ""
	}
	if len(cc.on) > 0 {
		return ""
	}
	return cc.offMessage()
}

// phoneCallsEnabled is the errand planner's own check for make_phone_call (errands.PhoneCalls).
func (m *Module) phoneCallsEnabled(ctx context.Context, hh string) bool {
	if m.ErrandPhone != nil {
		return m.ErrandPhone.Enabled(ctx, hh)
	}
	return errandPhone{m}.Enabled(ctx, hh)
}

// computerControlHint is the turn's line when it needs a computer: with none switched on, that
// it's off; with one on, that it's an errand (run_errand), whose computer task drives the
// browser or app with the household's rules and the checkout confirmation, never the turn's own
// browser or desktop tools.
func (m *Module) computerControlHint(ctx context.Context, conv *conversation, utterance string) string {
	if !needsComputer(utterance) {
		return ""
	}
	msg := m.computerControlOff(ctx, conv.householdID, utterance)
	if msg == "" {
		if !conv.serverNames["run_errand"] {
			return ""
		}
		if cc, err := m.computerControl(ctx, conv.householdID); err != nil || len(cc.on) == 0 {
			return ""
		}
		return computerOnHint
	}
	return "[computer control: off on every computer. If doing this needs Jarvis to use a computer (buying or ordering " +
		"online, filling in a website's forms, working in an app), don't start an errand or use any other tool for it; " +
		"tell the user: " + msg + "]"
}

// computerOnHint is the turn's line for a request that needs a computer with the switch on.
const computerOnHint = "[computer control: on. Do this with run_errand, with the user's whole request (every " +
	"detail and web address) as the goal: it runs on the computer and asks the user on their phone before " +
	"paying or sending. Don't use the browser or desktop tools for it yourself.]"

// needsComputer is whether a request (an utterance, an errand's goal) asks Jarvis to act on a
// website or app: buy, order, check out, fill in a form (or a web page), download (a statement).
// "Use the computer to …" asks for it outright (#575). A phone call ("call Joe's and order …"), a reminder ("remind me to buy milk"), a list item and
// research don't, nor do the words after "type" or "write" ("type buy more coffee filters into
// omawrite"): they are the text to type. "Type of" is the noun ("the right type of phone case").
func needsComputer(s string) bool {
	web := strings.Contains(s, "://")
	ws := strings.Fields(strings.ToLower(s))
	for i := range ws {
		ws[i] = compactName(ws[i])
	}
	has := func(w string) bool {
		for _, x := range ws {
			if x == w {
				return true
			}
		}
		return false
	}
	if has("call") || has("ring") || has("dial") || has("remind") || has("reminder") || has("list") {
		return false // a phone call, a reminder or a list item about buying something
	}
	for i, w := range ws {
		next := ""
		if i+1 < len(ws) {
			next = ws[i+1]
		}
		switch w {
		case "type", "write", "dictate":
			if next != "of" { // the noun: "the right type of phone case"
				return false // what follows is the text, typed by the desktop tools
			}
		case "buy", "purchase", "checkout", "reorder", "cart", "download":
			return true
		case "order":
			// The verb: "order me a …", "order a …"; not "in order", "my order", "did I order".
			if orderObjects[next] && (i == 0 || !orderNotVerb[ws[i-1]]) {
				return true
			}
		case "use":
			// "use the computer to find …" (#575): the user asks for the computer itself.
			obj := next
			if (next == "the" || next == "my" || next == "your") && i+2 < len(ws) {
				obj = ws[i+2]
			}
			if computerWords[obj] {
				return true
			}
		case "check":
			if next == "out" {
				return true
			}
		case "fill":
			if (next == "out" || next == "in") && (has("form") || web) {
				return true
			}
		}
	}
	return false
}

// computerWords name the computer after "use (the|my|your)".
var computerWords = map[string]bool{"computer": true, "pc": true, "laptop": true, "desktop": true, "mac": true}

// orderObjects follow "order" used as a verb; orderNotVerb precede "order" when it isn't one.
var (
	orderObjects = map[string]bool{"me": true, "us": true, "a": true, "an": true, "some": true, "the": true, "it": true,
		"one": true, "two": true, "new": true, "more": true, "another": true, "them": true, "my": true}
	orderNotVerb = map[string]bool{"in": true, "my": true, "the": true, "an": true, "your": true, "this": true, "that": true,
		"i": true, "we": true, "you": true, "did": true}
)
