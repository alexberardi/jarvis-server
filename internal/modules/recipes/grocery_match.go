package recipes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/recipes/quantity"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The background SKU-matching pass (recipes.grocery_match; RD1 ports it; prompt P5,
// fixtures/golden/recipes/prompts/P5_grocery_match.json). It runs on the background LLM label
// (llm.background_model_name), nobody waits on it, and it can only alias an unmapped ingredient
// to a mapping the household already has: the model picks a candidate id or declines, and every
// id is checked against the caller's visible map before use. A model failure is "nothing
// learned", not a failed job (legacy match_grocery_items returned []).

// LLM is the in-process chat completion the module uses (the llm module's Service).
type LLM interface {
	Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)
}

const (
	maxMatchCandidates = 60
	matchMaxTokens     = 1500
	// matchReadyWait is how long the pass waits for a background model that is still loading.
	matchReadyWait = 2 * time.Minute
	// matchTimeout bounds the whole LLM call (legacy: 120 s read, plus the ready wait).
	matchTimeout = matchReadyWait + 2*time.Minute
)

// matchSystemPrompt is grocery_service._MATCH_SYSTEM_PROMPT, byte for byte.
const matchSystemPrompt = "You map recipe ingredients onto grocery products a household has already bought.\n\n" +
	"You are given CANDIDATES (products already in their list, each with an id) and INGREDIENTS that have no product yet.\n\n" +
	"For each ingredient, choose the candidate id that is the SAME GROCERY ITEM, or null if none is.\n\n" +
	"Rules:\n" +
	"- Different amounts of the same thing DO match: '1.4 lb 90/10 ground beef' is the candidate 'ground beef'.\n" +
	"- Different forms of the same thing DO match: 'garlic cloves' is 'garlic'.\n" +
	"- Related but distinct items DO NOT match: 'sour cream' is not 'cream', 'chicken thighs' is not 'chicken broth', 'buttermilk' is not 'butter'.\n" +
	"- When unsure, answer null. A wrong product ends up in someone's cart and is only discovered at the checkout.\n\n" +
	`Return ONLY JSON: {"matches": [{"ingredient": "<exact input string>", "candidate_id": <id or null>}]}`

// buildMatchPrompt is build_match_prompt: the first 60 candidates as "<id>: <name>
// (<product>)" and the first 25 unmatched names.
func buildMatchPrompt(candidates []skuMapping, unmatched []string) []llm.Message {
	var listing []string
	for i, c := range candidates {
		if i == maxMatchCandidates {
			break
		}
		line := strconv.FormatInt(c.ID, 10) + ": " + c.IngredientName
		if c.ProductName != nil && *c.ProductName != "" {
			line += " (" + *c.ProductName + ")"
		}
		listing = append(listing, line)
	}
	var wanted []string
	for i, n := range unmatched {
		if i == maxUnmatchedPerPass {
			break
		}
		wanted = append(wanted, "- "+n)
	}
	return []llm.Message{
		{Role: "system", Content: llm.TextContent(matchSystemPrompt)},
		{Role: "user", Content: llm.TextContent("CANDIDATES:\n" + strings.Join(listing, "\n") + "\n\nINGREDIENTS:\n" + strings.Join(wanted, "\n"))},
	}
}

// matchRequest is the chat request for the pass: temperature 0, JSON mode, 1500 tokens, on the
// label the setting names.
func (m *Module) matchRequest(ctx context.Context, msgs []llm.Message) llm.ChatRequest {
	setting := m.settings.String(ctx, SettingBackgroundModel, settings.Scope{})
	label := llm.NormalizeLabel(setting)
	if !strings.EqualFold(setting, llm.LabelLive) && !strings.EqualFold(setting, llm.LabelBackground) {
		m.deps.Log.Warn("recipes: llm.background_model_name is not a label; using live", "value", setting)
	}
	temp, maxTok := 0.0, matchMaxTokens
	return llm.ChatRequest{
		Label: label, Messages: msgs, Temperature: &temp, MaxTokens: &maxTok,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}
}

// controlChars is _strip_invalid_control_chars: ASCII controls other than \t, \n, \r.
var controlChars = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F]`)

// matchGroceryItems is match_grocery_items: the model's matches (dicts only), or none on any
// failure.
func (m *Module) matchGroceryItems(ctx context.Context, msgs []llm.Message) []map[string]any {
	if m.LLM == nil {
		m.deps.Log.Warn("recipes: grocery matching skipped: no LLM")
		return nil
	}
	cctx, cancel := context.WithTimeout(llm.WithReadyWait(ctx, matchReadyWait), matchTimeout)
	defer cancel()
	resp, err := m.LLM.Chat(cctx, m.matchRequest(ctx, msgs))
	if err != nil {
		m.deps.Log.Warn("recipes: grocery matching failed", "err", err)
		return nil
	}
	return parseMatches(resp.Content)
}

// parseMatches reads {"matches": [...]} and keeps the objects. Anything else is no matches.
func parseMatches(content string) []map[string]any {
	dec := json.NewDecoder(strings.NewReader(controlChars.ReplaceAllString(content, "")))
	dec.UseNumber()
	var parsed map[string]any
	if err := dec.Decode(&parsed); err != nil {
		return nil
	}
	list, _ := parsed["matches"].([]any)
	var out []map[string]any
	for _, e := range list {
		if mm, ok := e.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

// candidateID is the model's candidate_id as a map id. Python looked it up in a dict keyed by
// int, so 5 and 5.0 match and "5" does not. (true also matched id 1 there; not here.)
func candidateID(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	if i, err := strconv.ParseInt(n.String(), 10, 64); err == nil {
		return i, true
	}
	f, err := n.Float64()
	if err != nil || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
		return 0, false
	}
	return int64(f), true
}

// applyMatches is apply_matches: each match whose candidate id is in the caller's visible map
// becomes an "llm" alias of that mapping (never overwriting a manual one). B16: the model's
// ingredient must also be one of the names the pass asked about (legacy trusted it).
func (m *Module) applyMatches(ctx context.Context, tx *sql.Tx, c caller, matches []map[string]any, retailer string,
	attempted []string) ([]skuMapping, error) {
	cands, err := listMap(ctx, tx, c, retailer)
	if err != nil {
		return nil, err
	}
	visible := map[int64]skuMapping{}
	for _, s := range cands {
		visible[s.ID] = s
	}
	asked := map[string]bool{}
	for _, a := range attempted {
		asked[strings.ToLower(quantity.PyStrip(a))] = true
	}
	var written []skuMapping
	for _, mt := range matches {
		name, _ := mt["ingredient"].(string)
		if name == "" || mt["candidate_id"] == nil {
			continue
		}
		id, ok := candidateID(mt["candidate_id"])
		cand, visibleOK := visible[id]
		if !ok || !visibleOK {
			m.deps.Log.Warn("recipes: grocery match named a candidate the household cannot see; ignored",
				"candidate_id", mt["candidate_id"], "user_id", c.ID)
			continue
		}
		if !asked[strings.ToLower(quantity.PyStrip(name))] {
			m.deps.Log.Warn("recipes: grocery match named an ingredient the pass did not ask about; ignored",
				"ingredient", name, "user_id", c.ID)
			continue
		}
		s, err := m.upsertMapping(ctx, tx, c, name, cand.SKU, retailer, cand.ProductName, cand.UnitSize, "llm")
		if err != nil {
			return nil, err
		}
		written = append(written, s)
	}
	return written, nil
}

type learnedMapping struct {
	IngredientName string  `json:"ingredient_name"`
	SKU            string  `json:"sku"`
	ProductName    *string `json:"product_name"`
}

// groceryResult is the job's result_json: {learned, attempted, unresolved}. Zero learned is a
// normal outcome: the model is meant to decline when nothing in the map is the same item.
func groceryResult(learned []skuMapping, attempted []string) map[string]any {
	l := []learnedMapping{}
	got := map[string]bool{}
	for _, s := range learned {
		l = append(l, learnedMapping{s.IngredientName, s.SKU, s.ProductName})
		got[s.IngredientName] = true
	}
	unresolved := []string{}
	for _, a := range attempted {
		if !got[a] {
			unresolved = append(unresolved, a)
		}
	}
	return map[string]any{"learned": l, "attempted": attempted, "unresolved": unresolved}
}

// runGroceryMatch is the recipes.grocery_match handler (_process_grocery_match_job).
func (m *Module) runGroceryMatch(ctx context.Context, qj queue.Job) ([]byte, error) {
	j, run, err := m.claimJob(ctx, qj)
	if err != nil || !run {
		return nil, err
	}
	err = m.groceryMatch(ctx, j)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err // shutting down: the queue re-runs it
		}
		m.deps.Log.Error("recipes: grocery match job failed", "parse_job_id", j.ID, "job_type", j.JobType, "err", err)
		if merr := m.markError(context.WithoutCancel(ctx), j.ID, "handler_error", err.Error()); merr != nil {
			return nil, merr
		}
	}
	return nil, nil
}

func (m *Module) groceryMatch(ctx context.Context, j parseJob) error {
	var data struct {
		Retailer  string `json:"retailer"`
		Unmatched []any  `json:"unmatched"`
	}
	_ = json.Unmarshal([]byte(j.JobData.String), &data)
	if data.Retailer == "" {
		data.Retailer = retailerWalmart
	}
	attempted := []string{}
	for _, n := range data.Unmatched {
		if s, ok := n.(string); ok {
			attempted = append(attempted, s)
		}
	}
	if len(attempted) == 0 {
		m.deps.Log.Info("recipes: grocery match job done", "parse_job_id", j.ID, "job_type", j.JobType, "outcome", "empty_job")
		return m.markError(ctx, j.ID, "empty_job", "No ingredients to match")
	}
	c, err := m.jobCaller(ctx, j)
	if err != nil {
		return err
	}
	cands, err := listMap(ctx, m.deps.DB.Read, c, data.Retailer)
	if err != nil {
		return err
	}
	var learned []skuMapping
	if len(cands) > 0 { // else the map was emptied meanwhile: nothing to learn from
		matches := m.matchGroceryItems(ctx, buildMatchPrompt(cands, attempted))
		err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
			var err error
			learned, err = m.applyMatches(ctx, tx, c, matches, data.Retailer, attempted)
			return err
		})
		if err != nil {
			return err
		}
	}
	m.deps.Log.Info("recipes: grocery match job done", "parse_job_id", j.ID, "job_type", j.JobType,
		"outcome", "complete", "learned", len(learned), "attempted", len(attempted))
	return m.markComplete(ctx, j.ID, groceryResult(learned, attempted))
}
