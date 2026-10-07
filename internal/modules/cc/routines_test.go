package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
)

const rhh = "hh-r"

// routineEnv is an env with a fake notifier, an owner token in rhh and the platform queue and
// scheduler on the same database (handlers are invoked directly, not by queue workers).
type routineEnv struct {
	*env
	notify *fakeNotifier
	owner  string
	sched  *scheduler.Scheduler
}

func newRoutineEnv(t *testing.T) *routineEnv {
	t.Helper()
	re := &routineEnv{notify: &fakeNotifier{}}
	re.env = newEnv(t, envOpts{configure: func(m *Module) { m.Notify = re.notify }})
	re.owner = re.auth.addUser(1, rhh, authn.RoleOwner)
	ctx := context.Background()
	if err := db.Migrate(ctx, re.d, queue.MigrationModule, queue.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, re.d, scheduler.MigrationModule, scheduler.Migrations()); err != nil {
		t.Fatal(err)
	}
	q := queue.New(re.d, re.m.deps.Log)
	re.sched = scheduler.New(re.d, q, re.m.deps.Log)
	re.m.deps.Queue, re.m.deps.Scheduler = q, re.sched
	old := routineWait
	routineWait = 2 * time.Second
	t.Cleanup(func() { routineWait = old })
	return re
}

func (re *routineEnv) path(p string) string { return "/api/v0/households/" + rhh + "/routines" + p }

func routineBody(name string, over map[string]any) map[string]any {
	b := map[string]any{
		"name":            name,
		"trigger_phrases": []string{"good morning", "morning"},
		"steps": []any{map[string]any{
			"command": "get_weather",
			"args": []any{
				map[string]any{"key": "resolved_datetimes", "value": `["today"]`},
				map[string]any{"key": "city", "value": "Boston"},
			},
			"label": "weather",
		}},
		"response_instruction": "Cheerful briefing.",
		"response_length":      "short",
		"schedule":             nil,
	}
	for k, v := range over {
		b[k] = v
	}
	return b
}

func (re *routineEnv) create(name string, over map[string]any) map[string]any {
	re.t.Helper()
	return re.do("POST", re.path(""), routineBody(name, over), bearer(re.owner)).want(201).json()
}

func (re *routineEnv) row(id string) *routineRow {
	re.t.Helper()
	rt, err := scanRoutine(re.d.Read.QueryRow(`SELECT `+routineCols+` FROM cc_routines WHERE id = ?`, id))
	if err != nil {
		re.t.Fatal(err)
	}
	return rt
}

func TestRoutineCRUD(t *testing.T) {
	e := newRoutineEnv(t)
	created := e.create("Night", nil)
	if created["slug"] != "night" || created["name"] != "Night" || created["enabled"] != true ||
		created["schedule"] != nil || created["created_at"] != "2026-10-06T12:00:00" {
		t.Fatalf("created %v", created)
	}
	steps := created["steps"].([]any)[0].(map[string]any)
	if steps["command"] != "get_weather" || len(steps["args"].([]any)) != 2 {
		t.Fatalf("steps %v", steps)
	}
	// Collisions dedupe; the seeded defaults (D44) count, so "Good Morning" is good_morning_2.
	if got := e.create("Night", nil)["slug"]; got != "night_2" {
		t.Fatalf("second slug %v", got)
	}
	if got := e.create("  Good   Morning!  ", nil); got["slug"] != "good_morning_2" || got["name"] != "Good   Morning!" {
		t.Fatalf("seed collision %v", got)
	}
	if got := e.create("!!!", nil)["slug"]; got != "routine" {
		t.Fatalf("fallback slug %v", got)
	}

	list := e.do("GET", e.path(""), nil, bearer(e.owner)).want(200).json()["routines"].([]any)
	var names []string
	for _, r := range list {
		names = append(names, r.(map[string]any)["slug"].(string))
	}
	if strings.Join(names, ",") != "good_morning,good_night,morning_briefing,nightly_briefing,night,night_2,good_morning_2,routine" {
		t.Fatalf("list order %v", names)
	}

	id := created["id"].(string)
	got := e.do("GET", e.path("/"+id), nil, bearer(e.owner)).want(200).json()
	if got["name"] != "Night" {
		t.Fatalf("get %v", got)
	}
	// PATCH: rename keeps the slug; an explicit null schedule clears it; null fields are left alone.
	e.do("PATCH", e.path("/"+id), map[string]any{"schedule": map[string]any{"type": "interval", "interval_seconds": 3600}}, bearer(e.owner)).want(200)
	e.advance(time.Minute)
	p := e.do("PATCH", e.path("/"+id), map[string]any{"name": "Late Night", "schedule": nil, "response_length": "medium",
		"steps": nil, "enabled": false}, bearer(e.owner)).want(200).json()
	if p["slug"] != "night" || p["name"] != "Late Night" || p["schedule"] != nil || p["response_length"] != "medium" ||
		p["enabled"] != false || len(p["steps"].([]any)) != 1 || p["updated_at"] != "2026-10-06T12:01:00" {
		t.Fatalf("patched %v", p)
	}

	e.do("DELETE", e.path("/"+id), nil, bearer(e.owner)).want(204)
	e.do("GET", e.path("/"+id), nil, bearer(e.owner)).detail(404, "Routine not found")
	e.do("PATCH", e.path("/"+id), map[string]any{"name": "x"}, bearer(e.owner)).detail(404, "Routine not found")
	e.do("DELETE", e.path("/"+id), nil, bearer(e.owner)).detail(404, "Routine not found")
}

func TestRoutineValidation(t *testing.T) {
	e := newRoutineEnv(t)
	cases := []struct {
		over   map[string]any
		detail string
	}{
		{map[string]any{"name": "  "}, "name is required"},
		{map[string]any{"response_length": "epic"}, "response_length must be one of ['long', 'medium', 'short']"},
		{map[string]any{"schedule": map[string]any{"type": "weekly"}}, "schedule.type must be 'cron' or 'interval'"},
		{map[string]any{"schedule": map[string]any{"type": "cron", "cron": " "}}, "cron schedule requires a 'cron' expression"},
		{map[string]any{"schedule": map[string]any{"type": "cron", "cron": "@daily"}}, "cron schedule has an invalid 'cron' expression: @daily"},
		{map[string]any{"schedule": map[string]any{"type": "interval", "interval_seconds": 0}}, "interval schedule requires interval_seconds > 0"},
		{map[string]any{"schedule": map[string]any{"type": "cron", "cron": "0 8 * * *", "timezone": "Mars/Base"}},
			"schedule.timezone 'Mars/Base' is not a known time zone"},
	}
	for _, c := range cases {
		e.do("POST", e.path(""), routineBody("V", c.over), bearer(e.owner)).detail(422, c.detail)
	}
	// Pydantic-shaped errors are CC's 400.
	r := e.do("POST", e.path(""), routineBody("V", map[string]any{"steps": []any{map[string]any{"args": []any{map[string]any{"value": 1}}}}}),
		bearer(e.owner)).want(400).json()
	if fmt.Sprint(r["details"]) != "[body -> steps -> 0 -> command: Field required body -> steps -> 0 -> args -> 0 -> key: Field required "+
		"body -> steps -> 0 -> args -> 0 -> value: Input should be a valid string]" {
		t.Fatalf("details %v", r["details"])
	}
	e.do("POST", e.path(""), map[string]any{"trigger_phrases": []string{"x"}}, bearer(e.owner)).want(400)
	e.do("PATCH", e.path("/nope"), map[string]any{"response_length": "epic"}, bearer(e.owner)).detail(404, "Routine not found")
	id := e.create("Ok", nil)["id"].(string)
	e.do("PATCH", e.path("/"+id), map[string]any{"response_length": "epic"}, bearer(e.owner)).
		detail(422, "response_length must be one of ['long', 'medium', 'short']")
}

func TestRoutineAuth(t *testing.T) {
	e := newRoutineEnv(t)
	outsider := e.auth.addUser(2, "hh-other", authn.RoleOwner)
	e.do("GET", e.path(""), nil, nil).detail(401, "Authentication required")
	e.do("GET", e.path(""), nil, bearer("nope")).detail(401, "Invalid or expired JWT")
	e.do("GET", e.path(""), nil, hdr{"X-API-Key": "wrong"}).detail(401, "Invalid API key")
	e.do("GET", e.path(""), nil, bearer(outsider)).detail(403, "User is not a member of this household")
	e.do("POST", e.path(""), routineBody("X", nil), bearer(outsider)).detail(403, "User is not a member of this household")
	// The admin key bypasses membership (infrastructure); a plain member has full access (no role floor).
	e.do("GET", e.path(""), nil, adminH()).want(200)
	member := e.auth.addUser(3, rhh, authn.RoleMember)
	id := e.create("Mine", nil)["id"].(string)
	e.do("PATCH", e.path("/"+id), map[string]any{"name": "Ours"}, bearer(member)).want(200)
	e.do("GET", "/api/v0/households/hh-other/routines/"+id, nil, bearer(outsider)).detail(404, "Routine not found")
}

func TestNodePullRoutines(t *testing.T) {
	e := newRoutineEnv(t)
	n := e.createNode("n1", rhh)
	e.create("Pull Me", map[string]any{"steps": []any{map[string]any{"command": "c", "args": []any{
		map[string]any{"key": "arr", "value": `["today"]`},
		map[string]any{"key": "obj", "value": `{"a": 1}`},
		map[string]any{"key": "bad", "value": "[bad"},
		map[string]any{"key": "empty", "value": ""},
		map[string]any{"key": "num", "value": "3"},
		map[string]any{"key": "", "value": "dropped"},
	}}}})
	off := e.create("Off", map[string]any{"enabled": false})
	e.do("GET", "/api/v0/nodes/other/routines", nil, n.h()).detail(403, "Node mismatch")
	got := e.do("GET", "/api/v0/nodes/n1/routines", nil, n.h()).want(200).json()["routines"].(map[string]any)
	if _, ok := got["off"]; ok || off["slug"] != "off" {
		t.Fatal("a disabled routine was pulled")
	}
	if len(got) != 5 { // 4 seeded defaults + pull_me
		t.Fatalf("pulled %d routines", len(got))
	}
	pm := got["pull_me"].(map[string]any)
	raw, _ := json.Marshal(pm)
	want := `{"response_instruction":"Cheerful briefing.","response_length":"short","steps":[{"args":{"arr":["today"],"bad":"[bad","empty":"","num":"3","obj":{"a":1}},"command":"c","label":""}],"trigger_phrases":["good morning","morning"]}`
	if string(raw) != want {
		t.Fatalf("node shape\n got %s\nwant %s", raw, want)
	}
	gm := got["good_morning"].(map[string]any)["steps"].([]any)[0].(map[string]any)
	if gm["command"] != "control_device" || fmt.Sprint(gm["args"]) != "map[action:turn_on floor:Downstairs]" {
		t.Fatalf("seeded default %v", gm)
	}
	// A node with no household gets a valid empty set.
	e.auth.mu.Lock()
	e.auth.nodes["n1"].household = ""
	e.auth.mu.Unlock()
	if got := e.do("GET", "/api/v0/nodes/n1/routines", nil, n.h()).want(200).json()["routines"].(map[string]any); len(got) != 0 {
		t.Fatalf("no-household pull %v", got)
	}
}

func TestSeededDefaultStaysDeleted(t *testing.T) {
	e := newRoutineEnv(t)
	list := e.do("GET", e.path(""), nil, bearer(e.owner)).want(200).json()["routines"].([]any)
	if len(list) != 4 {
		t.Fatalf("seeded %d", len(list))
	}
	first := list[0].(map[string]any)
	if first["slug"] != "good_morning" || first["response_length"] != "short" {
		t.Fatalf("seed %v", first)
	}
	e.do("DELETE", e.path("/"+first["id"].(string)), nil, bearer(e.owner)).want(204)
	if got := e.do("GET", e.path(""), nil, bearer(e.owner)).want(200).json()["routines"].([]any); len(got) != 3 {
		t.Fatalf("re-seeded: %d", len(got))
	}
}

func TestRoutineNudge(t *testing.T) {
	e := newRoutineEnv(t)
	a := e.createNode("na", rhh)
	e.createNode("nb", "hh-other")
	c := e.dialNode(a)
	if code := c.subscribe("jarvis/nodes/na/#"); code != 1 {
		t.Fatalf("suback %d", code)
	}
	id := e.create("Nudge", nil)["id"].(string)
	pk := c.next()
	if pk.TopicName != "jarvis/nodes/na/routines/sync" {
		t.Fatalf("topic %s", pk.TopicName)
	}
	if p := payload(t, pk); p["event"] != "routines_changed" || p["household_id"] != rhh || len(p) != 2 {
		t.Fatalf("payload %v", p)
	}
	e.do("PATCH", e.path("/"+id), map[string]any{"name": "N"}, bearer(e.owner)).want(200)
	c.next()
	e.do("DELETE", e.path("/"+id), nil, bearer(e.owner)).want(204)
	c.next()
	c.nothing(100 * time.Millisecond)
}

// fakeNodeRun answers the next routine command on c with output, returning its details.
func (re *routineEnv) answerRoutine(c *mqttClient, n testNode, output map[string]any) map[string]any {
	re.t.Helper()
	verb, d := command(re.t, c.next())
	if verb != "routine" {
		re.t.Fatalf("verb %s", verb)
	}
	if output != nil {
		re.do("POST", "/api/v0/device-control-results/"+d["reply_request_id"].(string), map[string]any{"output": output}, n.h()).want(200)
	}
	return d
}

func TestRunNow(t *testing.T) {
	e := newRoutineEnv(t)
	n := e.createNode("n1", rhh)
	other := e.createNode("n2", "hh-other")
	c := e.dialNode(n)
	if code := c.subscribe("jarvis/nodes/n1/commands"); code != 1 {
		t.Fatalf("suback %d", code)
	}
	rt := e.create("Run Me", nil)
	run := e.path("/" + rt["id"].(string) + "/run-now")

	type out struct{ r *resp }
	start := func(body any) chan out {
		ch := make(chan out, 1)
		go func() { ch <- out{e.do("POST", run, body, bearer(e.owner))} }()
		return ch
	}

	ch := start(map[string]any{"node_id": "n1"})
	d := e.answerRoutine(c, n, map[string]any{"success": true, "passed": 2, "failed": 0, "message": "All set"})
	if _, ok := d["trusted"]; ok || d["reply_request_id"] != d["request_id"] || d["routine_name"] != "run_me" ||
		d["voice_command"] != "routine: run_me" || d["tool_call_id"] == "" || d["user_id"] != nil {
		t.Fatalf("details %v", d)
	}
	def, _ := json.Marshal(d["routine"])
	if !strings.Contains(string(def), `"steps":[{"args":{"city":"Boston","resolved_datetimes":["today"]},"command":"get_weather","label":"weather"}]`) {
		t.Fatalf("inline definition %s", def)
	}
	got := (<-ch).r.want(200).json()
	if got["success"] != true || got["status"] != "success" || got["message"] != "All set" || got["passed"] != 2.0 ||
		got["failed"] != 0.0 || len(got) != 5 {
		t.Fatalf("result %v", got)
	}

	// Partial and failed map from output.success / output.failed.
	ch = start(map[string]any{"node_id": "n1"})
	e.answerRoutine(c, n, map[string]any{"success": true, "passed": 1, "failed": 1, "message": "Mostly"})
	if got := (<-ch).r.want(200).json(); got["status"] != "partial" {
		t.Fatalf("partial %v", got)
	}
	ch = start(map[string]any{"node_id": "n1"})
	e.answerRoutine(c, n, map[string]any{"success": false, "error": "All routine steps failed."})
	if got := (<-ch).r.want(200).json(); got["status"] != "failed" || got["success"] != false || got["message"] != nil {
		t.Fatalf("failed %v", got)
	}

	// No answer: timeout; the late result is acknowledged and dropped.
	ch = start(map[string]any{"node_id": "n1"})
	d = e.answerRoutine(c, n, nil)
	if got := (<-ch).r.want(200).json(); got["status"] != "timeout" || got["success"] != false {
		t.Fatalf("timeout %v", got)
	}
	e.do("POST", "/api/v0/device-control-results/"+d["reply_request_id"].(string), map[string]any{"output": map[string]any{}}, n.h()).want(200)

	// A result from another node is refused (D4).
	ch = start(map[string]any{"node_id": "n1"})
	d = e.answerRoutine(c, n, nil)
	e.do("POST", "/api/v0/device-control-results/"+d["reply_request_id"].(string), map[string]any{"output": map[string]any{"success": true}}, other.h()).
		detail(403, "Request does not belong to this node")
	<-ch

	// Node choice: none and no primary → 400; another household's node → 404; the primary node default.
	e.do("POST", run, map[string]any{}, bearer(e.owner)).detail(400, "No node specified and no household primary node configured")
	e.do("POST", run, map[string]any{"node_id": "n2"}, bearer(e.owner)).detail(404, "Node not found")
	e.do("POST", run, map[string]any{"node_id": "ghost"}, bearer(e.owner)).detail(404, "Node not found")
	e.do("POST", run, nil, bearer(e.owner)).want(400)
	if err := e.m.settings.Set(context.Background(), settingPrimaryNode, "n1", settingsScope(rhh)); err != nil {
		t.Fatal(err)
	}
	ch = start(map[string]any{"node_id": nil})
	e.answerRoutine(c, n, map[string]any{"success": true})
	if got := (<-ch).r.want(200).json(); got["status"] != "success" {
		t.Fatalf("primary %v", got)
	}
	e.do("POST", e.path("/nope/run-now"), map[string]any{}, bearer(e.owner)).detail(404, "Routine not found")
	if len(e.notify.items) != 0 {
		t.Fatalf("run-now notified: %v", e.notify.items)
	}
}

func TestRunNowOverlapAndNoBroker(t *testing.T) {
	e := newRoutineEnv(t)
	n := e.createNode("n1", rhh)
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/commands")
	rt := e.create("Slow", nil)
	run := e.path("/" + rt["id"].(string) + "/run-now")
	ch := make(chan *resp, 1)
	go func() { ch <- e.do("POST", run, map[string]any{"node_id": "n1"}, bearer(e.owner)) }()
	d := e.answerRoutine(c, n, nil)
	// While the first run waits, a second is skipped with a note (D40 08.Q10).
	got := e.do("POST", run, map[string]any{"node_id": "n1"}, bearer(e.owner)).want(200).json()
	if got["status"] != "failed" || got["message"] != "'Slow' is already running." || got["error"] != "'Slow' is already running." {
		t.Fatalf("overlap %v", got)
	}
	e.do("POST", "/api/v0/device-control-results/"+d["reply_request_id"].(string), map[string]any{"output": map[string]any{"success": true}}, n.h()).want(200)
	if r := (<-ch).want(200).json(); r["status"] != "success" {
		t.Fatalf("first run %v", r)
	}

	ne := newEnv(t, envOpts{noMQTT: true})
	owner := ne.auth.addUser(1, rhh, authn.RoleOwner)
	ne.createNode("n1", rhh)
	id := ne.do("POST", "/api/v0/households/"+rhh+"/routines", routineBody("X", nil), bearer(owner)).want(201).json()["id"].(string)
	got = ne.do("POST", "/api/v0/households/"+rhh+"/routines/"+id+"/run-now", map[string]any{"node_id": "n1"}, bearer(owner)).want(200).json()
	if got["status"] != "failed" || got["error"] != "cc: MQTT not available" {
		t.Fatalf("no broker %v", got)
	}
}

// fire runs a routine trigger's job the way the scheduler would enqueue it.
func (re *routineEnv) fireRoutine(id string) map[string]any {
	re.t.Helper()
	inner, _ := json.Marshal(map[string]string{"routine_id": id})
	p, _ := json.Marshal(scheduler.Fire{Trigger: routineTrigger(id), ScheduledAt: re.clock(), Payload: inner})
	out, err := re.m.runRoutineFire(context.Background(), queue.Job{Type: routineFireJob, Payload: p})
	if err != nil {
		re.t.Fatal(err)
	}
	var v map[string]any
	_ = json.Unmarshal(out, &v)
	return v
}

func (re *routineEnv) status(id string) (scheduler.Status, error) {
	return re.sched.Status(context.Background(), routineTrigger(id))
}

func TestRoutineTriggers(t *testing.T) {
	e := newRoutineEnv(t)
	e.createNode("n1", rhh)
	ny, _ := time.LoadLocation("America/New_York")
	cron := map[string]any{"type": "cron", "cron": "0 8 * * *", "timezone": "America/New_York", "target_node_id": "n1", "enabled": true}
	rt := e.create("Morning", map[string]any{"schedule": cron})
	id := rt["id"].(string)
	sched := rt["schedule"].(map[string]any)
	if sched["target_node_id"] != "n1" || sched["last_fired_at"] != nil || sched["interval_seconds"] != nil {
		t.Fatalf("schedule %v", sched)
	}
	st, err := e.status(id)
	if err != nil {
		t.Fatal(err)
	}
	if local := st.NextFireAt.In(ny); local.Hour() != 8 || local.Minute() != 0 {
		t.Fatalf("next fire %v, want 08:00 New York", local)
	}

	// An interval routine: re-saving with the same timing keeps the next fire (§8.4).
	iv := e.create("Hourly", map[string]any{"schedule": map[string]any{"type": "interval", "interval_seconds": 3600, "target_node_id": "n1"}})
	ivID := iv["id"].(string)
	st1, _ := e.status(ivID)
	time.Sleep(5 * time.Millisecond)
	e.do("PATCH", e.path("/"+ivID), map[string]any{"name": "Hourly!", "schedule": map[string]any{"type": "interval",
		"interval_seconds": 3600, "target_node_id": "n1"}}, bearer(e.owner)).want(200)
	if st2, _ := e.status(ivID); !st2.NextFireAt.Equal(st1.NextFireAt) {
		t.Fatalf("resave moved the next fire: %v → %v", st1.NextFireAt, st2.NextFireAt)
	}
	// A timing change re-arms it.
	e.do("PATCH", e.path("/"+ivID), map[string]any{"schedule": map[string]any{"type": "interval", "interval_seconds": 60,
		"target_node_id": "n1"}}, bearer(e.owner)).want(200)
	if st3, _ := e.status(ivID); st3.NextFireAt.Sub(time.Now()) > time.Minute {
		t.Fatalf("not re-armed: %v", st3.NextFireAt)
	}
	// Disabling the routine or its schedule drops the trigger; enabling brings it back.
	e.do("PATCH", e.path("/"+ivID), map[string]any{"enabled": false}, bearer(e.owner)).want(200)
	if _, err := e.status(ivID); err != sql.ErrNoRows {
		t.Fatalf("disabled routine kept its trigger: %v", err)
	}
	e.do("PATCH", e.path("/"+ivID), map[string]any{"enabled": true}, bearer(e.owner)).want(200)
	if _, err := e.status(ivID); err != nil {
		t.Fatalf("re-enabled: %v", err)
	}
	e.do("PATCH", e.path("/"+ivID), map[string]any{"schedule": map[string]any{"type": "interval", "interval_seconds": 60, "enabled": false}}, bearer(e.owner)).want(200)
	if _, err := e.status(ivID); err != sql.ErrNoRows {
		t.Fatalf("disabled schedule kept its trigger: %v", err)
	}
	e.do("DELETE", e.path("/"+id), nil, bearer(e.owner)).want(204)
	if _, err := e.status(id); err != sql.ErrNoRows {
		t.Fatalf("deleted routine kept its trigger: %v", err)
	}

	// Start reconciles: a scheduled row with no trigger (an import) gets one.
	again := e.create("Again", map[string]any{"schedule": cron})["id"].(string)
	if err := e.sched.Delete(context.Background(), routineTrigger(again)); err != nil {
		t.Fatal(err)
	}
	if err := e.m.startRoutines(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.status(again); err != nil {
		t.Fatalf("not reconciled: %v", err)
	}

	// The scheduler enqueues the routine job when it comes due.
	if _, err := e.d.Write.Exec(`UPDATE platform_triggers SET next_fire_at = 1 WHERE name = ?`, routineTrigger(again)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sched.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := e.d.Read.QueryRow(`SELECT COUNT(*) FROM platform_jobs WHERE type = ?`, routineFireJob).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("routine jobs %d %v", jobs, err)
	}
}

func TestScheduledRoutineFire(t *testing.T) {
	e := newRoutineEnv(t)
	n := e.createNode("n1", rhh)
	rt := e.create("Wake", map[string]any{"schedule": map[string]any{"type": "cron", "cron": "0 7 * * *", "target_node_id": "n1"}})
	id := rt["id"].(string)

	// Offline target: a "couldn't run" card to the household, and the fire still counts.
	e.advance(20 * time.Minute)
	e.fireRoutine(id)
	if len(e.notify.items) != 1 {
		t.Fatalf("cards %v", e.notify.items)
	}
	card := e.notify.items[0]
	if card.Title != "⚠️ 'Wake' couldn't run" || card.Summary != "the target node was offline" || card.Category != "routine" ||
		card.UserID != nil || card.Metadata["status"] != "failed" || card.Metadata["routine_id"] != id {
		t.Fatalf("offline card %+v", card)
	}
	if p := e.notify.pushes[0]; p.TargetType != "household" || p.TargetID != rhh {
		t.Fatalf("push %+v", p)
	}
	s := e.row(id).sched()
	if s.LastFiredAt == nil || *s.LastFiredAt != "2026-10-06T12:20:00+00:00" {
		t.Fatalf("last_fired_at %v", s.LastFiredAt)
	}
	// The mobile editor resends the schedule without last_fired_at: it is kept.
	got := e.do("PATCH", e.path("/"+id), map[string]any{"schedule": map[string]any{"type": "cron", "cron": "0 7 * * *",
		"target_node_id": "n1"}}, bearer(e.owner)).want(200).json()
	if got["schedule"].(map[string]any)["last_fired_at"] != "2026-10-06T12:20:00+00:00" {
		t.Fatalf("resave lost last_fired_at: %v", got["schedule"])
	}

	// Online: it runs and posts the result card.
	c := e.dialNode(n) // authenticating refreshes last_seen
	c.subscribe("jarvis/nodes/n1/commands")
	done := make(chan map[string]any, 1)
	go func() { done <- e.fireRoutine(id) }()
	e.answerRoutine(c, n, map[string]any{"success": true, "passed": 3, "failed": 0, "message": "Good morning!"})
	if r := <-done; r["status"] != "success" {
		t.Fatalf("fire result %v", r)
	}
	if card := e.notify.items[1]; card.Title != "✅ 'Wake' finished" || card.Summary != "Good morning!" || card.Body != "Good morning!" {
		t.Fatalf("success card %+v", card)
	}
	// Timeout card.
	go func() { done <- e.fireRoutine(id) }()
	e.answerRoutine(c, n, nil)
	<-done
	if card := e.notify.items[2]; card.Title != "⏱️ 'Wake' didn't finish" || card.Summary != "The node didn't respond in time." {
		t.Fatalf("timeout card %+v", card)
	}

	// A target in another household never runs it (and a gone routine drops its trigger).
	if _, err := e.d.Write.Exec(`UPDATE cc_nodes SET household_id = 'hh-other' WHERE node_id = 'n1'`); err != nil {
		t.Fatal(err)
	}
	e.fireRoutine(id)
	if card := e.notify.items[3]; card.Summary != "the target node was offline" {
		t.Fatalf("cross-household card %+v", card)
	}
	if _, err := e.d.Write.Exec(`DELETE FROM cc_routines WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	e.fireRoutine(id)
	if _, err := e.status(id); err != sql.ErrNoRows {
		t.Fatalf("orphan trigger kept: %v", err)
	}
	if len(e.notify.items) != 4 {
		t.Fatalf("extra cards %d", len(e.notify.items))
	}
}

func TestRoutineDefinitionsDedupe(t *testing.T) {
	defs := Definitions()
	n := 0
	for _, d := range defs {
		if d.Key == settingPrimaryNode {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%s declared %d times", settingPrimaryNode, n)
	}
	if again := routineDefinitions(defs); len(again) != len(defs) {
		t.Fatal("declared twice")
	}
}
