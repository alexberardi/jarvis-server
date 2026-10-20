package computer

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// Verify before done (plan P3 piece 12, C4).

func thanksPage() Observation {
	return Observation{URL: "https://shop.example.com/thanks?order=1234", Title: "Thank you", Gen: 2,
		Image: &Image{MIME: "image/png", Data: []byte("png"), CaptureID: "c9", Width: 800, Height: 600},
		Elements: []Element{
			{Ref: "e1", Role: "link", Name: "Home"},
			{Ref: "e2", Role: "link", Name: "Order 1234"},
			{Ref: "e3", Role: "button", Name: "Track   your order"},
		}}
}

func (e *tenv) verify(c *SuccessCheck) Verified {
	return e.loop.Verify(context.Background(), "cr_1", task().Target, Pick{Label: llm.LabelBackground, Vision: true}, c)
}

func TestVerifyURLAndText(t *testing.T) {
	e := newEnv(t, thanksPage())
	v := e.verify(&SuccessCheck{URLContains: "/THANKS", TextPresent: []string{"order 1234", "track your order"}})
	if !v.OK || v.Checked != `the address contains "/THANKS"; the page shows "order 1234"; the page shows "track your order"` {
		t.Fatalf("verify %+v", v)
	}
	if o := e.exec.observes; len(o) != 1 || !o[0].Snapshot || !o[0].Screenshot || o[0].RunID != "cr_1" {
		t.Fatalf("observes %+v", e.exec.observes)
	}
	if len(e.llm.reqs) != 0 {
		t.Fatalf("text in the snapshot needs no model call: %d", len(e.llm.reqs))
	}

	if v := e.verify(&SuccessCheck{URLContains: "/receipt"}); v.OK || !strings.Contains(v.Why, `not one with "/receipt"`) {
		t.Fatalf("wrong address passed: %+v", v)
	}

	// Text the snapshot doesn't hold is looked for on the screenshot.
	e.script("No, the page shows an error.")
	v = e.verify(&SuccessCheck{TextPresent: []string{"Order confirmed"}})
	if v.OK || v.Why != `the page doesn't show "Order confirmed" (the page shows an error.)` {
		t.Fatalf("verify %+v", v)
	}
	e.script("Yes. It says Order confirmed.")
	if v := e.verify(&SuccessCheck{TextPresent: []string{"Order confirmed"}}); !v.OK {
		t.Fatalf("verify %+v", v)
	}
	req := e.llm.reqs[len(e.llm.reqs)-1]
	if req.Label != llm.LabelBackground || req.Priority != llm.PriorityBackground || *req.Temperature != 0 {
		t.Fatalf("screen request %+v", req)
	}
	if parts := req.Messages[1].Content.Parts; len(parts) != 2 || parts[1].ImageURL == nil ||
		!strings.Contains(parts[0].Text, `"Order confirmed"`) {
		t.Fatalf("screen request parts %+v", parts)
	}
}

func TestVerifyScreen(t *testing.T) {
	e := newEnv(t, thanksPage())
	e.script("yes")
	if v := e.verify(&SuccessCheck{Screen: "Is an order number shown?"}); !v.OK || v.Checked != "Is an order number shown?: yes" {
		t.Fatalf("verify %+v", v)
	}
	e.script("Maybe.")
	if v := e.verify(&SuccessCheck{Screen: "Is an order number shown?"}); v.OK {
		t.Fatalf("an unclear answer passed: %+v", v)
	}
	// Without vision the screen check fails; it never passes unchecked.
	v := e.loop.Verify(context.Background(), "cr_1", task().Target, Pick{Label: llm.LabelBackground}, &SuccessCheck{Screen: "Is it done?"})
	if v.OK || !strings.Contains(v.Why, "can't see screenshots") {
		t.Fatalf("verify %+v", v)
	}
}

func TestVerifyDownload(t *testing.T) {
	e := newEnv(t)
	e.exec.files = map[string]FileInfo{"list ~/Downloads": {Entries: []FileEntry{
		{Name: "Statements", Dir: true},
		{Name: "statement-2026-09.pdf.crdownload", Bytes: 90000},
		{Name: "statement-2026-08.pdf", Bytes: 12},
		{Name: "Statement-2026-09.pdf", Bytes: 48 << 10},
	}}}
	v := e.verify(&SuccessCheck{Download: &DownloadCheck{NameLike: "statement*.pdf", MinBytes: 1000}})
	if !v.OK || v.Checked != "Statement-2026-09.pdf is in Downloads (48 KB)" {
		t.Fatalf("verify %+v", v)
	}
	if len(e.exec.observes) != 0 || len(e.exec.fileArgs) != 1 || e.exec.fileArgs[0] != (Files{RunID: "cr_1", Op: "list", Path: "~/Downloads"}) {
		t.Fatalf("observes %d, files %+v", len(e.exec.observes), e.exec.fileArgs)
	}
	if v := e.verify(&SuccessCheck{Download: &DownloadCheck{NameLike: "2026-08", MinBytes: 1000}}); v.OK ||
		v.Why != "statement-2026-08.pdf in Downloads is too small to be it" {
		t.Fatalf("verify %+v", v)
	}
	// Separators don't matter: the house model writes statement_2026_09 for statement-2026-09.
	if v := e.verify(&SuccessCheck{Download: &DownloadCheck{NameLike: "STATEMENT_2026 09", MinBytes: 1000}}); !v.OK ||
		v.Checked != "Statement-2026-09.pdf is in Downloads (48 KB)" {
		t.Fatalf("verify %+v", v)
	}
	if v := e.verify(&SuccessCheck{Download: &DownloadCheck{NameLike: "invoice"}}); v.OK ||
		v.Why != `there's no file like "invoice" in Downloads` {
		t.Fatalf("verify %+v", v)
	}
}

func TestVerifyFile(t *testing.T) {
	e := newEnv(t)
	e.exec.files = map[string]FileInfo{
		"read budget.csv": {Path: "/home/a/Documents/Jarvis/budget.csv", Bytes: 40, Text: "date,amount\n2026-09-01,12.50\nTotal,  412.00\n"},
		"read scan.pdf":   {Path: "/home/a/Documents/Jarvis/scan.pdf", Bytes: 4000},
	}
	if v := e.verify(&SuccessCheck{File: &FileCheck{Path: "budget.csv", Contains: []string{"2026-09-01", "total, 412.00"}}}); !v.OK ||
		v.Checked != `budget.csv contains "2026-09-01", "total, 412.00"` {
		t.Fatalf("verify %+v", v)
	}
	if v := e.verify(&SuccessCheck{File: &FileCheck{Path: "budget.csv", Contains: []string{"2026-10"}}}); v.OK ||
		v.Why != `budget.csv doesn't contain "2026-10"` {
		t.Fatalf("verify %+v", v)
	}
	if v := e.verify(&SuccessCheck{File: &FileCheck{Path: "scan.pdf"}}); !v.OK || v.Checked != "scan.pdf exists" {
		t.Fatalf("verify %+v", v)
	}
	if v := e.verify(&SuccessCheck{File: &FileCheck{Path: "scan.pdf", Contains: []string{"x"}}}); v.OK {
		t.Fatalf("a file that isn't text passed a contains check: %+v", v)
	}
	if v := e.verify(&SuccessCheck{File: &FileCheck{Path: "missing.csv"}}); v.OK || !strings.Contains(v.Why, "couldn't read missing.csv") {
		t.Fatalf("verify %+v", v)
	}
	if v := e.verify(&SuccessCheck{}); v.OK {
		t.Fatalf("an empty check passed: %+v", v)
	}
}

const doneThanks = `{"done": true, "summary": "Ordered the soap.", "verify": {"url_contains": "/thanks"}}`

func TestVerifyFailsThenRecovers(t *testing.T) {
	e := newEnv(t, shopPage(), shopPage(), shopPage(), thanksPage())
	e.script(doneThanks, actions(press("e4", "Open the cart")), doneThanks)
	out := e.run()
	if out.State != StateDone || out.Checked != `the address contains "/thanks"` ||
		out.Summary != `Ordered the soap. Checked: the address contains "/thanks".` || out.Rounds != 3 {
		t.Fatalf("outcome %+v", out)
	}
	// The failed check is the next round's feedback.
	if txt := userText(e.llm.reqs[1]); !strings.Contains(txt, `Last turn: you said done, but my check failed: the address is https://shop.example.com/soap, not one with "/thanks". So the task isn't finished: don't say done now; answer with the action it still needs`) {
		t.Fatalf("no feedback in the prompt:\n%s", txt)
	}
	r := e.storedRun(out.RunID)
	if r.State != RunDone || r.Verify == nil || r.Verify.FailedAt != 1 {
		t.Fatalf("stored run %+v %+v", r, r.Verify)
	}
}

func TestVerifyFailsReports(t *testing.T) {
	e := newEnv(t)
	e.script(doneThanks, actions(press("e1", "Go home")), doneThanks, actions(press("e1", "Go home")),
		actions(press("e1", "Go home")), doneThanks, actions(press("e1", "Go home")))
	out := e.run()
	if out.State != StateFailed || out.Rounds != 6 ||
		out.Summary != `I couldn't confirm the address contains "/thanks": the address is https://shop.example.com/soap, not one with "/thanks".` {
		t.Fatalf("outcome %+v", out)
	}
	if len(e.llm.answers) != 1 {
		t.Fatalf("expected 1 round + 5 more, %d answers left", len(e.llm.answers))
	}
	if r := e.storedRun(out.RunID); r.State != RunFailed || r.Error != out.Summary {
		t.Fatalf("stored run %+v", r)
	}
	if len(e.exec.stops) == 0 {
		t.Fatal("the agent's run was not stopped")
	}
}

func TestVerifyBudgetSurvivesResume(t *testing.T) {
	e := newEnv(t)
	e.script(doneThanks, actions(press("e1", "Go home")), `{"ask": "Which colour?", "reason": "unclear"}`)
	out := e.run()
	if out.State != StateAsk || out.Rounds != 3 {
		t.Fatalf("outcome %+v", out)
	}
	if r := e.storedRun(out.RunID); r.Verify == nil || r.Verify.FailedAt != 1 || r.Verify.Why == "" {
		t.Fatalf("stored verify %+v", r.Verify)
	}
	tk := task()
	tk.RunID, tk.Answer = out.RunID, "purple"
	e.script(actions(press("e1", "Go home")), actions(press("e1", "Go home")), actions(press("e1", "Go home")),
		actions(press("e1", "Go home")))
	out = e.loop.Run(context.Background(), tk)
	if out.State != StateFailed || out.Rounds != 6 || !strings.HasPrefix(out.Summary, "I couldn't confirm") {
		t.Fatalf("outcome %+v", out)
	}
}

func TestDoneWithoutCheckAsksOnce(t *testing.T) {
	t.Run("then gives one", func(t *testing.T) {
		e := newEnv(t)
		e.script(`{"done": true, "summary": "Ordered the soap."}`, doneAnswer)
		out := e.run()
		if out.State != StateDone || out.Rounds != 2 {
			t.Fatalf("outcome %+v", out)
		}
		if txt := userText(e.llm.reqs[1]); !strings.Contains(txt, `Last turn: you said done but gave no "verify" check`) {
			t.Fatalf("no request for a check:\n%s", txt)
		}
	})
	t.Run("twice fails", func(t *testing.T) {
		e := newEnv(t)
		e.script(`{"done": true, "summary": "Ordered the soap."}`, `{"done": true, "summary": "Really done."}`)
		out := e.run()
		if out.State != StateFailed || out.Rounds != 2 ||
			out.Summary != "I couldn't confirm the task was finished: there was no way to check it." {
			t.Fatalf("outcome %+v", out)
		}
	})
	t.Run("the task's own check needs none", func(t *testing.T) {
		e := newEnv(t, thanksPage())
		tk := task()
		tk.Success = &SuccessCheck{TextPresent: []string{"Order 1234"}}
		e.script(`{"done": true, "summary": "Ordered the soap.", "verify": {"url_contains": "/nowhere"}}`)
		out := e.loop.Run(context.Background(), tk)
		if out.State != StateDone || out.Checked != `the page shows "Order 1234"` || out.Rounds != 1 {
			t.Fatalf("the task's check comes first: %+v", out)
		}
	})
}

// Text in the page's text (a heading, a paragraph) is checked there, with no model call.
func TestVerifyTextInPageText(t *testing.T) {
	page := thanksPage()
	page.Text = "Thank you, your order is placed\nOrder number:   TS-100001"
	e := newEnv(t, page)
	if v := e.verify(&SuccessCheck{TextPresent: []string{"Order number: TS-100001"}}); !v.OK || len(e.llm.reqs) != 0 {
		t.Fatalf("verify %+v, %d calls", v, len(e.llm.reqs))
	}
}

// The task's success said only in words (a planner's "an order confirmation page") gives way to
// the model's own checks of the address, the text, a download or a file; with none, it is asked
// of the screen.
func TestDoneScreenOnlyTaskTakesModelChecks(t *testing.T) {
	e := newEnv(t, shopPage())
	e.script(doneAnswer)
	tk := task()
	tk.Success = &SuccessCheck{Screen: "an order confirmation page"}
	out := e.loop.Run(context.Background(), tk)
	if out.State != StateDone || out.Checked != `the address contains "/soap"` || len(e.llm.reqs) != 1 {
		t.Fatalf("outcome %+v, %d calls", out, len(e.llm.reqs))
	}

	e = newEnv(t, thanksPage())
	e.script(`{"done": true, "summary": "Ordered the soap.", "verify": {"screen": "is the order placed?"}}`, "Yes, the order page.")
	out = e.loop.Run(context.Background(), tk)
	if out.State != StateDone || out.Checked != "an order confirmation page: yes" {
		t.Fatalf("outcome %+v", out)
	}
}

// A failed download check points at the page's download button.
func TestDownloadCheckFailsPointsAtButton(t *testing.T) {
	page := shopPage()
	page.Elements = append(page.Elements, Element{Ref: "e8", Role: "button", Name: "Download CSV"})
	e := newEnv(t, page)
	e.exec.files = map[string]FileInfo{"list ~/Downloads": {}}
	e.script(`{"done": true, "summary": "Downloaded it.", "verify": {"download": {"name_like": "statement"}}}`, doneAnswer)
	e.run()
	if txt := userText(e.llm.reqs[1]); !strings.Contains(txt, `there's no file like "statement" in Downloads. Nothing was downloaded yet: press "Download CSV" (e8) to download it.`) {
		t.Fatalf("no pointer:\n%s", txt)
	}
}

// A browser task's done names the files it downloaded, for the errand's next steps to read.
func TestDoneNamesDownloads(t *testing.T) {
	e := newEnv(t)
	e.exec.files = map[string]FileInfo{"list ~/Downloads": {Entries: []FileEntry{{Name: "old.pdf", Bytes: 10}}}}
	e.exec.onAct = func() {
		e.exec.files["list ~/Downloads"] = FileInfo{Entries: []FileEntry{{Name: "old.pdf", Bytes: 10},
			{Name: "bank-statement-2026-09.csv", Bytes: 900}, {Name: "card-statement-2026-09.csv", Bytes: 800},
			{Name: "x.csv.crdownload", Bytes: 5}, {Name: "New folder", Dir: true}}}
	}
	e.script(actions(press("e3", "Download it")), `{"done": true, "summary": "Downloaded both.", "verify": {"download": {"name_like": "statement"}}}`)
	out := e.run()
	if out.State != StateDone || !strings.HasPrefix(out.Summary, "Downloaded both. Downloaded: bank-statement-2026-09.csv, card-statement-2026-09.csv (in ~/Downloads). Checked: ") {
		t.Fatalf("outcome %+v", out)
	}

	// No Downloads folder yet when the task began: every file in it at the end is new.
	e = newEnv(t)
	e.exec.onAct = func() {
		e.exec.files = map[string]FileInfo{"list ~/Downloads": {Entries: []FileEntry{{Name: "statement-2026-09.csv", Bytes: 900}}}}
	}
	e.script(actions(press("e3", "Download it")), `{"done": true, "summary": "Downloaded it.", "verify": {"download": {"name_like": "statement"}}}`)
	if out := e.run(); out.State != StateDone || !strings.Contains(out.Summary, "Downloaded: statement-2026-09.csv (in ~/Downloads).") {
		t.Fatalf("outcome %+v", out)
	}

	// Nothing new: nothing said.
	e = newEnv(t)
	e.exec.files = map[string]FileInfo{"list ~/Downloads": {Entries: []FileEntry{{Name: "old.pdf", Bytes: 10}}}}
	e.script(doneAnswer)
	if out := e.run(); out.State != StateDone || strings.Contains(out.Summary, "Downloaded:") {
		t.Fatalf("outcome %+v", out)
	}
}
