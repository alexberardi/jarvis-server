# recipes 00 — porting jarvis-recipes-server into jarvisd

Source: `/home/alex/jarvis/jarvis-recipes-server` (main, read-only). Paths below are relative to
`jarvis_recipes/app/` in that repo unless they start with `internal/`, `cmd/`, `contract/`, `docs/` or
`fixtures/` (this repo), or name another repo.

Decision being implemented: **recipes moves into jarvisd** (STATUS decision log, 2026-10-08). It becomes
a `recipes` module on the legacy port **7030**, wire-compatible for its clients. It uses SQLite,
jarvisd's durable queue (`internal/platform/queue`), the blob store (`internal/platform/blob`), and
jarvisd's in-process OCR and LLM, with no Postgres, Redis or MinIO. This reverses the 2026-10-06 "add-on" decision.
recipes PR #39 (`feat/jarvisd-addon`) stays unmerged. What it learned is folded in here (§9, §10).

**Fate vocabulary** (same as `docs/admin/00-inventory.md`):

- **KEEP**: the route is ported with the same path, auth and shape.
- **CHANGE**: same path, but behaviour or shape changes on purpose (the reason is given).
- **CUT**: not ported.
- **NEW**: does not exist today.

Product questions are in [QUESTIONS.md](QUESTIONS.md) (RQ1…). They are referenced as RQn.

---

## 0. Summary

| | Routes |
|---|---|
| Legacy surface | 55 method+path pairs from the live OpenAPI on the MBP (2026-10-08), plus the `/media` static mount = **56** |
| KEEP | 39 |
| CHANGE | 6 (`/recipes/import/image`, `/media/*`, `POST /tags`, `/meal-plans/generate/jobs`, `/planner/commit`, `/recipes/parse-url/jobs`) |
| CUT | 11 (`/recipes/parse-url`, `/recipes/parse-url/status/{id}`, `/recipes/jobs`, `/recipes/parse-url/jobs/{id}/cancel`, `/recipes/stock`, `/recipes/import/url`, tag attach/detach ×2, both `/admin/*` seeds, `/planner/draft`); plus FastAPI's `/openapi.json`/`/docs` |
| NEW | 0 public routes. The OCR callback from PR #39 is not needed in process. |

**In one paragraph.** Recipes is a small CRUD service (recipes, meal plans, a shopping list and staples) with
three asynchronous pipelines on top:

1. **webview URL import:** the phone posts JSON-LD/HTML, then schema.org, heuristics and an LLM fallback run.
2. **photo import:** OCR, a quality gate, then LLM structuring.
3. **LLM meal-plan generation**, plus a small background job that matches grocery SKUs.

It has **exactly one real client**, `jarvis-recipes-mobile`. jarvis-node-mobile, command-center, node-setup, jarvis-web
and the admin make **no** recipes calls (§2). That makes wire compatibility a bounded job: the app actually calls 35 method+path pairs (plus 3 it defines but never calls).

Most of the porting risk is not CRUD. It is in:

- the URL fetcher's SSRF guard;
- the ingredient, quantity and name-normalisation regexes, where the golden fixtures are the contract;
- the four LLM prompts, which are byte-exact goldens;
- a pile of legacy bugs in the job lifecycle (§10), which a straight port would faithfully copy.

The legacy Redis/RQ worker, the OCR fan-out with its join deadline, and the write-only mailbox all disappear.
Whether every feature is worth porting at all (the grocery SKU-matching job has never produced a mapping) is RQ1.
The in-process queue and OCR make them unnecessary.

---

## 1. Purpose

Recipes is the household's recipe box and meal planner:

- **Store recipes.** Title, description, image, servings, total time, ingredients (text, display quantity, parsed quantity,
  unit), ordered steps and global tags. Each recipe is authored by a user and **visible to their
  household** (`household_id` from the JWT).
- **Import recipes.**
  - From a web page: the phone renders it in a WebView and posts the JSON-LD and HTML. The server returns a
    draft, and the phone commits it with `POST /recipes`.
  - From 1–8 photos of a cookbook page: OCR, then an LLM turns the text into a draft.
- **Plan meals.**
  - Quick random plans from the box.
  - An LLM-ranked plan per slot, with optional "core" stock recipes staged for 72 h.
  - Commit a plan, list, view and delete plans, and move meals between days.
- **Shop.**
  - A computed shopping list over the committed plans in a date range. It groups ingredients by a normalised key, sums
    parsed quantities per unit, and flags staples.
  - A Walmart cart link for SKUs the household has mapped. A background LLM pass guesses mappings for
    unmatched ingredients from the household's existing ones.
- **Reference data.** A curated list of 198 ingredients and 51 units for the recipe editor's pickers.

---

## 2. Clients

Found by grepping every repo under `/home/alex/jarvis` (2026-10-08).

| Client | Calls | Notes |
|---|---|---|
| **jarvis-recipes-mobile** (`src/api/recipesApi.ts`, `src/services/*`) | 35 method+path pairs (§3, "client" column) | **The only real client.** It discovers `jarvis-recipes-server` via `GET {config}/services?style=external` and falls back to plain `/services`. It sends `Authorization: Bearer <user JWT>`. On a 401 it refreshes once against `{auth}/auth/refresh` and retries. Axios has a 10 s timeout. All async work is polled with backoff (1 s, 2 s, then 3 s; 90 s default, 180 s for imports). The active job is kept in AsyncStorage and resumed. There is no SSE. Paths have no trailing slash. |
| jarvis-node-mobile | none | Its discovery resolves only auth, CC, notifications and pantry. The "pantry" tab is jarvis-pantry. |
| jarvis-command-center, node-setup, jarvis-web, admin, installer, pantry | none | Recipes appears only in the registry, compose and docs. **CLAUDE.md's "CC voice commands" consumer does not exist.** |
| jarvis-mcp | `GET /health` | Being dropped. |
| install-e2e (`test_recipes.py`) | `GET /recipes` (expects 401/403), `GET /openapi.json` (asserts 4 paths), `GET /health` | The OpenAPI assertion needs a decision (§11.6). |
| jarvisd OCR (PR #39 only) | `POST /internal/ocr/callback` | Not needed: OCR is called in process (§7.3). |

Calls the mobile app defines but never makes: `POST /recipes/parse-url/jobs/{id}/abandon` (no such route exists, so
it would 404), `GET /grocery/sku-map`, `DELETE /grocery/sku-map/{id}`, `POST /tags`. The
`ParseRecipeStatus` screen is registered but unreachable.

---

## 3. Routes

**Auth.** "user" means `Authorization: Bearer <user JWT>`, verified locally. A missing header gives 401
`{"detail":"Not authenticated"}`; a bad, expired or unsupported-algorithm token gives 401
`{"detail":"Invalid or expired token"}` (both observed on the MBP). Domain routes do not accept app-to-app
credentials. Scoping is described in §4.1. "visible" means `visible_to` and a denial is **404**, never 403.

**Error shapes.**

- `HTTPException` gives `{"detail": <str|object>}`.
- Request validation (pydantic) gives a **custom 422**:
  `{"error_code":"validation_error","message":"Invalid request payload.","details":[{"field":"body.x","message":"…"}],"job_id":"<random uuid4>"}`.
  `field` is the dotted `loc`, e.g. `body.ingredients`, `query.start_date` or `path.recipe_id`.
  The Go port must reproduce `field` and the shape. `message` texts are pydantic's and are matched by type only.
- A 422 raised by code (`POST /staples` empty name, `POST /grocery/cart` with end < start) uses the plain `detail` shape.

**Client column.** "M" means the recipes mobile app calls it (screen in brackets). "—" means no caller.

### 3.1 Recipes

| # | Method, path | Auth | Request | Response | Status codes | Client | Fate |
|---|---|---|---|---|---|---|---|
| 1 | `GET /recipes` | user | — | `[RecipeRead]`, visible, `created_at` desc | 200 | M (list) | KEEP |
| 2 | `POST /recipes` | user | `RecipeCreate`: `{title, description?, servings?, prep_time_minutes?, cook_time_minutes?, total_time_minutes?, source_type='manual'\|'image'\|'url', source_url?, image_url?, ingredients:[{text, quantity_display?, quantity_value?, unit?}] (≥1), steps:[{step_number, text}] (≥1), tags:[str]=[], parse_job_id?}` | `RecipeRead` | 201; 404 "Parse job not found"; 409 "Parse job not ready" (job not COMPLETE); 422 | M (create) | KEEP |
| 3 | `GET /recipes/{recipe_id:int}` | user | — | `RecipeRead` | 200; 404 "Recipe not found"; 422 for a non-int | M | KEEP |
| 4 | `PATCH /recipes/{recipe_id}` | user | `RecipeUpdate`: every field optional. `null` means "leave as is". `ingredients`, `steps` and `tags` replace wholesale when present. | `RecipeRead` | 200; 404 | M (edit) | KEEP |
| 5 | `DELETE /recipes/{recipe_id}` | user | — | empty | 204; 404 | M | KEEP |
| 6 | `GET /recipes/user/{recipe_id}` | user | — | `RecipeRead`, **author only** (`user_id == caller`) | 200; 404 | M (`/recipes/{source}/{id}` from plan results) | KEEP |
| 7 | `GET /recipes/stage/{stage_id:int}` | user | — | `{id: str, title, description, yield, prep_time_minutes, cook_time_minutes, ingredients, steps, tags, notes}`. The JSON columns are returned raw. **Author only.** | 200; 404 "Not found"; 410 "Stage recipe expired" | M (plan results) | KEEP |
| 8 | `GET /recipes/core/{recipe_id}` | **none** | — | always 404 "Core recipe not found" | 404 | M (plan results with `source=core`) | KEEP (a stub, kept because the client calls it) |
| 9 | `GET /recipes/stock` | user | `q?`, `limit=50` | placeholder stock recipes | 200 | — | **CUT** (no caller; the data is placeholders, see §5.3) |

`RecipeRead` = `{id, user_id: str, title, description, servings, prep_time_minutes, cook_time_minutes,
total_time_minutes, source_type, source_url, image_url, created_at, updated_at, ingredients:[{id, text,
quantity_display, quantity_value, unit}], steps:[{id, step_number, text}], tags:[{id, name}]}`. The details:

- `prep_time_minutes` and `cook_time_minutes` are **always null**. There are no such columns: create and
  update fold them into `total_time_minutes` when total is absent (§10 B1).
- `quantity_value` is a pydantic Decimal, serialised as a **string** read back from a `Numeric(10,4)` column, so always 4 decimals (`"1.5000"`, `"2.0000"`, `"0.3333"`, `"1000.0000"`; seen live, R0). The **server re-parses it from
  `quantity_display`** and ignores the client's value.
- `created_at` and `updated_at` are naive ISO timestamps without a zone.
- Ingredient order is insertion order. Steps are ordered by `step_number`.

### 3.2 Parse jobs and imports

| # | Method, path | Auth | Request | Response | Status codes | Client | Fate |
|---|---|---|---|---|---|---|---|
| 10 | `POST /recipes/parse-url` | user | `{url, use_llm_fallback=true, save=false}` | `ParseUrlResponse` | 200 (failures in the body) | — | **CUT**. A synchronous debug path with no caller. The Go extractor chain stays testable through goldens. |
| 11 | `POST /recipes/parse-url/async` | user | `{url: AnyHttpUrl, use_llm_fallback=true}` | `{job_id, status:"PENDING", result:null, error_code:null, error_message:null, next_action:"webview_extract", next_action_reason:"webview_required"}`. **No row is created**; the id is a fresh uuid that 404s if polled. | 200; 400 `{"detail":{error_code, message, status_code, job_id, next_action?, next_action_reason?}}` when the preflight fails (§4.3); 422 | M (add from URL; it never polls this id) | KEEP |
| 12 | `POST /recipes/parse-payload/async` | user | `{input:{source_type:"server_fetch"\|"client_webview"\|"image_upload", source_url?, jsonld_blocks?:[str], html_snippet?, extracted_at?, client?, images?:[{filename, content_type?, data_base64}]}}` | `{id, status:"PENDING"}` | 200; 422 | M (WebViewExtract: `client_webview`) | KEEP. `server_fetch` and `image_upload` are kept on the wire but behave as §4.3 says. |
| 13 | `GET /recipes/jobs/{job_id}` | user | — | `ParseJobStatus` `{id, status, result, error_code, error_message, next_action:null, next_action_reason:null}` (FastAPI serialises by alias, so the wire key is **`id`**, also on #11 and #17; corrected in R0). **Author only.** | 200; 404 "Job not found" | M (every poller) | KEEP |
| 14 | `GET /recipes/parse-url/status/{job_id}` | user | — | same as #13 | | — | **CUT** (an alias with no caller) |
| 15 | `GET /recipes/parse-url/jobs` | user | `status="COMPLETE"`, `include_expired=false` | `{jobs:[{id, job_type, url, status, completed_at, warnings, preview:{title, source_host}\|null}]}`. Author only. Limit 50, `completed_at` desc. When `status` is COMPLETE (or empty) and `include_expired` is false, only jobs with `completed_at` within `parse_job.abandon_minutes` are listed. | 200 | M (recipe list badge, Mailbox screen) | **CHANGE**: fix `preview` and `warnings`, which read keys that no longer exist and so are always null and `[]` (B6) |
| 16 | `GET /recipes/jobs` | user | — | **unreachable**: `/recipes/{recipe_id:int}` matches first, so this answers 422 | 422 | — | **CUT**. A Go mux would make it reachable, so it is deliberately not registered. It then falls through to #3 and answers the same 422 as legacy. |
| 17 | `POST /recipes/jobs/{job_id}/cancel` | user | — | `ParseJobStatus` with `status:"CANCELED"` and `result:null` | 200; 404 "Job not found"; 409 "Job cannot be canceled" (ERROR, COMMITTED, ABANDONED or CANCELED) | M (import status, Mailbox, create) | KEEP |
| 18 | `POST /recipes/parse-url/jobs/{job_id}/cancel` | user | | same as #17 | | — | **CUT** (alias) |
| 19 | `POST /recipes/from-image/jobs` | user | multipart: repeated `images` (1–8), query `title_hint?`, `tier_max=3` | `{ingestion_id, job_id}` | 202; 400 "No images provided", "Too many images (max 8)", "Empty image upload", "Unrecognized image file"; 413 "Image too large"; 500 "Failed to upload images: …" | M (photo import; always JPEG ≤2048 px) | KEEP (`tier_max` accepted and stored, but unused) |
| 20 | `POST /recipes/import/image` | user | multipart `file` | `RecipeDraft` `{title:"Draft from image", ingredients:[2 placeholders], steps:[2 placeholders], tags:[], image_url:"/media/<hex><ext>"}` | 200 | M (recipe editor photo upload; reads **only** `image_url`) | **CHANGE**: the image is stored in the blob store, not `./media`. The response shape is unchanged. See RQ3 on the relative URL. |
| 21 | `POST /recipes/import/url` | user | `{url}` | a placeholder draft | 200 | — | **CUT** (stub, no caller) |
| 22 | `GET /media/{file}` | **none** | — | the image bytes (Starlette StaticFiles: `Content-Type` by extension, ETag/Last-Modified) | 200; 404 `{"detail":"Not Found"}` | M (indirectly, `image_url` values) | **CHANGE**: served from the blob store under `recipes/media/<file>`. It stays unauthenticated: the names are 128-bit random, as today. |

`result` of a URL, webview or image job (`parse_job_service.mark_complete`):

```
{"recipe_draft": {title, description, ingredients:[{name, quantity, unit, notes:null}], steps:[str],
                  prep_time_minutes, cook_time_minutes, total_time_minutes, servings, tags,
                  source:{type:"url"|"ocr", source_url, image_url}},
 "pipeline": {parser_strategy, used_llm, warnings, source_url, error_code, error_message,
              next_action, next_action_reason, raw_pipeline}}
```

For a photo job, the OCR path writes `{"recipe_draft": RecipeDraft.model_dump(), "pipeline": ingestion.pipeline_json}`
directly instead. The mobile app tolerates both forms. It reads `result.recipe_draft` (or `recipe`), `result.pipeline.attempts[].warnings`,
`result.success`, `result.error_message`, `result.next_action` and `next_action_reason`. These
`result` shapes are frozen by contract tests in R0 (§12).

### 3.3 Tags and stock

| # | Method, path | Auth | Request | Response | Status codes | Client | Fate |
|---|---|---|---|---|---|---|---|
| 23 | `GET /tags` | user | — | `[{id, name}]`: tags attached to at least one visible recipe, ordered by name | 200 | M | KEEP |
| 24 | `POST /tags` | user | `{name}` | `{id, name}`: global, case-insensitive get-or-create | 201; 400 "Tag name required" | M service (never called) | **CHANGE**: commit the row. Legacy only flushed, so the tag vanished after the response and its id was phantom (B9). |
| 25 | `POST /recipes/{recipe_id}/tags/{tag_id}` | user | — | `{id, name}` | 200; 404 "Recipe not found" / "Tag not found" | — | **CUT** (no caller; tags are set through PATCH `tags`) |
| 26 | `DELETE /recipes/{recipe_id}/tags/{tag_id}` | user | — | empty | 204; 404 | — | **CUT** |
| 27 | `GET /ingredients/stock` | user | `q?` (ILIKE on name), `limit=10` (1..1000) | `[{id, name}]` ordered by name | 200; 422 | M (editor pickers; `q=''`, `limit=1000`) | KEEP. The mobile type also declares `category` and `synonyms`, but the server never sends them, so nothing changes. |
| 28 | `GET /units/stock` | user | `q?` (name or abbreviation), `limit=10` (1..100) | `[{id, name, abbreviation}]` | 200; 422 | M (`limit=100`) | KEEP |
| 29 | `POST /admin/static-data/seed` | `X-Admin-Secret` | — | `{ingredients_inserted, …_updated, units_inserted, units_updated}` | 200; 401 "Invalid admin secret" | — | **CUT**: the data is embedded and seeded at module start (§5.3) |
| 30 | `POST /admin/static-recipes/seed` | `X-Admin-Secret` | `?user_id=` | `{inserted, skipped}` | 200; 401 | — | **CUT**: the 190 "stock recipes" are placeholders ("Ingredient 1/2/3") |

### 3.4 Meal planning

| # | Method, path | Auth | Request | Response | Status codes | Client | Fate |
|---|---|---|---|---|---|---|---|
| 31 | `POST /meal-plans/generate/jobs` | user | `MealPlanGenerateRequest` `{days:[{date, meals:{breakfast\|lunch\|dinner\|snack\|dessert: {servings>0, tags=[], note?, is_meal_prep=false, repeat?:{mode:"same"\|"similar", count>0}}}}] (≥1), preferences:{hard:{allergens, excluded_ingredients, diet?}, soft:{tags, cuisines, max_prep_minutes?, max_cook_minutes?}}}`. Extra keys from the app (`pinned_recipe_id`, `allow_external_recipes`) are ignored. | `{job_id, request_id}` | 202; 400 `detail=str(exc)`; 422 | M (LLM planner) | **CHANGE**: the job carries the caller's `household_id`, so generation searches the household's box. Legacy searched only the author's own recipes (B13). |
| 32 | `GET /meal-plans/generate/jobs/{job_id}` | user | — | `{id, status, result, error_code, error_message}`, where `result` = `{"result": MealPlanResult, "slot_failures_count": n}` | 200; 404 "Job not found" (also for another `job_type`). Author only. | M (progress, results) | KEEP |
| 33 | `POST /meal-plans/random` | user | `{slots:[{date, meal_type: str}], exclude_recipe_ids:[int]=[]}` | `{slots:[{date, meal_type, recipe_id\|null, title, image_url, total_time_minutes, servings}], incomplete}` | 200; 422 | M (QuickPlan) | KEEP |
| 34 | `POST /meal-plans/random/reroll` | user | `{meal_type?, exclude_recipe_ids=[], tags=[]}` | one slot, with `date:null` and `meal_type: meal_type or ""` | 200; 409 "No other recipe available to swap in. Add more recipes, or clear a slot." | M | KEEP |
| 35 | `POST /planner/draft` | user | `{start_date, end_date, preferences?}` | stub: `"<Meal> idea"` × 3 per day | 200 | — | **CUT** (stub, no caller) |
| 36 | `POST /planner/commit` | user | `{name?, start_date, items:[{date, meal_type, recipe_id:int, source:"user"\|"stage"="user"}]}` | `MealPlanRead` `{id, user_id, name, start_date, items:[{id, date, meal_type, recipe_id, title, image_url, total_time_minutes}]}` | **200**; 404 "Staged recipe {id} not found" | M | **CHANGE**: a `source:"user"` `recipe_id` must be visible to the caller, giving 404 "Recipe not found". Legacy accepted any id, leaking other households' titles, images and ingredients (B14), and answered 500 for a nonexistent id. |
| 37 | `GET /planner/current` | user | — | `MealPlanRead`, or `{}` when there is none | 200 | M | KEEP. "Today" becomes the household's date (§4.6), not the container's. |
| 38 | `GET /planner/plans` | user | — | `[{id, name, start_date, end_date, meal_count, created_at}]`, `created_at` desc | 200 | M | KEEP |
| 39 | `GET /planner/plans/{plan_id}` | user | — | `MealPlanRead` | 200; 404 "Meal plan not found" | M | KEEP |
| 40 | `DELETE /planner/plans/{plan_id}` | user | — | empty (items cascade; recipes stay) | 204; 404 | M | KEEP |
| 41 | `PATCH /planner/plans/{plan_id}/items` | user | `{moves:[{item_id, date, meal_type}]}` | `MealPlanRead` | 200; 404 "Item {id} is not part of this plan"; 409 "Two meals cannot be moved to the same day and meal type" | M | KEEP (swap semantics in §4.6) |

### 3.5 Shopping, staples and grocery

| # | Method, path | Auth | Request | Response | Status codes | Client | Fate |
|---|---|---|---|---|---|---|---|
| 42 | `GET /shopping-list` | user | `start_date`, `end_date` (required, inclusive) | `{start_date, end_date, items:[{name, amounts:[{unit\|null, quantity: float\|null, unparsed:[str]}], recipes:[title], is_staple}], plan_count}` | 200; 422 | M | KEEP |
| 43 | `GET /staples` | user | — | `[{id, name}]` (visible, deduped by name, lowest id kept) | 200 | M | KEEP |
| 44 | `POST /staples` | user | `{name}` (1..200) | `{id, name}`, where name is the **normalised** shopping key. Idempotent. | **201** even when it already exists; 422 `{"detail":"A staple needs a name"}` | M | KEEP |
| 45 | `DELETE /staples/{staple_id}` | user | — | empty. Deletes **every visible row with that name**. | 204; 404 "Staple not found" | M | KEEP |
| 46 | `GET /grocery/sku-map` | user | `retailer=walmart` (Literal) | `[{id, retailer, ingredient_name, sku, product_name, unit_size, source}]` ordered by name | 200; 422 | M service (unused) | KEEP (cheap, and the only way to see the learned mappings) |
| 47 | `PUT /grocery/sku-map` | user | `{ingredient_name (1..255), raw=false, sku (1..64), retailer="walmart", product_name?, unit_size?}` | `SkuMappingRead` | 200 (insert or update); 422 | M (ProductPicker; no `retailer`) | KEEP |
| 48 | `DELETE /grocery/sku-map/{mapping_id}` | user | — | empty | 204; 404 "Mapping not found" | M service (unused) | KEEP |
| 49 | `POST /grocery/cart` | user | **query** `start_date`, `end_date`, `retailer=walmart`; no body | `{retailer, url\|null, items:[{ingredient_name, sku, quantity:int, product_name, unit_size, source}], unmatched:[{ingredient_name, amount_display, recipes}], match_job_id\|null}` | 200; 422 `{"detail":"end_date must not be before start_date"}` | M | KEEP |

### 3.6 Settings and health

| # | Method, path | Auth | Fate |
|---|---|---|---|
| 50–52 | `GET /settings`, `/settings/`, `/settings/categories`, `/settings/{key}` | app creds **or** superuser JWT | KEEP: `internal/platform/settings` `Mount` with `CombinedGuard` (already in `TestSettingsAppAuth`) |
| 53 | `PUT /settings/{key}` | superuser JWT | KEEP (`SuperuserGuard`) |
| 54–55 | `POST /settings/sync-from-env`, `/settings/invalidate-cache` | superuser JWT | KEEP (the platform mounts both) |
| 56 | `GET /health` | none, `{"status":"ok"}` | KEEP (already in `TestHealth`) |

### 3.7 Not ported: OpenAPI

`GET /openapi.json` and `/docs` are FastAPI built-ins. Only install-e2e reads them (§11.6). **CUT**.

---

## 4. Behaviour

### 4.1 Auth and scoping

- **Identity.** jarvisd verifies the user JWT in process (`authn` + the auth module's `VerifyUser`). The recipes module
  needs `sub` → user id, `email`, and the **`household_id` claim**. jarvisd mints the user's *first*
  household into the token (`internal/modules/auth/tokens.go:72`), exactly as legacy did. A user with several
  households sees one household's box (RQ7).
- **`visible_to(user)`.** With no household: `user_id = caller`. Otherwise: `household_id = hh OR (household_id IS
  NULL AND user_id = caller)`. The NULL arm keeps pre-household rows visible to their author.
- **`owned_by` = `visible_to`.** Any household member can edit or delete any member's recipe, plan, staple
  or mapping. This is by design (a shared kitchen), and the user's stance is never to restrict members on their own install.
- **Author-only reads:** `GET /recipes/user/{id}`, `GET /recipes/stage/{id}`, every parse-job route
  (`/recipes/jobs/*`, `/meal-plans/generate/jobs/{id}`, the job list), and stage materialisation in commit.
- **Writes stamp** `user_id = caller` and `household_id = token claim` (may be NULL).
- **`recipes_users`** is a shadow table of user ids that own data. `ensure_user` inserts on demand before FK writes.
- **Denials are 404**, so a distinct status does not act as a membership oracle.

### 4.2 Recipe CRUD details to keep

- `quantity_value` = `parse_quantity_display(quantity_display)`. It handles `"1"`, `"0.5"`, `"1/2"` and `"1 1/2"`, and returns None on a
  zero denominator or garbage. Python's Decimal also accepts `"NaN"` and `"1e3"`: **freeze NaN → null** in Go (B2).
- Steps are replaced by delete-then-insert, because of `UNIQUE(recipe_id, step_number)`.
- Tags are global and get-or-created case-insensitively by name.
- `PATCH` cannot null a field. `ingredients`, `steps` and `tags` replace wholesale. `prep` and `cook` fold into total only
  when `total_time_minutes` is absent.
- `POST /recipes` with `parse_job_id` checks the job (author only, COMPLETE) and marks it **COMMITTED**
  (`committed_at`) after the insert.
- Deleting a recipe that a plan uses: **legacy answers 500** (the ORM nulls `meal_plan_items.recipe_id`, a NotNullViolation, and nothing is deleted; R0). jarvisd **fixes** it: the item cascades, 204, and the plan silently loses that meal (§8 item 11). Contract: `LEGACY-BUG` branch in `TestRecipesPlanner`.

### 4.3 URL import (webview flow)

1. `POST /recipes/parse-url/async` runs `preflight_validate_url` (3 s): SSRF host check, HEAD, then GET on 405, then a 5 KB GET to sniff
   encoding. Error codes: `invalid_url`, `fetch_timeout`, `fetch_failed` (with `next_action=webview_extract` on
   401/403), `unsupported_content_type`, `encoding_error`. On success it **always** answers `next_action=webview_extract`.
2. The app loads the page in a WebView, collects every `application/ld+json` block and ≤50 000 chars of
   `article[itemtype*=Recipe]|article|main|body` innerHTML, and posts `parse-payload/async` with
   `source_type=client_webview`.
3. The handler creates a `recipe_parse_jobs` row (`job_type="ingestion"`, `job_data=input`) and enqueues it.
4. The worker (`ingestion_service`):
   - Limits: 10 JSON-LD blocks, 200 KB per block, 400 KB html. The HTML is cleaned (`clean_soup_for_content` + `find_main_node`) and cut to 100 000 chars.
   - Order: JSON-LD → schema.org (`client_json_ld`); then HTML → schema.org, microdata (a stub), heuristic (`client_html`); then
     the LLM fallback when `use_llm_fallback`; otherwise `invalid_payload`.
   - `server_fetch` fetches with `fetch_html` first. `image_upload` returns `not_implemented`.
5. On success the result is stored (§3.2), with status COMPLETE and `completed_at`. The phone polls `GET /recipes/jobs/{id}`, opens
   the draft in the editor, and commits with `POST /recipes` + `parse_job_id`.

Extractors:

- **schema.org:** every ld+json script, including `@graph` and lists. `@type` must **equal** `recipe` case-insensitively (or a list containing it); `RecipeCollection` is rejected. A missing description becomes `""`; `HowToSection` steps are dropped (R0 goldens).
  A recipe needs a title, ≥1 ingredient and ≥1 step. Tags come from keywords + recipeCategory + recipeCuisine. Servings is the first integer of
  recipeYield. Times come from an ISO 8601 duration with no day part.
- **heuristic:** title from h1, else `<title>`. Container: article, main, then class `recipe|post|content`, then body.
  Ingredients: the first list with ≥ max(2, n/2) lines that look like quantities or units. Steps: the list after a "direction|instruction|method" heading, else the
  first `ol`.
- **LLM fallback** (§7.4 P1): adds the warning "LLM fallback used; please verify ingredients."

Retry policy: only `llm_timeout`, `llm_failed` and `fetch_failed` retry (no `next_action`, not encoding,
attempts < `queue.max_retries`). A result that carries `next_action` is stored, and the status is then forced to ERROR.

### 4.4 Photo import

1. `POST /recipes/from-image/jobs`:
   - Size-gates each file (`image.max_bytes`) and decodes it (400 on failure).
   - **EXIF-transposes** the image, converts it to RGB, and downsamples when it exceeds 1280·28·28 px: scale to fit, round each side to a multiple of 28, never upscale.
   - Re-encodes as JPEG q90 and stores it under `recipe-images/{user}/{ingestion}/{idx}.jpg`.
   - Writes `recipe_ingestions` (PENDING) and `recipe_parse_jobs` (`job_type="image"`, `job_data={ingestion_id, tier_max, title_hint}`), then answers 202.
2. Legacy then LPUSHed an OCR request to every host in `OCR_QUEUES`. Each host RQ-enqueued `ocr.completed`. `ocr_join`
   accumulated the readings per provider and waited for all hosts, or for a 90 s `ocr.join_deadline`, and the first claimant
   continued. **jarvisd: one queue job runs OCR in process** (§7.3) and has every reading at once, so the join and
   deadline machinery goes.
3. `_structure_from_readings`:
   - Readings are sorted by engine rank (apple_vision, llm_proxy_vision, llm_proxy_cloud, rapidocr,
     tesseract, paddleocr, easyocr, unknown last). Each reading's images are joined with `\n\n`; empty readings are dropped.
   - **Quality gate** (`ocr_quality`) on the longest reading:
     - hard fail below 250 chars or 10 lines, or when gibberish (alpha < .65, vowel ratio < .30, < 20 vowel tokens, < 50 two-letter tokens);
     - score: confidence ≥ 50, ≥ 2 keyword lines, an ingredient-like line, a numbered step;
     - pass when not a hard fail and score ≥ 2. A failure stores `quality_gate_failed` with a friendly message.
   - P2 text structuring on `llm.lightweight_model_name`. With several readings, the ensemble rules are appended.
   - P3 cleanup runs when validation failed, a name contains a unit, a name contains " and " or a comma, or the description is missing.
   - Then `validate_minimums`: title ≥ 3 chars, ≥ 3 ingredients, ≥ 2 steps; otherwise `draft_validation_failed`.
   - On success: ingestion SUCCEEDED, `pipeline_json`, job COMPLETE.
4. The mobile app also treats `type == recipe_image_ingestion_completed|_failed` on the poll as terminal. That is a relic of the
   mailbox; the server never sends it on this route.

### 4.5 Job model (`recipe_parse_jobs`)

- Statuses: `PENDING → RUNNING → COMPLETE → (COMMITTED | CANCELED | ABANDONED)`, `ERROR`, `CANCELED`.
- `mark_running` increments `attempts`. `mark_complete` and `mark_error` skip CANCELED, COMMITTED and ABANDONED.
- Job types in use: `ingestion` (webview), `image`, `meal_plan_generate`, `grocery_match`. The legacy `url` type is unreachable
  (B10).
- Cancel works from PENDING, RUNNING or COMPLETE. jarvisd also cancels the queue job (`queue.CancelByDedupPrefix`) so the
  work stops. Legacy kept running and then skipped the write.
- Abandon: COMPLETE jobs older than `parse_job.abandon_minutes` (3 days) that were never committed → ABANDONED. Legacy ran
  this only from `scripts/run_cleanup.py`, which **nothing scheduled**. jarvisd runs it hourly (§6).

### 4.6 Meal planning

- **Random** (`pick_one`): visible recipes not in `exclude`, `ORDER BY random() LIMIT n`. Tries, in order:
  1. slot tags, or the meal type as a tag;
  2. the meal-type tag alone;
  3. the whole box.

  `pick_plan` runs per slot with an accumulating used-list (no repeats in a plan).
- **LLM generation** (`meal_plan_service`):
  - Days are sorted, and slots run in order breakfast → dessert.
  - Candidates: visible recipes, optionally filtered by the slot note (title or description ILIKE) and up to 5
    excluded terms (title only), `LIMIT 25`. Then a Python tag filter. Then the stock ("core") recipes are appended.
  - P4 ranks them. The pick plus ≤ 2 alternatives are taken. Core picks are copied into `stage_recipes` (72 h, `request_id`) and
    returned as `source:"stage"` with the stage id as a string.
  - When the LLM is down, it falls back to `candidates[0]`.
  - Progress and completion went to the mailbox (§5.4). The result is stored on the job and **not** persisted as a plan: the app commits
    it.
- **Commit:**
  - `source:"stage"` items materialise the staged recipe into `recipes` (author only, once per stage id per request;
    the stage row is kept). `source:"user"` items reference a recipe.
  - The status is **200**, not 201.
  - **Mobile quirk:** the app drops non-integer `recipe_id`s on commit (`MealPlanResultsScreen`). Legacy stage ids were UUIDs until
    `f6a7b8c9d0e1`; they are now integer strings, so they pass.
- **Current plan:**
  - The plan with any item dated ≥ today. Order: earliest such item, then `created_at` desc.
  - "Today" was the container's `date.today()`. jarvisd uses `cc`'s household clock (`Module.HouseholdClock`, the zone of the household's most recently seen node),
    falling back to the host zone.
- **Move items:**
  - Each move targets (date, meal_type). An occupied target held by a non-moving item swaps into the moved item's old slot.
  - Two moves onto one slot → 409. With duplicate `item_id`s, the last wins. `start_date` is reset to the minimum item date.

### 4.7 Shopping list, staples, cart

- **`normalize_name(text)`** (the shopping key, also used for staples and SKU keys). Applied in order:
  1. strip parenthetical asides;
  2. strip a ", chopped/diced/…/to taste" suffix;
  3. strip a trailing "to taste";
  4. strip a leading quantity (digits, unicode fractions, `./-`);
  5. strip a leading unit word;
  6. lowercase, and if that leaves nothing, use the original lowercased.

  **Golden-fixture contract.**
- **List:**
  - Inputs: visible plans with an item in range, using only items in range.
  - Grouping: by key, then by unit (`unit.strip().lower()` or null). `quantity_value` is summed when present; otherwise the raw text goes to `unparsed`.
  - No unit conversion. Recipe titles are unique and kept in insertion order. Items are sorted by name. `is_staple` = key ∈ staples.
- **Cart:**
  - Staples are excluded.
  - Exact key lookup in the visible SKU map for the retailer.
  - Quantity = `ceil(qty/size)`, minimum 1, when the `unit_size` regex unit matches; otherwise 1.
  - URL = `https://affil.walmart.com/cart/addToCart?items=` + `quote("sku_qty,…", safe=",")`.
  - `match_job_id`: when anything is unmatched and the map is non-empty, a `grocery_match` job is enqueued with ≤ 25 unmatched names.
- **SKU match job** (P5, background model):
  - Candidates are the household's existing mappings (≤ 60).
  - `apply_matches` keeps only candidate ids from the visible map, then upserts `source="llm"`.
  - **An LLM upsert never overwrites a manual row.**
  - `result_json` = `{learned, attempted, unresolved}`.
- **Staples:** POST is idempotent (returns the existing row, still 201). DELETE removes every visible row with that name.

### 4.8 Media

- `POST /recipes/import/image` stores the upload **as is**: no resize, no type check. Legacy wrote it to
  `media_root/<uuid4 hex><ext>` and returned `"/media/<name>"`. `GET /media/<name>` serves it unauthenticated.
- **The mobile app uses `image_url` verbatim as an `<Image source={{uri}}>`.** A relative `/media/…` URI does not
  load in React Native, so **photos uploaded from the editor have never displayed** (B15). URL-imported recipes carry
  the site's absolute image URL (hotlinked) and do display. See RQ3.

---

## 5. Data

### 5.1 Tables → SQLite

The baseline was written in 0.7 and removed when recipes became an add-on (commit `c06c74a`). **R1 restores
`internal/modules/recipes/migrations/00001_baseline.sql` from commit `887601b`**, with the type notes in
`docs/schema/recipes.md` unchanged, plus the changes below. All tables are prefixed `recipes_`, and the database is the single shared
jarvisd SQLite file.

| Legacy table | SQLite | Rows on prod | Port notes |
|---|---|---|---|
| `users` | `recipes_users` | ? (≥ 2) | A shadow of auth user ids (TEXT). Keep: the FK target for cascade-on-delete. |
| `recipes` | `recipes_recipes` | 48 (one user) | `source_type` is lowercased on import (legacy stores `MANUAL`/`URL`/`IMAGE`). The ids are referenced by plan items. |
| `ingredients` | `recipes_ingredients` | ? | `quantity_value` is REAL. The API renders it as a decimal **string** (§3.1), so the Go renderer formats it with **exactly 4 decimals** (`"0.5000"`, `"1.5000"`, `"0.3333"`), matching the legacy `Numeric(10,4)` round trip (R0 correction; the earlier "trim trailing zeros" was wrong). |
| `steps` | `recipes_steps` | ? | |
| `tags`, `recipe_tags` | `recipes_tags`, `recipes_recipe_tags` | ? | Global tags. |
| `meal_plans`, `meal_plan_items` | same, prefixed | 2 plans (users 1 and 4) | `date` is TEXT `YYYY-MM-DD`. |
| `staples` | `recipes_staples` | ? | UNIQUE(user_id, name). |
| `grocery_sku_map` | `recipes_grocery_sku_map` | 0 | **Fix the scope index**: legacy `UNIQUE(household_id, user_id, retailer, ingredient_name)` let NULL households duplicate. Use `UNIQUE(COALESCE(household_id,''), user_id, retailer, ingredient_name)`. |
| `recipe_parse_jobs` | `recipes_recipe_parse_jobs` | 52 | **Add `queue_job_id INTEGER`** (the jarvisd queue row, for cancel/inspect). `job_data` / `result_json` are TEXT JSON. |
| `recipe_ingestions` | `recipes_recipe_ingestions` | 13 | **Drop** `ocr_expected` and `ocr_joined_at` (no fan-out join) and `tier1_text`, `tier2_text`, `selected_tier`, `tier3_raw_response` (only the dead legacy HTTP path wrote them; `tier3` was never written). Keep `ocr_readings` (now written once, for debugging) and `pipeline_json`. |
| `stage_recipes` | `recipes_stage_recipes` | 9 | Integer id. Expires after 72 h. |
| `mailbox_messages` | **dropped** | 10 | Write-only: **no route ever read it** (§5.4). Not created, not imported. |
| `stock_ingredients`, `stock_units_of_measure` | same, prefixed | seeded | Seeded at Start from embedded JSON (§5.3). |
| `settings` | `recipes_settings` (platform) | 0 | Created by `internal/platform/settings`. Nothing to import. |

Indexes are as in `docs/schema/recipes.md`, plus `recipes_recipe_parse_jobs(user_id, job_type, completed_at)` for the list.

### 5.2 Deletion hooks (NEW)

Legacy recipes had no account-deletion purge (auth's `purgeServices` lists only CC and notifications). jarvisd's
module registers:

- `OnUserDeleted`: delete the user's parse jobs, ingestions (and their blobs, after commit), stage recipes, their
  private rows (`household_id IS NULL`), and the `recipes_users` row. What happens to **household-shared recipes, plans and
  staples they authored** depends on RQ4.
- `OnMemberRemoved(user, hh)`: the same, scoped to that household.
- `OnHouseholdDeleted(hh)`: delete every row with that `household_id`, plus the blobs.

### 5.3 Reference data

`static_data/ingredients.json` (198 rows) and `units_of_measure.json` (51) are embedded with `go:embed` and **upserted
at module Start** by name. Each has a version hash in the module's settings-free meta row, so this runs only when the file changes.
`ingredients.allergen` is ignored, as in legacy.

`stock_recipes.json` holds **190 placeholder recipes** (every one has "Ingredient 1/2/3"). Today they are appended to every LLM
meal-plan candidate list as "core" recipes, so a generated plan can schedule "Scrambled Eggs" with fake ingredients.
See RQ2.

### 5.4 The mailbox is not mail

`mailbox_messages` is **not email**, and nothing arrives from outside. It is a write-only table the worker filled with
`recipe_image_ingestion_completed`, `meal_plan_generation_{progress,completed,failed}` and (legacy path only) `…_failed`.
**No HTTP route reads it.** The mobile "Mailbox" screen is a list of COMPLETE parse jobs
(`GET /recipes/parse-url/jobs`), not this table. The port **cuts the table** and the writes.

---

## 6. Background jobs → jarvisd queue

The legacy setup was RQ on Redis queue `jarvis.recipes.jobs`, a separate worker process (`scripts/run_rq_worker.py`, with a scheduler for
`enqueue_in`), plus raw LPUSHes to OCR queues. jarvisd replaces this with `deps.Queue.Register(type, Handler{…})` in `Register`, and
`EnqueueTx` in the same transaction that inserts the `recipe_parse_jobs` row, so no job can exist without its row.
The queue payload is `{"parse_job_id": "<uuid>"}`, and the dedup key is `recipes:<parse_job_id>`.

| Legacy job | jarvisd queue type | Concurrency | MaxAttempts / retry | Lease | Notes |
|---|---|---|---|---|---|
| `ingestion` (webview payload) | `recipes.ingest` | 2 | `queue.max_retries` (3); `queue.Permanent` unless the error is `llm_timeout`, `llm_failed` or `fetch_failed` without `next_action` | 3 m | The handler wraps `mark_running`/`mark_complete`/`mark_error` on the row. |
| `image` + `ocr.completed` + `ocr.join_deadline` | `recipes.image` | 1 | 2 | 10 m | One job: load the blobs → OCR in process (all configured engines, §7.3) → gate → P2/P3 → draft. **No join, no deadline.** |
| `meal_plan_generate` | `recipes.mealplan` | 1 | 1 (legacy never retried) | 10 m | One LLM call per slot on the live label. A crash gives `mark_error` with `generation_failed`. |
| `grocery_match` | `recipes.grocery_match` | 1 | 1 | 5 m | Background label. Failures leave the job COMPLETE with nothing learned (legacy). |
| `url`, `recipe.import.url.requested`, `recipe.create.manual.requested` | — | | | | **CUT**: unreachable or `not_implemented` (B10). |
| `scripts/run_cleanup.py` (never scheduled) | scheduler trigger `recipes.cleanup`, hourly | | | | Abandon stale COMPLETE jobs; delete expired stage recipes; **reap jobs stuck in RUNNING** past the lease ×2 → ERROR `worker_lost` (legacy never did); delete `recipe-images/` and `ocr_readings` of ingestions older than 30 days (RQ6). |
| `scripts/run_parse_worker.py` (a DB-polling worker, unused) | — | | | | **CUT**. |

The queue's job id is internal and never on the wire. Clients see only the `recipe_parse_jobs` uuid.

---

## 7. External dependencies

### 7.1 Web fetching and the SSRF guard

`jarvis-web-scraper` is **not** used: that directory is empty, and recipes has its own hardened fetcher copy.

The guard (`url_parsing/html_fetcher.py`), ported:

- Block private, loopback, link-local, reserved, multicast, unspecified and not-`is_global` addresses, unwrapping IPv4-mapped IPv6 first.
- Block `localhost`. Block when **any** resolved address is blocked; a resolution failure is blocked too (fail closed).
- Follow redirects by hand: ≤ 5 hops, re-checking each one, and drop Authorization, Cookie and Proxy-Authorization on cross-origin hops.

**Reuse** `internal/modules/cc/servertools/web.go` `Fetcher`/`IPBlocked` (the legacy quick_search guard, already ported
with goldens). Move it to `internal/platform/ssrf` in R7 if the two rule sets match. Otherwise freeze both and add the difference to
recipes' copy.

Where fetching happens now: the preflight, `server_fetch` ingestion (no client sends it) and nothing else. The webview flow means **the
server never fetches recipe pages in the main path**. Port the `r.jina.ai` fallback? See B11 and RQ8.

### 7.2 Blob store

| Use | Key | Notes |
|---|---|---|
| Photo-import inputs | `recipes/ingest/{user}/{ingestion_id}/{idx}.jpg` (legacy `recipe-images/…` keys are rewritten on import) | `recipe_ingestions.image_s3_keys` holds this JSON array |
| Editor photo uploads | `recipes/media/{uuid4hex}{ext}` | Served by `GET /media/{name}` |

The blob store has no presign, and none is needed: OCR runs in process and reads bytes. Content type: from `Put`'s
`contentType` argument, falling back to the extension.

### 7.3 OCR (in process)

Legacy fanned one request out to every OCR host and ensembled the readings. jarvisd's OCR module already has the engines
(`Tesseract`, `AppleVision`, `LLMVision` in `internal/modules/ocr/engines.go`), but **no exported in-process
API**. R8 adds one, small:

```go
// Recognize runs engine names (empty = every available engine, rank order) over imgs and returns one
// reading per engine. Per-image errors are in the Result, not err.
func (m *Module) Recognize(ctx context.Context, imgs []Image, o Options, engines []string) ([]Reading, error)
```

- **Ensemble default: every available engine.** On the Mac that means Apple Vision + tesseract (+ LLM vision when configured);
  on Linux it is tesseract (+ LLM vision). This matches legacy's best case without the join.
- The `ENGINE_RANK` order moves into the OCR module.
- **A passed deadline keeps the finished readings** (ID13/M4): when the caller's context times out
  (recipes' `ocrTimeout`, 5 min), the engines that finished keep their text and the cut-short images
  carry the error, so a slow LLM vision reading cannot discard Apple Vision's; only cancellation
  is an error.
- **Confidence scale:** `meta.confidence` arrived as 0–1 on the queue path, but the gate tested ≥ 50, so it never
  scored (B4). Go passes 0–100 to the gate.

### 7.4 LLM (in process)

The module calls `llm.Service.Chat` with `ChatRequest{Label, Messages, Temperature, MaxTokens, ResponseFormat:{Type:"json_object"}}`.
There are no app credentials and no HTTP. The model settings (`llm.full_model_name` etc.) hold **label names**: `live` or `background`. Any
other value gets `NormalizeLabel` treatment and is logged.

| # | Prompt | Where | Label | Params | Expected JSON | Golden fixture needs |
|---|---|---|---|---|---|---|
| P1 | URL/HTML extraction | `url_parsing/extractors/llm.py:122-136` | full (`live`) | t=0, json_object, max 800, 90 s | `ParsedRecipe` or `{"error":"invalid"}` | **byte-exact** system and user prompt for 2 inputs (short page padded to 200 lines; long page capped at 10 000 chars) |
| P1r | JSON repair | `llm_client.py:345-362` | full | max 800, 60 s | the schema | byte-exact prompt; local repair goldens (`_try_local_json_repair`) |
| P2 | OCR → RecipeDraft | `llm_client.py:586-606` + `_ENSEMBLE_RULES` 537-547 + `_readings_message` 550 | lightweight (`live`) | max 1100, 60 s | RecipeDraft or `{"error":"garbage_ocr"}` | byte-exact for 1 reading and for 2 readings (`OCR READING n (provider)`, `<<<Rn_START>>>`) |
| P3 | Draft cleanup | `llm_client.py:455-479` | lightweight | max 1000, 30 s | RecipeDraft | byte-exact; and the trigger predicate goldens |
| P4 | Meal-plan select | `llm_client.py:681-706` | full | t=0.2, max 300, 30 s | `{ranked_recipes:[{recipe_id, confidence, reason}], warnings}` | byte-exact (candidates JSON with `summary` cut to 100 chars) |
| P5 | Grocery SKU match | `grocery_service.py:329-360`, `llm_client.py:832-885` | background | t=0, max 1500, 120 s | `{matches:[{ingredient, candidate_id}]}` | byte-exact (first 60 candidates, first 25 ingredients) |

Post-processing to golden:

- `_coerce_recipe_draft` (`llm_client.py:54`): name/label, directions, prepTime, …
- `_parse_llm_json_content`
- `clean_parsed_ingredients`
- P4's id filter, with confidence clamped to 0.5 when out of range.
- P5's `apply_matches`.

**Fix in the coercer:** numeric `servings` and `quantity` are stringified (PR #39 found Qwen3-4B returning
`"servings": 4`, which threw the whole draft away; B5).

### 7.5 Settings

| key | Fate | Notes |
|---|---|---|
| `llm.full_model_name` (`live`) | KEEP | label for P1, P1r, P4 |
| `llm.lightweight_model_name` (`live`) | KEEP | label for P2, P3 |
| `llm.background_model_name` (`background`) | KEEP | label for P5 |
| `queue.max_retries` (3) | KEEP | `recipes.ingest` MaxAttempts |
| `parse_job.abandon_minutes` (4320) | KEEP | the list window and the cleanup |
| `image.max_bytes` (10 MiB) | KEEP | both upload routes (legacy gated only from-image; Go gates `import/image` too) |
| `scraper.user_agent` | KEEP | preflight **and** fetch (legacy fetch hard-coded Chrome/120; B12) |
| `ocr.transport` (PR #39 only) | not added | |
| env `SCRAPER_COOKIES` | **CUT** | Unused on prod as far as we know, and it leaked cookies to r.jina.ai (B11). Re-add as a secret setting if RQ8 wants it. |
| env `OCR_QUEUES`, `OCR_JOIN_TIMEOUT_SECONDS`, `REDIS_*`, `S3_*`, `AWS_*`, `RECIPE_IMAGE_S3_*`, `MEDIA_ROOT`, `DATABASE_URL`, `MIGRATIONS_DATABASE_URL`, `AUTH_*`, `ADMIN_SECRET`, `LLM_BASE_URL`, `JARVIS_*_URL` | **CUT** | in process |

---

## 8. Invariants and non-obvious behaviour

1. **One scoping predicate** (`visible_to`). Every read goes through it, and every denial is 404. In Go, one SQL fragment
   builder, with a test that greps handlers for raw `user_id =` filters (as legacy replaced 49 hand-written ones).
2. **Author vs household.** `user_id` is authorship and `household_id` is visibility. Parse jobs and stage recipes are
   author-only.
3. **Staples are flagged on the list and excluded from the cart.** Never hide an ingredient from the list.
4. **A manual SKU mapping beats an LLM one.** The background pass never overwrites `source="manual"`.
5. **The SKU match never invents mappings.** It can only reuse a candidate id from the household's own map. *Change:* also
   require the model's `ingredient` to be one of the attempted names (legacy trusted it; B16).
6. **The shopping list is a view.** It is computed per request and never stored.
7. **The `parse-url/async` id is not a job.** Polling it 404s. The client knows this and goes to the webview.
8. **`GET /recipes/core/{id}` needs no auth** and always 404s.
9. **Stage ids and recipe ids share the integer space** in client state (`Selection.recipe_id`, `exclude_recipe_ids`). Stage
   ids go out as strings, recipe ids as ints. Keep both.
10. **Integer recipe ids survive the cutover import** where possible (§13). Plan items reference them.
11. **Deleting a recipe deletes its plan items.** Plans are not "repaired".
12. The 422 `job_id` is random per response and correlates with nothing. Keep it a fresh uuid.
13. **Status codes clients branch on:**
    - 409 on reroll ("nothing left");
    - 409 on commit-with-job;
    - 410 for an expired stage recipe;
    - the 400 `detail.next_action` on preflight;
    - 413 for an image that is too large;
    - 202 on the two job submits, 201 on recipe, tag and staple create, 200 on commit.

---

## 9. What PR #39 taught (kept, dropped)

| Finding | Here |
|---|---|
| jarvisd has no Redis; the OCR handoff went over HTTP with a callback | Moot: OCR in process (§7.3). `/internal/ocr/callback` is not ported. |
| jarvisd's callbacks were unsigned on a fresh install → jarvisd mints its own app client | Kept in jarvisd. Not needed by recipes any more. |
| `AUTH_SECRET_KEY` defaulted to `change-me`, so forged HS256 tokens verified | Moot: jarvisd verifies its own tokens. Contract test: an HS256 token signed with `change-me` gives 401. |
| jarvisd tokens carry a `kid`; the verifier must follow a key swap | Moot (in process). |
| Qwen3-4B returns numeric `servings` | Fixed in the coercer (B5). |
| `remap_users.py`: map by email, households to the highest-ranking matched member's household, refuse ambiguity, `legacy_id_remap` log | **The basis of the cutover import** (§13). Same rules, in Go. |
| The registration row `jarvis-recipes-server` on Connections | Moot: jarvisd serves the listener, and its registry row is synced. **Remove** any external Connections row named `jarvis-recipes-server` before serving 7030 (R1 checks for a collision). |

---

## 10. Legacy bugs: freeze or fix

**Freeze** means the Go port reproduces the bug, because a client depends on it or the fix is a product call. **Fix** means
the Go port behaves correctly, and the contract test asserts the fixed behaviour against Go only (marked `jarvisd-only`).

| # | Bug | Where | Decision |
|---|---|---|---|
| B1 | `prep_time_minutes`/`cook_time_minutes` are accepted but never stored, so they are always null on read. The editor's prep and cook fields are lost after save. | `recipes_service`, `models.Recipe` | **RQ5.** Recommended fix: add the two columns and return them. The wire shape is unchanged; the values stop being null. |
| B2 | `"NaN"`/`"1e3"` parse as Decimal quantities | `quantity_parser` | Fix: null |
| B3 | Image jobs flip back to RUNNING when a late OCR reading or the deadline arrives after COMPLETE, so they stick in RUNNING | `queue_worker:91` | Moot (no fan-out) |
| B4 | The OCR confidence scale mismatch means confidence never scores | `ocr_quality` / queue path | Fix (0–100) |
| B5 | Numeric `servings` rejects the draft | `schemas/ingestion.py` | Fix |
| B6 | The job list's `preview`/`warnings` read `result.recipe`/`result.warnings`, which no longer exist, so they are always null and `[]` | `routes/recipes.py:315-345` | **Fix**: read `recipe_draft` and `pipeline.warnings`. The app shows a title in the Mailbox when present. Contract: key set frozen, values jarvisd-only. |
| B7 | OCR completion never sets `completed_at`, so photo jobs never appear in the job list or Mailbox and are never abandoned | `queue_worker:641` | Fix |
| B8 | One failing OCR host fails the whole fan-out job | `queue_worker` | Moot. Go: a job fails only when every engine fails. |
| B9 | `POST /tags` never commits | `recipes_service._get_or_create_tag` | Fix |
| B10 | `url` jobs are enqueued in an envelope that the worker rejects | `queue_service` | CUT (unreachable) |
| B11 | `fetch_html` falls back to `r.jina.ai` and sends the scraper cookies there. The fallback also never passes the HTML check (jina returns markdown). | `html_fetcher` | Cut the fallback (RQ8) |
| B12 | The preflight and the fetch use different UAs and cookie formats; the preflight makes two GETs on success | `html_fetcher` | Fix: one UA setting. Keep the double GET (harmless, ≤ 5 KB). |
| B13 | Meal-plan generation jobs drop `household_id`, so they search only the author's own recipes | `routes/meal_plans.py` | **Fix** (CHANGE #31) |
| B14 | `planner/commit` accepts any `recipe_id`, so plans can expose other households' recipes; a missing id gives 500 | `meal_plan_service.commit_plan` | **Fix** (CHANGE #36): 404 "Recipe not found" |
| B15 | Editor photo uploads return a relative `/media/…` URL that React Native cannot load | `routes/import.py`, mobile | **RQ3** |
| B16 | The SKU match trusts the model's `ingredient` name | `grocery_service.apply_matches` | Fix |
| B17 | The core/stock append is indented inside the per-recipe loop, so it is duplicated N times and absent when the box is empty | `meal_plan_service.py:355-374` | Depends on RQ2. If core recipes are kept: append once. |
| B18 | Tag filter after `LIMIT 25` with no ORDER BY | `meal_plan_service.search_recipes` | Fix: filter in SQL, `ORDER BY random()` |
| B19 | `LLM proxy error` is not treated as an LLM failure, so the slot fails instead of falling back | `meal_plan_service` | Fix |
| B20 | `used_recipe_ids` mixes stage, stock and recipe id namespaces | `meal_plan_service` | Fix: key by `(source, id)` |
| B21 | Stage cleanup compares against `"COMPLETED"`, so it abandons ERROR and COMMITTED meal-plan jobs | `cleanup_expired_stage_recipes` | Fix |
| B22 | `create_job` lets a Redis error escape after commit, giving a 500 for a job that exists | `parse_job_service` | Moot (EnqueueTx) |
| B23 | Ignored meal-plan constraints: `allergens`, `diet`, `cuisines`, `max_*_minutes`, `repeat`; excluded ingredients match on the title only | `meal_plan_service` | Freeze (product scope; listed in RQ9) |
| B24 | The LLM extractor writes `/tmp/llm_raw_<sha1>.log` on every call | `extractors/llm.py:211` | Fix: debug log only |
| B25 | `normalize_unit_token` strips any trailing "s" ("glass" → "glas"); ISO durations with days give None | `ingredient_parser` | **Freeze** (golden contract). Day support is a fix: `P1DT2H` = 1560. |
| B26 | The ingestion path skips `clean_parsed_ingredients` and runs schema.org twice; `parser_strategy` says `client_html` even when schema.org matched on the HTML | `ingestion_service` | Freeze the strategy names. Skip the duplicate run (no output change). |
| B27 | `/admin/static-recipes/seed` FK error for an unknown user | `stock` | CUT |
| B28 | Server-local `date.today()` and naive UTC timestamps | everywhere | "Today" uses the household clock. Timestamps stay naive-UTC strings on the wire (`TimestampNaive`), so the client parsing is unchanged. |

---

## 11. Go port notes

### 11.1 Layout

```
internal/modules/recipes/
  recipes.go          Module: Name "recipes", Listener pconfig.ListenerRecipes, Register, Start; deletion hooks
  migrations/         00001_baseline.sql (restored + §5.1 changes)
  auth.go             user(): bearer → authn.User (+ household claim), legacy 401 details; visible() SQL builder
  errors.go           detail(), the custom 422 validation shape (field = "body.x" etc.)
  store_*.go          recipes, plans, staples, sku, jobs, stage (database/sql, prepared in Tx)
  routes_*.go         one file per §3 group
  quantity/           parse_quantity_display, ingredient_parser, split_qty_unit, iso durations (pure; goldens)
  shopping/           normalize_name, list builder, cart, pack quantity (pure; goldens)
  extract/            schemaorg, heuristic, html cleaning (goquery-free: golang.org/x/net/html), llm extractor
  ocrq/               quality gate, readings ordering, draft coercion/validation (pure; goldens)
  jobs.go             queue handlers, cleanup trigger
  static/             ingredients.json, units_of_measure.json (embedded)
```

### 11.2 Libraries

- HTML parsing: `golang.org/x/net/html`, already a dependency (cc web tools). BeautifulSoup's behaviour differs on
  malformed HTML. The schema.org path only needs `<script type=application/ld+json>` text, which is robust. The heuristic path is
  goldened on the existing fixtures plus 5 saved real pages.
- Image decode: `image/jpeg`, `image/png`, `golang.org/x/image/webp`, plus EXIF orientation. That needs a ~60-line reader of
  the APP1 orientation tag, with tests from 8 orientation fixtures. There is **no pure-Go HEIC decoder**: HEIC gives 400
  "Unrecognized image file". The app's photo import always sends JPEG (it resizes client-side). The editor upload
  (`import/image`) stores bytes as is and never decodes, so HEIC still uploads there.
- Resize: `golang.org/x/image/draw.CatmullRom`, which is not byte-identical to PIL's LANCZOS and doesn't need to be. The dimensions are
  goldened (rounding to multiples of 28).
- JSON: Python `json.dumps` spacing matters only inside prompts (P4 and P5 embed JSON). Use the `pyjson` helper
  cc already has for byte-exact prompt JSON.

### 11.3 Errors and shapes

- FastAPI 422s carry `field` = the dotted loc. Go decodes strictly enough to name the first failing field (`body`,
  `body.ingredients`, `query.start_date`, `path.recipe_id`). The contract asserts the shape and the `field` values for the
  validation cases the app can hit (missing ingredients or steps, bad date, non-int id).
- Pydantic accepts `"5"` for int fields and `5` for str fields in lax mode. Match that leniency where the app sends
  either (e.g. `recipe_id`). The contract has a case for each type the app sends.
- `GET /planner/current` answers `{}` (an empty object), not `null`.

### 11.4 Concurrency and SQLite

- Handlers read on `DB.Read` and write in `DB.Tx`.
- Random selection: `ORDER BY random()` is fine at household scale (48 recipes).
- The shopping list loads plans, items, recipes and ingredients in **3 queries** (legacy did N+1 lazy loads).

### 11.5 Observability

Log with `deps.Log` and include `parse_job_id`, `job_type` and the outcome on every job end (legacy lost in-job logs to the RQ fork; noted in its
CLAUDE.md).

### 11.6 install-e2e

`test_recipes.py` asserts that `GET /openapi.json` lists 4 paths. jarvisd will not serve OpenAPI. **Change the test** to
probe the routes directly: `GET /planner/plans` etc. without a token → 401. Listed in EXTERNAL-CHANGES.

---

## 12. Tests

### 12.0 Corrections found in R0 (2026-10-08)

Found while freezing the contract against the MBP and dumping the goldens. The inline text above is
already corrected where marked; the rest:

- **Recipe tags** come back in insertion order (no `ORDER BY` on the relationship); `GET /tags` is ordered by name.
  Stock pickers are ordered by name in the **database collation** (Postgres en_US puts "blackberries" before
  "black pepper"); the contract does not freeze the tie-break.
- **#15 B6** legacy `preview` is `{"title": null, "source_host": null}` (not `null`) for a job with a result.
- **§4.3 step 4**: the webview input has no `use_llm_fallback`; the LLM fallback **always** runs when there is HTML and
  the extractors fail. JSON-LD inside `html_snippet` is stripped by `clean_soup_for_content`; only `jsonld_blocks`
  reach schema.org. A schema.org draft has `prep_time_minutes: 0`, `cook_time_minutes` = the total, and `tags` from
  `keywords` minus the first entry seen live (`"soup, easy"` → `["easy"]`); frozen by the extract goldens.
- **§4.4**: the quality gate judges the reading with the most whitespace-separated words, not the longest. The P3
  trigger's unit check is a substring test over a list that includes `"g"`, so any unit-less ingredient containing a
  "g" ("eggs") triggers cleanup: it fires almost always.
- **§3.2 photo result**: the OCR path's `recipe_draft.source` is `{type, original_filename, ocr_tier_used}`, not the
  URL path's `{type, source_url, image_url}`.
- **§7.4**: P1 read timeout is 80 s (connect 10, write/pool 90); the LLM sees JSON-LD text (≤ 2000 chars a block) only
  when no main node is found. P1r is `llm_client.py:352-369`, `_ENSEMBLE_RULES` 544-554. P2 with an empty model name
  falls back to `llm.full_model_name`. P4 accepts `confidence: true` as 1.0; integer `recipe_id`s from the model never
  match (candidate ids are strings); user candidates send `prep_time_minutes: 0` and the total as `cook_time`.
- **B2** is wider: `NaN`, `Infinity`, `1_000` and Arabic-Indic digits parse too; NaN/Infinity then fail
  `IngredientRead`'s finite check. **B5** is wider: a quantity dict without `value` and fractional minutes also
  reject the draft.
- **RQ9 / B23**: `excluded_ingredients` and `max_prep/cook_minutes` *are* sent to P4 (`diet` always null there), and
  soft `tags` filter candidates when the slot has none. Fully ignored: `allergens`, `diet`, `cuisines`, `repeat`.
- **§12 names**: `ingredient_parser.parse_ingredient` is `extract_ingredients`; `_split_qty_unit` lives in
  `parse_job_service.py`. The dumper is `tools/golden/export_recipes.py` (beside the cc ones), not `scripts/golden/`.
- **Staples delete-by-name** cannot be exercised through the API: `POST /staples` returns the existing visible row,
  so two same-name rows only arise from pre-household data. Unit-test it in R4.
- **The MBP cannot run photo import**: its recipes container has no `S3_BUCKET`, so `POST /recipes/from-image/jobs`
  answers 500 `Failed to upload images: S3_BUCKET or RECIPE_IMAGE_S3_BUCKET must be configured`. The slow
  `TestRecipesFromImageJob` is therefore proven against jarvisd only (R8).


- **Contract** (`contract/recipes_*_test.go`, listener `recipes`): frozen against the MBP legacy stack first (R0), then run
  against jarvisd (R10). The fixtures are throwaway users and households from `auth_fixtures.go` (two users in one household + one
  outsider), and every row created is deleted through the API at the end. **Do not touch** pre-existing MBP data. The MBP recipes DB
  is empty today, but stock data must be seeded once with the admin seed route; that is reference data, so it is not cleaned up.
- **Golden fixtures** (`fixtures/golden/recipes/`): a Python dumper run against the legacy code at a recorded SHA
  writes input→output JSON for:
  - `parse_quantity_display`, `ingredient_parser.parse_ingredient`, `_split_qty_unit`, `normalize_fraction_display`, ISO durations;
  - `normalize_name` (300+ lines: every ingredient text in the legacy test fixtures plus the 198 stock names and synthetic edge cases);
  - `_pack_quantity`, the cart URL;
  - `ocr_quality` (`tests/fixtures/ocr_samples.py` + 20 synthetic);
  - `_coerce_recipe_draft` (30 shapes);
  - local JSON repair;
  - schema.org and heuristic extraction on `tests/fixtures/ingestion/*` + 5 saved pages;
  - prompts P1–P5 rendered byte-exact.
- **Live smoke** (not CI): `recipe_parsing_tests/url_based` and `image_based` (5 photo sets with fuzzy expectations) against a
  jarvisd with a real model, on this box and the MBP.

---

## 13. Cutover import

> **Built in R11 (2026-10-08).** Where the build differs from the plan below: the bundle has no recipes `users.json` (nothing needs it) and only
> `id, email` from legacy auth users; the export checks the head but writes no alembic version of auth; refusals are the three of PR #39 translated
> to an import (two legacy users → one account, two households → one household, counting earlier runs via the log; an unknown `--household`
> target). PR #39's third rule (an unmatched legacy id equal to a live jarvisd id) protected an *in-place* rewrite that left such ids in
> place; the import never writes a bare legacy id, so instead: unmatched authors' rows are skipped and reported (all of them by default),
> `--park-unmatched` imports their **household** rows now under the author `legacy-<id>` (no account can ever have that id), and the run
> after they sign up re-owns them; private rows always wait. Photos are content-sniffed (an HTML "photo" is dropped), and tags keep their
> legacy id when free. A displaced id goes above both the table's max and the bundle's max, so it never displaces a later legacy row.
> Legacy check from the rehearsal: `source_type` is stored `MANUAL`, timestamps are naive UTC, `quantity_value` a 4-decimal numeric.

**What's on prod** (counts from the parent survey; prod is read-only and **not** connected to by this work):

| Data | Count | Import? |
|---|---|---|
| recipes (+ ingredients, steps, tags) | 48, one author | **yes** |
| meal plans (+ items) | 2 (users 1 and 4) | **yes** |
| staples | ? | **yes** |
| grocery_sku_map | 0 | yes (no-op) |
| recipe_parse_jobs | 52 | **no**: only COMPLETE jobs < 3 days old are ever visible, and at cutover they are stale. RQ6. |
| recipe_ingestions + MinIO `recipe-images/` | 13 | **no** (OCR inputs, never displayed). RQ6. |
| stage_recipes | 9 | no (72 h lifetime) |
| mailbox_messages | 10 | no (table cut) |
| settings | 0 | nothing |

**Photos.** Recipe images are `recipes.image_url`. They are either absolute site URLs from URL imports (kept verbatim) or
`/media/<name>` from editor uploads. Those are files in the legacy container's `media_root` (`/app/media`), which **may not be a
volume on prod**. MinIO holds only the ingestion OCR inputs. The pre-flight read-only survey (an addition to cutover runbook §3) answers this:

- `SELECT image_url FROM recipes` patterns: count of absolute, `/media/` and other;
- `docker inspect` of the recipes container's mounts;
- whether the files exist.

If they exist, they are copied into the bundle.

**Shape: export bundle + `jarvisd import-recipes`.** This keeps a Postgres driver out of the binary and gives a fixture-testable input:

1. **Export** (`scripts/legacy/recipes-export.sh`, run on prod by the operator during the window; read-only).
   - It writes one JSON file per table via `docker exec jarvis-postgres psql -At -c "SELECT json_agg(t) FROM <table> t"`:
     recipes, ingredients, steps, tags, recipe_tags, meal_plans, meal_plan_items, staples, grocery_sku_map, users.
   - It adds `auth_users.json` from the legacy **jarvis_auth** `users(id, email)` and `household_memberships(household_id, user_id, role)` + `households(id, name)`.
   - It adds a `media/` directory copied from the container (`docker cp`), and a `manifest.json` (alembic head, counts, sha256 per file).
   - It refuses to run if the recipes alembic head is not `e1f2a3b4c5d6`.
   - It is tarred.
2. **Import** (`jarvisd import-recipes <bundle.tar> [--apply] [--household OLD=NEW]`, a subcommand like `migrate`; dry run by default).
   - **Users.** Legacy user id → email → jarvisd `auth_users.email`, case-insensitive. Unmatched users: their rows are **skipped and
     reported**. Re-running after they sign up imports them (incremental).
   - **Households.** Same rules as PR #39's `remap_users`. Map to the household of the highest-ranking matched member (admin > power_user > member).
     When ambiguous, narrow by same name, then by the households all matched members share. Anything still ambiguous is reported, and needs `--household`.
     A NULL legacy household stays NULL (author-only, the legacy semantics).
   - **Refusals** (nothing written): two legacy users → one jarvisd user; two legacy households → one jarvisd household.
   - **Ids:** the legacy integer id is kept when it is free in the target table. Otherwise a new id is allocated and the FKs are rewritten within
     the import, for recipe → ingredients, steps, recipe_tags and meal_plan_items. Tags merge by name.
   - **Transforms:**
     - `source_type` is lowercased;
     - timestamps go from naive to ISO-8601 UTC text;
     - `quantity_value` numeric → REAL;
     - `image_url` `/media/<name>` → copy `media/<name>` into blob `recipes/media/<name>` (URL unchanged); absolute URLs are kept;
     - anything else (`s3://…`, a MinIO URL) → copy the object if the bundle has it, else null it and report.
   - **Bookkeeping:** one SQLite transaction. A `recipes_import_log(legacy_kind, legacy_id, new_id, imported_at)` table makes
     re-runs idempotent: rows already imported are skipped.
   - **Report:** counts per table imported, skipped and orphaned, plus every unmatched email (shown masked as `a***@domain`).
3. **Rehearsal** (R11): build a synthetic legacy bundle on the MBP. Take its (empty) legacy recipes DB, fill it through the legacy API
   with fixtures that mirror prod's shape (2 users, 48 recipes, one with a `/media` photo, 2 plans, staples), export it with the
   script, and import into a throwaway jarvisd with matching accounts. Then compare `GET /recipes`, `/planner/plans` and
   `/shopping-list` between legacy and jarvisd for the same users (the ids may differ only where remapped). Afterwards, delete
   the fixtures through the legacy API.

---

## 14. Port plan (agent-sized, in order)

Each step leaves `main` green and can be reviewed alone. R2–R5 can run in parallel worktrees after R1. R7–R9 run after R6.

| Step | Scope | Done when |
|---|---|---|
| **R0** | **Freeze the contract against legacy.** `contract/recipes_test.go` + `recipes_jobs_test.go`, covering every KEEP/CHANGE route in §3 with the fixtures in §12. Cases: CRUD round trip (including `quantity_value` as a string and prep/cook null); scoping (household member sees and edits, outsider 404, author-only routes 404 for a member); both 401 details; the 422 custom shape with `field` for 4 cases; 404/409/410 details; stock (seed once via `X-Admin-Secret`); tags; staples idempotent 201 and delete-by-name; sku-map; shopping list grouping; cart URL and `match_job_id`; planner commit/current/plans/get/delete/patch (swap, 409); random/reroll (409); `parse-url/async` preflight (public URL ok, `http://127.0.0.1` → 400 `invalid_url`); `parse-payload/async` with a JSON-LD fixture → poll to COMPLETE → freeze the `result` key sets → `POST /recipes` with `parse_job_id` → COMMITTED → second commit 409; cancel matrix. **Slow tier** (`JARVIS_CONTRACT_SLOW_TIMEOUT`, skipped without a model): from-image with a fixture photo, meal-plan generate. Start the MBP's `parse-worker` container (and the OCR worker) for the job tests; stop them again afterwards if they were stopped. Plus the golden dumper (`scripts/golden/recipes_dump.py`) and `fixtures/golden/recipes/*`. | All non-slow tests pass against the MBP legacy stack; the slow ones pass once with the MBP's model; goldens committed with the legacy SHA. Fixed-bug cases (§10 "Fix") are written but tagged `jarvisd-only`. |
| **R1** | Module skeleton: `internal/modules/recipes` (Name, Listener 7030, Migrations = restored baseline + §5.1 changes), settings Definitions (§7.5) with `CombinedGuard`/`SuperuserGuard`, `GET /health`, user auth + `visible()` builder + the 422 helper, `ensure_user`, deletion hooks (RQ4 default until answered), stock data embed + Start upsert. Wire it into `cmd/jarvisd/main.go`; registry row synced. Refuse to start the listener if an external registry row `jarvis-recipes-server` points elsewhere (log + doctor warning with the fix). | `TestHealth` and `TestSettingsAppAuth` recipes rows pass against jarvisd; migration test (`baseline_noncc_test.go` gains recipes again); doctor lists 7030. **Done 2026-10-08 (`f9aee29`).** Notes for later steps: RD5's columns are in 00001 (nothing shipped a recipes migration, so no 00002); recipes/plans/staples have **no FK to `recipes_users`** (RD4: deleting the author's shadow row must not cascade to household rows), ingestions and stage recipes keep it and gain `household_id` (stamp it on insert, R6/R8/R9); `recipes_recipe_tags` is a rowid table (attach order); tag and stock names are unique `COLLATE NOCASE`. Scoping is `caller.visible()`/`authorOnly()` in `auth.go` (RD7 via auth's new `UserHouseholds`; a stale token household writes to the first current membership); a test fails on hand-written `user_id`/`household_id` filters outside `auth.go`/`hooks.go`. Hooks delete blobs through a `recipes.blob_purge` job enqueued in auth's transaction; a parse job's queue row is **not** cancelled by the hooks (no second writer inside the tx), so R6 handlers must treat a missing parse-job row as done. The registry collision is a takeover warning in config `syncSelf` (jarvisd already overwrote foreign rows for served names), not a refusal. Stock routes (#27, #28) landed here with the seeding. |
| **R2** | Recipes CRUD (#1–8), tags (#23, #24 fixed), stock (#27, #28); the `quantity` package with goldens; B1 per RQ5 (migration 00002 if columns are added). | R0's CRUD, tags, stock and scoping cases green vs jarvisd; quantity goldens green. **Done 2026-10-08 (`9e35f91`, `33dca8f`).** Against a throwaway jarvisd: `TestHealth`, `TestSettingsAppAuth`, `TestRecipesCRUD`, `…QuantityEdgeCases`, `…Scoping`, `…Tags`, `…Stock` green; `TestRecipesAuth`/`…Validation` fail only on R4–R6 routes. Goldens green: `quantity.json`, `ingredients.json` (the `quantity` package also has extract/clean ingredients, durations, servings, `_split_qty_unit` for R7) and `normalize_name.json` (606 rows, `shopping.NormalizeName`, ahead of R4). B25's day fix is applied (`P1DT2H` = 1560); the golden test overrides those three rows. `quantity_value` rounds half away from zero (Postgres), not the dumper's half-even. |
| **R3** | Media: `POST /recipes/import/image` (#20) into the blob store, `GET /media/{name}` (#22) with ETag/Last-Modified, `image.max_bytes`; RQ3's answer. | Upload → fetch round trip in contract; 404 shape; unauthenticated GET works. **Done 2026-10-08 (`d81c244`).** Stored as is under `recipes/media/<hex><ext>`; the wire keeps the relative `/media/<name>` (RD3). Deliberate tightening: `image.max_bytes` → 413 "Image too large", empty → 400 "Empty image upload", and only images are accepted (extension allowlist jpg/jpeg/png/gif/webp/heic/heif checked against a content sniff; no usable extension → the sniffed one; anything else → 400 "Unrecognized image file"): legacy stored any bytes under any name and served them back from the recipes origin (stored XSS). `GET /media/{name...}`: no auth, content type by extension, `nosniff`, ETag/Last-Modified/conditional GETs, `Cache-Control: public, max-age=31536000, immutable`; 404 `{"detail":"Not Found"}` for anything else. Deleting a recipe, or PATCHing its `image_url` to something else, enqueues R1's `recipes.blob_purge` in the same tx (kept while another recipe shows the photo). **For R6:** an upload never attached to a recipe is never deleted; the hourly cleanup should sweep `recipes/media/*` older than a day that no recipe references. |
| **R4** | Planner (#36–41), random/reroll (#33, #34), shopping list (#42), staples (#43–45); the `shopping` package with `normalize_name` goldens; household clock for `/planner/current`. | R0's planner, random, list and staples cases green vs jarvisd; B14 jarvisd-only case green; goldens green. **Done 2026-10-08 (`a548208`).** `planner.go`, `random.go`, `shopping_routes.go`; `shopping.Build` and the cart helpers (green on `cart.json`). "Today" comes from `Module.Clock` (cc gained an exported `HouseholdTimezone`; wired in main.go) for the caller's write household, host zone as fallback. Commit checks every `source:"user"` id with `visible()` inside the tx (404 "Recipe not found", nothing written); stage items materialise once per stage id (author only; `quantity_value` re-parsed from the display, where legacy left it null). Moves keep legacy's order-dependent swap (the occupant search skips every moving item and sees earlier moves; a repeated item_id moves once, to its last target). Shopping sums are exact decimals (`StoredQuantity` → big.Rat); plans, items and ingredients in id order. Staples: `UNIQUE(user_id, name)` is per author, so re-adding a name the caller authored in a household they have left moves that row to the current household instead of a 500. Tag matching for random picks lowercases in SQLite (ASCII only; legacy's Postgres lowered Unicode). |
| **R5** | Grocery: sku-map (#46–48), cart (#49), and (unless RQ1 cuts it) the `recipes.grocery_match` queue handler with P5 (prompt golden), `apply_matches` with B16. | Cart and sku cases green; a fake-LLM unit test learns a mapping and never overwrites a manual one. **Done 2026-10-08 (`ec9aaf4`).** `grocery.go`, `grocery_match.go`, and the parse-job core in `jobs.go`: `createJob` (row + `EnqueueTx`, dedup `recipes:<uuid>`, `queue_job_id`), `claimJob` (loads the row, marks RUNNING unless CANCELED/COMMITTED/ABANDONED; a missing row is done), `markComplete`/`markError` (same guard), `jobCaller` (the author's RD7 union, rebuilt from the row), plus `GET /recipes/jobs/{job_id}` (#13, needed to poll the match job). `recipes.grocery_match`: concurrency 1, 1 attempt, P5 byte-exact (golden test), label from `llm.background_model_name` via `NormalizeLabel` (a non-label value is logged), temperature 0, `json_object`, 1500 tokens, 2 min ready wait, 4 min cap; `Module.LLM` is `llm.Service()` in main.go. A model error or garbage completes with nothing learned (legacy); a DB failure is ERROR `handler_error`; an empty job ERROR `empty_job`. B16: a match is written only when its ingredient (stripped, lowercased) is one the pass asked about. Legacy quirk kept: a match onto a name with a manual mapping returns the manual row unchanged, and it counts as `learned`. The slow contract test passed against a throwaway with Qwen3-8B on CPU (model as an absolute path in `llm.background.model`, CPU engine build copied in; no downloads). **For R6:** the legacy job list (#15) lists every job type, so grocery_match (and meal-plan) jobs show as COMPLETE rows with no draft; decide whether the Mailbox filters to import types. Cancel must also `CancelByDedupPrefix("recipes:"+id)`. |
| **R6** | Jobs core: `recipe_parse_jobs` store with the status rules (§4.5), `EnqueueTx`, `GET /recipes/jobs/{id}` (#13), list (#15, B6 fixed), cancel (#17, also cancels the queue job), commit-with-job in `POST /recipes`, the hourly `recipes.cleanup` trigger (abandon, stage purge, RUNNING reaper). | Job lifecycle unit tests (every transition, including cancel during RUNNING); contract cancel matrix green with jobs created through R7's route stubbed by a test-only enqueue. **Done 2026-10-08 (`a6172e9`).** `jobs_routes.go`: list (#15) shows `ingestion` and `image` jobs only (RD10), B6 fixed (preview `{title, source_host}` from `recipe_draft`/`pipeline`, warnings from `pipeline.warnings`; null without a result); an empty `status=` lists every status with no window. Cancel (#17) is a guarded update plus `queue.CancelByDedupPrefix("recipes:"+id)`; a handler finishing later is a no-op (markComplete/markError guards). Commit-with-job was already in `POST /recipes` (R2); unit-tested here. `cleanup.go`: scheduler trigger `recipes.cleanup` (hourly, StartNow) abandons stale COMPLETE jobs, deletes expired stage rows and abandons only their COMPLETE meal-plan jobs (B21), reaps RUNNING jobs older than 2x their lease (ingest 5 m, image 15 m, mealplan 30 m, grocery 5 m) to ERROR `worker_lost`, deletes photo imports (blobs, then the row with `ocr_readings`) 30 days after the import (RD6), deletes finished job rows 30 days after their last update (judgement call: nothing lists them by then), and deletes `recipes/media/*` blobs over a day old that no recipe shows. Each step is independent and logged. |
| **R7** | URL import: preflight (#11) on the shared SSRF fetcher (moved to `internal/platform/ssrf` if the rules match, §7.1), `parse-payload/async` (#12), the `recipes.ingest` handler (limits, cleaning, schema.org → heuristic → P1 + P1r), the retry policy. | Extraction and prompt goldens green; SSRF tests (blocked hosts, redirect re-check, header strip) green; R0's webview flow green vs jarvisd end to end with a fake LLM and with a real one. **Done 2026-10-08 (`6a0d0f9`).** The rule sets matched, so the guard moved: `internal/platform/ssrf` (`IPBlocked`, `Fetcher` with `Do(method)` for HEAD, credentials dropped per *origin*, the stricter recipes rule; cc/servertools aliases it). New `extract` package (x/net/html parsed with scripting off so `<noscript>` is markup as in lxml; `get_text` skips script/style/template text as bs4 >= 4.10 does): schema.org, heuristic, cleaning/main node, `LLMContent`, P1/P1r, JSON helpers, `Ingest`, `JobResult` (mark_complete's draft) - green on every row of `extract.json` (schema_org, heuristic, ingestion incl. `result_json`, with the dumper's LLM replies from `tools/golden/inputs/recipes_pages.json`), P1 x2, P1r and the three JSON-helper sections. Overrides: B25 day fix (`P1DT2H` = 1560); `llm_failed` messages are not pydantic's text (code compared). Kept: the LLM fallback always runs when there is HTML; the ingestion path adds no "LLM fallback used" warning (golden); llm_failed/llm_timeout/fetch_failed retry while `attempts < queue.max_retries` (row back to PENDING, the queue re-runs it). B26: the duplicate schema.org pass is skipped except for `server_fetch` pages. B5 widened to P1 replies (numeric `quantity_display`/`unit` kept as text; a non-integer servings/time becomes null). `server_fetch` uses the guard with the UA setting (B12), no jina/cookies (RD8); only UTF-8 and Latin-1 decode (no x/text), anything else is `encoding_error` -> webview. |
| **R8** | Photo import: from-image route (#19, decode + EXIF + resize goldens on dimensions), `ocr.Module.Recognize` export (§7.3), the `recipes.image` handler (readings order, gate with the 0–100 fix, P2/P3, coercer with B5, minimums), `ocr_readings` / `pipeline_json`. | The 5 `image_based` photo sets parse on this box (tesseract) and on the MBP (Apple Vision + tesseract) within the fuzzy expectations; R0's slow from-image case green vs jarvisd. **Done 2026-10-08 (`b25d831`).** `ocr.Module.Recognize(ctx, imgs, opts, engines)` runs every enabled+available engine concurrently in `ocr.EngineRank` order (boxes on, mean word confidence on 0-100: B4); no LLM validation step (the recipes gate judges). `photo.go`: jpeg/png/gif/webp (new dep `golang.org/x/image`), EXIF orientation 1-8 (own APP1 reader, tested both byte orders against PIL's transpose semantics), RGB on black, `visionSize` (legacy arithmetic incl. its 1003x1001 -> 1008x1008 quirk), CatmullRom, JPEG q90 at `recipes/ingest/<user>/<ingestion>/<idx>.jpg`; count checked before size (legacy order). `ocrq` package: readings sort/combine/gate text, `ScoreQuality`, P2 (ensemble rules for n > 1), P3 (`pyjson.DumpsIndent`, new), P3 trigger, `Coerce` (pydantic-lax direct path, then coercion) - green on `ocr.json`, all 34 coerce rows, P2/P3/P1r-draft goldens; B5 fixes are explicit overrides (numeric/float servings, value-less quantity dict, fractional minutes rounded half away from zero). Job: OCR -> `ocr_readings` -> B8 (fails only when no engine read text, `ocr_no_text`) -> gate (`quality_gate_failed`, legacy message) -> P2 on the lightweight label (full when empty) with local then P1r repair -> P3 when triggered (failure keeps the draft) -> minimums -> `{recipe_draft, pipeline}` with `completed_at` (B7). No OCR wired -> `ocr_unavailable`. Slow contract `TestRecipesFromImageJob` green on this box (tesseract + Qwen3-8B CPU). Not done: the 5 `image_based` sets on the MBP (needs a jarvisd there; R10). |
| **R9** | Meal-plan generation (#31 with B13, #32): `recipes.mealplan` handler, candidates (B18, B19, B20), P4, stage recipes, RQ2's answer on core recipes (B17). | Unit tests with a fake LLM (rank, fallback, null pick, already-used); R0's slow generate case green vs jarvisd. **Done 2026-10-08 (`91f81a9`).** `mealplan.go`: validation (meal keys literal -> `...meals.<k>.[key]`, servings > 0, empty days/meals), job_data `{request_id, payload}` with defaults filled; #32 answers raw `result_json` and 404s other job types. Candidates: the caller's RD7 union, note LIKE on title/description, excluded terms on title (first 5 each), tags any-of case-insensitive in SQL, used ids excluded in SQL, `ORDER BY random() LIMIT 25` (B18); RD2 no core/stock rows, so nothing is staged (stage routes and commit stay). P4 byte-exact (golden incl. the 25 cap); `parseSelection` green on 9 of 11 `meal_plan_select` rows (proxy_error/http_500 are HTTP-only; in process every model error is "LLM error", which falls back: B19). Used set keyed `source:id` (B20). User candidates still send prep 0 and the total as cook (RD9 as is). Lease 30 m (one P4 call per slot). |
| **R10** | Whole contract suite vs a real jarvisd binary (277xx setup) and vs MBP legacy again (no regression in the frozen set); install-e2e change (§11.6); EXTERNAL-CHANGES updated (recipes rows: PR #39 closed unmerged; mobile: no change unless RQ3 picks the app fix). | `scripts/contract.sh -run TestRecipes` green on both targets; CI green. **Done 2026-10-08.** Whole suite, both sides, no divergence and no code change: MBP legacy (7030) every non-slow `TestRecipes*` + `TestHealth`/`TestSettingsAppAuth` green twice, slow `TestRecipesMealPlanGenerate`/`…GroceryMatchJob` green on its Qwen3-8B (`…FromImageJob` still unprovable there: no `S3_BUCKET`); a throwaway jarvisd binary (617xx/61030, own home, mDNS/browser off) the same set green twice plus the whole slow tier (Qwen3-8B CPU, tesseract). install-e2e: `test_recipes.py` probes the four routes for 401 instead of reading `/openapi.json` (umbrella repo PR [#37](https://github.com/alexberardi/jarvis/pull/37), not merged; legacy and jarvisd both 401 ×4). EXTERNAL-CHANGES: recipes section now lists only the e2e PR, PR #39 to close unmerged, and the mobile RD3 fix (recipes-mobile #20, merged). Not done here: the 5 `image_based` photo sets with Apple Vision on the MBP (needs a jarvisd there). |
| **R11** | Cutover import: `scripts/legacy/recipes-export.sh`, `jarvisd import-recipes` (dry run default, report, incremental, `recipes_import_log`), the rehearsal in §13 step 3; cutover runbook updated (pre-flight survey of `image_url` + media mounts, export in the window, import after the household admins have signed up, verification V-rows: recipe count per user, a plan renders, a `/media` photo loads). | The rehearsal's legacy-vs-jarvisd comparison is equal; a second run imports nothing; an unmatched-user run reports and skips; runbook reviewed. **Done 2026-10-08 (`c85413d`).** As built (differences from §13 noted there): `scripts/legacy/recipes-export.sh` (strictly read-only; `--ssh` or on the host; photos by `docker cp …/app/media/<name> -`, so a stopped container works) and `jarvisd import-recipes BUNDLE [--apply] [--household OLD=NEW] [--park-unmatched] [--legacy-host HOST]` (`legacy_bundle.go`, `legacy_import.go`, migration 00002 `recipes_import_log`, `auth.ReadDirectory`). Unit tests on fixture bundles: dry run writes nothing, apply read back through the API (owners, scoping, ids, tags in attach order, quantities, photos), second run imports nothing, incremental after a sign-up, park + re-own, the three refusals, ambiguous household + override, same-name narrowing, taken ids (no cascade), existing staples/mappings/tags matched, a deleted row not resurrected, bundle checks (tar.gz, sha256, schema head, unsafe paths), an HTML "photo" refused; CLI test for the guards. **Rehearsal** (MBP legacy, seeded through its API: 3 users, ann + bob in one household, cat alone; 5 recipes incl. an editor photo and a hotlinked image, 2 plans / 4 items across both members, 3 staples incl. one in bob's own household, 1 SKU mapping): exported, imported into a throwaway jarvisd (ann's email in another case, cat not signed up). Dry run reported cat unmatched and bob's own household ambiguous (both his jarvisd households are "My Home") → `--household`; apply 4 recipes / 8 ingredients / 8 steps / 3 tags / 2 plans / 4 items / 3 staples / 1 mapping / 1 photo; second run 0; `GET /recipes`, `/recipes/{id}`, `/planner/plans` (+ each plan), `/staples`, `/grocery/sku-map`, `/shopping-list` **equal** for ann and bob (minus `user_id`/`household_id`; same ids; timestamps equal to the second; authors = the matched accounts), photo bytes identical; cat signed up later → the re-run imported exactly her recipe (and kept the earlier `--household` from the log). Legacy cleaned through the API afterwards (recipes, plans, staples, mapping, users); left, as the contract documents for its own runs: 3 recipes `users` shadow rows, tags `r11-rehearsal-a/b`, one `/media` upload. Runbook §3/§4.1/§4.7.1/§5 V14–V16 carry the exact commands. |
