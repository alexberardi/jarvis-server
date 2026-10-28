package builtins

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// Todo is todo_list (commands/todo_list_command.py): the household's to-do list, one record
// per task in the node's store under "todo_list", as the Python command kept it. A task is a
// whole phrase, never split on "and" ("call mom and dad" is one task).
type Todo struct {
	Store *nodesettings.Store
	Now   func() time.Time // nil: time.Now
}

const todoName = "todo_list"

func (t *Todo) Available(context.Context) []node.ToolDef { return []node.ToolDef{def(todoName)} }

func (t *Todo) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Todo) Call(_ context.Context, name string, args map[string]any, req node.Request) (map[string]any, bool) {
	if name != todoName {
		return nil, false
	}
	args = todoPostProcess(args)
	if out := validated(def(name), args); out != nil {
		return out, true
	}
	action, _ := args["action"].(string)
	var tasks []string
	if l, ok := args["tasks"].([]any); ok {
		for _, x := range l {
			if s := strings.TrimSpace(pyStr(x)); s != "" {
				tasks = append(tasks, s)
			}
		}
	}
	if (action == "add" || action == "complete" || action == "uncomplete" || action == "remove") && len(tasks) == 0 {
		return finish(map[string]any{"error": "Which task?", "message": "Which task?"}, false, true), true
	}
	switch action {
	case "add":
		return t.add(tasks, req.UserID), true
	case "read":
		return t.read(), true
	case "complete":
		return t.setCompleted(tasks, true), true
	case "uncomplete":
		return t.setCompleted(tasks, false), true
	case "remove":
		return t.remove(tasks), true
	case "clear":
		scope, _ := args["scope"].(string)
		return t.clear(cmpOrStr(scope, "all")), true
	}
	return failure("Unknown to-do list action: "+action,
		map[string]any{"message": "I don't know the to-do list action '" + action + "'."}), true
}

// todoPostProcess is post_process_tool_call: a string is one task, a list is kept as the
// model gave it, the action defaulted, an unknown scope dropped.
func todoPostProcess(in map[string]any) map[string]any {
	args := make(map[string]any, len(in))
	for k, v := range in {
		args[k] = v
	}
	switch tasks := args["tasks"].(type) {
	case string:
		if c := strings.TrimSpace(tasks); c != "" {
			args["tasks"] = []any{c}
		} else {
			args["tasks"] = []any{}
		}
	case []any:
		out := []any{}
		for _, e := range tasks {
			if s := strings.TrimSpace(pyStr(e)); s != "" {
				out = append(out, s)
			}
		}
		args["tasks"] = out
	}
	if a, _ := args["action"].(string); a == "" && !truthy(args["action"]) {
		if l, _ := args["tasks"].([]any); len(l) > 0 {
			args["action"] = "add"
		} else {
			args["action"] = "read"
		}
	}
	if sc, ok := args["scope"]; ok && sc != nil && sc != "all" && sc != "completed" {
		delete(args, "scope")
	}
	return args
}

func (t *Todo) records() []rec {
	var out []rec
	for _, r := range t.Store.Records(todoName, t.now()) {
		out = append(out, rec{r.Key, r.Data})
	}
	return out
}

func (t *Todo) add(tasks []string, by *int64) map[string]any {
	now := pyISO(t.now())
	var added, already []string
	for _, task := range tasks {
		key := itemKey(task)
		if key == "" {
			continue
		}
		existing, ok := t.Store.Data(todoName, key, t.now())
		if r := (rec{key, existing}); ok && !r.truthy("completed") {
			already = append(already, cmpOrStr(r.str("task"), task))
			continue
		}
		var record nodesettings.Object
		setJSON(&record, "task", task)
		setJSON(&record, "completed", false)
		setJSON(&record, "added_at", now)
		setJSON(&record, "completed_at", nil)
		if by != nil {
			setJSON(&record, "added_by", *by)
		}
		_ = t.Store.SaveData(todoName, key, record, nil)
		added = append(added, task)
	}
	var sentences []string
	if len(added) > 0 {
		sentences = append(sentences, "Added "+spokenJoin(added)+" to your to-do list.")
	}
	if len(already) > 0 {
		verb := "are"
		if len(already) == 1 {
			verb = "is"
		}
		sentences = append(sentences, capitalize(spokenJoin(already))+" "+verb+" already on it.")
	}
	if len(sentences) == 0 {
		return failure("I couldn't make out any tasks to add.", map[string]any{"message": "I couldn't make out any tasks to add."})
	}
	return finish(map[string]any{"message": strings.Join(sentences, " "), "added": nonNil(added), "already_on_list": nonNil(already)}, true, false)
}

func (t *Todo) read() map[string]any {
	var open []rec
	for _, r := range t.records() {
		if r.truthy("task") && !r.truthy("completed") {
			open = append(open, r)
		}
	}
	slices.SortStableFunc(open, func(a, b rec) int { return strings.Compare(a.str("added_at"), b.str("added_at")) })
	var tasks []string
	for _, r := range open {
		tasks = append(tasks, r.str("task"))
	}
	msg := "Your to-do list is empty."
	switch {
	case len(tasks) == 1:
		msg = "You have one thing on your to-do list: " + tasks[0] + "."
	case len(tasks) > 1:
		msg = "You have " + strconv.Itoa(len(tasks)) + " things on your to-do list: " + spokenJoin(tasks) + "."
	}
	return finish(map[string]any{"message": msg, "tasks": nonNil(tasks)}, true, false)
}

// find is _find_record: the task's key, else the first record (in key order) whose task and
// the phrase contain one another.
func (t *Todo) find(task string) (rec, bool) {
	key := itemKey(task)
	if d, ok := t.Store.Data(todoName, key, t.now()); ok {
		return rec{key, d}, true
	}
	needle := strings.ToLower(strings.TrimSpace(task))
	if needle == "" {
		return rec{}, false
	}
	all := t.records()
	slices.SortStableFunc(all, func(a, b rec) int { return strings.Compare(a.key, b.key) })
	for _, r := range all {
		name := strings.ToLower(r.str("task"))
		if strings.Contains(name, needle) || strings.Contains(needle, name) {
			return r, true
		}
	}
	return rec{}, false
}

func (t *Todo) setCompleted(tasks []string, completed bool) map[string]any {
	now := pyISO(t.now())
	var changed, missing []string
	for _, task := range tasks {
		r, ok := t.find(task)
		if !ok {
			missing = append(missing, task)
			continue
		}
		setJSON(&r.obj, "completed", completed)
		if completed {
			setJSON(&r.obj, "completed_at", now)
		} else {
			setJSON(&r.obj, "completed_at", nil)
		}
		_ = t.Store.SaveData(todoName, r.key, r.obj, nil)
		changed = append(changed, cmpOrStr(r.str("task"), task))
	}
	verb := "not done"
	if completed {
		verb = "done"
	}
	var msg string
	switch {
	case len(changed) > 0 && len(missing) > 0:
		msg = "Marked " + spokenJoin(changed) + " " + verb + ". I couldn't find " + spokenJoin(missing) + " on the list."
	case len(changed) > 0:
		msg = "Marked " + spokenJoin(changed) + " " + verb + "."
	default:
		msg = "I couldn't find " + spokenJoin(missing) + " on your to-do list."
	}
	key := "reopened"
	if completed {
		key = "completed"
	}
	return finish(map[string]any{"message": msg, key: nonNil(changed), "not_found": nonNil(missing)}, true, false)
}

func (t *Todo) remove(tasks []string) map[string]any {
	var removed, missing []string
	for _, task := range tasks {
		r, ok := t.find(task)
		if !ok {
			missing = append(missing, task)
			continue
		}
		if gone, _ := t.Store.DeleteData(todoName, r.key); gone {
			removed = append(removed, cmpOrStr(r.str("task"), task))
		} else {
			missing = append(missing, task)
		}
	}
	var msg string
	switch {
	case len(removed) > 0 && len(missing) > 0:
		msg = "Removed " + spokenJoin(removed) + " from your to-do list. I couldn't find " + spokenJoin(missing) + "."
	case len(removed) > 0:
		msg = "Removed " + spokenJoin(removed) + " from your to-do list."
	default:
		msg = "I couldn't find " + spokenJoin(missing) + " on your to-do list."
	}
	return finish(map[string]any{"message": msg, "removed": nonNil(removed), "not_found": nonNil(missing)}, true, false)
}

func (t *Todo) clear(scope string) map[string]any {
	if scope == "completed" {
		removed := 0
		for _, r := range t.records() {
			if r.truthy("completed") {
				_, _ = t.Store.DeleteData(todoName, r.key)
				removed++
			}
		}
		msg := "You have no completed tasks to clear."
		if removed > 0 {
			noun := strconv.Itoa(removed) + " completed tasks"
			if removed == 1 {
				noun = "one completed task"
			}
			msg = "Cleared " + noun + " from your to-do list."
		}
		return finish(map[string]any{"message": msg, "removed_count": removed, "scope": scope}, true, false)
	}
	n, _ := t.Store.DeleteAllData(todoName)
	msg := "Your to-do list is already empty."
	if n > 0 {
		noun := strconv.Itoa(n) + " tasks"
		if n == 1 {
			noun = "one task"
		}
		msg = "Cleared your to-do list — removed " + noun + "."
	}
	return finish(map[string]any{"message": msg, "removed_count": n, "scope": "all"}, true, false)
}

// --- fast paths ---

const (
	todoNoun     = `(?:my\s+|the\s+|our\s+)?(?:to[\s-]?do|task)\s+list`
	statusDone   = `(?:complete|completed|done|finished)`
	statusUndone = `(?:incomplete|unfinished|undone|not\s+(?:complete|completed|done|finished))`
)

var (
	todoAddRe  = regexp.MustCompile(`(?i)^\s*(?:add|put)\s+(?P<task>.+?)\s+(?:to|on(?:to)?)\s+` + todoNoun + `\s*[?.!]*$`)
	todoReadRe = regexp.MustCompile(`(?i)^\s*(?:what(?:'s|\s+is)\s+on|read(?:\s+me)?|show(?:\s+me)?|list)\s+` + todoNoun + `\s*[?.!]*$`)
	// Python's task_m2 branch is (?:(?!\s+not\s+).)+?; RE2 has no lookahead, so a task_m2 that
	// spans " not " is refused after the match (notInTask).
	todoCompleteRe = regexp.MustCompile(`(?i)^\s*(?:mark\s+(?P<task_m1>.+?)\s+(?:on|in)\s+` + todoNoun + `\s+(?:as\s+)?` + statusDone +
		`|mark\s+(?P<task_m2>.+?)\s+(?:as\s+)?` + statusDone + `\s+(?:on|in)\s+` + todoNoun +
		`|(?:check(?:\s+off)?|cross(?:\s+off|\s+out)?)\s+(?P<task_c>.+?)\s+(?:off\s+)?(?:(?:of|from|on)\s+)?` + todoNoun + `)\s*[?.!]*$`)
	todoUncompleteRe = regexp.MustCompile(`(?i)^\s*(?:mark\s+(?P<task_m1>.+?)\s+(?:on|in)\s+` + todoNoun + `\s+(?:as\s+)?` + statusUndone +
		`|mark\s+(?P<task_m2>.+?)\s+(?:as\s+)?` + statusUndone + `\s+(?:on|in)\s+` + todoNoun +
		`|(?:uncheck|uncross)\s+(?:off\s+)?(?P<task_u>.+?)\s+(?:off\s+)?(?:(?:of|from|on)\s+)?` + todoNoun + `)\s*[?.!]*$`)
	todoRemoveRe = regexp.MustCompile(`(?i)^\s*(?:remove|take|delete)\s+(?P<task>.+?)\s+(?:off|from)\s+(?:of\s+)?` + todoNoun + `\s*[?.!]*$`)
	todoClearRe  = regexp.MustCompile(`(?i)^\s*(?:clear|empty|wipe)\s+(?:out\s+)?` + todoNoun + `\s*[?.!]*$`)
	notWordRe    = regexp.MustCompile(`(?i)\s+not\s+`)
)

// taskAction is a handler for a single-task pattern: the first non-empty task group.
func taskAction(re *regexp.Regexp, action string, groups ...string) func([]string) (map[string]any, bool) {
	return func(m []string) (map[string]any, bool) {
		for _, g := range groups {
			if task := strings.TrimSpace(group(re, m, g)); task != "" {
				return map[string]any{"action": action, "tasks": []any{task}}, true
			}
		}
		return nil, false
	}
}

// completeMatch is todoCompleteRe with Python's lookahead: "mark X as not done on my to-do
// list" isn't a complete.
func completeMatch(said string) (map[string]any, bool) {
	m := todoCompleteRe.FindStringSubmatchIndex(said)
	if m == nil {
		return nil, false
	}
	if i := todoCompleteRe.SubexpIndex("task_m2"); m[2*i] >= 0 {
		// The group may not take in a " not " (a lookahead at each of its characters).
		for _, loc := range notWordRe.FindAllStringIndex(said, -1) {
			if loc[0] >= m[2*i] && loc[0] < m[2*i+1] {
				return nil, false
			}
		}
	}
	sub := make([]string, len(m)/2)
	for j := range sub {
		if m[2*j] >= 0 {
			sub[j] = said[m[2*j]:m[2*j+1]]
		}
	}
	return taskAction(todoCompleteRe, "complete", "task_m1", "task_m2", "task_c")(sub)
}

var todoPatterns = []pattern{
	regexPattern("todo_list.add", "Bypass LLM for 'add X to the to-do list'", "add call the dentist to my to-do list",
		todoAddRe, taskAction(todoAddRe, "add", "task")),
	regexPattern("todo_list.read", "Bypass LLM for 'what's on the to-do list'", "what's on my to-do list", todoReadRe, action("read")),
	regexPattern("todo_list.uncomplete", "Bypass LLM for 'mark X not done on the to-do list'", "mark the laundry as not done on my to-do list",
		todoUncompleteRe, taskAction(todoUncompleteRe, "uncomplete", "task_m1", "task_m2", "task_u")),
	{id: "todo_list.complete", description: "Bypass LLM for 'mark X done on the to-do list'", example: "mark the laundry done on my to-do list",
		match: completeMatch},
	regexPattern("todo_list.remove", "Bypass LLM for 'remove X from the to-do list'", "remove fold the laundry from my to-do list",
		todoRemoveRe, taskAction(todoRemoveRe, "remove", "task")),
	regexPattern("todo_list.clear", "Bypass LLM for 'clear the to-do list'", "clear my to-do list", todoClearRe, action("clear")),
}

func (t *Todo) Direct(ctx context.Context, said string) (map[string]any, bool) {
	return directBy(ctx, t.Store, todoName, todoPatterns, said, func(a map[string]any) map[string]any {
		out, _ := t.Call(ctx, todoName, a, node.Request{UserID: node.Speaker(ctx)})
		return out
	})
}

func (t *Todo) FastPaths(command string) []nodesettings.FastPath {
	if command != todoName {
		return nil
	}
	return patternList(todoPatterns)
}

// TodoMatch is the arguments Direct would run (for comparing with N:'s pre_route).
func TodoMatch(said string) (Match, bool) {
	for _, p := range todoPatterns {
		if args, ok := p.match(said); ok {
			return Match{todoName, args}, true
		}
	}
	return Match{}, false
}

// --- the app's Stored data browser ---

func (t *Todo) DataCommands() []nodesettings.DataCommand {
	return []nodesettings.DataCommand{browserDef(todoName)}
}

// Summaries is display_summary: "✓ task" and Done once completed.
func (t *Todo) Summaries(_ context.Context, _ string, records []nodesettings.Object) ([]map[string]any, error) {
	out := make([]map[string]any, len(records))
	for i, o := range records {
		r := rec{obj: o}
		task := cmpOrStr(r.str("task"), "Task")
		s := map[string]any{"title": task, "subtitle": nil, "icon": "checkbox-blank-circle-outline"}
		if r.truthy("completed") {
			s = map[string]any{"title": "✓ " + task, "subtitle": "Done", "icon": "checkbox-marked-circle-outline"}
		}
		out[i] = s
	}
	return out, nil
}

func (t *Todo) Create(context.Context, string, nodesettings.Object, json.Number) (string, json.RawMessage, error) {
	return "", nil, noCreate(todoName)
}
