package extract

import (
	"context"
	"encoding/base64"
	"errors"

	"github.com/alexberardi/jarvis-server/internal/modules/recipes/quantity"
)

// Input is schemas/ingestion_input.IngestionInput (the parse-payload body's "input").
type Input struct {
	SourceType   string     `json:"source_type"`
	SourceURL    *string    `json:"source_url"`
	JSONLDBlocks []string   `json:"jsonld_blocks"`
	HTMLSnippet  *string    `json:"html_snippet"`
	ExtractedAt  *string    `json:"extracted_at"`
	Client       *string    `json:"client"`
	Images       []ImageRef `json:"images"`
}

// ImageRef is one base64 image of an image_upload payload.
type ImageRef struct {
	Filename    string  `json:"filename"`
	ContentType *string `json:"content_type"`
	DataBase64  string  `json:"data_base64"`
}

// Result is url_parsing.models.ParseResult.
type Result struct {
	Success          bool     `json:"success"`
	Recipe           *Recipe  `json:"recipe"`
	UsedLLM          bool     `json:"used_llm"`
	ParserStrategy   *string  `json:"parser_strategy"`
	Warnings         []string `json:"warnings"`
	ErrorCode        *string  `json:"error_code"`
	ErrorMessage     *string  `json:"error_message"`
	NextAction       *string  `json:"next_action"`
	NextActionReason *string  `json:"next_action_reason"`
}

// Fail is a failed Result.
func Fail(code, msg string, warnings ...string) Result {
	if warnings == nil {
		warnings = []string{}
	}
	return Result{ErrorCode: &code, ErrorMessage: &msg, Warnings: warnings}
}

func ok(r *Recipe, strategy string, usedLLM bool) Result {
	return Result{Success: true, Recipe: r, ParserStrategy: &strategy, UsedLLM: usedLLM, Warnings: []string{}}
}

// Chat runs one prompt on the full (live) model and returns the reply's content.
type Chat func(ctx context.Context, system, user string) (string, error)

// Fetcher fetches a page for a server_fetch payload. A failure is returned as the Result to
// report (legacy fetch_html's exception mapping).
type Fetcher func(ctx context.Context, url string) (string, *Result)

// Ingest is ingestion_service.parse_recipe: JSON-LD → schema.org (client_json_ld); else the
// HTML (cleaned snippet + the JSON-LD blocks) → heuristic (client_html); else, for an
// image_upload, not_implemented; else the LLM fallback on that HTML (llm_fallback), which
// always runs when there is HTML (the webview input has no use_llm_fallback). Legacy ran
// schema.org again on the HTML; that can never match what the JSON-LD pass did not (the
// snippet's own scripts are cleaned away), so it is skipped (B26, no output change). The error
// is schema.org's TypeError on a bad ingredient field (legacy: worker_error).
func Ingest(ctx context.Context, in Input, chat Chat, fetch Fetcher) (Result, error) {
	url := ""
	if in.SourceURL != nil {
		url = *in.SourceURL
	}
	var page string
	switch in.SourceType {
	case "server_fetch":
		if url == "" {
			return Fail("invalid_payload", "source_url required"), nil
		}
		if fetch == nil {
			return Fail("fetch_failed", "server fetch unavailable", "fetch_http_error"), nil
		}
		html, fail := fetch(ctx, url)
		if fail != nil {
			return *fail, nil
		}
		page = html
	case "client_webview":
		if len(in.JSONLDBlocks) > MaxJSONLDBlocks {
			return Fail("invalid_payload", "too_many_jsonld_blocks"), nil
		}
		for _, b := range in.JSONLDBlocks {
			if len(b) > MaxJSONLDBytes {
				return Fail("invalid_payload", "jsonld_block_too_large"), nil
			}
		}
		snippet := ""
		if in.HTMLSnippet != nil {
			snippet = *in.HTMLSnippet
		}
		if len(snippet) > MaxHTMLBytes {
			return Fail("invalid_payload", "html_snippet_too_large"), nil
		}
		page = WebviewHTML(in.JSONLDBlocks, snippet)
	case "image_upload":
	default:
		return Fail("invalid_payload", "unknown_source_type"), nil
	}

	if len(in.JSONLDBlocks) > 0 {
		if len(in.JSONLDBlocks) > MaxJSONLDBlocks {
			return Fail("invalid_payload", "too_many_jsonld_blocks"), nil
		}
		for _, b := range in.JSONLDBlocks {
			if len(b) > MaxJSONLDBytes {
				return Fail("invalid_payload", "jsonld_block_too_large"), nil
			}
		}
		r, err := SchemaOrg(ScriptTexts(JSONLDScripts(in.JSONLDBlocks)), url)
		if err != nil {
			return Result{}, err
		}
		if r != nil {
			return ok(r, "client_json_ld", false), nil
		}
	}
	if page != "" {
		if in.SourceType == "server_fetch" {
			// A fetched page's own JSON-LD is only reachable here.
			r, err := SchemaOrg(ScriptTexts(page), url)
			if err != nil {
				return Result{}, err
			}
			if r != nil {
				return ok(r, "client_html", false), nil
			}
		}
		if r := Heuristic(page, url); r != nil {
			return ok(r, "client_html", false), nil
		}
	}
	if len(in.Images) > 0 {
		if len(in.Images) > MaxImages {
			return Fail("invalid_payload", "too_many_images"), nil
		}
		for _, img := range in.Images {
			data, err := base64.StdEncoding.DecodeString(img.DataBase64)
			if err != nil {
				return Fail("invalid_payload", "Incorrect padding"), nil
			}
			if len(data) > MaxImageBytes {
				return Fail("invalid_payload", "image_too_large"), nil
			}
		}
		return Fail("not_implemented", "image_ingestion_not_implemented"), nil
	}
	if page != "" {
		r, err := ViaLLM(ctx, page, url, chat)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return Result{}, err
			}
			return Fail("llm_failed", err.Error(), "llm_failed"), nil
		}
		return ok(r, "llm_fallback", true), nil
	}
	return Fail("invalid_payload", "no content to parse"), nil
}

// ViaLLM is extract_recipe_via_llm: P1 on the page, the reply parsed, else repaired locally,
// else repaired by the model (P1r), then read as a recipe.
func ViaLLM(ctx context.Context, page, url string, chat Chat) (*Recipe, error) {
	if chat == nil {
		return nil, errors.New("LLM unavailable")
	}
	title, content, err := LLMContent(page)
	if err != nil {
		return nil, err
	}
	reply, err := chat(ctx, P1System, P1User(url, title, content))
	if err != nil {
		return nil, err
	}
	if quantity.PyStrip(reply) == "" {
		return nil, errors.New("LLM response missing assistant content")
	}
	v, perr := ParseJSONContent(reply)
	if perr != nil {
		v = nil
		if fixed, ok := LocalRepair(reply); ok {
			v, _ = decodeJSON(fixed)
		}
		if v == nil {
			if fixed, err := chat(ctx, P1rSystem, P1rUser(P1RecipeSchemaHint, reply)); err == nil {
				if good, ok := RepairReply(fixed); ok {
					v, _ = decodeJSON(good)
				}
			} else if errors.Is(err, context.Canceled) {
				return nil, err
			}
		}
		if v == nil {
			return nil, errors.New("LLM response was not valid JSON after repair attempts")
		}
	}
	return RecipeFromJSON(v, url)
}

// Draft is the recipe_draft a URL/webview job stores (parse_job_service.mark_complete's
// _recipe_dict_to_draft over a ParsedRecipe).
type Draft struct {
	Title       string         `json:"title"`
	Description *string        `json:"description"`
	Ingredients []DraftIngr    `json:"ingredients"`
	Steps       []string       `json:"steps"`
	Prep        int            `json:"prep_time_minutes"`
	Cook        int            `json:"cook_time_minutes"`
	Total       int            `json:"total_time_minutes"`
	Servings    *int           `json:"servings"`
	Tags        []string       `json:"tags"`
	Source      map[string]any `json:"source"`
}

// DraftIngr is one draft ingredient.
type DraftIngr struct {
	Name     string  `json:"name"`
	Quantity *string `json:"quantity"`
	Unit     *string `json:"unit"`
	Notes    *string `json:"notes"`
}

// JobResult is mark_complete's result_json for a URL/webview job: {recipe_draft, pipeline}.
// The quantity is split from its unit (_split_qty_unit); prep is 0 and cook and total are the
// estimated time (legacy, frozen by the goldens).
func JobResult(res Result) map[string]any {
	r := res.Recipe
	if r == nil {
		r = &Recipe{}
	}
	ings := []DraftIngr{}
	for _, ing := range r.Ingredients {
		var qty, unit *string
		if ing.QuantityDisplay != nil && *ing.QuantityDisplay != "" {
			qty, unit = quantity.SplitQtyUnit(*ing.QuantityDisplay)
		}
		if qty == nil || *qty == "" {
			qty = ing.QuantityDisplay
		}
		if ing.Unit != nil && *ing.Unit != "" {
			unit = ing.Unit
		}
		ings = append(ings, DraftIngr{Name: ing.Text, Quantity: qty, Unit: unit})
	}
	est := 0
	if r.EstimatedTimeMinutes != nil {
		est = *r.EstimatedTimeMinutes
	}
	title := r.Title
	if title == "" {
		title = "Untitled"
	}
	steps, tags, warnings := r.Steps, r.Tags, res.Warnings
	if steps == nil {
		steps = []string{}
	}
	if tags == nil {
		tags = []string{}
	}
	if warnings == nil {
		warnings = []string{}
	}
	return map[string]any{
		"recipe_draft": Draft{
			Title: title, Description: r.Description, Ingredients: ings, Steps: steps,
			Prep: 0, Cook: est, Total: est, Servings: r.Servings, Tags: tags,
			Source: map[string]any{"type": "url", "source_url": r.SourceURL, "image_url": r.ImageURL},
		},
		"pipeline": map[string]any{
			"parser_strategy": res.ParserStrategy, "used_llm": res.UsedLLM, "warnings": warnings,
			"source_url": r.SourceURL, "error_code": res.ErrorCode, "error_message": res.ErrorMessage,
			"next_action": res.NextAction, "next_action_reason": res.NextActionReason, "raw_pipeline": nil,
		},
	}
}
