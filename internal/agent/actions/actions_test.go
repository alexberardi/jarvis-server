package actions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"sync"
	"testing"
)

// fakeCC is jarvisd as the node reaches it: verification, the callback payload and the posts.
type fakeCC struct {
	mu       sync.Mutex
	valid    bool
	verified []string
	payload  map[string]any // GET /api/v0/callbacks/{job}; nil answers 404
	posts    []post
}

type post struct {
	Path string
	Body map[string]any
}

func (f *fakeCC) Verify(_ context.Context, rid string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verified = append(f.verified, rid)
	return f.valid, nil
}

func (f *fakeCC) Do(_ context.Context, method, path string, body, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if method == http.MethodGet {
		if path != "/api/v0/callbacks/job-1" || f.payload == nil {
			return errors.New("404")
		}
		b, _ := json.Marshal(f.payload)
		return json.Unmarshal(b, out)
	}
	b, _ := json.Marshal(body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	f.posts = append(f.posts, post{path, m})
	return nil
}

// fakeCommands is the sidecar: what it was asked and what it answers.
type fakeCommands struct {
	asked  []map[string]any
	answer map[string]any
	err    error
}

func (f *fakeCommands) Action(params map[string]any) (map[string]any, error) {
	f.asked = append(f.asked, params)
	return f.answer, f.err
}

func (f *fakeCommands) Callback(params map[string]any) (map[string]any, error) {
	f.asked = append(f.asked, params)
	return f.answer, f.err
}

func newHandler(cmds *fakeCommands) (*Handler, *[]string, map[string]bool) {
	said := &[]string{}
	enabled := map[string]bool{}
	h := &Handler{
		Commands: cmds,
		Enabled: func(name string) bool {
			on, set := enabled[name]
			return on || !set
		},
		SetEnabled: func(name string, on bool) error { enabled[name] = on; return nil },
		Say:        func(_ context.Context, text string) error { *said = append(*said, text); return nil },
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return h, said, enabled
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// An action jarvisd didn't issue to this node (verification fails) runs nothing and gets no
// reply: the caller times out, as with an offline node.
func TestActionFailingVerificationIsDropped(t *testing.T) {
	cc := &fakeCC{valid: false}
	cmds := &fakeCommands{answer: map[string]any{"success": true}}
	h, _, _ := newHandler(cmds)
	h.Verbs()[VerbAction](context.Background(), cc, decode(t, `{"request_id":"r1","reply_request_id":"r1",
		"command_name":"control_device","action_name":"turn_on","context":{"protocol":"fakelight"}}`))
	if len(cmds.asked) != 0 || len(cc.posts) != 0 {
		t.Fatalf("asked %v, posts %v", cmds.asked, cc.posts)
	}
	if !reflect.DeepEqual(cc.verified, []string{"r1"}) {
		t.Fatalf("verified %v", cc.verified)
	}
}

// The app's device control: verified, run by the sidecar, and answered with a top-level
// {success, error, input_required} on the device-control sink under reply_request_id.
func TestActionDeviceControlPostsTopLevelResult(t *testing.T) {
	cc := &fakeCC{valid: true}
	ir := map[string]any{"type": "pin", "prompt": "Enter the code"}
	cmds := &fakeCommands{answer: map[string]any{"success": false, "error": "pair first", "input_required": ir}}
	h, said, _ := newHandler(cmds)
	h.Verbs()[VerbAction](context.Background(), cc, decode(t, `{"request_id":"r1","reply_request_id":"r1",
		"command_name":"control_device","action_name":"pair_start",
		"context":{"protocol":"fakelight","entity_id":"light.lamp"}}`))

	want := map[string]any{"command_name": "control_device", "action_name": "pair_start",
		"context": map[string]any{"protocol": "fakelight", "entity_id": "light.lamp"}, "user_id": nil, "enabled": true}
	if len(cmds.asked) != 1 || !reflect.DeepEqual(cmds.asked[0], want) {
		t.Fatalf("asked %v", cmds.asked)
	}
	wantPost := []post{{"/api/v0/device-control-results/r1",
		map[string]any{"success": false, "error": "pair first", "input_required": ir}}}
	if !reflect.DeepEqual(cc.posts, wantPost) {
		t.Fatalf("posts %v", cc.posts)
	}
	if len(*said) != 0 {
		t.Fatalf("said %v for an app request", *said)
	}
}

// A command's own action (an inbox button): no reply_request_id, so the result goes under
// request_id, the user is passed through, and the command's message is spoken.
func TestActionCommandSpeaksItsMessageWhenVoiceOriginated(t *testing.T) {
	cc := &fakeCC{valid: true}
	cmds := &fakeCommands{answer: map[string]any{"success": true, "error": nil, "message": "Sent."}}
	h, said, _ := newHandler(cmds)
	h.Verbs()[VerbAction](context.Background(), cc, decode(t, `{"request_id":"r2",
		"command_name":"email","action_name":"send_click","context":{"draft":"hi"},"user_id":7}`))

	if got := cmds.asked[0]["user_id"]; got != float64(7) {
		t.Fatalf("user_id %v", got)
	}
	wantPost := []post{{"/api/v0/device-control-results/r2", map[string]any{"success": true, "error": nil}}}
	if !reflect.DeepEqual(cc.posts, wantPost) {
		t.Fatalf("posts %v", cc.posts)
	}
	if !reflect.DeepEqual(*said, []string{"Sent."}) {
		t.Fatalf("said %v", *said)
	}
}

// A command the household turned off (toggle_command) is not offered to the sidecar, so
// control_device falls through to the device family as on the Python node.
func TestActionTellsTheSidecarAboutADisabledCommand(t *testing.T) {
	cc := &fakeCC{valid: true}
	cmds := &fakeCommands{answer: map[string]any{"success": true}}
	h, _, enabled := newHandler(cmds)
	enabled["control_device"] = false
	h.Verbs()[VerbAction](context.Background(), cc, decode(t, `{"request_id":"r1","reply_request_id":"r1",
		"command_name":"control_device","action_name":"turn_on","context":{}}`))
	if cmds.asked[0]["enabled"] != false {
		t.Fatalf("asked %v", cmds.asked)
	}
}

// The sidecar failing (none running, or it crashed) is still an answer, not a timeout.
func TestActionSidecarErrorIsAFailedResult(t *testing.T) {
	cc := &fakeCC{valid: true}
	cmds := &fakeCommands{err: errors.New("no packages are installed on this computer")}
	h, _, _ := newHandler(cmds)
	h.Verbs()[VerbAction](context.Background(), cc, decode(t, `{"request_id":"r1","reply_request_id":"r9",
		"command_name":"control_device","action_name":"turn_on","context":{}}`))
	wantPost := []post{{"/api/v0/device-control-results/r9",
		map[string]any{"success": false, "error": "no packages are installed on this computer"}}}
	if !reflect.DeepEqual(cc.posts, wantPost) {
		t.Fatalf("posts %v", cc.posts)
	}
}

// Without request_id, or without a command or action name, nothing runs (N: logs and returns).
func TestActionIncompleteIsIgnored(t *testing.T) {
	for _, det := range []string{
		`{"command_name":"control_device","action_name":"turn_on"}`,
		`{"request_id":"r1","action_name":"turn_on"}`,
		`{"request_id":"r1","command_name":"control_device"}`,
	} {
		cc := &fakeCC{valid: true}
		cmds := &fakeCommands{answer: map[string]any{"success": true}}
		h, _, _ := newHandler(cmds)
		h.Verbs()[VerbAction](context.Background(), cc, decode(t, det))
		if len(cmds.asked) != 0 || len(cc.posts) != 0 {
			t.Fatalf("%s: asked %v, posts %v", det, cmds.asked, cc.posts)
		}
	}
}

// A callback: the payload is fetched from jarvisd (its verification), run by the sidecar with
// the request's details, and the result posted to the job.
func TestCallbackFetchesRunsAndPosts(t *testing.T) {
	cc := &fakeCC{payload: map[string]any{"job_id": "job-1", "command_name": "reminder", "callback_name": "snooze",
		"data": map[string]any{"minutes": 10}, "user_id": 7, "voice_command": "cb:snooze", "conversation_id": "callback:job-1"}}
	cmds := &fakeCommands{answer: map[string]any{"success": true, "error": nil, "context_data": map[string]any{"message": "Snoozed."}}}
	h, said, _ := newHandler(cmds)
	h.Verbs()[VerbCallback](context.Background(), cc, map[string]any{"request_id": "job-1"})

	want := map[string]any{"command_name": "reminder", "callback_name": "snooze", "data": map[string]any{"minutes": float64(10)},
		"user_id": float64(7), "voice_command": "cb:snooze", "conversation_id": "callback:job-1", "enabled": true}
	if len(cmds.asked) != 1 || !reflect.DeepEqual(cmds.asked[0], want) {
		t.Fatalf("asked %v", cmds.asked)
	}
	wantPost := []post{{"/api/v0/callbacks/job-1/result",
		map[string]any{"success": true, "error": nil, "context_data": map[string]any{"message": "Snoozed."}}}}
	if !reflect.DeepEqual(cc.posts, wantPost) {
		t.Fatalf("posts %v", cc.posts)
	}
	if len(*said) != 0 {
		t.Fatalf("a callback spoke: %v", *said)
	}
}

// The payload's defaults when jarvisd leaves them out, as N: fills them.
func TestCallbackDefaults(t *testing.T) {
	cc := &fakeCC{payload: map[string]any{"command_name": "reminder", "callback_name": "snooze"}}
	cmds := &fakeCommands{answer: map[string]any{"success": true}}
	h, _, _ := newHandler(cmds)
	h.Verbs()[VerbCallback](context.Background(), cc, map[string]any{"request_id": "job-1"})
	got := cmds.asked[0]
	if got["voice_command"] != "cb:snooze" || got["conversation_id"] != "callback:job-1" ||
		!reflect.DeepEqual(got["data"], map[string]any{}) || got["user_id"] != nil {
		t.Fatalf("asked %v", got)
	}
}

// Every failure after the fetch still posts a result, so the inbox item doesn't hang; a fetch
// that fails (an expired or someone else's job) posts nothing.
func TestCallbackFailures(t *testing.T) {
	cc := &fakeCC{}
	cmds := &fakeCommands{answer: map[string]any{"success": true}}
	h, _, _ := newHandler(cmds)
	h.Verbs()[VerbCallback](context.Background(), cc, map[string]any{"request_id": "job-1"})
	if len(cmds.asked) != 0 || len(cc.posts) != 0 {
		t.Fatalf("fetch failed: asked %v, posts %v", cmds.asked, cc.posts)
	}

	cc = &fakeCC{payload: map[string]any{"command_name": "reminder"}}
	h.Verbs()[VerbCallback](context.Background(), cc, map[string]any{"request_id": "job-1"})
	want := []post{{"/api/v0/callbacks/job-1/result", map[string]any{"success": false, "error": "missing command_name or callback_name"}}}
	if !reflect.DeepEqual(cc.posts, want) {
		t.Fatalf("posts %v", cc.posts)
	}

	cc = &fakeCC{payload: map[string]any{"command_name": "reminder", "callback_name": "snooze"}}
	h.Commands = &fakeCommands{err: errors.New("sidecar exited")}
	h.Verbs()[VerbCallback](context.Background(), cc, map[string]any{"request_id": "job-1"})
	want = []post{{"/api/v0/callbacks/job-1/result", map[string]any{"success": false, "error": "sidecar exited"}}}
	if !reflect.DeepEqual(cc.posts, want) {
		t.Fatalf("posts %v", cc.posts)
	}

	cc = &fakeCC{payload: map[string]any{"command_name": "reminder", "callback_name": "snooze"}}
	h.Verbs()[VerbCallback](context.Background(), cc, map[string]any{})
	if len(cc.posts) != 0 {
		t.Fatalf("no request_id: posts %v", cc.posts)
	}
}

// toggle_command turns a command on or off as the command_registry push does; enabled is read
// as N: reads it (true, 1, yes in any case; absent is on).
func TestToggleCommand(t *testing.T) {
	cases := []struct {
		details string
		want    bool
	}{
		{`{"command_name":"control_device","enabled":false}`, false},
		{`{"command_name":"control_device","enabled":"false"}`, false},
		{`{"command_name":"control_device","enabled":"0"}`, false},
		{`{"command_name":"control_device","enabled":0}`, false},
		{`{"command_name":"control_device","enabled":true}`, true},
		{`{"command_name":"control_device","enabled":"Yes"}`, true},
		{`{"command_name":"control_device","enabled":1}`, true},
		{`{"command_name":"control_device"}`, true},
	}
	for _, c := range cases {
		h, _, enabled := newHandler(&fakeCommands{})
		enabled["control_device"] = !c.want
		h.Verbs()[VerbToggle](context.Background(), &fakeCC{}, decode(t, c.details))
		if enabled["control_device"] != c.want {
			t.Fatalf("%s: enabled %v", c.details, enabled["control_device"])
		}
	}
	h, _, enabled := newHandler(&fakeCommands{})
	h.Verbs()[VerbToggle](context.Background(), &fakeCC{}, decode(t, `{"enabled":false}`))
	if len(enabled) != 0 {
		t.Fatalf("no command_name: %v", enabled)
	}
}
