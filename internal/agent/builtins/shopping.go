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

// Shopping is shopping_list (commands/shopping_list_command.py): the household's list, one
// record per item in the node's store under "shopping_list", keyed as the Python command keyed
// them, so the list a Python node or an earlier sidecar kept is the same list. Its fast paths
// ("add milk to the shopping list") run on the node.
type Shopping struct {
	Store *nodesettings.Store
	Now   func() time.Time // nil: time.Now
}

const shoppingName = "shopping_list"

func (s *Shopping) Available(context.Context) []node.ToolDef {
	return []node.ToolDef{def(shoppingName)}
}

func (s *Shopping) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Shopping) Call(_ context.Context, name string, args map[string]any, req node.Request) (map[string]any, bool) {
	if name != shoppingName {
		return nil, false
	}
	args = shoppingPostProcess(args)
	if out := validated(def(name), args); out != nil {
		return out, true
	}
	action, _ := args["action"].(string)
	if _, ok := args["action"]; !ok {
		action = "read"
	}
	var items []string
	if l, ok := args["items"].([]any); ok {
		for _, i := range l {
			if t := strings.TrimSpace(pyStr(i)); t != "" {
				items = append(items, t)
			}
		}
	}
	if (action == "add" || action == "check_off" || action == "remove") && len(items) == 0 {
		return finish(map[string]any{"error": "Which items?", "message": "Which items?"}, false, true), true
	}
	switch action {
	case "add":
		return s.add(items, req.UserID, truthy(args["regular"])), true
	case "read":
		return s.read(), true
	case "check_off":
		return s.checkOff(items), true
	case "remove":
		return s.remove(items), true
	case "clear":
		scope, _ := args["scope"].(string)
		return s.clear(cmpOrStr(scope, "trip")), true
	}
	return failure("Unknown shopping list action: "+action,
		map[string]any{"message": "I don't know the shopping list action '" + action + "'."}), true
}

func cmpOrStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// shoppingPostProcess is post_process_tool_call: a string of items split, a list kept as the
// model segmented it, the action defaulted, a string "regular" read, an unknown scope dropped.
func shoppingPostProcess(in map[string]any) map[string]any {
	args := make(map[string]any, len(in))
	for k, v := range in {
		args[k] = v
	}
	switch items := args["items"].(type) {
	case string:
		args["items"] = toAny(splitItems(items))
	case []any:
		out := []any{}
		for _, e := range items {
			if t := strings.TrimSpace(pyStr(e)); t != "" {
				out = append(out, t)
			}
		}
		args["items"] = out
	}
	if a, _ := args["action"].(string); a == "" && !truthy(args["action"]) {
		if l, _ := args["items"].([]any); len(l) > 0 {
			args["action"] = "add"
		} else {
			args["action"] = "read"
		}
	}
	if r, ok := args["regular"].(string); ok {
		r = strings.ToLower(strings.TrimSpace(r))
		args["regular"] = r == "true" || r == "yes" || r == "1"
	}
	if sc, ok := args["scope"]; ok && sc != nil && sc != "trip" && sc != "everything" {
		delete(args, "scope")
	}
	return args
}

func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

var (
	leadingFillerRe = regexp.MustCompile(`(?i)^(?:some|a\s+few|a\s+couple(?:\s+of)?|a\s+thing\s+of|a|an|the)\s+`)
	itemSplitRe     = regexp.MustCompile(`(?i)\s*,\s*(?:and\s+)?|\s+and\s+`)
	itemKeyRe       = regexp.MustCompile(`[^a-z0-9]+`)
)

// splitItems is _split_items: "milk, eggs, and butter" is three items, each without its "some".
func splitItems(text string) []string {
	var out []string
	for _, part := range itemSplitRe.Split(text, -1) {
		if c := strings.TrimSpace(leadingFillerRe.ReplaceAllString(strings.TrimSpace(part), "")); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// itemKey is _item_key: "Almond Butter" is stored as almond_butter.
func itemKey(name string) string {
	return strings.Trim(itemKeyRe.ReplaceAllString(strings.ToLower(name), "_"), "_")
}

// spokenJoin is _spoken_join: "a", "a and b", "a, b, and c".
func spokenJoin(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
}

// pyISO is Python's datetime.now(timezone.utc).isoformat().
func pyISO(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond()/1000 != 0 {
		return t.Format("2006-01-02T15:04:05.000000+00:00")
	}
	return t.Format("2006-01-02T15:04:05+00:00")
}

// rec is a stored record's values (numbers as json.Number).
type rec struct {
	key string
	obj nodesettings.Object
}

// str is str(record.get(k) or ""): "" for a missing or empty value.
func (r rec) str(k string) string {
	if !r.truthy(k) {
		return ""
	}
	return pyStr(r.obj.Value(k))
}
func (r rec) truthy(k string) bool {
	switch v := r.obj.Value(k).(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case json.Number:
		f, _ := v.Float64()
		return f != 0
	}
	return true
}

// quantity is int(record.get("quantity") or 1).
func (r rec) quantity() int {
	n, err := pyInt(r.obj.Value("quantity"))
	if err != nil || n == 0 {
		return 1
	}
	return n
}

func setJSON(o *nodesettings.Object, k string, v any) {
	b, _ := json.Marshal(v)
	o.Set(k, b)
}

func capitalize(s string) string { // Python's str.capitalize
	if s == "" {
		return s
	}
	r := []rune(strings.ToLower(s))
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

func (s *Shopping) add(items []string, by *int64, regular bool) map[string]any {
	now := pyISO(s.now())
	var added, promoted, already []string
	for _, item := range items {
		key := itemKey(item)
		if key == "" {
			continue
		}
		existing, ok := s.Store.Data(shoppingName, key, s.now())
		r := rec{key, existing}
		if ok && !r.truthy("checked") {
			if regular && !r.truthy("regular") {
				setJSON(&existing, "regular", true)
				_ = s.Store.SaveData(shoppingName, key, existing, nil)
				promoted = append(promoted, cmpOrStr(r.str("item"), item))
			} else {
				already = append(already, cmpOrStr(r.str("item"), item))
			}
			continue
		}
		wasRegular, wasQuantity := false, 1
		if ok {
			wasRegular, wasQuantity = r.truthy("regular"), r.quantity()
		}
		var record nodesettings.Object
		setJSON(&record, "item", item)
		setJSON(&record, "checked", false)
		setJSON(&record, "regular", regular || wasRegular)
		setJSON(&record, "quantity", wasQuantity)
		setJSON(&record, "added_at", now)
		setJSON(&record, "checked_at", nil)
		if by != nil {
			setJSON(&record, "added_by", *by)
		}
		_ = s.Store.SaveData(shoppingName, key, record, nil)
		added = append(added, item)
	}
	var sentences []string
	if len(added) > 0 {
		suffix := ""
		if regular {
			suffix = " as a regular"
			if len(added) > 1 {
				suffix = " as regulars"
			}
		}
		sentences = append(sentences, "Added "+spokenJoin(added)+" to your shopping list"+suffix+".")
	}
	if len(promoted) > 0 {
		noun := "regulars"
		if len(promoted) == 1 {
			noun = "a regular"
		}
		sentences = append(sentences, "Made "+spokenJoin(promoted)+" "+noun+".")
	}
	if len(already) > 0 {
		verb := "are"
		if len(already) == 1 {
			verb = "is"
		}
		sentences = append(sentences, capitalize(spokenJoin(already))+" "+verb+" already on it.")
	}
	if len(sentences) == 0 {
		return failure("I couldn't make out any items to add.", map[string]any{"message": "I couldn't make out any items to add."})
	}
	return finish(map[string]any{"message": strings.Join(sentences, " "), "added": nonNil(added),
		"promoted_to_regular": nonNil(promoted), "already_on_list": nonNil(already)}, true, false)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Shopping) records() []rec {
	var out []rec
	for _, r := range s.Store.Records(shoppingName, s.now()) {
		out = append(out, rec{r.Key, r.Data})
	}
	return out
}

func (s *Shopping) read() map[string]any {
	var open []rec
	for _, r := range s.records() {
		if r.truthy("item") && !r.truthy("checked") {
			open = append(open, r)
		}
	}
	slices.SortStableFunc(open, func(a, b rec) int {
		return strings.Compare(strings.ToLower(a.str("item")), strings.ToLower(b.str("item")))
	})
	var items []string
	for _, r := range open {
		name := r.str("item")
		if q := r.quantity(); q > 1 {
			name += " times " + strconv.Itoa(q)
		}
		items = append(items, name)
	}
	msg := "Your shopping list is empty."
	switch {
	case len(items) == 1:
		msg = "You have one item on your shopping list: " + items[0] + "."
	case len(items) > 1:
		msg = "You have " + strconv.Itoa(len(items)) + " items on your shopping list: " + spokenJoin(items) + "."
	}
	return finish(map[string]any{"message": msg, "items": nonNil(items)}, true, false)
}

// find is _find_record: the item's key, else the first record (in key order) whose name and
// the item contain one another.
func (s *Shopping) find(item string) (rec, bool) {
	key := itemKey(item)
	if d, ok := s.Store.Data(shoppingName, key, s.now()); ok {
		return rec{key, d}, true
	}
	needle := strings.ToLower(strings.TrimSpace(item))
	if needle == "" {
		return rec{}, false
	}
	all := s.records()
	slices.SortStableFunc(all, func(a, b rec) int { return strings.Compare(a.key, b.key) })
	for _, r := range all {
		name := strings.ToLower(r.str("item"))
		if strings.Contains(name, needle) || strings.Contains(needle, name) {
			return r, true
		}
	}
	return rec{}, false
}

func (s *Shopping) checkOff(items []string) map[string]any {
	now := pyISO(s.now())
	var checked, missing []string
	for _, item := range items {
		r, ok := s.find(item)
		if !ok {
			missing = append(missing, item)
			continue
		}
		setJSON(&r.obj, "checked", true)
		setJSON(&r.obj, "checked_at", now)
		_ = s.Store.SaveData(shoppingName, r.key, r.obj, nil)
		checked = append(checked, cmpOrStr(r.str("item"), item))
	}
	var msg string
	switch {
	case len(checked) > 0 && len(missing) > 0:
		msg = "Checked off " + spokenJoin(checked) + ". I couldn't find " + spokenJoin(missing) + " on the list."
	case len(checked) > 0:
		msg = "Checked off " + spokenJoin(checked) + "."
	default:
		msg = "I couldn't find " + spokenJoin(missing) + " on your shopping list."
	}
	return finish(map[string]any{"message": msg, "checked": nonNil(checked), "not_found": nonNil(missing)}, true, false)
}

func (s *Shopping) remove(items []string) map[string]any {
	var removed, missing []string
	for _, item := range items {
		r, ok := s.find(item)
		if !ok {
			missing = append(missing, item)
			continue
		}
		if gone, _ := s.Store.DeleteData(shoppingName, r.key); gone {
			removed = append(removed, cmpOrStr(r.str("item"), item))
		} else {
			missing = append(missing, item)
		}
	}
	var msg string
	switch {
	case len(removed) > 0 && len(missing) > 0:
		msg = "Removed " + spokenJoin(removed) + " from your shopping list. I couldn't find " + spokenJoin(missing) + "."
	case len(removed) > 0:
		msg = "Removed " + spokenJoin(removed) + " from your shopping list."
	default:
		msg = "I couldn't find " + spokenJoin(missing) + " on your shopping list."
	}
	return finish(map[string]any{"message": msg, "removed": nonNil(removed), "not_found": nonNil(missing)}, true, false)
}

func (s *Shopping) clear(scope string) map[string]any {
	if scope == "everything" {
		n, _ := s.Store.DeleteAllData(shoppingName)
		msg := "Your shopping list is already empty."
		if n > 0 {
			noun := strconv.Itoa(n) + " items"
			if n == 1 {
				noun = "one item"
			}
			msg = "Cleared your entire shopping list — removed " + noun + ", including regulars."
		}
		return finish(map[string]any{"message": msg, "removed_count": n, "scope": scope}, true, false)
	}
	// A trip: one-offs go, regulars come back unchecked for the next list.
	removed, regulars := 0, 0
	for _, r := range s.records() {
		if r.truthy("regular") {
			regulars++
			if r.truthy("checked") {
				setJSON(&r.obj, "checked", false)
				setJSON(&r.obj, "checked_at", nil)
				_ = s.Store.SaveData(shoppingName, r.key, r.obj, nil)
			}
		} else {
			_, _ = s.Store.DeleteData(shoppingName, r.key)
			removed++
		}
	}
	items := func(n int) string {
		if n == 1 {
			return "item"
		}
		return "items"
	}
	regNoun := "regulars are"
	if regulars == 1 {
		regNoun = "regular is"
	}
	var msg string
	switch {
	case removed == 0 && regulars == 0:
		msg = "Your shopping list is already empty."
	case regulars > 0 && removed > 0:
		msg = "Cleared your shopping list — removed " + strconv.Itoa(removed) + " " + items(removed) + ". Your " +
			strconv.Itoa(regulars) + " " + regNoun + " back on it."
	case regulars > 0:
		msg = "Cleared your shopping list — your " + strconv.Itoa(regulars) + " " + regNoun + " back on it."
	default:
		msg = "Cleared your shopping list — removed " + strconv.Itoa(removed) + " " + items(removed) + "."
	}
	return finish(map[string]any{"message": msg, "removed_count": removed, "regulars_reset": regulars, "scope": scope}, true, false)
}

// --- fast paths ---

const listNoun = `(?:my\s+|the\s+|our\s+)?(?:shopping|grocery|groceries)\s+list`

var (
	shopAddRe      = regexp.MustCompile(`(?i)^\s*(?:add|put)\s+(?P<items>.+?)\s+(?:to|on(?:to)?)\s+` + listNoun + `\s*[?.!]*$`)
	shopReadRe     = regexp.MustCompile(`(?i)^\s*(?:what(?:'s|\s+is)\s+on|read(?:\s+me)?|show(?:\s+me)?|list)\s+` + listNoun + `\s*[?.!]*$`)
	shopCheckOffRe = regexp.MustCompile(`(?i)^\s*(?:check(?:\s+off)?|cross(?:\s+off|\s+out)?)\s+(?P<items>.+?)\s+(?:off\s+)?(?:(?:of|from|on)\s+)?` + listNoun + `\s*[?.!]*$`)
	shopRemoveRe   = regexp.MustCompile(`(?i)^\s*(?:remove|take|delete)\s+(?P<items>.+?)\s+(?:off|from)\s+(?:of\s+)?` + listNoun + `\s*[?.!]*$`)
	shopClearRe    = regexp.MustCompile(`(?i)^\s*(?:clear|empty|wipe)\s+` + listNoun + `\s*[?.!]*$`)
)

func itemsAction(re *regexp.Regexp, action string) func([]string) (map[string]any, bool) {
	return func(m []string) (map[string]any, bool) {
		items := splitItems(group(re, m, "items"))
		return map[string]any{"action": action, "items": toAny(items)}, len(items) > 0
	}
}

// asRegularRe is a trailing "as a regular" (or staple) in what to add. N:'s fast path kept it
// in the item's name ("soda as a regular"), not a regular, so a trip clear removed it.
var asRegularRe = regexp.MustCompile(`(?i)\s+as\s+(?:a\s+)?(?:regular|staple)s?$`)

// addAction is the add fast path's handler: the items, and regular when they end "as a regular".
func addAction(m []string) (map[string]any, bool) {
	phrase := group(shopAddRe, m, "items")
	regular := asRegularRe.MatchString(phrase)
	items := splitItems(asRegularRe.ReplaceAllString(phrase, ""))
	args := map[string]any{"action": "add", "items": toAny(items)}
	if regular {
		args["regular"] = true
	}
	return args, len(items) > 0
}

var shoppingPatterns = []pattern{
	regexPattern("shopping_list.add", "Bypass LLM for 'add X to the shopping list'", "add milk to the shopping list", shopAddRe, addAction),
	regexPattern("shopping_list.read", "Bypass LLM for 'what's on the shopping list'", "what's on the shopping list", shopReadRe, action("read")),
	regexPattern("shopping_list.check_off", "Bypass LLM for 'check X off the shopping list'", "check milk off the shopping list",
		shopCheckOffRe, itemsAction(shopCheckOffRe, "check_off")),
	regexPattern("shopping_list.remove", "Bypass LLM for 'remove X from the shopping list'", "remove milk from the shopping list",
		shopRemoveRe, itemsAction(shopRemoveRe, "remove")),
	regexPattern("shopping_list.clear", "Bypass LLM for 'clear the shopping list'", "clear the shopping list", shopClearRe, action("clear")),
}

func (s *Shopping) Direct(ctx context.Context, said string) (map[string]any, bool) {
	return directBy(ctx, s.Store, shoppingName, shoppingPatterns, said, func(a map[string]any) map[string]any {
		out, _ := s.Call(ctx, shoppingName, a, node.Request{UserID: node.Speaker(ctx)})
		return out
	})
}

func (s *Shopping) FastPaths(command string) []nodesettings.FastPath {
	if command != shoppingName {
		return nil
	}
	return patternList(shoppingPatterns)
}

// ShoppingMatch is the arguments Direct would run (for comparing with N:'s pre_route).
func ShoppingMatch(said string) (Match, bool) {
	for _, p := range shoppingPatterns {
		if args, ok := p.match(said); ok {
			return Match{shoppingName, args}, true
		}
	}
	return Match{}, false
}

// --- the app's Stored data browser ---

func (s *Shopping) DataCommands() []nodesettings.DataCommand {
	return []nodesettings.DataCommand{browserDef(shoppingName)}
}

// Summaries is display_summary: "✓ milk" once checked, with Checked off, Regular and × N.
func (s *Shopping) Summaries(_ context.Context, _ string, records []nodesettings.Object) ([]map[string]any, error) {
	out := make([]map[string]any, len(records))
	for i, o := range records {
		r := rec{obj: o}
		item := cmpOrStr(r.str("item"), "Item")
		title := item
		var parts []string
		if r.truthy("checked") {
			title = "✓ " + item
			parts = append(parts, "Checked off")
		}
		if r.truthy("regular") {
			parts = append(parts, "Regular")
		}
		if q := r.quantity(); q > 1 {
			parts = append(parts, "× "+strconv.Itoa(q))
		}
		var sub any
		if len(parts) > 0 {
			sub = strings.Join(parts, " • ")
		}
		out[i] = map[string]any{"title": title, "subtitle": sub, "icon": "cart"}
	}
	return out, nil
}

func (s *Shopping) Create(context.Context, string, nodesettings.Object, json.Number) (string, json.RawMessage, error) {
	return "", nil, noCreate(shoppingName)
}
