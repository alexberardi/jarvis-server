package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// fakeRules is a RuleSource keyed scopeKind|scopeID|tool.
type fakeRules struct {
	rules map[string]*Rule
	err   error
}

func (f fakeRules) Rule(_ context.Context, _, scopeKind, scopeID, tool string) (*Rule, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.rules[scopeKind+"|"+scopeID+"|"+tool], nil
}

func rule(kind, id, tool string, e Effect) *Rule {
	return &Rule{HouseholdID: "hh", ScopeKind: kind, ScopeID: id, Tool: tool, Effect: e}
}

func obj(k string, v any) *pyjson.Object {
	o := pyjson.NewObject()
	o.Set(k, v)
	return o
}

func TestDecide(t *testing.T) {
	hhAllow := rule(ScopeHousehold, "", "t", Allow)
	hhBlock := rule(ScopeHousehold, "", "t", Block)
	hhAsk := rule(ScopeHousehold, "", "t", Ask)
	watchAllow := rule(ScopeWatch, "wt_1", "t", Allow)
	const (
		riskyWarn    = "t declares run_without_confirmation but is risky; ignored"
		businessWarn = "t declares run_without_confirmation but has a business counterparty; ignored"
	)
	cases := []struct {
		name    string
		rules   RuleSource
		action  Action
		effect  Effect
		scope   string
		reason  string
		rule    *Rule
		warning string
	}{
		{name: "no rules, plain tool", rules: fakeRules{}, action: Action{Tool: "t"},
			effect: Ask, scope: "", reason: "no rule; asks by default"},
		{name: "household allow", rules: fakeRules{rules: map[string]*Rule{"household||t": hhAllow}},
			action: Action{Tool: "t"}, effect: Allow, scope: ScopeHousehold, reason: "household rule", rule: hhAllow},
		{name: "household block", rules: fakeRules{rules: map[string]*Rule{"household||t": hhBlock}},
			action: Action{Tool: "t"}, effect: Block, scope: ScopeHousehold, reason: "household rule", rule: hhBlock},
		{name: "watch rule beats household",
			rules:  fakeRules{rules: map[string]*Rule{"household||t": hhBlock, "watch|wt_1|t": watchAllow}},
			action: Action{Tool: "t", Scope: &Scope{Kind: ScopeWatch, ID: "wt_1"}},
			effect: Allow, scope: ScopeWatch, reason: "watch rule", rule: watchAllow},
		{name: "inline automation effect beats household",
			rules:  fakeRules{rules: map[string]*Rule{"household||t": hhBlock}},
			action: Action{Tool: "t", Scope: &Scope{Kind: ScopeAutomation, ID: "presence.left", Effect: Allow}},
			effect: Allow, scope: ScopeAutomation, reason: "set on the automation"},
		{name: "declared, no rules", rules: fakeRules{}, action: Action{Tool: "t", RunWithoutConfirmation: true},
			effect: Allow, scope: "tool", reason: "the tool declares run_without_confirmation"},
		{name: "declared but risky", rules: fakeRules{},
			action: Action{Tool: "t", RunWithoutConfirmation: true, IsRisky: true},
			effect: Ask, scope: "", reason: "no rule; asks by default", warning: riskyWarn},
		{name: "declared, pyjson business", rules: fakeRules{},
			action: Action{Tool: "t", RunWithoutConfirmation: true, Args: obj("business", "Joe's Pizza")},
			effect: Ask, scope: "", reason: "no rule; asks by default", warning: businessWarn},
		{name: "declared, map business", rules: fakeRules{},
			action: Action{Tool: "t", RunWithoutConfirmation: true, Args: map[string]any{"business": "Joe's"}},
			effect: Ask, scope: "", reason: "no rule; asks by default", warning: businessWarn},
		{name: "declared, empty business", rules: fakeRules{},
			action: Action{Tool: "t", RunWithoutConfirmation: true, Args: obj("business", "")},
			effect: Allow, scope: "tool", reason: "the tool declares run_without_confirmation"},
		{name: "household ask beats declaration", rules: fakeRules{rules: map[string]*Rule{"household||t": hhAsk}},
			action: Action{Tool: "t", RunWithoutConfirmation: true},
			effect: Ask, scope: ScopeHousehold, reason: "household rule", rule: hhAsk},
		{name: "rules error", rules: fakeRules{err: errors.New("db down")},
			action: Action{Tool: "t", RunWithoutConfirmation: true},
			effect: Ask, scope: "", reason: "rules unavailable; asks"},
		{name: "nil rules, plain", action: Action{Tool: "t"},
			effect: Ask, scope: "", reason: "no rule; asks by default"},
		{name: "nil rules, declared", action: Action{Tool: "t", RunWithoutConfirmation: true},
			effect: Allow, scope: "tool", reason: "the tool declares run_without_confirmation"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &Policy{Rules: c.rules}
			v := p.Decide(context.Background(), "hh", c.action)
			if v.Effect != c.effect || v.Scope != c.scope || v.Reason != c.reason {
				t.Errorf("verdict = %q / %q / %q, want %q / %q / %q",
					v.Effect, v.Scope, v.Reason, c.effect, c.scope, c.reason)
			}
			if v.Rule != c.rule {
				t.Errorf("rule = %+v, want %+v", v.Rule, c.rule)
			}
			if v.Warning != c.warning {
				t.Errorf("warning = %q, want %q", v.Warning, c.warning)
			}
		})
	}
}

func TestHasBusiness(t *testing.T) {
	var nilObj *pyjson.Object
	cases := []struct {
		args any
		want bool
	}{
		{nil, false},
		{nilObj, false},
		{obj("business", "  "), false},
		{obj("business", false), false},
		{obj("business", nil), false},
		{obj("business", true), true},
		{obj("business", map[string]any{"name": "x"}), true},
		{map[string]any{"business": "x"}, true},
		{map[string]any{"other": "x"}, false},
		{"business", false},
	}
	for i, c := range cases {
		if got := hasBusiness(c.args); got != c.want {
			t.Errorf("case %d: hasBusiness(%#v) = %v, want %v", i, c.args, got, c.want)
		}
	}
}
