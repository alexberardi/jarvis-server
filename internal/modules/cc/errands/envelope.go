package errands

import "github.com/alexberardi/jarvis-server/internal/modules/cc/parse"

// WidensEnvelope is widens_envelope (workflow_engine.py:468-507), ported byte-for-byte (D15):
// the safety boundary for mid-run pause-and-replan. An amended plan widens the approved one
// when ANY axis holds:
//
//  1. NEW RISKY STEP  — more is_risky steps than the approved plan;
//  2. NEW CAPABILITY  — a command the approved plan doesn't have;
//  3. NEW COUNTERPARTY — an args.business party not already contacted;
//  4. GUARDRAIL-LOOSENING — stubbed false.
//
// Reordering or removing steps never widens.
func WidensEnvelope(approved, amended []Step) bool {
	party := func(s Step) string { return parse.PyStrip(truthyStr(s.Args, "business")) }
	cmds := map[string]bool{}
	parties := map[string]bool{}
	risky := 0
	for _, s := range approved {
		cmds[s.Command] = true
		if p := party(s); p != "" {
			parties[p] = true
		}
		if s.IsRisky {
			risky++
		}
	}
	n := 0
	for _, s := range amended {
		if s.IsRisky {
			n++
		}
	}
	if n > risky {
		return true
	}
	for _, s := range amended {
		if !cmds[s.Command] {
			return true
		}
		if p := party(s); p != "" && !parties[p] {
			return true
		}
	}
	return false
}
