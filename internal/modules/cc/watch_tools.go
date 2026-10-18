package cc

import (
	"context"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/watches"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// create_watch / list_watches / cancel_watch (docs/autonomy/PLAN.md §2, A8): offered with
// watches.enabled on and a known speaker (prompts.ServerToolAllowed), and acting only for that
// speaker: a member creates, sees and cancels their own watches.

const defaultWatchEveryMinutes = 60

// watchToolsDisabled is the refusal while the household has watches off.
func watchToolsDisabled() *pyjson.Object {
	return servertools.Obj("error", "disabled", "message",
		"Watches are turned off for this household. A household admin can turn them on.")
}

// watchToolPrecheck is the shared start of every watch tool: the speaker check, then the switch.
func (m *Module) watchToolPrecheck(ctx context.Context, turn servertools.Turn) *pyjson.Object {
	if e := toolSpeakerError(turn); e != nil {
		return e
	}
	if !m.watchesEnabled(ctx, turn.HouseholdID) {
		return watchToolsDisabled()
	}
	return nil
}

// watchToolDefinition builds a definition in the generated ones' shape.
func watchToolDefinition(name, description string, properties *pyjson.Object, required ...string) *pyjson.Object {
	req := make([]any, len(required))
	for i, r := range required {
		req[i] = r
	}
	return servertools.Obj("type", "function", "function", servertools.Obj("name", name, "description", description,
		"parameters", servertools.Obj("type", "object", "properties", properties, "required", req)))
}

func strProp(description string) *pyjson.Object {
	return servertools.Obj("type", "string", "description", description)
}

func enumProp(description string, values ...string) *pyjson.Object {
	enum := make([]any, len(values))
	for i, v := range values {
		enum[i] = v
	}
	return servertools.Obj("type", "string", "enum", enum, "description", description)
}

// argText reads a string argument, accepting a number the model sent unquoted ("value": 100).
func argText(call servertools.Call, name string) string {
	v, _ := call.Arg(name)
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case *big.Int:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

// argInt reads an integer argument (0 when absent or not a whole number).
func argInt(call servertools.Call, name string) int {
	v, _ := call.Arg(name)
	switch x := v.(type) {
	case *big.Int:
		if x.IsInt64() {
			return int(x.Int64())
		}
	case float64:
		if x == float64(int(x)) {
			return int(x)
		}
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	}
	return 0
}

func argBool(call servertools.Call, name string) bool {
	v, _ := call.Arg(name)
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(strings.TrimSpace(x), "true")
	}
	return false
}

func watchTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// speakerLiveWatches is the speaker's active and paused watches, in creation order.
func (m *Module) speakerLiveWatches(ctx context.Context, turn servertools.Turn) ([]watches.Watch, error) {
	uid := turn.Speaker.UserID
	return m.watches.List(ctx, turn.HouseholdID, &uid, watches.StateActive, watches.StatePaused)
}

type createWatchTool struct{ m *Module }

func (*createWatchTool) Name() string { return "create_watch" }

func (*createWatchTool) Definition() *pyjson.Object {
	kinds := make([]string, len(signalCatalog))
	labels := make([]string, len(signalCatalog))
	for i, k := range signalCatalog {
		kinds[i] = k.kind
		labels[i] = k.kind + " = " + k.label
	}
	return watchToolDefinition("create_watch",
		"Watch a web page, a web search or a household signal and tell the user when it changes or crosses a value. "+
			"Use when the user asks to keep an eye on, watch or track something and be told later (e.g. 'tell me when "+
			"this drops below $100', 'let me know if it comes back in stock').",
		servertools.Obj(
			"title", strProp("What to watch, in the user's words"),
			"source", enumProp("web for a page or a search, signal for something that happens in the household",
				watches.SourceWeb, watches.SourceSignal),
			"url", strProp("For web: the page to watch"),
			"query", strProp("For web: a search to watch instead of a page"),
			"look_for", strProp("For web: the value to read, e.g. 'the price'"),
			"signal_kind", enumProp("For signal: which signal ("+strings.Join(labels, ", ")+")", kinds...),
			"field", strProp("For signal: the detail to test, e.g. 'state' (presence signals: 'away' or 'home'); "+
				"omit to test the whole signal"),
			"op", enumProp("lt: below a number, gt: above a number, changed: any change, equals, contains, "+
				"absent_for: a signal's field is not value for for_minutes",
				watches.OpLT, watches.OpGT, watches.OpChanged, watches.OpEquals, watches.OpContains, watches.OpAbsentFor),
			"value", strProp("The number or text to compare with, e.g. '100' or 'in stock'"),
			"for_minutes", servertools.Obj("type", "integer", "description", "For absent_for: how many minutes"),
			"every_minutes", servertools.Obj("type", "integer", "description",
				"For web: how often to check, in minutes (default 60)"),
			"repeat", servertools.Obj("type", "boolean", "description",
				"Keep watching after the first alert (default: stop after one)"),
		),
		"title", "source", "op")
}

// Execute stores the speaker's watch after the household's limits, and arms a web watch's first
// check right away. Signal watches are driven by the bus (watches_signal.go).
func (t *createWatchTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	m := t.m
	if e := m.watchToolPrecheck(ctx, turn); e != nil {
		return e, nil
	}
	hh := turn.HouseholdID
	source := argText(call, "source")
	if source == watches.SourceWeb && !m.householdBool(ctx, settingWebScrapingExternal, hh) {
		return servertools.Obj("error", "web_fetch_off", "message",
			"I can't watch web pages until a household admin allows outside web fetches."), nil
	}
	sc := settings.Scope{HouseholdID: hh}
	live, err := m.watches.CountLive(ctx, hh)
	if err != nil {
		return nil, err
	}
	if limit := int(m.settings.Int(ctx, settingWatchesMaxActive, sc)); live >= limit {
		return servertools.Obj("error", "too_many", "message",
			"You already have "+strconv.Itoa(live)+" watches. Cancel one first."), nil
	}

	uid := turn.Speaker.UserID
	w := watches.Watch{HouseholdID: hh, UserID: &uid, Title: argText(call, "title"), SourceKind: source,
		FirePolicy: watches.FireOnce,
		Condition:  watches.Condition{Op: argText(call, "op"), Field: argText(call, "field"), Value: argText(call, "value")}}
	if argBool(call, "repeat") {
		w.FirePolicy = watches.FireEveryChange
	}
	if n := argInt(call, "for_minutes"); n > 0 {
		w.Condition.For = strconv.Itoa(n) + "m"
	}
	switch source {
	case watches.SourceWeb:
		w.Source = watches.Source{URL: argText(call, "url"), Query: argText(call, "query"), Extract: argText(call, "look_for")}
		every := argInt(call, "every_minutes")
		if every <= 0 {
			every = defaultWatchEveryMinutes
		}
		every = max(every, int(m.settings.Int(ctx, settingWatchesMinCadence, sc)))
		w.CadenceSeconds = every * 60
	case watches.SourceSignal:
		kind := argText(call, "signal_kind")
		k, ok := catalogKind(kind)
		if kind != "" && !ok {
			return servertools.Obj("error", "invalid", "message", "watch: unknown signal kind "+strconv.Quote(kind)), nil
		}
		w.Source = watches.Source{Kind: kind}
		// A per-member signal (presence) counts only the speaker's own: "when I leave home".
		for _, f := range k.facts {
			if f[0] == "user_id" {
				w.Source.UserID = &uid
			}
		}
	}
	if err := w.Validate(); err != nil {
		return servertools.Obj("error", "invalid", "message", err.Error()), nil
	}
	w, err = m.watches.Create(ctx, w)
	if err != nil {
		return nil, err
	}
	if w.SourceKind == watches.SourceWeb {
		m.armWatch(ctx, w, m.now())
	}
	m.deps.Log.Info("cc: watch created", "id", w.ID, "user_id", uid, "household_id", hh, "source", w.SourceKind)
	return servertools.Obj("status", "watching", "watch_id", w.ID, "description", watches.Describe(w),
		"expires_at", watchTime(w.ExpiresAt)), nil
}

type listWatchesTool struct{ m *Module }

func (*listWatchesTool) Name() string { return "list_watches" }

func (*listWatchesTool) Definition() *pyjson.Object {
	return watchToolDefinition("list_watches",
		"List what you are watching for the user (web pages, searches and household signals they asked you to keep "+
			"an eye on). Use when the user asks what you're watching or tracking for them.",
		servertools.Obj())
}

// A read-only lookup: may run in the background without a tap (A7).
func (*listWatchesTool) RunWithoutConfirmation() bool { return true }

func (t *listWatchesTool) Execute(ctx context.Context, _ servertools.Call, turn servertools.Turn) (any, error) {
	if e := t.m.watchToolPrecheck(ctx, turn); e != nil {
		return e, nil
	}
	ws, err := t.m.speakerLiveWatches(ctx, turn)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(ws))
	for i, w := range ws {
		out[i] = servertools.Obj("watch_id", w.ID, "title", w.Title, "description", watches.Describe(w), "state", w.State,
			"last_value", w.LastValue, "last_checked_at", watchTime(w.LastCheckedAt))
	}
	return servertools.Obj("watches", out), nil
}

type cancelWatchTool struct{ m *Module }

func (*cancelWatchTool) Name() string { return "cancel_watch" }

func (*cancelWatchTool) Definition() *pyjson.Object {
	return watchToolDefinition("cancel_watch",
		"Stop watching something for the user. Use when the user asks you to stop watching or tracking something.",
		servertools.Obj("watch", strProp("Which watch: its id, or words from its title (e.g. 'kettle price')")))
}

// Execute cancels the one live watch of the speaker's that matches: an id, or a title containing
// the words (any case). Several matches ask which; none says so.
func (t *cancelWatchTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	m := t.m
	if e := m.watchToolPrecheck(ctx, turn); e != nil {
		return e, nil
	}
	ws, err := m.speakerLiveWatches(ctx, turn)
	if err != nil {
		return nil, err
	}
	q := argText(call, "watch")
	var hits []watches.Watch
	for _, w := range ws {
		if w.ID == q {
			hits = []watches.Watch{w}
			break
		}
		if strings.Contains(strings.ToLower(w.Title), strings.ToLower(q)) {
			hits = append(hits, w)
		}
	}
	switch len(hits) {
	case 0:
		msg := "You aren't watching anything."
		if len(ws) > 0 {
			msg = "I couldn't find a watch matching " + strconv.Quote(q) + "."
		}
		return servertools.Obj("error", "not_found", "message", msg), nil
	case 1:
	default:
		titles := make([]any, len(hits))
		for i, w := range hits {
			titles[i] = w.Title
		}
		return servertools.Obj("error", "ambiguous", "message", "Several watches match; which one?",
			"candidates", titles), nil
	}
	w := hits[0]
	if _, err := m.watches.SetState(ctx, turn.HouseholdID, w.ID, watches.StateDone); err != nil {
		return nil, err
	}
	m.disarmWatch(ctx, w.ID)
	m.deps.Log.Info("cc: watch cancelled", "id", w.ID, "user_id", turn.Speaker.UserID)
	return servertools.Obj("status", "cancelled", "watch_id", w.ID, "title", w.Title), nil
}
