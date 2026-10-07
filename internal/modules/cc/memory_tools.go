package cc

import (
	"context"
	"math"
	"strconv"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// remember / recall / forget (core/tools/{remember,recall,forget}_tool.py). They are offered
// when the household has memory on and can identify speakers (warmup), and act only for the
// speaker jarvisd identified in this conversation: an unknown or ambiguous speaker gets the
// D21 refusal (M14 wording when recognition is off). Memories are the speaker's own, in this
// household only.

// toolSpeakerError is the shared precondition check: nil when the tool may act.
func toolSpeakerError(turn servertools.Turn) *pyjson.Object {
	if turn.ConversationID == "" {
		return servertools.Obj("error", "no_conversation", "message", "No conversation context available")
	}
	if !turn.Speaker.Known() {
		return servertools.Obj("error", "no_speaker", "message", turn.Speaker.Refusal())
	}
	if turn.HouseholdID == "" {
		return servertools.Obj("error", "no_household", "message", "No household context available")
	}
	return nil
}

type rememberTool struct{ m *Module }

func (*rememberTool) Name() string { return "remember" }

func (*rememberTool) Definition() *pyjson.Object { return servertools.LegacyDefinition("remember") }

// Execute saves the memory (source voice) and embeds it right away, best effort: without a
// vector it is still saved, found by keyword until the embedding sweep catches up.
func (t *rememberTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	if e := toolSpeakerError(turn); e != nil {
		return e, nil
	}
	content := call.Str("content")
	category := call.Str("category")
	if _, given := call.Arg("category"); !given {
		category = "general" // legacy default (outside the tool's own enum)
	}
	uid := turn.Speaker.UserID
	w := memWrite{UserID: &uid, HouseholdID: turn.HouseholdID, Content: content, Category: category,
		Key: call.Str("key"), Source: "voice"}
	if vec, model := t.m.embedOne(ctx, content, embedTimeout); vec != nil {
		w.Vec, w.Model = packFloat32(vec), model
	}
	if _, err := t.m.saveMemory(ctx, t.m.deps.DB.Write, w); err != nil {
		t.m.deps.Log.Error("cc: failed to save memory", "err", err)
		return servertools.Obj("error", "save_failed", "message", "Failed to save memory: "+err.Error()), nil
	}
	t.m.deps.Log.Info("cc: remembered", "user_id", uid, "household_id", turn.HouseholdID, "category", category)
	return servertools.Obj("status", "remembered", "content", content, "category", category), nil
}

// packFloat32 re-encodes an already normalised vector.
func packFloat32(v []float32) []byte {
	f := make([]float64, len(v))
	for i, x := range v {
		f[i] = float64(x)
	}
	return packVector(f)
}

type recallTool struct{ m *Module }

func (*recallTool) Name() string { return "recall" }

func (*recallTool) Definition() *pyjson.Object { return servertools.LegacyDefinition("recall") }

// Execute is semantic search over the speaker's own memories, unioned with keyword search
// (M1, LD6). category "general" is a wildcard.
func (t *recallTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	if e := toolSpeakerError(turn); e != nil {
		return e, nil
	}
	query := call.Str("query")
	category := call.Str("category")
	if category == "general" {
		category = ""
	}
	sc := settings.Scope{HouseholdID: turn.HouseholdID}
	limit := int(t.m.settings.Int(ctx, settingRecallMaxResults, sc))
	threshold := t.m.settings.Float(ctx, settingRecallThreshold, sc)
	uid := turn.Speaker.UserID
	hits, method, err := t.m.searchMemories(ctx, memScope{HouseholdID: turn.HouseholdID, UserID: &uid, Category: category},
		query, limit, threshold, embedTimeout)
	if err != nil {
		t.m.deps.Log.Error("cc: failed to recall memories", "err", err)
		return servertools.Obj("error", "recall_failed", "message", "Failed to search memories: "+err.Error()), nil
	}
	if len(hits) == 0 {
		return servertools.Obj("status", "no_results", "message", "No memories found matching '"+query+"'"), nil
	}
	mems := make([]any, len(hits))
	for i, h := range hits {
		mems[i] = servertools.Obj("content", h.Content, "category", h.Category, "similarity", math.Round(h.Score*1000)/1000)
	}
	t.m.deps.Log.Info("cc: recall", "user_id", uid, "results", len(hits), "method", method)
	return servertools.Obj("status", "found", "count", len(mems), "memories", mems, "search_method", method), nil
}

type forgetTool struct{ m *Module }

func (*forgetTool) Name() string { return "forget" }

func (*forgetTool) Definition() *pyjson.Object { return servertools.LegacyDefinition("forget") }

// Execute hard-deletes the speaker's memories containing content_match (D40 04.Q6) and their
// characterization (D30).
func (t *forgetTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	if e := toolSpeakerError(turn); e != nil {
		return e, nil
	}
	match := call.Str("content_match")
	n, err := t.m.forgetMemories(ctx, turn.Speaker.UserID, turn.HouseholdID, match)
	if err != nil {
		t.m.deps.Log.Error("cc: failed to forget memory", "err", err)
		return servertools.Obj("error", "forget_failed", "message", "Failed to forget memory: "+err.Error()), nil
	}
	if n > 0 {
		t.m.deps.Log.Info("cc: forgot memories", "user_id", turn.Speaker.UserID, "count", strconv.Itoa(n))
		return servertools.Obj("status", "forgotten", "count", n, "match", match), nil
	}
	return servertools.Obj("status", "not_found", "count", 0, "message", "No memories found matching '"+match+"'"), nil
}
