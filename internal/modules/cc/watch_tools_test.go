package cc

import (
	"context"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/watches"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

const priceWatchArgs = `{"title": "kettle price", "source": "web", "url": "https://www.example.com/item", ` +
	`"look_for": "the price", "op": "lt", "value": 100}`

// watchTurn is a tool turn in the watch env's household for the speaker (0 = unknown).
func watchTurn(speaker int64) servertools.Turn {
	return servertools.Turn{ConversationID: "c", HouseholdID: sigHH, Speaker: servertools.Speaker{UserID: speaker}}
}

func (we *webWatchEnv) tool(name, args string, speaker int64) map[string]any {
	we.t.Helper()
	return toolJSON(we.t, we.m.tools.Execute(context.Background(), call(name, args), watchTurn(speaker)))
}

func (we *webWatchEnv) wantToolError(r map[string]any, code string) {
	we.t.Helper()
	if r["error"] != code {
		we.t.Fatalf("want %s: %v", code, r)
	}
}

func (we *webWatchEnv) live(uid int64) []watches.Watch {
	we.t.Helper()
	ws, err := we.m.watches.List(context.Background(), sigHH, &uid, watches.StateActive, watches.StatePaused)
	if err != nil {
		we.t.Fatal(err)
	}
	return ws
}

func TestWatchToolsRefusals(t *testing.T) {
	we := newWebWatchEnv(t)
	for _, name := range []string{"create_watch", "list_watches", "cancel_watch"} {
		r := we.tool(name, priceWatchArgs, 0)
		if r["error"] != "no_speaker" || r["message"] != "I'm not sure who's speaking." {
			t.Fatalf("%s unknown speaker: %v", name, r)
		}
	}
	we.set(settingWatchesEnabled, false, sigHH)
	for _, name := range []string{"create_watch", "list_watches", "cancel_watch"} {
		r := we.tool(name, priceWatchArgs, 7)
		if r["error"] != "disabled" ||
			r["message"] != "Watches are turned off for this household. A household admin can turn them on." {
			t.Fatalf("%s disabled: %v", name, r)
		}
	}
	we.set(settingWatchesEnabled, true, sigHH)
	we.set(settingWebScrapingExternal, false, sigHH)
	r := we.tool("create_watch", priceWatchArgs, 7)
	if r["error"] != "web_fetch_off" ||
		r["message"] != "I can't watch web pages until a household admin allows outside web fetches." {
		t.Fatalf("web fetch off: %v", r)
	}
	if n := len(we.live(7)); n != 0 {
		t.Fatalf("%d watches stored by refusals", n)
	}
}

func TestCreateWatchWeb(t *testing.T) {
	we := newWebWatchEnv(t)
	r := we.tool("create_watch", priceWatchArgs, 7)
	if r["status"] != "watching" || r["description"] != "the price to drop below 100 on example.com" {
		t.Fatalf("create: %v", r)
	}
	ws := we.live(7)
	if len(ws) != 1 {
		t.Fatalf("%d watches", len(ws))
	}
	w := ws[0]
	if r["watch_id"] != w.ID || w.UserID == nil || *w.UserID != 7 || w.Title != "kettle price" ||
		w.FirePolicy != watches.FireOnce || w.CadenceSeconds != 3600 || w.Condition.Value != "100" {
		t.Fatalf("row %+v", w)
	}
	if r["expires_at"] != w.ExpiresAt.UTC().Format(time.RFC3339) {
		t.Fatalf("expires_at %v, row %v", r["expires_at"], w.ExpiresAt)
	}
	if at := we.nextCheck(w.ID); !at.Equal(we.clock()) {
		t.Fatalf("first check at %v, want now %v", at, we.clock())
	}

	// repeat keeps watching; a cadence under the household's minimum is raised to it.
	r = we.tool("create_watch", `{"title": "kettle", "source": "web", "query": "kettle deals", "look_for": "the price", `+
		`"op": "changed", "every_minutes": 5, "repeat": true}`, 7)
	w2, _ := we.m.watches.Get(context.Background(), sigHH, r["watch_id"].(string))
	if w2 == nil || w2.FirePolicy != watches.FireEveryChange || w2.CadenceSeconds != 30*60 {
		t.Fatalf("repeat: %v %+v", r, w2)
	}
}

func TestCreateWatchSignal(t *testing.T) {
	we := newWebWatchEnv(t)
	r := we.tool("create_watch", `{"title": "when I leave home", "source": "signal", "signal_kind": "presence.left", `+
		`"field": "state", "op": "equals", "value": "away"}`, 7)
	if r["status"] != "watching" {
		t.Fatalf("create: %v", r)
	}
	w, _ := we.m.watches.Get(context.Background(), sigHH, r["watch_id"].(string))
	if w == nil || w.Source.Kind != "presence.left" || w.Source.UserID == nil || *w.Source.UserID != 7 || w.CadenceSeconds != 0 {
		t.Fatalf("row %+v", w)
	}
	if !we.nextCheck(w.ID).IsZero() {
		t.Fatal("a signal watch was armed")
	}
	// absent_for takes for_minutes; an appointment is the household's (no member filter).
	r = we.tool("create_watch", `{"title": "door", "source": "signal", "signal_kind": "appt.upcoming", "field": "location", `+
		`"op": "absent_for", "value": "home", "for_minutes": 20}`, 7)
	w, _ = we.m.watches.Get(context.Background(), sigHH, r["watch_id"].(string))
	if w == nil || w.Condition.For != "20m" || w.Source.UserID != nil {
		t.Fatalf("absent_for: %v %+v", r, w)
	}
	we.wantToolError(we.tool("create_watch", `{"title": "x", "source": "signal", "signal_kind": "nope", "op": "changed"}`, 7), "invalid")
}

func TestCreateWatchInvalid(t *testing.T) {
	we := newWebWatchEnv(t)
	r := we.tool("create_watch", `{"title": "kettle", "source": "web", "url": "https://www.example.com/item", `+
		`"look_for": "the price", "op": "below"}`, 7)
	if r["error"] != "invalid" || r["message"] != `watch: condition: unknown op "below"` {
		t.Fatalf("invalid op: %v", r)
	}
	if n := len(we.live(7)); n != 0 {
		t.Fatalf("%d watches stored", n)
	}
}

func TestCreateWatchTooMany(t *testing.T) {
	we := newWebWatchEnv(t)
	we.set(settingWatchesMaxActive, 1, sigHH)
	we.create(watches.Source{URL: watchURLItem, Extract: "the price"}, 0) // another member's counts too
	r := we.tool("create_watch", priceWatchArgs, 8)
	if r["error"] != "too_many" || r["message"] != "You already have 1 watches. Cancel one first." {
		t.Fatalf("too many: %v", r)
	}
}

func TestListWatches(t *testing.T) {
	we := newWebWatchEnv(t)
	mine := we.tool("create_watch", priceWatchArgs, 8)["watch_id"].(string)
	paused := we.tool("create_watch", `{"title": "toaster", "source": "web", "url": "https://www.example.com/t", `+
		`"look_for": "the price", "op": "changed"}`, 8)["watch_id"].(string)
	done := we.tool("create_watch", priceWatchArgs, 8)["watch_id"].(string)
	ctx := context.Background()
	if _, err := we.m.watches.SetState(ctx, sigHH, paused, watches.StatePaused); err != nil {
		t.Fatal(err)
	}
	if _, err := we.m.watches.SetState(ctx, sigHH, done, watches.StateDone); err != nil {
		t.Fatal(err)
	}
	we.create(watches.Source{URL: watchURLItem, Extract: "the price"}, 0) // member 7's
	if err := we.m.watches.RecordCheck(ctx, mine, "$120", ""); err != nil {
		t.Fatal(err)
	}
	r := we.tool("list_watches", `{}`, 8)
	list, _ := r["watches"].([]any)
	if len(list) != 2 {
		t.Fatalf("list: %v", r)
	}
	byID := map[string]map[string]any{}
	for _, e := range list {
		byID[e.(map[string]any)["watch_id"].(string)] = e.(map[string]any)
	}
	first, second := byID[mine], byID[paused]
	if first == nil || first["state"] != "active" || first["last_value"] != "$120" || first["last_checked_at"] == nil ||
		first["description"] != "the price to drop below 100 on example.com" {
		t.Fatalf("first: %v", first)
	}
	if second == nil || second["state"] != "paused" || second["last_checked_at"] != nil {
		t.Fatalf("second: %v", second)
	}
	tool, _ := we.m.tools.Get("list_watches")
	if !servertools.RunsWithoutConfirmation(tool) {
		t.Fatal("list_watches needs a tap")
	}
}

func TestCancelWatch(t *testing.T) {
	we := newWebWatchEnv(t)
	kettle := we.tool("create_watch", priceWatchArgs, 8)["watch_id"].(string)
	we.tool("create_watch", `{"title": "Kettle deals", "source": "web", "query": "kettle deals", "look_for": "the price", `+
		`"op": "changed"}`, 8)
	toaster := we.tool("create_watch", `{"title": "Toaster price", "source": "web", "url": "https://www.example.com/t", `+
		`"look_for": "the price", "op": "changed"}`, 8)["watch_id"].(string)
	other := we.createURL() // member 7's "Kettle price"

	r := we.tool("cancel_watch", `{"watch": "KETTLE"}`, 8)
	cands, _ := r["candidates"].([]any)
	if r["error"] != "ambiguous" || len(cands) != 2 || !(cands[0] == "kettle price" && cands[1] == "Kettle deals" ||
		cands[1] == "kettle price" && cands[0] == "Kettle deals") {
		t.Fatalf("ambiguous: %v", r)
	}
	we.wantToolError(we.tool("cancel_watch", `{"watch": "lawnmower"}`, 8), "not_found")
	// Another member's watch is invisible: neither by id nor by title.
	we.wantToolError(we.tool("cancel_watch", `{"watch": "`+other.ID+`"}`, 8), "not_found")

	r = we.tool("cancel_watch", `{"watch": "`+kettle+`"}`, 8)
	if r["status"] != "cancelled" || r["watch_id"] != kettle || r["title"] != "kettle price" {
		t.Fatalf("by id: %v", r)
	}
	if got := we.watch(sigHH, kettle); got.State != watches.StateDone || !we.nextCheck(kettle).IsZero() {
		t.Fatalf("not done and disarmed: %s %v", got.State, we.nextCheck(kettle))
	}
	r = we.tool("cancel_watch", `{"watch": "toaster"}`, 8)
	if r["status"] != "cancelled" || r["watch_id"] != toaster {
		t.Fatalf("by title: %v", r)
	}
	if got := we.watch(sigHH, other.ID); got.State != watches.StateActive {
		t.Fatalf("member 7's watch is %s", got.State)
	}
}

// TestWatchToolsOffered: the tools are offered at warmup only with watches on and a known speaker.
func TestWatchToolsOffered(t *testing.T) {
	names := []string{"create_watch", "list_watches", "cancel_watch"}
	ce := newChatEnv(t, prompts.Qwen3_8B)
	cid := ce.warm("tok-7")
	for _, n := range names {
		if ce.m.convs.get(cid).serverNames[n] {
			t.Fatalf("%s offered with watches off", n)
		}
	}
	ce.set(settingWatchesEnabled, true, settings.Scope{HouseholdID: voiceHH})
	cid = ce.warm("tok-7")
	for _, n := range names {
		if !ce.m.convs.get(cid).serverNames[n] {
			t.Fatalf("%s not offered to a known speaker: %v", n, ce.m.convs.get(cid).serverNames)
		}
	}
	// A node conversation with recognition off has no known speaker.
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.set(settingWatchesEnabled, true, settings.Scope{HouseholdID: voiceHH})
	ve.start("c1", weatherTool)
	for _, n := range names {
		if ve.m.convs.get("c1").serverNames[n] {
			t.Fatalf("%s offered to an unknown speaker", n)
		}
	}
}
