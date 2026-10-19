package cc

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

const wfBase = "/api/v0/workflows"

// wfEnv is a household hh-1 with an owner (1), another member (2) and an admin (3), and a member
// (4) of hh-2.
type wfEnv struct {
	*env
	owner, member, admin, outsider hdr
}

func newWFEnv(t *testing.T) wfEnv {
	t.Helper()
	e := newEnv(t, envOpts{noMQTT: true})
	return wfEnv{e,
		bearer(e.auth.addUser(1, "hh-1", authn.RoleMember)),
		bearer(e.auth.addUser(2, "hh-1", authn.RoleMember)),
		bearer(e.auth.addUser(3, "hh-1", authn.RoleAdmin)),
		bearer(e.auth.addUser(4, "hh-2", authn.RoleMember)),
	}
}

func wfStep() WStep {
	return WStep{Intent: "open the bank's statements page", Target: "browser", Site: "bank.example",
		Action: computeruse.Action{Kind: "navigate", Intent: "open statements", URL: "https://bank.example/statements"},
		Element: &RecordedElement{Role: "link", Name: "Statements"}}
}

// seedWorkflow stores a workflow of hh-1 owned by user 1.
func (e wfEnv) seedWorkflow(name string, shared bool) TaughtWorkflow {
	e.t.Helper()
	w, err := e.m.createWorkflow(context.Background(), TaughtWorkflow{
		HouseholdID: "hh-1", OwnerID: 1, Name: name, Shared: shared,
		Triggers: []string{"download my statement"},
		Params:   []Param{{Name: "month", Kind: "month", Example: "September"}},
		Steps:    []WStep{wfStep()},
		Success:  computer.SuccessCheck{Download: &computer.DownloadCheck{NameLike: "statement", MinBytes: 1000}},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return w
}

func wfNames(l []any) []string {
	var out []string
	for _, x := range l {
		out = append(out, x.(map[string]any)["name"].(string))
	}
	return out
}

func TestWorkflowVisibility(t *testing.T) {
	e := newWFEnv(t)
	private := e.seedWorkflow("Bank statement", false)
	shared := e.seedWorkflow("Order printer ink", true)
	if private.Slug != "bank_statement" || private.Version != 1 || !strings.HasPrefix(private.ID, "tw_") {
		t.Fatalf("created: %+v", private)
	}

	list := func(h hdr) []string {
		return wfNames(e.do("GET", wfBase+"?household_id=hh-1", nil, h).want(200).json()["workflows"].([]any))
	}
	if got := list(e.owner); strings.Join(got, ",") != "Bank statement,Order printer ink" {
		t.Errorf("owner sees %v", got)
	}
	if got := list(e.member); strings.Join(got, ",") != "Order printer ink" {
		t.Errorf("another member sees %v", got)
	}
	e.do("GET", wfBase+"?household_id=hh-1", nil, e.outsider).detail(403, "User is not a member of this household")
	e.do("GET", wfBase, nil, e.owner).want(400) // household_id is required

	// One workflow: the owner reads both, with the stored fields.
	got := e.do("GET", wfBase+"/"+private.ID, nil, e.owner).want(200).json()
	if got["slug"] != "bank_statement" || got["editable"] != true || got["shared"] != false ||
		got["success"].(map[string]any)["download"].(map[string]any)["name_like"] != "statement" ||
		got["steps"].([]any)[0].(map[string]any)["element"].(map[string]any)["role"] != "link" {
		t.Fatalf("owner GET: %v", got)
	}
	// Another member sees the shared one (not editable) and not the private one.
	if got := e.do("GET", wfBase+"/"+shared.ID, nil, e.member).want(200).json(); got["editable"] != false {
		t.Errorf("member GET shared: %v", got)
	}
	e.do("GET", wfBase+"/"+private.ID, nil, e.member).detail(404, "Workflow not found")
	// Another household sees neither, shared or not, and can't tell them from a missing one.
	e.do("GET", wfBase+"/"+shared.ID, nil, e.outsider).detail(404, "Workflow not found")
	e.do("GET", wfBase+"/"+private.ID, nil, e.outsider).detail(404, "Workflow not found")
	e.do("GET", wfBase+"/tw_missing", nil, e.owner).detail(404, "Workflow not found")
	// Run (replay is piece 31) checks the same visibility first.
	e.do("POST", wfBase+"/"+private.ID+"/run", nil, e.member).detail(404, "Workflow not found")
	e.do("POST", wfBase+"/"+shared.ID+"/run", nil, e.member).want(501)
}

func wfPutBody(w TaughtWorkflow) map[string]any {
	return map[string]any{"name": w.Name, "triggers": w.Triggers, "params": w.Params, "steps": w.Steps,
		"success": w.Success, "shared": w.Shared}
}

func TestOnlyOwnerEdits(t *testing.T) {
	e := newWFEnv(t)
	w := e.seedWorkflow("Bank statement", true)

	// A member who only sees it (shared) may not change or delete it.
	edit := w
	edit.Name = "Monthly bank statement"
	e.do("PUT", wfBase+"/"+w.ID, wfPutBody(edit), e.member).detail(403, "Only the workflow's owner or a household admin can change it")
	e.do("DELETE", wfBase+"/"+w.ID, nil, e.member).want(403)
	e.do("PUT", wfBase+"/"+w.ID, wfPutBody(edit), e.outsider).want(404)
	e.do("DELETE", wfBase+"/"+w.ID, nil, e.outsider).want(404)

	// The owner renames it: new slug, version bumped.
	got := e.do("PUT", wfBase+"/"+w.ID, wfPutBody(edit), e.owner).want(200).json()
	if got["name"] != "Monthly bank statement" || got["slug"] != "monthly_bank_statement" || got["version"] != float64(2) {
		t.Fatalf("owner PUT: %v", got)
	}
	// A household admin may too (here: stop sharing it), though they don't own it.
	edit.Shared = false
	if got := e.do("PUT", wfBase+"/"+w.ID, wfPutBody(edit), e.admin).want(200).json(); got["shared"] != false || got["version"] != float64(3) {
		t.Fatalf("admin PUT: %v", got)
	}
	// Unshared, the admin still sees it to manage it; the other member no longer does.
	if got := e.do("GET", wfBase+"/"+w.ID, nil, e.admin).want(200).json(); got["editable"] != true {
		t.Errorf("admin GET: %v", got)
	}
	e.do("GET", wfBase+"/"+w.ID, nil, e.member).want(404)

	// A second workflow with the renamed one's name gets its own slug.
	w2 := e.seedWorkflow("Monthly bank statement", false)
	if w2.Slug != "monthly_bank_statement_2" {
		t.Errorf("slug clash: %q", w2.Slug)
	}

	e.do("DELETE", wfBase+"/"+w.ID, nil, e.admin).want(200)
	e.do("GET", wfBase+"/"+w.ID, nil, e.owner).want(404)
	e.do("DELETE", wfBase+"/"+w2.ID, nil, e.owner).want(200)
	if n := len(e.do("GET", wfBase+"?household_id=hh-1", nil, e.owner).want(200).json()["workflows"].([]any)); n != 0 {
		t.Errorf("%d left", n)
	}
}

func TestValidateSteps(t *testing.T) {
	e := newWFEnv(t)
	w := e.seedWorkflow("Bank statement", false)
	put := func(mut func(*TaughtWorkflow)) *resp {
		c := w
		c.Steps = []WStep{wfStep(), wfStep()}
		c.Params = append([]Param(nil), w.Params...)
		mut(&c)
		return e.do("PUT", wfBase+"/"+w.ID, wfPutBody(c), e.owner)
	}
	put(func(c *TaughtWorkflow) { c.Steps[1].Action.Kind = "teleport" }).detail(400, `steps[1]: unknown kind "teleport"`)
	put(func(c *TaughtWorkflow) { c.Steps[1].Action.Intent = "" }).detail(400, "steps[1]: intent is required")
	put(func(c *TaughtWorkflow) { c.Steps[0].Action = computeruse.Action{Kind: "click", Intent: "click"} }).
		detail(400, "steps[0]: click needs x and y")
	put(func(c *TaughtWorkflow) { c.Steps[0].Intent = " " }).detail(400, "steps[0]: intent is required")
	put(func(c *TaughtWorkflow) { c.Steps[1].Target = "phone" }).detail(400, "steps[1]: target must be browser or desktop")
	put(func(c *TaughtWorkflow) { c.Steps[1].Risk = "yolo" }).detail(400, `steps[1]: unknown risk "yolo"`)
	put(func(c *TaughtWorkflow) { c.Steps = nil }).detail(400, "steps: a workflow needs at least one step")
	put(func(c *TaughtWorkflow) { c.Name = "  " }).detail(400, "name is required")
	put(func(c *TaughtWorkflow) { c.Params[0].Kind = "colour" }).detail(400, "params[0]: kind must be one of date, month, text, number, file")
	put(func(c *TaughtWorkflow) { c.Params = append(c.Params, c.Params[0]) }).detail(400, `params[1]: duplicate name "month"`)
	put(func(c *TaughtWorkflow) { c.Triggers = []string{""} }).detail(400, "triggers[0]: empty")
	// An unknown field is refused rather than dropped.
	e.do("PUT", wfBase+"/"+w.ID, map[string]any{"name": "x", "owner_id": 2}, e.owner).want(400)
	// Nothing was stored by the refusals; a valid two-step PUT is.
	if got := e.do("GET", wfBase+"/"+w.ID, nil, e.owner).want(200).json(); got["version"] != float64(1) {
		t.Fatalf("a refused PUT changed it: %v", got)
	}
	got := put(func(c *TaughtWorkflow) { c.Steps[1].Risk = computeruse.CatPay }).want(200).json()
	if s := got["steps"].([]any); len(s) != 2 || s[1].(map[string]any)["risk"] != "pay" {
		t.Fatalf("valid PUT: %v", got)
	}
	// The store refuses an invalid workflow too (piece 30 creates them).
	if _, err := e.m.createWorkflow(context.Background(), TaughtWorkflow{HouseholdID: "hh-1", OwnerID: 1, Name: "x"}); err == nil {
		t.Error("createWorkflow accepted a workflow without steps")
	}
}

func TestWorkflowsPurgedWithTheirOwner(t *testing.T) {
	e := newWFEnv(t)
	e.seedWorkflow("Bank statement", true)
	tx, err := e.d.Write.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.PurgeUser(context.Background(), tx, 1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := len(e.do("GET", wfBase+"?household_id=hh-1", nil, e.member).want(200).json()["workflows"].([]any)); n != 0 {
		t.Errorf("%d workflows outlive their owner", n)
	}
}
