//go:build contract

package contract

import (
	"net/http"
	"testing"
)

// Simple modes are jarvisd-only; the legacy Advanced MQTT case stays unchanged.
func TestCCSimpleRoutines(t *testing.T) {
	if !Jarvisd() {
		t.Skip("contract: Simple routines are jarvisd-only")
	}
	tg := T(t)
	tg.NeedMQTT(t)
	u, n := SharedUser(t), SharedCCNode(t)
	c := DialMQTTNode(t, n)
	c.Subscribe(t, "jarvis/nodes/"+n.ID+"/routines/sync")
	path := "/api/v0/households/" + u.HouseholdID + "/routines"
	shape := Obj{"id": UUID, "slug": String, "name": String, "mode": String, "utterance": String, "trigger_phrases": ArrayOf(String),
		"steps":                ArrayOf(Obj{"command": String, "args": ArrayOf(Obj{"key": String, "value": String}), "label": String}),
		"response_instruction": String, "response_length": String, "schedule": Null, "enabled": Bool, "created_at": TimestampNaive, "updated_at": TimestampNaive}
	tg.Get(t, CommandCenter, path, u.H()).Expect(http.StatusOK, Obj{"routines": ArrayOf(shape), "supported_modes": Eq([]any{"advanced", "simple"})})
	created := tg.Post(t, CommandCenter, path, map[string]any{"name": "Simple contract " + tg.RunID, "mode": "simple", "utterance": "Turn on the kitchen lights", "trigger_phrases": []string{"contract shortcut"}, "steps": []any{}}, u.H()).Expect(http.StatusCreated, shape).Object()
	id, slug := created["id"].(string), created["slug"].(string)
	t.Cleanup(func() { tg.do(CommandCenter, http.MethodDelete, path+"/"+id, nil, u.H()) })
	ExpectMQTT(t, c.NextTopic(t, mqttWait, "jarvis/nodes/"+n.ID+"/routines/sync"), Obj{"event": Eq("routines_changed"), "household_id": Eq(u.HouseholdID)})
	projection := Obj{"mode": Eq("simple"), "utterance": Eq("Turn on the kitchen lights"), "trigger_phrases": Eq([]any{"contract shortcut"}), "steps": Eq([]any{}), "response_instruction": Eq(""), "response_length": Eq("short")}
	tg.Get(t, CommandCenter, "/api/v0/nodes/"+n.ID+"/routines?simple_routines=1", n.APIKeyH()).Expect(http.StatusOK, Obj{"routines": Open{slug: projection}})
	inert := Obj{"trigger_phrases": Eq([]any{}), "steps": Eq([]any{map[string]any{"command": "__jarvis_simple_routine_requires_update__", "args": map[string]any{}, "label": "unavailable"}}), "response_instruction": Eq(""), "response_length": Eq("short")}
	tg.Get(t, CommandCenter, "/api/v0/nodes/"+n.ID+"/routines", n.APIKeyH()).Expect(http.StatusOK, Obj{"routines": Open{slug: inert}})
	tg.Do(t, CommandCenter, http.MethodPatch, path+"/"+id, map[string]any{"steps": []any{}}, u.H()).ExpectError(http.StatusConflict, "Update the app to edit a Simple routine")
	tg.Do(t, CommandCenter, http.MethodPatch, path+"/"+id, map[string]any{"enabled": false}, u.H()).Expect(http.StatusOK, shape)
	tg.Get(t, CommandCenter, "/api/v0/nodes/"+n.ID+"/routines?simple_routines=1", n.APIKeyH()).Expect(http.StatusOK, Obj{"routines": Open{slug: inert}})
}
