package cc

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func TestMemoryVectorPacking(t *testing.T) {
	b := packVector([]float64{3, 4})
	if len(b) != 8 {
		t.Fatalf("len %d", len(b))
	}
	v := unpackVector(b)
	if math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Fatalf("not normalised: %v", v)
	}
	if c := cosine(v, v); math.Abs(c-1) > 1e-6 {
		t.Fatalf("self cosine %v", c)
	}
	if cosine(v, []float32{1}) != -2 || packVector([]float64{0, 0}) != nil || unpackVector([]byte{1, 2, 3}) != nil {
		t.Fatal("degenerate vectors")
	}
}

func TestMemoryMigrationAnyDimension(t *testing.T) {
	d := migrated(t)
	if _, err := d.Write.Exec(`INSERT INTO cc_user_memories (household_id, content, embedding, embedding_model)
		VALUES ('h', 'c', ?, 'bge-small')`, packVector(make768())); err != nil {
		t.Fatalf("768-d vector rejected: %v", err)
	}
	if _, err := d.Write.Exec(`INSERT INTO cc_user_memories (household_id, content, embedding) VALUES ('h', 'c', x'000000')`); err == nil {
		t.Fatal("a vector that isn't whole float32s was accepted")
	}
}

func make768() []float64 {
	v := make([]float64, 768)
	v[3] = 1
	return v
}

func TestMemoryProfileTextD43(t *testing.T) {
	me := newMemEnv(t)
	ctx := context.Background()
	alex := i64(7)
	me.save(memWrite{UserID: alex, HouseholdID: voiceHH, Content: "general thing", Category: "general"})
	me.save(memWrite{UserID: alex, HouseholdID: voiceHH, Content: "has a dog named Leo", Category: "fact"})
	me.advance(time.Minute)
	me.save(memWrite{UserID: alex, HouseholdID: voiceHH, Content: "likes coffee black", Category: "preference"})
	me.save(memWrite{UserID: alex, HouseholdID: voiceHH, Content: "pinned note", Category: "note", Source: "ui", Pinned: true})
	me.advance(time.Minute)
	me.save(memWrite{UserID: alex, HouseholdID: voiceHH, Content: "newer fact", Category: "fact"})
	past := me.clock().Add(-time.Hour)
	me.save(memWrite{UserID: alex, HouseholdID: voiceHH, Content: "expired", Category: "preference", ExpiresAt: &past})
	me.save(memWrite{UserID: alex, HouseholdID: otherHH, Content: "other household", Category: "preference"})
	me.save(memWrite{UserID: i64(8), HouseholdID: voiceHH, Content: "sam's", Category: "preference"})
	me.save(memWrite{HouseholdID: voiceHH, Content: "household row", Category: "preference"})

	got := memoryProfile{me.m}.ProfileText(ctx, 7, voiceHH)
	want := "- pinned note\n- likes coffee black\n- newer fact\n- has a dog named Leo\n- general thing"
	if got != want {
		t.Fatalf("profile\n%q\nwant\n%q", got, want)
	}
	// The budget: lines are added until the next would exceed it (newline counted).
	me.set(settingPinnedMaxChars, int64(len("- pinned note\n- likes coffee black\n")), settings.Scope{})
	if got := (memoryProfile{me.m}).ProfileText(ctx, 7, voiceHH); got != "- pinned note\n- likes coffee black" {
		t.Fatalf("budgeted profile %q", got)
	}
	if (memoryProfile{me.m}).ProfileText(ctx, 0, voiceHH) != "" {
		t.Fatal("unknown speaker got a profile")
	}
}

func TestMemorySaveUpsertQ7(t *testing.T) {
	me := newMemEnv(t)
	ctx := context.Background()
	alex := i64(7)
	vec := packVector([]float64{1, 0})
	id := me.save(memWrite{UserID: alex, HouseholdID: voiceHH, Content: "coffee black", Category: "preference",
		Key: "coffee", Source: "voice", Vec: vec, Model: "m"})
	// Same key, new content: updated in place, vector dropped (recall never matches old text).
	res, err := me.m.saveMemory(ctx, me.d.Write, memWrite{UserID: alex, HouseholdID: voiceHH, Content: "coffee with milk",
		Category: "preference", Key: "coffee", Source: "voice"})
	if err != nil || res.ID != id || !res.Updated {
		t.Fatalf("upsert %+v %v", res, err)
	}
	if me.count(`SELECT count(*) FROM cc_user_memories WHERE id = ? AND embedding IS NULL AND embedding_model IS NULL`, id) != 1 {
		t.Fatal("stale vector kept after a content change")
	}
	// A household row with the same key is a different row (IS NULL matching).
	hid := me.save(memWrite{HouseholdID: voiceHH, Content: "house coffee", Key: "coffee"})
	if hid == id {
		t.Fatal("household row matched a personal key")
	}
	// Passive writes never overwrite a permanent row...
	res, _ = me.m.saveMemory(ctx, me.d.Write, memWrite{UserID: alex, HouseholdID: voiceHH, Content: "passive rewrite",
		Category: "fact", Key: "coffee", Source: "passive"})
	if !res.Skipped {
		t.Fatal("passive overwrote a permanent row")
	}
	// ...nor a ui-authored or a pinned one, even when it expires.
	exp := me.clock().Add(time.Hour)
	me.save(memWrite{UserID: alex, HouseholdID: voiceHH, Content: "ui fact", Key: "ui", Source: "ui", ExpiresAt: &exp})
	me.save(memWrite{UserID: alex, HouseholdID: voiceHH, Content: "pinned", Key: "pin", Source: "ui", Pinned: true, ExpiresAt: &exp})
	for _, k := range []string{"ui", "pin"} {
		res, _ = me.m.saveMemory(ctx, me.d.Write, memWrite{UserID: alex, HouseholdID: voiceHH, Content: "x", Key: k, Source: "passive", ExpiresAt: &exp})
		if !res.Skipped {
			t.Fatalf("passive overwrote %s", k)
		}
	}
	// A passive row with an expiry is updated by passive extraction.
	me.save(memWrite{UserID: alex, HouseholdID: voiceHH, Content: "dentist friday", Key: "dentist", Source: "passive", ExpiresAt: &exp})
	res, _ = me.m.saveMemory(ctx, me.d.Write, memWrite{UserID: alex, HouseholdID: voiceHH, Content: "dentist monday", Key: "dentist", Source: "passive", ExpiresAt: &exp})
	if res.Skipped || !res.Updated {
		t.Fatalf("passive refresh %+v", res)
	}
	// A voice write keeps the user's pin; only a ui write changes it.
	res, _ = me.m.saveMemory(ctx, me.d.Write, memWrite{UserID: alex, HouseholdID: voiceHH, Content: "pinned v2", Key: "pin", Source: "voice"})
	if me.count(`SELECT count(*) FROM cc_user_memories WHERE id = ? AND is_pinned = 1 AND content = 'pinned v2'`, res.ID) != 1 {
		t.Fatal("voice write unpinned a row")
	}
}

// turnFor is a tool turn for the speaker (0 = unknown).
func turnFor(speaker int64) servertools.Turn {
	return servertools.Turn{ConversationID: "c", HouseholdID: voiceHH, Speaker: servertools.Speaker{UserID: speaker}}
}

func call(name, args string) servertools.Call {
	v, _ := pyjson.Loads(args)
	o, _ := v.(*pyjson.Object)
	return servertools.Call{ID: "x", Name: name, Args: o}
}

func toolJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(pyjson.Dumps(v, true)), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMemoryToolsD21Refusals(t *testing.T) {
	me := newMemEnv(t)
	ctx := context.Background()
	for _, name := range []string{"remember", "recall", "forget"} {
		tool, ok := me.m.ServerTools().Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		r := toolJSON(t, must(tool.Execute(ctx, call(name, `{"content": "x", "query": "x", "content_match": "x"}`), turnFor(0))))
		if r["error"] != "no_speaker" || r["message"] != "I'm not sure who's speaking." {
			t.Fatalf("%s unknown speaker: %v", name, r)
		}
		off := turnFor(0)
		off.Speaker.RecognitionOff = true
		r = toolJSON(t, must(tool.Execute(ctx, call(name, `{}`), off)))
		if !strings.Contains(r["message"].(string), "Speaker recognition is off") {
			t.Fatalf("%s M14: %v", name, r)
		}
	}
	if me.count(`SELECT count(*) FROM cc_user_memories`) != 0 {
		t.Fatal("an unknown speaker's remember stored something")
	}
}

func must(v any, err error) any {
	if err != nil {
		panic(err)
	}
	return v
}

func TestMemoryRememberRecallForget(t *testing.T) {
	me := newMemEnv(t)
	ctx := context.Background()
	reg := me.m.ServerTools()
	remember, _ := reg.Get("remember")
	recall, _ := reg.Get("recall")
	forget, _ := reg.Get("forget")

	r := toolJSON(t, must(remember.Execute(ctx, call("remember", `{"content": "likes coffee black", "category": "preference"}`), turnFor(7))))
	if r["status"] != "remembered" || r["category"] != "preference" {
		t.Fatalf("remember %v", r)
	}
	// Embedded immediately, tagged with the model.
	if me.count(`SELECT count(*) FROM cc_user_memories WHERE user_id = 7 AND source = 'voice' AND embedding_model = 'minilm-a'`) != 1 {
		t.Fatal("remember didn't embed")
	}
	// Default category is legacy's "general".
	toolJSON(t, must(remember.Execute(ctx, call("remember", `{"content": "my sister is Kim"}`), turnFor(7))))
	if me.count(`SELECT count(*) FROM cc_user_memories WHERE content = 'my sister is Kim' AND category = 'general'`) != 1 {
		t.Fatal("default category")
	}
	// Someone else's memory is never recalled.
	me.save(memWrite{UserID: i64(8), HouseholdID: voiceHH, Content: "coffee with oat milk", Category: "preference"})

	r = toolJSON(t, must(recall.Execute(ctx, call("recall", `{"query": "coffee preferences", "category": "general"}`), turnFor(7))))
	if r["status"] != "found" || r["count"] != 1.0 || r["search_method"] != "vector" {
		t.Fatalf("recall %v", r)
	}
	hit := r["memories"].([]any)[0].(map[string]any)
	if hit["content"] != "likes coffee black" || hit["similarity"].(float64) < 0.3 {
		t.Fatalf("hit %v", hit)
	}
	// M1: a name the vectors score low is still found by keyword ("Kim" vs "who is kim").
	me.set(settingRecallThreshold, 0.99, settings.Scope{})
	r = toolJSON(t, must(recall.Execute(ctx, call("recall", `{"query": "who is Kim"}`), turnFor(7))))
	if r["status"] != "found" || r["memories"].([]any)[0].(map[string]any)["content"] != "my sister is Kim" {
		t.Fatalf("M1 substring fallback %v", r)
	}
	// No embeddings engine: keyword search only.
	me.emb.set("minilm-a", true)
	r = toolJSON(t, must(recall.Execute(ctx, call("recall", `{"query": "black coffee"}`), turnFor(7))))
	if r["status"] != "found" || r["search_method"] != "substring" {
		t.Fatalf("recall without embeddings %v", r)
	}
	r = toolJSON(t, must(recall.Execute(ctx, call("recall", `{"query": "zebra"}`), turnFor(7))))
	if r["status"] != "no_results" || r["message"] != "No memories found matching 'zebra'" {
		t.Fatalf("no results %v", r)
	}
	// remember without embeddings still saves.
	toolJSON(t, must(remember.Execute(ctx, call("remember", `{"content": "allergic to peanuts", "category": "fact"}`), turnFor(7))))
	if me.count(`SELECT count(*) FROM cc_user_memories WHERE content = 'allergic to peanuts' AND embedding IS NULL`) != 1 {
		t.Fatal("remember without embeddings")
	}

	// forget: hard delete of the speaker's own matching rows, and the characterization.
	me.exec(`INSERT INTO cc_person_characterizations (user_id, household_id, body) VALUES (7, ?, '{}')`, voiceHH)
	r = toolJSON(t, must(forget.Execute(ctx, call("forget", `{"content_match": "COFFEE"}`), turnFor(7))))
	if r["status"] != "forgotten" || r["count"] != 1.0 {
		t.Fatalf("forget %v", r)
	}
	if me.count(`SELECT count(*) FROM cc_user_memories WHERE content LIKE '%coffee%'`) != 1 { // sam's stays
		t.Fatal("forget reached another user's memory or soft-deleted")
	}
	if me.count(`SELECT count(*) FROM cc_person_characterizations`) != 0 {
		t.Fatal("forget kept the characterization (D30)")
	}
	r = toolJSON(t, must(forget.Execute(ctx, call("forget", `{"content_match": "coffee"}`), turnFor(7))))
	if r["status"] != "not_found" || r["count"] != 0.0 {
		t.Fatalf("forget again %v", r)
	}
}

// LD6: recall compares only vectors of the current model; stale rows are found by keyword
// until the sweep re-embeds them.
func TestMemoryRecallAcrossModelSwitch(t *testing.T) {
	me := newMemEnv(t)
	ctx := context.Background()
	recall, _ := me.m.ServerTools().Get("recall")
	me.save(memWrite{UserID: i64(7), HouseholdID: voiceHH, Content: "plays tennis on sundays", Category: "fact"})
	me.save(memWrite{UserID: i64(7), HouseholdID: voiceHH, Content: "drives a blue car", Category: "fact"})
	if n, err := me.m.embedSweep(ctx); err != nil || n != 2 {
		t.Fatalf("sweep %d %v", n, err)
	}
	me.emb.set("bge-b", false) // the embedding model changed
	r := toolJSON(t, must(recall.Execute(ctx, call("recall", `{"query": "tennis"}`), turnFor(7))))
	if r["status"] != "found" || r["search_method"] != "substring" {
		t.Fatalf("stale vectors must not be compared: %v", r)
	}
	if n, _ := me.m.embedSweep(ctx); n != 2 {
		t.Fatalf("re-embed %d", n)
	}
	if me.count(`SELECT count(*) FROM cc_user_memories WHERE embedding_model = 'bge-b'`) != 2 {
		t.Fatal("rows not re-tagged")
	}
	r = toolJSON(t, must(recall.Execute(ctx, call("recall", `{"query": "tennis sundays"}`), turnFor(7))))
	if r["search_method"] != "vector" {
		t.Fatalf("after re-embed %v", r)
	}
	if n, _ := me.m.embedSweep(ctx); n != 0 {
		t.Fatal("sweep re-embedded current rows")
	}
	me.emb.set("bge-b", true)
	if n, err := me.m.embedSweep(ctx); n != 0 || err != nil {
		t.Fatal("sweep without an engine must be a no-op")
	}
}

// End to end: an identified speaker's remember lands, and the next conversation's speaker
// block carries it (D43, D3).
func TestMemoryVoiceTurn(t *testing.T) {
	me := newMemEnv(t)
	me.stt.recognition = true
	alex := int64(7)
	me.stt.speaker = &alex
	me.start("m1", "[]")
	me.transcribe("m1", false)
	me.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_r1", "type": "function",
		"function": map[string]any{"name": "remember", "arguments": `{"content": "takes coffee black", "category": "preference"}`}}}})
	me.eng.say("Got it.")
	me.turn("/api/v0/voice/command", "m1", "remember that I take my coffee black", nil).want(200)
	var tool map[string]any
	for _, m := range messagesOf(me.eng.last()) {
		if m["role"] == "tool" {
			_ = json.Unmarshal([]byte(m["content"].(string)), &tool)
		}
	}
	if tool["status"] != "remembered" {
		t.Fatalf("tool result %v", tool)
	}
	me.start("m2", "[]")
	me.transcribe("m2", false)
	me.eng.say("Black.")
	me.turn("/api/v0/voice/command", "m2", "how do I take my coffee", nil).want(200)
	msgs := messagesOf(me.eng.last())
	if !strings.Contains(msgs[1]["content"].(string), "User Profile") || !strings.HasSuffix(msgs[1]["content"].(string), "- takes coffee black") {
		t.Fatalf("speaker block %v", msgs[1]["content"])
	}
}

func TestMemoryAgentContextHint(t *testing.T) {
	me := newMemEnv(t)
	ctx := context.Background()
	exp := me.clock().Add(time.Hour)
	me.save(memWrite{HouseholdID: voiceHH, Content: "current weather: rain all afternoon", Category: "weather", Source: "agent", ExpiresAt: &exp})
	me.save(memWrite{UserID: i64(7), HouseholdID: voiceHH, Content: "personal weather note", Source: "voice"})
	if _, err := me.m.embedSweep(ctx); err != nil {
		t.Fatal(err)
	}
	conv := &conversation{householdID: voiceHH}
	if h := me.m.agentContextHint(ctx, conv, "what's the weather this afternoon"); h != "" {
		t.Fatalf("advanced_context is off by default: %q", h)
	}
	me.set(settingAdvancedContext, true, settings.Scope{HouseholdID: voiceHH})
	h := me.m.agentContextHint(ctx, conv, "what's the weather this afternoon")
	if h != agentContextHeader+"\n- current weather: rain all afternoon" {
		t.Fatalf("hint %q", h)
	}
	if formatAgentContext([]string{"x"}, 10) != "" {
		t.Fatal("budget")
	}
}

func TestParseExtractionResponse(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`[{"category": "fact", "key": "k", "content": "a"}]`, []string{"a"}},
		{"<think>hmm [x]</think>\n```json\n[{\"content\": \"b\"}]\n```", []string{"b"}},
		{"<think>truncated reasoning... [{\"content\": \"c\"}]", []string{"c"}},
		{"Here you go: [{\"content\": \"d\"}, {\"content\": \"\"}, 3, {\"x\": 1}] done", []string{"d"}},
		{"<think>never closed", nil},
		{`{"content": "not a list"}`, nil},
		{`[]`, nil},
	}
	for _, c := range cases {
		got := parseExtractionResponse(c.in)
		var contents []string
		for _, g := range got {
			contents = append(contents, g.content)
		}
		if strings.Join(contents, "|") != strings.Join(c.want, "|") {
			t.Errorf("%q → %v, want %v", c.in, contents, c.want)
		}
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		ttl  string
		days int
	}{{`7`, 7}, {`30`, 30}, {`"7"`, 7}, {`7.9`, 7}, {`null`, -1}, {`"soon"`, -1}, {`"7.5"`, -1}} {
		got := parseExtractionResponse(`[{"content": "x", "ttl_days": ` + c.ttl + `}]`)[0].expiresAt(now)
		switch {
		case c.days < 0 && got != nil:
			t.Errorf("ttl %s: want permanent, got %v", c.ttl, got)
		case c.days >= 0 && (got == nil || !got.Equal(now.AddDate(0, 0, c.days))):
			t.Errorf("ttl %s: got %v", c.ttl, got)
		}
	}
	if parseExtractionResponse(`[{"content": "x"}]`)[0].expiresAt(now) != nil {
		t.Error("absent ttl must be permanent")
	}
}
