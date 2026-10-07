package cc

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func memPath(id int64, hh string) string {
	return fmt.Sprintf("/api/v0/mobile/memories/%d?household_id=%s", id, hh)
}

func ids(list []any) []float64 {
	var out []float64
	for _, e := range list {
		out = append(out, e.(map[string]any)["id"].(float64))
	}
	return out
}

func TestMobileMemoriesMatrix(t *testing.T) {
	me := newMemEnv(t)
	alexTok, samTok, powerTok, adminTok, outsiderTok := "tok-7", "tok-8", "tok-9", "tok-10", "tok-11"
	exp := me.clock().Add(time.Hour)
	own := me.save(memWrite{UserID: i64(7), HouseholdID: voiceHH, Content: "alex fact", Category: "fact"})
	sams := me.save(memWrite{UserID: i64(8), HouseholdID: voiceHH, Content: "sam fact"})
	house := me.save(memWrite{HouseholdID: voiceHH, Content: "house rule", Source: "ui"})
	agent := me.save(memWrite{HouseholdID: voiceHH, Content: "weather", Source: "agent", Category: "weather", ExpiresAt: &exp})
	me.save(memWrite{UserID: i64(11), HouseholdID: otherHH, Content: "elsewhere"})

	// Auth first, then the query: no JWT is 401; a missing household is 400.
	me.do("GET", "/api/v0/mobile/memories?household_id="+voiceHH, nil, nil).want(401)
	me.do("GET", "/api/v0/mobile/memories", nil, bearer(alexTok)).want(400)
	// Outsiders (other household) are refused outright.
	me.do("GET", "/api/v0/mobile/memories?household_id="+voiceHH, nil, bearer(outsiderTok)).
		detail(403, "User is not a member of this household")

	// MEMBER sees own only; POWER_USER own + household; include_household=false narrows.
	l := me.do("GET", "/api/v0/mobile/memories?household_id="+voiceHH, nil, bearer(alexTok)).want(200).list()
	if got := ids(l); len(got) != 1 || got[0] != float64(own) {
		t.Fatalf("member list %v", got)
	}
	first := l[0].(map[string]any)
	for _, k := range []string{"id", "user_id", "household_id", "category", "key", "content", "source", "is_active",
		"is_pinned", "created_at", "updated_at", "expires_at", "editable"} {
		if _, ok := first[k]; !ok {
			t.Fatalf("response missing %s: %v", k, first)
		}
	}
	if first["editable"] != true || first["created_at"] != "2026-10-06T12:00:00" {
		t.Fatalf("serialization %v", first)
	}
	l = me.do("GET", "/api/v0/mobile/memories?household_id="+voiceHH, nil, bearer(powerTok)).want(200).list()
	if len(l) != 2 { // house + agent (power user has no own rows)
		t.Fatalf("power list %v", ids(l))
	}
	for _, e := range l {
		o := e.(map[string]any)
		if o["source"] == "agent" && o["editable"] != false {
			t.Fatal("power user can't edit agent rows")
		}
		if o["source"] == "ui" && o["editable"] != true {
			t.Fatal("power user edits non-agent household rows")
		}
	}
	if l = me.do("GET", "/api/v0/mobile/memories?household_id="+voiceHH+"&include_household=false", nil, bearer(powerTok)).want(200).list(); len(l) != 0 {
		t.Fatalf("include_household=false %v", ids(l))
	}
	if l = me.do("GET", "/api/v0/mobile/memories?household_id="+voiceHH+"&category=weather", nil, bearer(adminTok)).want(200).list(); len(l) != 1 {
		t.Fatalf("category filter %v", ids(l))
	}

	// Invisible rows are 404 (existence never leaks): another user's, another household's.
	me.do("GET", memPath(sams, voiceHH), nil, bearer(alexTok)).detail(404, "Memory not found")
	me.do("GET", memPath(own, otherHH), nil, bearer(outsiderTok)).detail(404, "Memory not found")
	me.do("GET", memPath(house, voiceHH), nil, bearer(alexTok)).detail(404, "Memory not found")
	me.do("GET", memPath(999, voiceHH), nil, bearer(alexTok)).detail(404, "Memory not found")
	me.do("GET", "/api/v0/mobile/memories/abc?household_id="+voiceHH, nil, bearer(alexTok)).want(400)
	// Visible but read-only: 403 with the agent-specific message.
	me.do("PUT", memPath(agent, voiceHH), map[string]any{"content": "x"}, bearer(powerTok)).
		detail(403, "Agent-injected memories are read-only for non-admin users")
	// Even an admin can't see another member's personal memories (legacy _can_read).
	me.do("PUT", memPath(sams, voiceHH), map[string]any{"content": "x"}, bearer(adminTok)).detail(404, "Memory not found")
	me.do("PUT", memPath(agent, voiceHH), map[string]any{"content": "cloudy"}, bearer(adminTok)).want(200)

	// Create: source forced to ui; household scope needs POWER_USER; agent_context needs ADMIN.
	c := me.do("POST", "/api/v0/mobile/memories?household_id="+voiceHH, map[string]any{"content": "likes jazz",
		"category": "preference", "source": "agent", "is_pinned": true}, bearer(alexTok)).want(201).json()
	if c["source"] != "ui" || c["user_id"] != 7.0 || c["is_pinned"] != true || c["editable"] != true {
		t.Fatalf("create %v", c)
	}
	me.do("POST", "/api/v0/mobile/memories?household_id="+voiceHH, map[string]any{"content": "x", "scope": "household"}, bearer(alexTok)).
		detail(403, "Only POWER_USER or higher can create household-wide memories")
	me.do("POST", "/api/v0/mobile/memories?household_id="+voiceHH, map[string]any{"content": "x", "category": "agent_context"}, bearer(powerTok)).
		detail(403, "Only ADMIN can create agent_context memories")
	h := me.do("POST", "/api/v0/mobile/memories?household_id="+voiceHH, map[string]any{"content": "wifi is upstairs", "scope": "household"}, bearer(powerTok)).want(201).json()
	if h["user_id"] != nil {
		t.Fatalf("household create %v", h)
	}
	r := me.do("POST", "/api/v0/mobile/memories?household_id="+voiceHH, map[string]any{"content": "", "scope": "team"}, bearer(alexTok)).want(400).json()
	if d := fmt.Sprint(r["details"]); !strings.Contains(d, "body -> content: String should have at least 1 character") ||
		!strings.Contains(d, "body -> scope: Input should be 'user' or 'household'") {
		t.Fatalf("validation %v", r)
	}
	me.do("POST", "/api/v0/mobile/memories?household_id="+voiceHH, map[string]any{"content": strings.Repeat("é", 2001)}, bearer(alexTok)).want(400)

	// Update: content change drops the vector; agent_context re-categorization is ADMIN-only.
	if _, err := me.m.embedSweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	u := me.do("PUT", memPath(own, voiceHH), map[string]any{"content": "alex new fact", "is_pinned": true}, bearer(alexTok)).want(200).json()
	if u["content"] != "alex new fact" || u["is_pinned"] != true {
		t.Fatalf("update %v", u)
	}
	if me.count(`SELECT count(*) FROM cc_user_memories WHERE id = ? AND embedding IS NULL`, own) != 1 {
		t.Fatal("PUT kept the stale vector (04 §8.9)")
	}
	me.do("PUT", memPath(own, voiceHH), map[string]any{"category": "agent_context"}, bearer(alexTok)).
		detail(403, "Only ADMIN can set category=agent_context")

	// Delete: hard delete, and the owner's characterization goes too (D30).
	me.exec(`INSERT INTO cc_person_characterizations (user_id, household_id, body) VALUES (7, ?, '{}')`, voiceHH)
	d := me.do("DELETE", memPath(own, voiceHH), nil, bearer(alexTok)).want(200).json()
	if d["status"] != "deleted" || d["id"] != float64(own) {
		t.Fatalf("delete %v", d)
	}
	if me.count(`SELECT count(*) FROM cc_user_memories WHERE id = ?`, own) != 0 || me.count(`SELECT count(*) FROM cc_person_characterizations`) != 0 {
		t.Fatal("delete must be hard and drop the characterization")
	}
	me.do("DELETE", memPath(own, voiceHH), nil, bearer(alexTok)).detail(404, "Memory not found")
	me.do("DELETE", memPath(house, voiceHH), nil, bearer(samTok)).detail(404, "Memory not found")
}

func TestTranscriptRoutes(t *testing.T) {
	me := newMemEnv(t)
	a1 := me.addTranscript(7, voiceHH, "first", "one")
	me.advance(time.Minute)
	a2 := me.addTranscript(7, otherHH, "second", "")
	me.exec(`UPDATE cc_conversation_transcripts SET tool_calls_json = ? WHERE id = ?`,
		`[{"id": "c1", "type": "function", "function": {"name": "get_weather", "arguments": "{}"}}]`, a2)
	sams := me.addTranscript(8, voiceHH, "sam's", "x")

	me.do("GET", "/api/v0/transcripts/recent", nil, nil).want(401)
	l := me.do("GET", "/api/v0/transcripts/recent", nil, bearer("tok-7")).want(200).list()
	if got := ids(l); len(got) != 2 || got[0] != float64(a2) || got[1] != float64(a1) {
		t.Fatalf("recent %v", got)
	}
	newest := l[0].(map[string]any)
	if newest["assistant_message"] != "" || newest["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"] != "get_weather" {
		t.Fatalf("row %v", newest)
	}
	if l[1].(map[string]any)["tool_calls"] != nil || l[1].(map[string]any)["user_rating"] != nil {
		t.Fatalf("row %v", l[1])
	}
	if got := ids(me.do("GET", "/api/v0/transcripts/recent?limit=1", nil, bearer("tok-7")).want(200).list()); len(got) != 1 {
		t.Fatalf("limit %v", got)
	}
	since := me.clock().Add(-30 * time.Second).Format("2006-01-02T15:04:05")
	if got := ids(me.do("GET", "/api/v0/transcripts/recent?since="+since, nil, bearer("tok-7")).want(200).list()); len(got) != 1 {
		t.Fatalf("since %v", got)
	}
	me.do("GET", "/api/v0/transcripts/recent?limit=0", nil, bearer("tok-7")).want(400)
	me.do("GET", "/api/v0/transcripts/recent?limit=201", nil, bearer("tok-7")).want(400)
	me.do("GET", "/api/v0/transcripts/recent?since=yesterday", nil, bearer("tok-7")).want(400)

	rate := fmt.Sprintf("/api/v0/transcripts/%d/rate", a1)
	me.do("POST", rate, map[string]any{"rating": 2}, bearer("tok-7")).detail(400, "rating must be -1, 0, or 1")
	me.do("POST", rate, map[string]any{}, bearer("tok-7")).want(400)
	r := me.do("POST", rate, map[string]any{"rating": -1, "notes": "wrong room"}, bearer("tok-7")).want(200).json()
	if r["user_rating"] != -1.0 || r["rating_notes"] != "wrong room" || r["rated_at"] != "2026-10-06T12:01:00" {
		t.Fatalf("rate %v", r)
	}
	me.do("POST", fmt.Sprintf("/api/v0/transcripts/%d/rate", sams), map[string]any{"rating": 1}, bearer("tok-7")).
		detail(404, "Transcript not found")
}

func TestMemoryPurgeD20(t *testing.T) {
	me := newMemEnv(t)
	ctx := context.Background()
	me.save(memWrite{UserID: i64(7), HouseholdID: voiceHH, Content: "a"})
	me.save(memWrite{UserID: i64(7), HouseholdID: otherHH, Content: "b"})
	me.save(memWrite{UserID: i64(8), HouseholdID: voiceHH, Content: "sam"})
	me.save(memWrite{HouseholdID: voiceHH, Content: "household"})
	me.addTranscript(7, voiceHH, "hi", "hello")
	me.exec(`INSERT INTO cc_person_characterizations (user_id, household_id, body) VALUES (7, ?, '{}'), (8, ?, '{}')`, voiceHH, voiceHH)
	me.exec(`INSERT INTO cc_request_traces (id, conversation_id, request_type, source, total_duration_ms, spans_json)
		VALUES ('t1', 'conv', 'voice', 'node', 1, '[]'), ('t2', 'other', 'voice', 'node', 1, '[]')`)
	me.set(settingPersona, "user pref", settings.Scope{UserID: 7})

	me.do("DELETE", "/api/v0/me/data", nil, nil).want(401)
	me.do("DELETE", "/api/v0/me/data", nil, bearer("tok-7")).want(204)
	for q, want := range map[string]int{
		`SELECT count(*) FROM cc_user_memories WHERE user_id = 7`:           0,
		`SELECT count(*) FROM cc_user_memories`:                             2,
		`SELECT count(*) FROM cc_conversation_transcripts`:                  0,
		`SELECT count(*) FROM cc_person_characterizations`:                  1,
		`SELECT count(*) FROM cc_request_traces`:                            1,
		`SELECT count(*) FROM cc_settings WHERE user_id = 7`:                0,
		`SELECT count(*) FROM cc_user_memories WHERE content = 'household'`: 1,
	} {
		if got := me.count(q); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
	me.do("DELETE", "/api/v0/me/data", nil, bearer("tok-7")).want(204) // idempotent

	// Leaving a household: only that household's data goes.
	me.save(memWrite{UserID: i64(8), HouseholdID: otherHH, Content: "sam elsewhere"})
	me.addTranscript(8, voiceHH, "x", "")
	if err := me.d.Tx(ctx, func(tx *sql.Tx) error { return me.m.PurgeUserHousehold(ctx, tx, 8, voiceHH) }); err != nil {
		t.Fatal(err)
	}
	if me.count(`SELECT count(*) FROM cc_user_memories WHERE user_id = 8`) != 1 ||
		me.count(`SELECT count(*) FROM cc_person_characterizations`) != 0 ||
		me.count(`SELECT count(*) FROM cc_conversation_transcripts`) != 0 {
		t.Fatal("household leave purge")
	}
}

func TestMemoryInject(t *testing.T) {
	me := newMemEnv(t)
	h := me.node.h()
	path := "/api/v0/memories/inject"
	me.do("POST", path, map[string]any{"memories": []any{}}, nil).
		detail(401, "Authentication required (X-Api-Key or X-Jarvis-App-Id/Key)")
	me.do("POST", path, map[string]any{"memories": []any{}}, hdr{"X-Jarvis-App-Id": "a", "X-Jarvis-App-Key": "b"}).
		detail(401, "Invalid app credentials")

	items := []any{
		map[string]any{"key": "weather:today", "content": "Rain this afternoon", "category": "weather", "ttl_hours": 6},
		map[string]any{"key": "news:1", "content": "first draft"},
		map[string]any{"key": "news:1", "content": "Local team wins the final"},
		map[string]any{"key": "cal:alex", "content": "Dentist at 3pm", "user_id": 7},
		map[string]any{"key": "cal:stranger", "content": "x", "user_id": 11},
	}
	r := me.do("POST", path, map[string]any{"memories": items}, h).want(200).json()
	if r["injected"] != 3.0 || r["updated"] != 0.0 || r["deduplicated"] != 1.0 || len(r["errors"].([]any)) != 1 {
		t.Fatalf("inject %v", r)
	}
	if me.count(`SELECT count(*) FROM cc_user_memories WHERE user_id = 11`) != 0 {
		t.Fatal("a non-member's personal memory was stored (D40 04.Q10)")
	}
	if me.count(`SELECT count(*) FROM cc_user_memories WHERE key = 'weather:today' AND source = 'agent'
		AND expires_at = ? AND embedding_model = 'minilm-a'`, dbTime(me.clock().Add(6*time.Hour))) != 1 {
		t.Fatal("weather row: source, TTL or vector")
	}
	// Key upsert across batches.
	r = me.do("POST", path, map[string]any{"memories": []any{map[string]any{"key": "weather:today", "content": "Sunny now"}}}, h).want(200).json()
	if r["injected"] != 0.0 || r["updated"] != 1.0 {
		t.Fatalf("upsert %v", r)
	}
	// Layer 3: the same story under a new key replaces the older row.
	r = me.do("POST", path, map[string]any{"memories": []any{map[string]any{"key": "news:2", "content": "Local team wins the final!"}}}, h).want(200).json()
	if r["deduplicated"] != 1.0 || me.count(`SELECT count(*) FROM cc_user_memories WHERE key LIKE 'news:%'`) != 1 ||
		me.count(`SELECT count(*) FROM cc_user_memories WHERE key = 'news:2'`) != 1 {
		t.Fatalf("content dedup %v", r)
	}
	// A node can't write into another household; validation; memory off.
	me.do("POST", path, map[string]any{"household_id": otherHH, "memories": []any{}}, h).
		detail(403, "household_id does not match node's household")
	me.do("POST", path, map[string]any{"memories": []any{map[string]any{"content": "x"}}}, h).want(400)
	me.do("POST", path, map[string]any{"memories": []any{map[string]any{"key": "k", "content": "x", "ttl_hours": 0}}}, h).want(400)
	many := make([]any, 101)
	for i := range many {
		many[i] = map[string]any{"key": fmt.Sprint(i), "content": "x"}
	}
	me.do("POST", path, map[string]any{"memories": many}, h).want(400)
	me.set(settingMemoryEnabled, false, settings.Scope{HouseholdID: voiceHH})
	me.do("POST", path, map[string]any{"memories": []any{}}, h).detail(409, "Memory system is disabled for this household")
	// Always stored regardless of model.advanced_context (D40 04.Q10): it was off throughout.
}
