package builtins

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
)

// ExportShopping is export_shopping_list (commands/export_shopping_list_command.py): the open
// shopping list sent to the speaker's phone as an interactive list (quantity steppers, and for
// Walmart each row gated on its item ID with Find ID / View pickers), and the export_selected
// callback, which builds the Walmart cart link or the copyable notes and ends the trip:
// exported one-offs leave the list, exported regulars come back unchecked. Walmart IDs live in
// the "walmart_items" records, which the app's Stored data browser edits.
type ExportShopping struct {
	Store *nodesettings.Store
	CC    CC
	Log   *slog.Logger
	Now   func() time.Time // nil: time.Now
}

const (
	exportShoppingName = "export_shopping_list"
	walmartItems       = "walmart_items"
	exportSelected     = "export_selected"
	walmartIDPattern   = `/ip/(?:[^/]+/)?(\d{5,})`
)

var exportProviders = []string{"notes", "walmart"} // sorted, as the secret's enum_values

func (e *ExportShopping) Available(context.Context) []node.ToolDef {
	return []node.ToolDef{def(exportShoppingName)}
}

func (e *ExportShopping) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *ExportShopping) log() *slog.Logger {
	if e.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return e.Log
}

func isProvider(p any) bool { return p == "walmart" || p == "notes" }

// provider is _get_provider_name: the EXPORT_PROVIDER setting, else walmart.
func (e *ExportShopping) provider() string {
	p := strings.ToLower(strings.TrimSpace(e.Store.Secret("EXPORT_PROVIDER", "integration", nil)))
	if isProvider(p) {
		return p
	}
	return "walmart"
}

// clampQuantity is _clamp_quantity: an int from 1 to 99, 1 when it isn't a number.
func clampQuantity(v any) int {
	n, err := pyInt(v)
	if err != nil || v == nil {
		return 1
	}
	return max(1, min(n, 99))
}

type shopEntry struct {
	key, item, walmartID string
	quantity             int
}

// providerFromUtterance is _provider_from_utterance: a destination named out loud.
func providerFromUtterance(said string) string {
	s := strings.ToLower(said)
	switch {
	case strings.Contains(s, "note"):
		return "notes"
	case strings.Contains(s, "walmart"), strings.Contains(s, "store"):
		return "walmart"
	}
	return ""
}

func (e *ExportShopping) Call(ctx context.Context, name string, args map[string]any, req node.Request) (map[string]any, bool) {
	if name != exportShoppingName {
		return nil, false
	}
	// post_process_tool_call: the destination comes from what was said, never the model.
	in := args
	args = map[string]any{}
	for k, v := range in {
		if k != "provider" {
			args[k] = v
		}
	}
	if p := providerFromUtterance(req.VoiceCommand); p != "" {
		args["provider"] = p
	}
	return e.run(ctx, args, req), true
}

// run is the command's run, on arguments already post-processed (the fast path's keep their
// provider).
func (e *ExportShopping) run(ctx context.Context, args map[string]any, req node.Request) map[string]any {
	if out := validated(def(exportShoppingName), args); out != nil {
		return out
	}
	provider, _ := args["provider"].(string)
	if !isProvider(provider) {
		provider = e.provider()
	}
	if req.UserID == nil {
		return failure("Unknown speaker — cannot target the export push.", map[string]any{"error": "unknown_speaker",
			"message": "I'm not sure who's asking — I need to know whose phone to send the list to. Try training your voice in the app."})
	}
	var regulars, oneOffs []shopEntry
	for _, r := range e.Store.Records(shoppingName, e.now()) {
		x := rec{r.Key, r.Data}
		if !x.truthy("item") || x.truthy("checked") {
			continue
		}
		item := pyStr(x.obj.Value("item"))
		en := shopEntry{key: r.Key, item: item, quantity: clampQuantity(cmpOrOne(x.obj.Value("quantity")))}
		if m, ok := e.Store.Data(walmartItems, r.Key, e.now()); ok {
			en.walmartID = strings.TrimSpace(rec{obj: m}.str("walmart_item_id"))
		} else if provider == "walmart" { // a row to fill the ID into from the app
			var seed nodesettings.Object
			setJSON(&seed, "item", item)
			setJSON(&seed, "walmart_item_id", "")
			_ = e.Store.SaveData(walmartItems, r.Key, seed, nil)
		}
		if x.truthy("regular") {
			regulars = append(regulars, en)
		} else {
			oneOffs = append(oneOffs, en)
		}
	}
	all := append(append([]shopEntry{}, regulars...), oneOffs...)
	if len(all) == 0 {
		return finish(map[string]any{"message": "Your shopping list is empty — nothing to export.", "item_count": 0}, true, false)
	}
	n, unmatched := len(all), 0
	for _, en := range all {
		if en.walmartID == "" {
			unmatched++
		}
	}
	items := func(k int) string {
		if k == 1 {
			return "item"
		}
		return "items"
	}
	needs := func(k int) string {
		if k == 1 {
			return "needs"
		}
		return "need"
	}
	summary := strconv.Itoa(n) + " " + items(n) + " ready to send to your cart."
	if provider == "notes" {
		summary = strconv.Itoa(n) + " " + items(n) + " ready to copy to your notes."
	} else if unmatched > 0 {
		summary += " " + strconv.Itoa(unmatched) + " still " + needs(unmatched) + " a Walmart ID."
	}
	var body []string
	for _, en := range all {
		line := "- " + en.item
		if en.quantity > 1 {
			line += " × " + strconv.Itoa(en.quantity)
		}
		if provider == "walmart" && en.walmartID == "" {
			line += " _(needs Walmart ID)_"
		}
		body = append(body, line)
	}
	item := map[string]any{"title": "Shopping list ready — " + strconv.Itoa(n) + " " + items(n), "summary": summary,
		"body": strings.Join(body, "\n"), "category": "interactive_list", "metadata": shopChecklist(regulars, oneOffs, provider),
		"user_id": *req.UserID, "create_push_notification": true, "target_type": "user"}
	if res := sdkhost.PostInbox(ctx, e.CC(), item, exportShoppingName, e.log()); res != "ok" {
		return exportFailure(res, "shopping list")
	}
	msg := "I sent your shopping list to your phone — " + strconv.Itoa(n) + " " + items(n) + "."
	if provider == "walmart" {
		msg = "I sent your shopping list to your phone — " + strconv.Itoa(n) + " " + items(n) + " ready"
		if unmatched > 0 {
			msg += ", " + strconv.Itoa(unmatched) + " still " + needs(unmatched) + " a Walmart match."
		} else {
			msg += "."
		}
	}
	return finish(map[string]any{"message": msg, "item_count": n, "unmatched_count": unmatched}, true, false)
}

// cmpOrOne is `v or 1`.
func cmpOrOne(v any) any {
	if pyTruthy(v) {
		return v
	}
	return 1
}

// shopChecklist is _build_metadata: Regulars then One-offs, 100 rows at most, each a
// checkbox_stepper; a Walmart row needs its ID and has Find ID / View.
func shopChecklist(regulars, oneOffs []shopEntry, provider string) map[string]any {
	walmart := provider == "walmart"
	remaining := maxInteractiveRows
	var sections []any
	for _, s := range []struct {
		title   string
		entries []shopEntry
	}{{"Regulars", regulars}, {"One-offs", oneOffs}} {
		take := s.entries[:min(len(s.entries), remaining)]
		remaining -= len(take)
		if len(take) == 0 {
			continue
		}
		rows := []any{}
		for _, en := range take {
			row := map[string]any{"key": en.key, "label": truncRunes(en.item, 120), "control": "checkbox_stepper",
				"default": map[string]any{"selected": true, "quantity": en.quantity}}
			if walmart {
				row["disabled_caption"] = "No Walmart match"
				row["requires_record_field"] = map[string]any{"command_name": exportShoppingName, "field": "walmart_item_id", "field_label": "ID"}
				save := map[string]any{"command_name": exportShoppingName, "field": "walmart_item_id"}
				row["row_actions"] = []any{
					map[string]any{"label": "Find ID", "type": "webview_pick", "start_url": "https://www.walmart.com/search?q={label}",
						"pattern": walmartIDPattern, "save": save},
					map[string]any{"label": "View", "type": "webview_pick", "start_url": "https://www.walmart.com/ip/{value}",
						"pattern": walmartIDPattern, "save": save},
				}
			}
			rows = append(rows, row)
		}
		sections = append(sections, map[string]any{"title": s.title, "rows": rows})
	}
	if len(sections) == 0 {
		sections = []any{map[string]any{"rows": []any{}}}
	}
	return map[string]any{"type": "interactive_list", "version": 1, "command_name": exportShoppingName,
		"empty_text": "Nothing to export", "context": map[string]any{"provider": provider}, "sections": sections,
		"actions": []any{map[string]any{"label": "Export {n} items", "callback": exportSelected, "style": "primary"}}}
}

// Callback is the export_selected @callback: the selected rows to the provider, then the trip
// ends for what was exported.
func (e *ExportShopping) Callback(params map[string]any) (map[string]any, bool) {
	if params["command_name"] != exportShoppingName {
		return nil, false
	}
	if enabled, ok := params["enabled"].(bool); ok && !enabled {
		return map[string]any{"success": false, "error": "Command '" + exportShoppingName + "' not found on this node"}, true
	}
	if params["callback_name"] != exportSelected {
		return map[string]any{"success": false, "error": "Callback '" + pyStr(params["callback_name"]) + "' not registered on '" + exportShoppingName + "'"}, true
	}
	fail := func(details, msg string) (map[string]any, bool) {
		return map[string]any{"success": false, "error": details, "context_data": map[string]any{"message": msg}}, true
	}
	data, _ := params["data"].(map[string]any)
	ctxData, _ := data["context"].(map[string]any)
	p := ctxData["provider"]
	if !pyTruthy(p) {
		p = data["provider"]
	}
	provider := ""
	if pyTruthy(p) {
		provider = strings.ToLower(strings.TrimSpace(pyStr(p)))
	}
	if !isProvider(provider) {
		provider = e.provider()
	}
	var selected []shopEntry
	if sel, ok := data["selected"].([]any); ok {
		for _, s := range sel {
			m, ok := s.(map[string]any)
			if !ok || !pyTruthy(m["key"]) {
				continue
			}
			if key := strings.TrimSpace(pyStr(m["key"])); key != "" {
				selected = append(selected, shopEntry{key: key, quantity: clampQuantity(m["quantity"])})
			}
		}
	}
	if len(selected) == 0 {
		return fail("export_selected called with no valid selected entries", "Nothing was selected to export.")
	}
	now := e.now()
	for i, en := range selected {
		sr, sok := e.Store.Data(shoppingName, en.key, now)
		mr, mok := e.Store.Data(walmartItems, en.key, now)
		item := ""
		if sok {
			item = rec{obj: sr}.str("item")
		}
		if item == "" && mok {
			item = rec{obj: mr}.str("item")
		}
		selected[i].item = cmpOrStr(item, en.key)
		if mok {
			selected[i].walmartID = strings.TrimSpace(rec{obj: mr}.str("walmart_item_id"))
		}
	}
	exportable := selected
	result := map[string]any{}
	if provider == "walmart" {
		exportable = nil
		var parts []string
		for _, en := range selected {
			if en.walmartID != "" {
				exportable = append(exportable, en)
				parts = append(parts, en.walmartID+"|"+strconv.Itoa(en.quantity))
			}
		}
		if len(exportable) == 0 {
			return fail("export_selected: zero resolvable walmart_item_ids", "None of the selected items have a Walmart ID yet.")
		}
		result["url"] = "https://affil.walmart.com/cart/addToCart?items=" + strings.Join(parts, ",")
	} else {
		lines := []string{"Shopping list:"}
		for _, en := range exportable {
			l := "- " + en.item
			if en.quantity > 1 {
				l += " x" + strconv.Itoa(en.quantity)
			}
			lines = append(lines, l)
		}
		result["text"] = strings.Join(lines, "\n")
	}
	var exported []string
	reset := 0
	for _, en := range exportable {
		exported = append(exported, en.item)
		o, ok := e.Store.Data(shoppingName, en.key, now)
		if !ok {
			continue
		}
		if (rec{obj: o}).truthy("regular") {
			o = o.Without(func(k string) bool { return k == "_data_key" || k == "_expires_at" || k == "data_key" })
			setJSON(&o, "checked", false)
			setJSON(&o, "checked_at", nil)
			_ = e.Store.SaveData(shoppingName, en.key, o, nil)
			reset++
		} else {
			_, _ = e.Store.DeleteData(shoppingName, en.key)
		}
	}
	items := "items"
	if len(exported) == 1 {
		items = "item"
	}
	msg := "Exported " + strconv.Itoa(len(exported)) + " " + items + " to your Walmart cart."
	if provider == "notes" {
		msg = "Your list is ready to copy — " + strconv.Itoa(len(exported)) + " " + items + "."
	}
	if reset > 0 {
		noun := "regulars are"
		if reset == 1 {
			noun = "regular is"
		}
		msg += " Your " + strconv.Itoa(reset) + " " + noun + " back on the list."
	}
	cd := map[string]any{"detail_lines": exported, "exported": exported, "message": msg}
	for k, v := range result {
		cd[k] = v
	}
	return map[string]any{"success": true, "error": nil, "context_data": cd}, true
}

var exportShoppingRe = regexp.MustCompile(`(?i)^\s*(?:send|export)\s+(?:my\s+|the\s+|our\s+)?(?:shopping|grocery|groceries)\s+list(?:\s+to\s+(?P<dest>walmart|the\s+store|notes|my\s+notes))?\s*[?.!]*$`)

var exportShoppingPatterns = []pattern{
	regexPattern("export_shopping_list.export", "Bypass LLM for 'send/export the shopping list (to walmart/notes)'",
		"send my shopping list to walmart", exportShoppingRe, func(m []string) (map[string]any, bool) {
			dest := spacesRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(group(exportShoppingRe, m, "dest"))), " ")
			switch dest {
			case "walmart", "the store":
				return map[string]any{"provider": "walmart"}, true
			case "notes", "my notes":
				return map[string]any{"provider": "notes"}, true
			}
			return map[string]any{}, true
		}),
}

func (e *ExportShopping) Direct(ctx context.Context, said string) (map[string]any, bool) {
	// A fast path's arguments go to run as they are (N:'s pre_route skips post-processing).
	return directBy(ctx, e.Store, exportShoppingName, exportShoppingPatterns, said, func(a map[string]any) map[string]any {
		return e.run(ctx, a, node.Request{UserID: node.Speaker(ctx), VoiceCommand: said})
	})
}

func (e *ExportShopping) FastPaths(command string) []nodesettings.FastPath {
	if command != exportShoppingName {
		return nil
	}
	return patternList(exportShoppingPatterns)
}

// ExportShoppingMatch is the arguments Direct would run (for comparing with N:'s pre_route).
func ExportShoppingMatch(said string) (Match, bool) {
	for _, p := range exportShoppingPatterns {
		if args, ok := p.match(said); ok {
			return Match{exportShoppingName, args}, true
		}
	}
	return Match{}, false
}

// Secrets is the command's EXPORT_PROVIDER setting, for the app's settings.
func (e *ExportShopping) Secrets(command string) []nodesettings.SecretSpec {
	if command != exportShoppingName {
		return nil
	}
	return []nodesettings.SecretSpec{{Key: "EXPORT_PROVIDER", Scope: "integration", Description: "Where 'send my shopping list' exports to",
		ValueType: "string", FriendlyName: "Export store", EnumValues: exportProviders}}
}

// --- the walmart_items records in the app's Stored data browser ---

func (e *ExportShopping) DataCommands() []nodesettings.DataCommand {
	return []nodesettings.DataCommand{browserDef(exportShoppingName)}
}

// Summaries is display_summary: the item, and its Walmart ID or that it needs one.
func (e *ExportShopping) Summaries(_ context.Context, _ string, records []nodesettings.Object) ([]map[string]any, error) {
	out := make([]map[string]any, len(records))
	for i, o := range records {
		r := rec{obj: o}
		sub := "Needs Walmart ID"
		if id := strings.TrimSpace(r.str("walmart_item_id")); id != "" {
			sub = "Walmart ID: " + id
		}
		out[i] = map[string]any{"title": cmpOrStr(r.str("item"), "Item"), "subtitle": sub, "icon": "cart-arrow-right"}
	}
	return out, nil
}

func (e *ExportShopping) Create(context.Context, string, nodesettings.Object, json.Number) (string, json.RawMessage, error) {
	return "", nil, noCreate(exportShoppingName)
}
