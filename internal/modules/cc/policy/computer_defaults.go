package policy

import (
	"errors"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// ToolComputerTask is the tool a computer task's actions are decided under (COMPUTER_CONTROL).
const ToolComputerTask = "computer_task"

const ( // Rule.Target and Action.Target prefixes
	TargetSite   = "site:"   // site:<host>; a rule also covers the host's subdomains
	TargetApp    = "app:"    // app:<id>
	TargetAction = "action:" // action:<computeruse risk category>
)

// computerCategories are the risk categories that ask by default. computeruse.CatDenied is not
// one: the built-in deny list is enforced by the agent and no rule lifts it.
var computerCategories = []string{
	computeruse.CatPay, computeruse.CatSubmitThirdParty, computeruse.CatSend, computeruse.CatDelete,
	computeruse.CatSharing, computeruse.CatInstall, computeruse.CatSubscribe, computeruse.CatUnsubscribe,
	computeruse.CatSecuritySettings, computeruse.CatCaptcha, computeruse.CatSensitiveInput,
}

// ComputerDefaults returns the household rules a computer task gets when none of the household's
// own rules match (they are not stored; a stored rule for the same target overrides one):
// every risk category asks, and any site or app is allowed (autonomy-first, C2). The "site:" and
// "app:" rules stand for every site and every app. newApp is the household's computer.new_app
// setting for an app used for the first time: Ask turns the app default into ask.
func ComputerDefaults(newApp Effect) []Rule {
	out := make([]Rule, 0, len(computerCategories)+2)
	for _, c := range computerCategories {
		out = append(out, defaultRule(TargetAction+c, Ask))
	}
	app := Allow
	if newApp == Ask {
		app = Ask
	}
	return append(out, defaultRule(TargetSite, Allow), defaultRule(TargetApp, app))
}

func defaultRule(target string, e Effect) Rule {
	return Rule{ScopeKind: ScopeHousehold, Tool: ToolComputerTask, Target: target, Effect: e,
		Note: "default"}
}

// computerDefault is the default rule for a computer task's action, or nil: the exact target
// (action:<category>), else the rule for every site or every app.
func computerDefault(a Action) *Rule {
	prefix, _, _ := strings.Cut(a.Target, ":")
	prefix += ":"
	var every *Rule
	for _, r := range ComputerDefaults(a.NewApp) {
		switch r.Target {
		case a.Target:
			return &r
		case prefix:
			if prefix == TargetSite || prefix == TargetApp {
				every = &r
			}
		}
	}
	return every
}

// normalizeTarget checks a rule's target and lowercases a site's host (and drops a trailing dot).
func normalizeTarget(t string) (string, error) {
	t = strings.TrimSpace(t)
	if t == "" {
		return "", nil
	}
	for _, p := range []string{TargetSite, TargetApp, TargetAction} {
		rest, ok := strings.CutPrefix(t, p)
		if !ok {
			continue
		}
		if p == TargetSite {
			rest = strings.TrimSuffix(strings.ToLower(rest), ".")
		}
		if rest == "" || strings.ContainsAny(rest, " \t\n") {
			break
		}
		return p + rest, nil
	}
	return "", errors.New("policy: invalid rule: target")
}
