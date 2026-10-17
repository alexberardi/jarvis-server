package cc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/policy"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/watches"
)

const watchURLItem = "https://www.example.com/item"

func newWatchEnv(t *testing.T) *sigEnv {
	t.Helper()
	se := newSigEnv(t)
	se.set(settingHouseholdTimezone, "America/New_York", sigHH)
	return se
}

// createWatch stores a web watch on the price at example.com, with prev as its last value.
func (se *sigEnv) createWatch(hh, fire string, cooldown int, cond watches.Condition, prev string) watches.Watch {
	se.t.Helper()
	ctx := context.Background()
	uid := int64(7)
	w, err := se.m.watches.Create(ctx, watches.Watch{HouseholdID: hh, UserID: &uid, Title: "Kettle price", SourceKind: watches.SourceWeb,
		Source: watches.Source{URL: watchURLItem, Extract: "the price"}, Condition: cond, FirePolicy: fire, CooldownSeconds: cooldown})
	if err != nil {
		se.t.Fatal(err)
	}
	if prev != "" {
		if err := se.m.watches.RecordCheck(ctx, w.ID, prev, ""); err != nil {
			se.t.Fatal(err)
		}
	}
	return se.watch(hh, w.ID)
}

func (se *sigEnv) watch(hh, id string) watches.Watch {
	se.t.Helper()
	w, err := se.m.watches.Get(context.Background(), hh, id)
	if err != nil || w == nil {
		se.t.Fatal(w, err)
	}
	return *w
}

// observe checks the stored watch against value, as a check would.
func (se *sigEnv) observe(id, value string) bool {
	se.t.Helper()
	w := se.watch(sigHH, id)
	return se.m.observeWatch(context.Background(), w, watchObservation{Value: value, SourceURL: watchURLItem, At: se.clock()})
}

func (se *sigEnv) alerts() []map[string]any {
	var out []map[string]any
	for _, it := range se.items() {
		if it.Category == "watch_alert" {
			out = append(out, it.Metadata)
		}
	}
	return out
}

var priceBelow100 = watches.Condition{Op: watches.OpLT, Value: "100"}

func TestWatchFireOnce(t *testing.T) {
	se := newWatchEnv(t)
	ctx := context.Background()
	w := se.createWatch(sigHH, watches.FireOnce, 0, priceBelow100, "$129")
	if !se.observe(w.ID, "$99") {
		t.Fatal("did not fire")
	}
	items := se.items()
	if len(items) != 1 {
		t.Fatal(items)
	}
	it := items[0]
	if it.Category != "watch_alert" || it.Title != "👀 Kettle price" || it.Summary != "$129 → $99" {
		t.Fatal(it.Category, it.Title, it.Summary)
	}
	if !strings.HasPrefix(it.Body, "Watching for the price to drop below 100 on example.com.\n\n$129 → $99\nChecked ") ||
		!strings.Contains(it.Body, "EDT") && !strings.Contains(it.Body, "EST") ||
		!strings.HasSuffix(it.Body, "\n[Open example.com]("+watchURLItem+")") {
		t.Fatal(it.Body)
	}
	md := it.Metadata
	want := map[string]any{"household_id": sigHH, "card_type": "watch_alert", "watch_id": w.ID, "state": "done",
		"looking_for": "the price to drop below 100 on example.com", "value": "$99", "previous": "$129",
		"checked_at": se.clock().UTC().Format(time.RFC3339), "source_url": watchURLItem, "source_label": "example.com"}
	for k, v := range want {
		if md[k] != v {
			t.Fatalf("%s: got %v, want %v", k, md[k], v)
		}
	}
	if _, ok := md["interactive_elements"]; ok || len(md) != len(want) {
		t.Fatal(md)
	}
	if got := se.watch(sigHH, w.ID); got.State != watches.StateDone || got.LastFiredAt == nil || got.LastValue != "$99" {
		t.Fatal(got)
	}
	evs, err := se.m.autonomy.Events(ctx, sigHH, 0)
	if err != nil || len(evs) != 1 {
		t.Fatal(evs, err)
	}
	if e := evs[0]; e.Source != policy.SourceWatch || e.SourceID != w.ID || e.Effect != policy.Allow || e.Outcome != policy.OutcomeFired ||
		e.Detail["value"] != "$99" || e.Detail["previous"] != "$129" {
		t.Fatal(e)
	}
}

func TestWatchNotMet(t *testing.T) {
	se := newWatchEnv(t)
	w := se.createWatch(sigHH, watches.FireOnce, 0, priceBelow100, "$135")
	if se.observe(w.ID, "$129") {
		t.Fatal("fired")
	}
	if len(se.items()) != 0 {
		t.Fatal(se.items())
	}
	if got := se.watch(sigHH, w.ID); got.LastValue != "$129" || got.State != watches.StateActive || got.LastCheckedAt == nil {
		t.Fatal(got)
	}
}

func TestWatchEveryChange(t *testing.T) {
	se := newWatchEnv(t)
	w := se.createWatch(sigHH, watches.FireEveryChange, 0, priceBelow100, "$129")
	se.observe(w.ID, "$99")
	se.observe(w.ID, "$99")
	if n := len(se.alerts()); n != 1 {
		t.Fatalf("same value: %d alerts", n)
	}
	se.observe(w.ID, "$89")
	alerts := se.alerts()
	if len(alerts) != 2 {
		t.Fatalf("new value: %d alerts", len(alerts))
	}
	for _, md := range alerts {
		els, _ := md["interactive_elements"].([]any)
		if md["state"] != "active" || len(els) != 2 {
			t.Fatal(md)
		}
		p, s := els[0].(map[string]any), els[1].(map[string]any)
		if p["label"] != "Pause" || p["callback"] != "pause_watch" || s["label"] != "Stop watching" || s["callback"] != "cancel_watch" ||
			p["command"] != "watch" || p["target"] != "server" || p["data"].(map[string]any)["watch_id"] != w.ID {
			t.Fatal(els)
		}
	}
	if alerts[1]["previous"] != "$99" || alerts[1]["value"] != "$89" {
		t.Fatal(alerts[1])
	}
	if got := se.watch(sigHH, w.ID); got.State != watches.StateActive {
		t.Fatal(got.State)
	}
}

func TestWatchCooldown(t *testing.T) {
	se := newWatchEnv(t)
	w := se.createWatch(sigHH, watches.FireCooldown, 3600, priceBelow100, "$129")
	se.observe(w.ID, "$99")
	se.advance(10 * time.Minute)
	se.observe(w.ID, "$98")
	if n := len(se.alerts()); n != 1 {
		t.Fatalf("within cooldown: %d alerts", n)
	}
	se.advance(61 * time.Minute)
	se.observe(w.ID, "$97")
	if n := len(se.alerts()); n != 2 {
		t.Fatalf("after cooldown: %d alerts", n)
	}
}

func TestWatchObservationError(t *testing.T) {
	se := newWatchEnv(t)
	w := se.createWatch(sigHH, watches.FireOnce, 0, priceBelow100, "$129")
	if se.m.observeWatch(context.Background(), w, watchObservation{Err: "page did not load", At: se.clock()}) {
		t.Fatal("fired")
	}
	if got := se.watch(sigHH, w.ID); got.LastError != "page did not load" || got.LastValue != "$129" {
		t.Fatal(got)
	}
	if len(se.items()) != 0 {
		t.Fatal(se.items())
	}
}

func TestWatchCallbacks(t *testing.T) {
	se := newWatchEnv(t)
	ctx := context.Background()
	w := se.createWatch(sigHH, watches.FireEveryChange, 0, priceBelow100, "")
	tap := func(hh, cb string) callbackResult {
		t.Helper()
		f, ok := se.m.serverCallbacks()[callbackKey("watch", cb)]
		if !ok {
			t.Fatal("no callback", cb)
		}
		r, err := f(ctx, callbackContext{JobID: "j", HouseholdID: hh, Data: map[string]any{"watch_id": w.ID}})
		if err != nil || !r.Success {
			t.Fatal(r, err)
		}
		return r
	}
	inbox := func(r callbackResult) map[string]any { return r.ContextData["inbox"].(map[string]any) }

	// Another household's id changes nothing.
	if r := tap("hh-other", "cancel_watch"); r.ContextData != nil {
		t.Fatal(r)
	}
	if se.watch(sigHH, w.ID).State != watches.StateActive {
		t.Fatal("changed")
	}

	r := inbox(tap(sigHH, "pause_watch"))
	if r["title"] != "⏸️ Watch paused" || r["summary"] != "I'll stop checking “Kettle price” until you resume it." {
		t.Fatal(r)
	}
	els := r["metadata"].(map[string]any)["interactive_elements"].([]any)
	if e := els[0].(map[string]any); e["id"] != "resume-watch" || e["label"] != "Resume" || e["callback"] != "resume_watch" {
		t.Fatal(els)
	}
	if se.watch(sigHH, w.ID).State != watches.StatePaused {
		t.Fatal("not paused")
	}
	if r := inbox(tap(sigHH, "pause_watch")); !strings.Contains(r["title"].(string), "already") {
		t.Fatal(r)
	}

	if r := inbox(tap(sigHH, "resume_watch")); r["title"] != "▶️ Watch resumed" || r["summary"] != "I'll keep checking “Kettle price”." {
		t.Fatal(r)
	}
	if se.watch(sigHH, w.ID).State != watches.StateActive {
		t.Fatal("not resumed")
	}

	if r := inbox(tap(sigHH, "cancel_watch")); r["title"] != "🛑 Stopped watching" || r["summary"] != "I won't check “Kettle price” any more." {
		t.Fatal(r)
	}
	if se.watch(sigHH, w.ID).State != watches.StateDone {
		t.Fatal("not done")
	}
	if r := inbox(tap(sigHH, "cancel_watch")); !strings.Contains(r["title"].(string), "Already") {
		t.Fatal(r)
	}

	// A missing watch is a quiet success.
	f := se.m.serverCallbacks()[callbackKey("watch", "pause_watch")]
	if r, err := f(ctx, callbackContext{HouseholdID: sigHH, Data: map[string]any{"watch_id": "wt_missing"}}); err != nil || !r.Success {
		t.Fatal(r, err)
	}
}

func TestWatchSweep(t *testing.T) {
	se := newWatchEnv(t)
	ctx := context.Background()
	exp := se.clock().Add(time.Hour)
	uid := int64(7)
	w, err := se.m.watches.Create(ctx, watches.Watch{HouseholdID: sigHH, UserID: &uid, Title: "Short", SourceKind: watches.SourceWeb,
		Source: watches.Source{URL: watchURLItem, Extract: "the price"}, Condition: priceBelow100, FirePolicy: watches.FireOnce, ExpiresAt: &exp})
	if err != nil {
		t.Fatal(err)
	}
	keep := se.createWatch(sigHH, watches.FireOnce, 0, priceBelow100, "")
	if err := se.m.sweepWatches(ctx); err != nil || se.watch(sigHH, w.ID).State != watches.StateActive {
		t.Fatal("expired early", err)
	}
	se.advance(2 * time.Hour)
	if err := se.m.sweepWatches(ctx); err != nil {
		t.Fatal(err)
	}
	if se.watch(sigHH, w.ID).State != watches.StateDone || se.watch(sigHH, keep.ID).State != watches.StateActive {
		t.Fatal("sweep")
	}
}
