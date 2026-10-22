package main

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// The Inbox flows find the rich profile's seeded items by the titles scripts/e2e passes them
// (docs/decisions/2026-10-18-e2e-mobile-runner.md): each must be what scripts/seed-dev.py
// seeds, and the result line what jarvisd answers the card's "Never suggest this" with.
func TestInboxFlowEnvMatchesTheSeed(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	env := map[string]string{}
	for _, m := range regexp.MustCompile(`-e "(INBOX_[A-Z_]+)=([^"$]*)"`).FindAllStringSubmatch(read("../../scripts/e2e"), -1) {
		env[m[1]] = m[2]
	}
	seed, cc := read("../../scripts/seed-dev.py"), read("../../internal/modules/cc/proposals.go")
	for k, src := range map[string]string{
		"INBOX_DETAIL_TITLE": seed, "INBOX_ACTIONS_TITLE": seed, "INBOX_ACTIONS_BUTTON": seed,
		"INBOX_LIST_TITLE": seed, "INBOX_DEPLOYED_TITLE": seed, "INBOX_ACTIONS_RESULT": cc,
	} {
		v := env[k]
		if v == "" {
			t.Errorf("scripts/e2e passes no %s to the flows", k)
			continue
		}
		if !regexp.MustCompile(regexp.QuoteMeta(strconv.Quote(v))).MatchString(src) {
			t.Errorf("%s=%q is not in its source", k, v)
		}
	}
}
