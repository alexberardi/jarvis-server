package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/policy"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

// TestCasesParse: every case file parses and validates, names are unique and match their files,
// every check is of a known kind, and the suite has exactly the planned cases.
func TestCasesParse(t *testing.T) {
	cases, err := LoadCases("cases", "tools/ccsuite/cases")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range cases {
		names = append(names, c.Name)
		if c.File != "tools/ccsuite/cases/"+c.Name+".json" {
			t.Errorf("%s: file %q", c.Name, c.File)
		}
		for _, ch := range c.Checks {
			if _, ok := checkKinds[ch.Kind]; !ok {
				t.Errorf("%s: unknown check %q", c.Name, ch.Kind)
			}
		}
		if strings.Contains(c.Say, "{{sites}}") && strings.Contains(c.Message("http://127.0.0.1:1/"), "{{") {
			t.Errorf("%s: placeholders left in %q", c.Name, c.Message("http://127.0.0.1:1/"))
		}
	}
	want := []string{"blocked_site", "budget_last_month", "injection_stops", "menu_big_page", "notes_macos", "notes_wlroots", "notes_x11",
		"replay_changed_param", "replay_moved_button", "store_order_approve", "store_order_cancel", "web_form"}
	if !slices.Equal(names, want) {
		t.Errorf("cases %v, want %v", names, want)
	}
	// The teach cases replay what the suite demonstrated.
	for _, c := range cases {
		if c.Phase == "teach" && c.Setup.Demonstrate != "bank_download" {
			t.Errorf("%s: setup.demonstrate %q, want bank_download", c.Name, c.Setup.Demonstrate)
		}
	}
}

func TestCaseMessage(t *testing.T) {
	c := Case{Say: "go to {{sites}}/bank/{{layout}}", Setup: Setup{Layout: "moved"}}
	if got := c.Message("http://127.0.0.1:5/"); got != "go to http://127.0.0.1:5/bank/?layout=moved" {
		t.Errorf("Message = %q", got)
	}
	c.Setup.Layout = ""
	if got := c.Message("http://127.0.0.1:5"); got != "go to http://127.0.0.1:5/bank/" {
		t.Errorf("Message = %q", got)
	}
}

// TestCasesRejected: what LoadCases refuses, each with a message naming the problem.
func TestCasesRejected(t *testing.T) {
	good := `{"name": "x", "phase": "browser", "status": "pending", "say": "hi", "target": "browser",
	  "setup": {"login": ["store"]}, "answers": [], "checks": [CHECK], "max_minutes": 5}`
	for name, tc := range map[string]struct{ file, body, want string }{
		"unknown kind":            {"x", strings.Replace(good, "CHECK", `{"kind": "teleported"}`, 1), `kind "teleported"`},
		"missing field":           {"x", strings.Replace(good, "CHECK", `{"kind": "note_exists"}`, 1), "note_exists needs title"},
		"in_cart needs a product": {"x", strings.Replace(good, "CHECK", `{"kind": "in_cart"}`, 1), "in_cart needs product"},
		"bad regexp":              {"x", strings.Replace(good, "CHECK", `{"kind": "reply_matches", "pattern": "("}`, 1), "missing closing"},
		"bad month":               {"x", strings.Replace(good, "CHECK", `{"kind": "download", "site": "bank", "month": "Sept"}`, 1), "YYYY-MM"},
		"unknown field":           {"x", strings.Replace(good, "CHECK", `{"kind": "no_order", "colour": "red"}`, 1), "unknown field"},
		"name vs file":            {"y", strings.Replace(good, "CHECK", `{"kind": "no_order"}`, 1), "not the file's name"},
		"bad phase":               {"x", strings.Replace(strings.Replace(good, "CHECK", `{"kind": "no_order"}`, 1), `"browser", "status"`, `"web", "status"`, 1), `phase "web"`},
		"bad login site":          {"x", strings.Replace(strings.Replace(good, "CHECK", `{"kind": "no_order"}`, 1), `["store"]`, `["shop"]`, 1), `setup.login "shop"`},
		"bad block site":          {"x", strings.Replace(strings.Replace(good, "CHECK", `{"kind": "no_order"}`, 1), `["store"]}`, `["store"], "block": ["http://x/"]}`, 1), `setup.block "http://x/"`},
		"bad demonstration": {"x", strings.Replace(strings.Replace(good, "CHECK", `{"kind": "no_order"}`, 1), `["store"]}`, `["store"], "demonstrate": "bank_upload"}`, 1),
			`setup.demonstrate "bank_upload": one of bank_download`},
		"demonstration on the desktop": {"x", strings.Replace(strings.Replace(strings.Replace(good, "CHECK", `{"kind": "no_order"}`, 1),
			`["store"]}`, `["store"], "demonstrate": "bank_download"}`, 1), `"target": "browser"`, `"target": "desktop"`, 1), "needs target browser"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.file+".json"), []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadCases(dir, "cases")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one with %q", err, tc.want)
			}
		})
	}
}

// TestBlockRules: setup.block replaces the household's computer-task rules with one block rule
// per site, so a case's block never outlives it; a case without one leaves none.
func TestBlockRules(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Write.ExecContext(ctx, `CREATE TABLE cc_tool_rules (id INTEGER PRIMARY KEY, household_id TEXT NOT NULL,
		scope_kind TEXT NOT NULL, scope_id TEXT NOT NULL DEFAULT '', tool TEXT NOT NULL, target TEXT NOT NULL DEFAULT '',
		effect TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', updated_by INTEGER, updated_at TEXT NOT NULL,
		UNIQUE (household_id, scope_kind, scope_id, tool, target))`); err != nil {
		t.Fatal(err)
	}
	st := &policy.Store{DB: d}
	if err := setBlockRules(ctx, st, "hh", []string{"127.0.0.1", "Shop.Example"}); err != nil {
		t.Fatal(err)
	}
	rules, _ := st.ListRules(ctx, "hh")
	var got []string
	for _, r := range rules {
		got = append(got, fmt.Sprintf("%s %s %s %s", r.ScopeKind, r.Tool, r.Target, r.Effect))
	}
	want := []string{"household computer_task site:127.0.0.1 block", "household computer_task site:shop.example block"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rules = %q, want %q", got, want)
	}
	if err := setBlockRules(ctx, st, "hh", nil); err != nil {
		t.Fatal(err)
	}
	if rules, _ := st.ListRules(ctx, "hh"); len(rules) != 0 {
		t.Errorf("after a case without block: %d rules left", len(rules))
	}
}
