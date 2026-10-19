// Package policy decides whether a background action runs without a tap, asks, or is blocked
// (docs/autonomy/PLAN.md §1, A7), and keeps the autonomy activity log.
package policy

import (
	"context"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Effect is what a rule (or the default) says about a background action.
type Effect string

const (
	Allow Effect = "allow"
	Ask   Effect = "ask"
	Block Effect = "block"
)

const ( // Action.Source and Event.Source
	SourceErrandPlan = "errand_plan"
	SourceErrandStep = "errand_step"
	SourceAutomation = "automation"
	SourceWatch      = "watch"
)

const ( // rule scopes
	ScopeHousehold  = "household"
	ScopeGoal       = "goal"
	ScopeWatch      = "watch"
	ScopeAutomation = "automation"
)

// Scope is a per-task scope. Effect, when set, is an inline rule (an automation's delivery).
type Scope struct {
	Kind, ID string
	Effect   Effect
}

// Action is one background tool call to decide on.
type Action struct {
	Tool                            string
	Target                          string // "", site:<host>, app:<id> or action:<category>
	NewApp                          Effect // app: target used for the first time: computer.new_app ("" = allow)
	Args                            any    // *pyjson.Object or map[string]any; only "business" is read
	IsRisky, RunWithoutConfirmation bool
	Source                          string
	Scope                           *Scope
}

// Rule is one row of cc_tool_rules.
type Rule struct {
	ID                                    int64
	HouseholdID, ScopeKind, ScopeID, Tool string
	Target                                string // "" = every target of the tool
	Effect                                Effect
	Note                                  string
	UpdatedBy                             *int64
	UpdatedAt                             time.Time
}

// Verdict is Decide's answer.
type Verdict struct {
	Effect  Effect
	Rule    *Rule  // the table rule that matched, or nil
	Scope   string // household|goal|watch|automation|tool|default|"" (what decided it)
	Reason  string
	Warning string // a declaration ignored because the tool is risky / has a business party
}

// RuleSource looks up one rule.
type RuleSource interface {
	Rule(ctx context.Context, hh, scopeKind, scopeID, tool, target string) (*Rule, error) // nil, nil: none
}

// Policy resolves actions against the household's rules.
type Policy struct{ Rules RuleSource } // nil Rules: no rules

// Decide resolves an action, first match wins (A7): an inline scope effect, a scoped table rule,
// the household rule, the computer task defaults, the tool's run_without_confirmation
// declaration, then ask. Within a scope, a rule for the action's target wins (for a site, then
// each parent domain), then the tool's rule for every target; a scoped rule beats any household
// rule.
func (p *Policy) Decide(ctx context.Context, hh string, a Action) Verdict {
	if s := a.Scope; s != nil && s.Effect != "" {
		return Verdict{Effect: s.Effect, Scope: s.Kind, Reason: "set on the " + s.Kind}
	}
	if p.Rules != nil {
		type scope struct{ kind, id string }
		scopes := []scope{{ScopeHousehold, ""}}
		if s := a.Scope; s != nil && s.ID != "" {
			scopes = []scope{{s.Kind, s.ID}, {ScopeHousehold, ""}}
		}
		targets := targetChain(a.Target)
		for _, s := range scopes {
			for _, t := range targets {
				r, err := p.Rules.Rule(ctx, hh, s.kind, s.id, a.Tool, t)
				if err != nil {
					return unavailable()
				}
				if r != nil {
					return Verdict{Effect: r.Effect, Rule: r, Scope: s.kind, Reason: s.kind + " rule"}
				}
			}
		}
	}
	if a.Tool == ToolComputerTask {
		if r := computerDefault(a); r != nil {
			return Verdict{Effect: r.Effect, Scope: "default", Reason: "computer task default (" + r.Target + ")"}
		}
	}
	var warning string
	if a.RunWithoutConfirmation {
		switch {
		case a.IsRisky:
			warning = a.Tool + " declares run_without_confirmation but is risky; ignored"
		case hasBusiness(a.Args):
			warning = a.Tool + " declares run_without_confirmation but has a business counterparty; ignored"
		default:
			return Verdict{Effect: Allow, Scope: "tool", Reason: "the tool declares run_without_confirmation"}
		}
	}
	return Verdict{Effect: Ask, Reason: "no rule; asks by default", Warning: warning}
}

// targetChain is the targets to look a rule up by, most specific first: the target, for a site
// each parent domain down to two labels (site:shop.example.com, site:example.com), then "".
func targetChain(target string) []string {
	if target == "" {
		return []string{""}
	}
	if t, err := normalizeTarget(target); err == nil {
		target = t
	}
	out := []string{target}
	if host, ok := strings.CutPrefix(target, TargetSite); ok {
		for {
			_, parent, ok := strings.Cut(host, ".")
			if !ok || !strings.Contains(parent, ".") {
				break
			}
			host = parent
			out = append(out, TargetSite+host)
		}
	}
	return append(out, "")
}

func unavailable() Verdict { return Verdict{Effect: Ask, Reason: "rules unavailable; asks"} }

// hasBusiness reports whether the args name a business counterparty.
func hasBusiness(args any) bool {
	var v any
	switch a := args.(type) {
	case *pyjson.Object:
		if a == nil {
			return false
		}
		v, _ = a.Get("business")
	case map[string]any:
		v = a["business"]
	default:
		return false
	}
	switch b := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(b) != ""
	case bool:
		return b
	default:
		return true
	}
}

const ( // Event.Outcome
	OutcomeLaunched = "launched"
	OutcomeRan      = "ran"
	OutcomeAsked    = "asked"
	OutcomeBlocked  = "blocked"
	OutcomeFailed   = "failed"
	OutcomeStopped  = "stopped"
	OutcomeFired    = "fired"
)

// Event is one row of the autonomy activity log (cc_autonomy_events).
type Event struct {
	ID                     int64
	HouseholdID            string
	UserID                 *int64
	Source, SourceID, Tool string
	Effect                 Effect
	Outcome                string
	RuleID                 *int64
	Reason                 string
	Detail                 map[string]any
	CreatedAt              time.Time
}
