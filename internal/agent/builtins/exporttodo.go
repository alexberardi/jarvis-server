package builtins

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
)

// ExportTodo is export_todo_list (commands/export_todo_list_command.py): the to-do list sent
// to the speaker's phone as an interactive checklist in the inbox, and its save_todo_state
// callback, which writes what was ticked back onto the todo_list records.
type ExportTodo struct {
	Store *nodesettings.Store
	CC    CC
	Log   *slog.Logger
	Now   func() time.Time // nil: time.Now
}

const (
	exportTodoName     = "export_todo_list"
	saveTodoState      = "save_todo_state"
	maxInteractiveRows = 100 // the SDK's _MAX_TOTAL_ROWS
)

func (e *ExportTodo) Available(context.Context) []node.ToolDef {
	return []node.ToolDef{def(exportTodoName)}
}

func (e *ExportTodo) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *ExportTodo) log() *slog.Logger {
	if e.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return e.Log
}

type todoEntry struct {
	key, task, addedAt string
	completed          bool
}

func (e *ExportTodo) Call(ctx context.Context, name string, args map[string]any, req node.Request) (map[string]any, bool) {
	if name != exportTodoName {
		return nil, false
	}
	if out := validated(def(name), args); out != nil {
		return out, true
	}
	if req.UserID == nil {
		return failure("Unknown speaker — cannot target the export push.", map[string]any{"error": "unknown_speaker",
			"message": "I'm not sure who's asking — I need to know whose phone to send the list to. Try training your voice in the app."}), true
	}
	var open, done []todoEntry
	for _, r := range e.Store.Records(todoName, e.now()) {
		x := rec{r.Key, r.Data}
		if !x.truthy("task") {
			continue
		}
		en := todoEntry{key: r.Key, task: pyStr(x.obj.Value("task")), addedAt: x.str("added_at"), completed: x.truthy("completed")}
		if en.completed {
			done = append(done, en)
		} else {
			open = append(open, en)
		}
	}
	if len(open)+len(done) == 0 {
		return finish(map[string]any{"message": "Your to-do list is empty — nothing to send.", "item_count": 0}, true, false), true
	}
	byAdded := func(a, b todoEntry) int { return strings.Compare(a.addedAt, b.addedAt) }
	slices.SortStableFunc(open, byAdded)
	slices.SortStableFunc(done, byAdded)
	open = open[:min(len(open), maxInteractiveRows)]
	done = done[:min(len(done), max(0, maxInteractiveRows-len(open)))]
	n := len(open) + len(done)
	noun := "tasks"
	if n == 1 {
		noun = "task"
	}
	item := map[string]any{
		"title":    "To-do list — " + strconv.Itoa(n) + " " + noun,
		"summary":  strconv.Itoa(len(open)) + " to do, " + strconv.Itoa(len(done)) + " done. Check off what you've finished and tap Save.",
		"body":     todoBody(open, done),
		"category": "interactive_list", "metadata": todoChecklist(open, done),
		"user_id": *req.UserID, "create_push_notification": true, "target_type": "user",
	}
	if res := sdkhost.PostInbox(ctx, e.CC(), item, exportTodoName, e.log()); res != "ok" {
		return exportFailure(res, "to-do list"), true
	}
	return finish(map[string]any{"message": "I sent your to-do list to your phone — " + strconv.Itoa(n) + " " + noun + ".",
		"item_count": n, "open_count": len(open), "done_count": len(done)}, true, false), true
}

// todoBody is _build_body: the markdown checklist for a client without the rich renderer.
func todoBody(open, done []todoEntry) string {
	var lines []string
	for _, t := range open {
		lines = append(lines, "- [ ] "+t.task)
	}
	for _, t := range done {
		lines = append(lines, "- [x] "+t.task)
	}
	return strings.Join(lines, "\n")
}

// todoChecklist is _build_metadata: InteractiveList.to_dict, each task a checkbox (ticked when
// done), and every shown key in context so the callback can reopen what was unticked.
func todoChecklist(open, done []todoEntry) map[string]any {
	var sections []any
	keys := []any{}
	for _, s := range []struct {
		title   string
		entries []todoEntry
	}{{"To do", open}, {"Completed", done}} {
		rows := []any{}
		for _, t := range s.entries {
			rows = append(rows, map[string]any{"key": t.key, "label": truncRunes(t.task, 120), "control": "checkbox",
				"default": map[string]any{"selected": t.completed}})
			keys = append(keys, t.key)
		}
		if len(rows) > 0 {
			sections = append(sections, map[string]any{"title": s.title, "rows": rows})
		}
	}
	if len(sections) == 0 {
		sections = []any{map[string]any{"rows": []any{}}}
	}
	return map[string]any{"type": "interactive_list", "version": 1, "command_name": exportTodoName,
		"empty_text": "Your to-do list is empty.", "context": map[string]any{"keys": keys}, "sections": sections,
		"actions": []any{map[string]any{"label": "Save", "callback": saveTodoState, "style": "primary"}}}
}

// truncRunes is Python's s[:n].
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// exportFailure is _post_failure_response for what (the "to-do list", the "shopping list").
func exportFailure(res, what string) map[string]any {
	var msg, details string
	switch res {
	case "no_cc_url":
		msg, details = "I can't find the server to send your "+what+".", "service_discovery returned no command-center URL"
	case "import_error", "no_backend":
		msg = "I can't export the list — the send module isn't loaded."
		details = "no SDK inbox backend registered"
		if res == "import_error" {
			details = "ImportError in the inbox backend — clients.rest_client or utils.service_discovery missing"
		}
	default:
		msg, details = "I couldn't reach the server right now.", "inbox post failed ("+res+": HTTP error, timeout, or unreachable)"
	}
	return failure(details, map[string]any{"message": msg})
}

// Callback is the save_todo_state @callback: the ticked rows done, the shown ones left unticked
// reopened; a task deleted since is skipped. handled is false for another command's callback.
func (e *ExportTodo) Callback(params map[string]any) (map[string]any, bool) {
	if params["command_name"] != exportTodoName {
		return nil, false
	}
	if enabled, ok := params["enabled"].(bool); ok && !enabled {
		return map[string]any{"success": false, "error": "Command '" + exportTodoName + "' not found on this node"}, true
	}
	if params["callback_name"] != saveTodoState {
		return map[string]any{"success": false, "error": "Callback '" + pyStr(params["callback_name"]) + "' not registered on '" + exportTodoName + "'"}, true
	}
	data, _ := params["data"].(map[string]any)
	var universe []string
	if c, ok := data["context"].(map[string]any); ok {
		if raw, ok := c["keys"].([]any); ok {
			universe = []string{}
			for _, k := range raw {
				if s := strings.TrimSpace(pyStr(k)); s != "" {
					universe = append(universe, s)
				}
			}
		}
	}
	ticked := map[string]bool{}
	if sel, ok := data["selected"].([]any); ok {
		for _, s := range sel {
			if m, ok := s.(map[string]any); ok {
				if k := strings.TrimSpace(pyStr(m["key"])); pyTruthy(m["key"]) && k != "" {
					ticked[k] = true
				}
			}
		}
	}
	if universe == nil { // a legacy payload without context: every task
		for _, r := range e.Store.Records(todoName, e.now()) {
			universe = append(universe, r.Key)
		}
	}
	now := pyISO(e.now())
	var done, reopened []string
	seen := map[string]bool{}
	for _, key := range universe {
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		o, ok := e.Store.Data(todoName, key, e.now())
		if !ok {
			continue
		}
		r := rec{key, o}
		want := ticked[key]
		if r.truthy("completed") == want {
			continue
		}
		o = o.Without(func(k string) bool { return k == "_data_key" || k == "_expires_at" || k == "data_key" })
		setJSON(&o, "completed", want)
		if want {
			setJSON(&o, "completed_at", now)
		} else {
			setJSON(&o, "completed_at", nil)
		}
		_ = e.Store.SaveData(todoName, key, o, nil)
		name := cmpOrStr(r.str("task"), key)
		if want {
			done = append(done, name)
		} else {
			reopened = append(reopened, name)
		}
	}
	var parts []string
	if len(done) > 0 {
		parts = append(parts, "marked "+spokenJoin(done)+" done")
	}
	if len(reopened) > 0 {
		parts = append(parts, "reopened "+spokenJoin(reopened))
	}
	msg := "Your to-do list is already up to date."
	if len(parts) > 0 {
		msg = "Updated your to-do list — " + strings.Join(parts, " and ") + "."
	}
	lines := []string{}
	for _, t := range done {
		lines = append(lines, "✓ "+t)
	}
	for _, t := range reopened {
		lines = append(lines, "○ "+t)
	}
	return map[string]any{"success": true, "error": nil, "context_data": map[string]any{"message": msg, "detail_lines": lines,
		"completed": nonNil(done), "reopened": nonNil(reopened)}}, true
}

var exportTodoRe = regexp.MustCompile(`(?i)^\s*(?:(?:send|export)\s+(?:me\s+)?` + todoNoun + `(?:\s+(?:to|on)\s+(?:my\s+)?phone)?|(?:show|pull\s+up|open|bring\s+up)\s+(?:me\s+)?` +
	todoNoun + `\s+(?:to|on)\s+(?:my\s+)?phone)\s*[?.!]*$`)

var exportTodoPatterns = []pattern{
	regexPattern("export_todo_list.export", "Bypass LLM for 'send/export the to-do list (to my phone)'", "send my to-do list to my phone",
		exportTodoRe, func([]string) (map[string]any, bool) { return map[string]any{}, true }),
}

func (e *ExportTodo) Direct(ctx context.Context, said string) (map[string]any, bool) {
	return directBy(ctx, e.Store, exportTodoName, exportTodoPatterns, said, func(a map[string]any) map[string]any {
		out, _ := e.Call(ctx, exportTodoName, a, node.Request{UserID: node.Speaker(ctx)})
		return out
	})
}

func (e *ExportTodo) FastPaths(command string) []nodesettings.FastPath {
	if command != exportTodoName {
		return nil
	}
	return patternList(exportTodoPatterns)
}

// ExportTodoMatch is the arguments Direct would run (for comparing with N:'s pre_route).
func ExportTodoMatch(said string) (Match, bool) {
	for _, p := range exportTodoPatterns {
		if args, ok := p.match(said); ok {
			return Match{exportTodoName, args}, true
		}
	}
	return Match{}, false
}

// DataCommands: export_todo_list's browser entry is disabled (it shows the todo_list records).
func (e *ExportTodo) DataCommands() []nodesettings.DataCommand {
	return []nodesettings.DataCommand{browserDef(exportTodoName)}
}

func (e *ExportTodo) Summaries(_ context.Context, _ string, records []nodesettings.Object) ([]map[string]any, error) {
	return make([]map[string]any, len(records)), nil
}

func (e *ExportTodo) Create(context.Context, string, nodesettings.Object, json.Number) (string, json.RawMessage, error) {
	return "", nil, noCreate(exportTodoName)
}
