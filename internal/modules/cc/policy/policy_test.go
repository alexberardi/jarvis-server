package policy

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// fakeRules is a RuleSource keyed scopeKind|scopeID|tool, plus |target when there is one.
type fakeRules struct {
	rules map[string]*Rule
	err   error
}

func (f fakeRules) Rule(_ context.Context, _, scopeKind, scopeID, tool, target string) (*Rule, error) {
	if f.err != nil {
		return nil, f.err
	}
	k := scopeKind + "|" + scopeID + "|" + tool
	if target != "" {
		k += "|" + target
	}
	return f.rules[k], nil
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

func TestDecideByTarget(t *testing.T) {
	const ct = ToolComputerTask
	site := &Rule{ScopeKind: ScopeHousehold, Tool: ct, Target: "site:shop.example.com", Effect: Block}
	parent := &Rule{ScopeKind: ScopeHousehold, Tool: ct, Target: "site:example.com", Effect: Ask}
	app := &Rule{ScopeKind: ScopeHousehold, Tool: ct, Target: "app:com.spotify.client", Effect: Block}
	pay := &Rule{ScopeKind: ScopeHousehold, Tool: ct, Target: "action:pay", Effect: Allow}
	toolOnly := &Rule{ScopeKind: ScopeHousehold, Tool: ct, Effect: Block}
	goalSite := &Rule{ScopeKind: ScopeGoal, ScopeID: "g1", Tool: ct, Effect: Allow}
	rules := fakeRules{rules: map[string]*Rule{
		"household||" + ct + "|site:shop.example.com":  site,
		"household||" + ct + "|site:example.com":       parent,
		"household||" + ct + "|app:com.spotify.client": app,
		"household||" + ct + "|action:pay":             pay,
	}}
	withTool := fakeRules{rules: map[string]*Rule{"household||" + ct: toolOnly, "household||" + ct + "|site:example.com": parent}}
	scoped := fakeRules{rules: map[string]*Rule{"household||" + ct + "|site:example.com": parent, "goal|g1|" + ct: goalSite}}
	cases := []struct {
		name   string
		rules  RuleSource
		action Action
		effect Effect
		scope  string
		rule   *Rule
	}{
		{"site exact", rules, Action{Tool: ct, Target: "site:shop.example.com"}, Block, ScopeHousehold, site},
		{"site exact, upper case", rules, Action{Tool: ct, Target: "site:Shop.Example.com"}, Block, ScopeHousehold, site},
		{"parent domain", rules, Action{Tool: ct, Target: "site:www.example.com"}, Ask, ScopeHousehold, parent},
		{"deep parent domain", rules, Action{Tool: ct, Target: "site:a.b.example.com"}, Ask, ScopeHousehold, parent},
		{"sibling domain does not match", rules, Action{Tool: ct, Target: "site:notexample.com"}, Allow, "default", nil},
		{"app", rules, Action{Tool: ct, Target: "app:com.spotify.client"}, Block, ScopeHousehold, app},
		{"action category", rules, Action{Tool: ct, Target: "action:pay"}, Allow, ScopeHousehold, pay},
		{"tool-only fallback", withTool, Action{Tool: ct, Target: "site:other.org"}, Block, ScopeHousehold, toolOnly},
		{"target beats tool-only", withTool, Action{Tool: ct, Target: "site:example.com"}, Ask, ScopeHousehold, parent},
		{"scoped rule wins", scoped, Action{Tool: ct, Target: "site:example.com", Scope: &Scope{Kind: ScopeGoal, ID: "g1"}},
			Allow, ScopeGoal, goalSite},
		{"other tool ignores target rules", rules, Action{Tool: "t", Target: "site:shop.example.com"}, Ask, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := (&Policy{Rules: c.rules}).Decide(context.Background(), "hh", c.action)
			if v.Effect != c.effect || v.Scope != c.scope || v.Rule != c.rule {
				t.Errorf("verdict = %q / %q / %+v (%s), want %q / %q / %+v", v.Effect, v.Scope, v.Rule, v.Reason, c.effect, c.scope, c.rule)
			}
		})
	}
}

func TestTargetChain(t *testing.T) {
	cases := map[string][]string{
		"":                     {""},
		"app:x":                {"app:x", ""},
		"action:pay":           {"action:pay", ""},
		"site:example.com":     {"site:example.com", ""},
		"site:a.b.example.com": {"site:a.b.example.com", "site:b.example.com", "site:example.com", ""},
		"site:localhost":       {"site:localhost", ""},
	}
	for in, want := range cases {
		if got := targetChain(in); !slices.Equal(got, want) {
			t.Errorf("targetChain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestComputerDefaults(t *testing.T) {
	defs := ComputerDefaults("")
	byTarget := map[string]Effect{}
	for _, r := range defs {
		if r.Tool != ToolComputerTask || r.ScopeKind != ScopeHousehold {
			t.Errorf("default %+v", r)
		}
		byTarget[r.Target] = r.Effect
	}
	for _, c := range []string{computeruse.CatPay, computeruse.CatSubmitThirdParty, computeruse.CatSend,
		computeruse.CatDelete, computeruse.CatSharing, computeruse.CatInstall, computeruse.CatSubscribe,
		computeruse.CatUnsubscribe, computeruse.CatSecuritySettings, computeruse.CatCaptcha,
		computeruse.CatSensitiveInput} {
		if byTarget["action:"+c] != Ask {
			t.Errorf("action:%s = %q, want ask", c, byTarget["action:"+c])
		}
	}
	if _, ok := byTarget["action:"+computeruse.CatDenied]; ok {
		t.Error("the deny list is not a rule")
	}
	if byTarget["site:"] != Allow || byTarget["app:"] != Allow || len(defs) != 13 {
		t.Errorf("site/app defaults: %v", byTarget)
	}
	for _, r := range ComputerDefaults(Ask) {
		if r.Target == "app:" && r.Effect != Ask {
			t.Errorf("new_app=ask: app: = %q", r.Effect)
		}
	}

	ct := ToolComputerTask
	stored := &Rule{ScopeKind: ScopeHousehold, Tool: ct, Target: "action:send", Effect: Allow}
	storedSite := &Rule{ScopeKind: ScopeHousehold, Tool: ct, Target: "site:example.com", Effect: Block}
	p := &Policy{Rules: fakeRules{rules: map[string]*Rule{
		"household||" + ct + "|action:send":      stored,
		"household||" + ct + "|site:example.com": storedSite,
	}}}
	cases := []struct {
		name   string
		action Action
		effect Effect
		scope  string
	}{
		{"category asks", Action{Tool: ct, Target: "action:pay"}, Ask, "default"},
		{"stored rule overrides a category", Action{Tool: ct, Target: "action:send"}, Allow, ScopeHousehold},
		{"site allowed", Action{Tool: ct, Target: "site:shop.example.org"}, Allow, "default"},
		{"stored rule overrides a site", Action{Tool: ct, Target: "site:www.example.com"}, Block, ScopeHousehold},
		{"app allowed", Action{Tool: ct, Target: "app:org.gimp.GIMP"}, Allow, "default"},
		{"new app allowed by default", Action{Tool: ct, Target: "app:org.gimp.GIMP", NewApp: Allow}, Allow, "default"},
		{"new_app=ask", Action{Tool: ct, Target: "app:org.gimp.GIMP", NewApp: Ask}, Ask, "default"},
		{"denied is no default", Action{Tool: ct, Target: "action:denied"}, Ask, ""},
		{"no target", Action{Tool: ct}, Ask, ""},
		{"not a computer task", Action{Tool: "t", Target: "site:example.org"}, Ask, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := p.Decide(context.Background(), "hh", c.action)
			if v.Effect != c.effect || v.Scope != c.scope {
				t.Errorf("verdict = %q / %q (%s), want %q / %q", v.Effect, v.Scope, v.Reason, c.effect, c.scope)
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
