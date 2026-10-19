package cc

import (
	"context"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
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
	if hh == "" {
		return out, nil
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+nodeCols+` FROM cc_nodes WHERE household_id = ? AND is_active ORDER BY node_id`, hh)
	if err != nil {
		return out, err
	}
	var nodes []*nodeRow
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			rows.Close()
			return out, err
		}
		if n.desktop() {
			nodes = append(nodes, n)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	var names map[string]string
	if len(nodes) > 0 {
		names = m.nodeNames(ctx, nodes...)
	}
	for _, n := range nodes {
		name := names[n.nodeID]
		if name == n.nodeID {
			name = ""
		}
		c := computerNode{id: n.nodeID, name: name}
		if n.computerOn() {
			out.on = append(out.on, c)
		} else {
			out.off = append(out.off, c)
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

// withoutComputerTools drops a node's computer-control tools (computeruse.IsInternalTool) and
// their commands: offered only for a computer whose switch is on.
func withoutComputerTools(tools, commands []*pyjson.Object) ([]*pyjson.Object, []*pyjson.Object) {
	var keptTools, keptCmds []*pyjson.Object
	for _, t := range tools {
		if !computeruse.IsInternalTool(toolName(t)) {
			keptTools = append(keptTools, t)
		}
	}
	for _, c := range commands {
		if !computeruse.IsInternalTool(toolName2(c)) {
			keptCmds = append(keptCmds, c)
		}
	}
	return keptTools, keptCmds
}

// computerControlOff is offMessage when a request for goal needs a computer and no computer in
// the household has the switch on, else "". A failed lookup is logged and lets it through: the
// agent refuses on its own (C8).
func (m *Module) computerControlOff(ctx context.Context, hh, goal string) string {
	if !needsComputer(goal) {
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

// computerControlHint is the turn's line when it needs a computer and none has the switch on.
func (m *Module) computerControlHint(ctx context.Context, conv *conversation, utterance string) string {
	msg := m.computerControlOff(ctx, conv.householdID, utterance)
	if msg == "" {
		return ""
	}
	return "[computer control: off on every computer. If doing this needs Jarvis to use a computer (buying or ordering " +
		"online, filling in a website's forms, working in an app), don't start an errand or use any other tool for it; " +
		"tell the user: " + msg + "]"
}

// needsComputer is whether a request (an utterance, an errand's goal) asks Jarvis to act on a
// website or app: buy, order, check out, fill in a form. A phone call ("call Joe's and order …")
// and research don't.
func needsComputer(s string) bool {
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
	if has("call") || has("ring") || has("dial") {
		return false
	}
	for i, w := range ws {
		next := ""
		if i+1 < len(ws) {
			next = ws[i+1]
		}
		switch w {
		case "buy", "purchase", "checkout", "reorder", "cart":
			return true
		case "order":
			// The verb: "order me a …", "order a …"; not "in order", "my order", "did I order".
			if orderObjects[next] && (i == 0 || !orderNotVerb[ws[i-1]]) {
				return true
			}
		case "check":
			if next == "out" {
				return true
			}
		case "fill":
			if (next == "out" || next == "in") && has("form") {
				return true
			}
		}
	}
	return false
}

// orderObjects follow "order" used as a verb; orderNotVerb precede "order" when it isn't one.
var (
	orderObjects = map[string]bool{"me": true, "us": true, "a": true, "an": true, "some": true, "the": true, "it": true,
		"one": true, "two": true, "new": true, "more": true, "another": true, "them": true, "my": true}
	orderNotVerb = map[string]bool{"in": true, "my": true, "the": true, "an": true, "your": true, "this": true, "that": true,
		"i": true, "we": true, "you": true, "did": true}
)
