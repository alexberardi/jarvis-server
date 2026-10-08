# recipes (port into jarvisd) — questions for the user

Ask these **one at a time**, most consequential first. Record each answer here as a decision (RD1,
RD2, …), then fold it into [`00-inventory.md`](00-inventory.md).

Technical choices that need no product call are already made in the inventory:

- the queue mapping (§6) and in-process OCR (§7.3)
- the export bundle + `jarvisd import-recipes` shape (§13)
- the error shapes (§3)
- the freeze/fix calls on bugs no one would want kept (§10)

**Already decided upstream, and not re-asked here:**

- **2026-10-08.** Recipes is a jarvisd module on 7030, wire-compatible, on SQLite, the queue and the blob store.
- **Cutover Q2.** Recipes must work at cutover; ownership is remapped by email.
- **ID6.** Accounts start clean.

**Usage evidence** (prod row counts, from the parent survey):

| Table | Rows |
|---|---|
| recipes | 48 (all by one user) |
| recipe_ingestions (photo imports) | 13 |
| recipe_parse_jobs (webview, photo, meal-plan and grocery jobs together) | 52 |
| stage_recipes (only LLM meal-plan generation creates them) | 9 |
| mailbox | 10 |
| meal_plans | 2 (users 1 and 4) |
| grocery_sku_map | **0** |
| settings | 0 |

## Decisions

| # | Date | Question | Decision |
|---|---|---|---|
| RD2 | 2026-10-08 | RQ2 core recipes | **(a) drop from LLM plan candidates**; routes stay on the wire; real starter recipes possible later. |
| RD3 | 2026-10-08 | RQ3 editor photos | **(b) relative on the wire, resolved by the app** (user: store relative, the app appends it to its current recipes base URL, so IP moves and LAN/public both work). Server keeps legacy `/media/<name>` (no rewrite). App change in jarvis-recipes-mobile: resolve relative `image_url` against the discovered recipes base URL; absolute URLs (web imports) unchanged. Must ship before cutover. SKU matching prior art: recipes `grocery_service.py` + queue worker (ported per RD1). |
| RD1 | 2026-10-08 | RQ1 scope | **(a) port everything**, including the LLM SKU-matching job (user). |

## Queue

### RQ1 [scope] Port every feature, or cut the ones with no use?

**Context.** Each pipeline is a step in the port plan and a surface to maintain. On prod:

- **URL import (webview):** most of the 48 recipes probably came this way. Keep, no question.
- **Photo import (OCR + LLM):** 13 attempts. In use.
- **LLM meal-plan generation:** 9 stage recipes, so it ran at least a few times. But only 2 plans were ever committed, and
  its candidate list is padded with placeholder recipes (RQ2).
- **Random "quick plan" + planner + shopping list + staples:** the deterministic core the app is built around.
- **Grocery cart + SKU mapping:** **zero mappings ever saved**. So the Walmart cart has never had anything in it, and
  the background LLM SKU-matching job (R5) has never had a candidate to match against. A mapping is created only by
  hand-picking a product in the app's Walmart WebView.

**Options.**

- (a) **Port everything** as specified.
- (b) **Keep the grocery routes, cut the LLM SKU-match job.** `PUT/GET/DELETE /grocery/sku-map` and `POST /grocery/cart` stay. They are
  deterministic and cheap, and the app's Shopping List screen calls the cart. `match_job_id` is always null. R5 shrinks by a
  queue handler and a prompt golden.
- (c) Also cut the LLM meal-plan generation. The app's "plan with AI" flow would then fail. That needs either an app change
  to hide it, or a stub that returns a job that ends in ERROR "unavailable".
- (d) Cut the grocery feature entirely (the routes 404, and the app's cart button breaks).

**Recommendation: (b).** Everything with evidence of use is ported. The one piece with provably no input to work on
(the matcher, with an empty map) is dropped, and can come back as a follow-up if anyone starts mapping products. LLM
generation stays, because the in-process LLM makes it cheap, and RQ2 fixes its worst output.

### RQ2 [behaviour] The 190 "core" stock recipes in LLM meal plans

**Context.** `static_data/stock_recipes.json` holds 190 recipes. Every one has the ingredients "Ingredient 1", "Ingredient 2" and
"Ingredient 3", and generic steps ("Prepare ingredients.", "Cook as needed."). LLM generation appends them to every
slot's candidate list. When the model picks one, it is copied into `stage_recipes` and, on commit, becomes a real recipe in the
household's box with placeholder ingredients. A legacy indentation bug (B17) also appends them once per user recipe and
leaves them out entirely when the box is empty.

**Options.**

- (a) Drop core recipes. Generation picks only from the household's own box.
- (b) Keep them, with the bug fixed (append once).
- (c) Replace them with real curated recipes. That is content work, and a later feature.

**Recommendation: (a).** It removes fake ingredients from shopping lists and plans. The routes (`GET /recipes/core/{id}`,
`/recipes/stage/{id}`, `source:"stage"` on commit) stay on the wire, so nothing breaks in the app. (c) can come later as
"starter recipes".

### RQ3 [bug fix] Photos uploaded in the recipe editor never display

**Context.** The editor's "add photo" uploads to `POST /recipes/import/image`. That returns
`image_url: "/media/<name>.jpg"`, a **relative** URL. The app stores it on the recipe and renders it as
`<Image source={{uri: "/media/…"}}>`, which React Native cannot load. So uploaded photos have never shown. Recipes
imported from a URL carry the site's absolute image URL and do show.

**Options.**

- (a) **Server-side fix.** Keep storing `/media/<name>`, but render `image_url` on the way out as an absolute URL for
  the host the request came in on. This is the same "answer in kind" rule `/services` already uses for the Cloudflare
  public URL (2026-10-07 decision): a LAN request gets `http://<lan>:7030/media/…`, a tunnel request gets
  `https://<public>/media/…`. No app release is needed, and stored data stays portable.
- (b) App fix: prefix relative URLs with the recipes base URL. That needs a mobile release before cutover.
- (c) Leave it broken.

**Recommendation: (a).** It works with the app already in people's hands, which matters because cutover is one
coordinated release. It is also only a string rewrite in `RecipeRead` and `MealPlanRead`. The contract freezes `image_url` as a string either
way.

### RQ4 [data] When a user deletes their account, what happens to recipes they shared with the household?

**Context.** Legacy had no deletion purge for recipes at all. Its FK `recipes.user_id → users ON DELETE CASCADE` would
delete everything the user authored, **including household-visible recipes and plans** other members use. jarvisd runs
module deletion hooks inside the account-deletion transaction (D20), and the recipes module needs one. This also applies when
a member is removed from a household (`OnMemberRemoved`).

**Options.**

- (a) **Keep household-shared rows.** Delete only the leaver's private rows (no household), jobs, imports and staged
  recipes. Their recipes, plans, staples and SKU mappings stay with the household, with `user_id` kept as a dangling author id
  (nothing displays it).
- (b) Delete everything they authored (legacy cascade semantics).
- (c) Keep the shared rows, but reassign authorship to the household's admin.

**Recommendation: (a).** A recipe box is a kitchen's, not a person's ("we always have olive oil" is a fact about a kitchen,
per the legacy code's own comment). Removing a member shouldn't empty the family's recipes. When the whole household is
deleted, everything goes (`OnHouseholdDeleted`).

### RQ5 [bug fix] Prep and cook times are silently discarded

**Context.** The editor sends `prep_time_minutes` and `cook_time_minutes`, but there are no such columns. The server folds them into
`total_time_minutes` (only when total is empty) and returns null for both. So a user who enters "prep 10, cook 20"
sees both fields empty when they reopen the recipe. Imported drafts carry real prep and cook values, which are lost the same way.

**Options.**

- (a) **Add the two columns and return them** (migration 00002). The wire shape is unchanged; the values stop being null.
  Imported prod rows keep null, as their data is already gone.
- (b) Freeze the legacy behaviour.

**Recommendation: (a).** It costs little and nothing depends on the nulls. The app already reads the fields.

### RQ6 [data] Import job history and photo-import originals? And how long to keep the originals?

**Context.** Prod has 52 parse jobs, 13 photo ingestions (their photos are in MinIO, about 13 × ≤8 images) and 9 staged
recipes. None of them is shown to anyone:

- The app lists only COMPLETE jobs from the last 3 days.
- Staged recipes expire after 72 h.
- The ingestion photos are OCR inputs, not the recipe's picture.

Legacy also never deleted those photos.

**Options.**

- (a) **Import only** recipes, plans, staples and SKU mappings. Keep photo-import originals for **30 days** after the job,
  then delete them with the OCR readings (the hourly cleanup).
- (b) Also import jobs, ingestions and the MinIO photos (for debugging old imports).
- (c) Like (a), but keep originals forever.

**Recommendation: (a).** Nothing user-visible is lost. The cutover gets simpler (no MinIO copy). The originals are
transient working data and are kept long enough to debug a bad import.

### RQ7 [behaviour] Users in several households see only their first household's recipes

**Context.** Scoping comes from the `household_id` in the JWT, which jarvisd (like legacy) fills with the user's **first**
household. The recipes app has no household picker. You've said users can belong to several households, and on prod 2 of
8 households have 2 members.

**Options.**

- (a) **Keep it: the first household's box** (legacy behaviour, zero app change). Revisit when the app gets a household
  switcher.
- (b) Show the union of every household the user belongs to. Writes still need one household, so it would be
  the first. Reads would mix boxes.
- (c) Honour an optional `X-Household-Id` header (validated against membership) so a future app version can switch.
  Without the header, it is (a).

**Recommendation: (a) now, with (c) as a small follow-up** once an app wants it. A union (b) makes plans and shopping lists
ambiguous about whose kitchen they are for.

### RQ8 [privacy] Drop the `r.jina.ai` fallback and the scraper cookies?

**Context.** When a recipe site blocks the server's fetch, legacy retried through **`https://r.jina.ai/<url>`**, a third-party
reader proxy, and sent `SCRAPER_COOKIES` along with it (B11). The fallback also never worked: jina returns markdown, which fails the HTML
check. Since the webview change, the main import path doesn't fetch pages on the server at all. The phone does. Server
fetching is left only in the preflight check.

**Options.**

- (a) **Drop both.** No third-party proxy, no cookie jar. A blocked site goes to the webview flow, as it already does.
- (b) Keep the jina fallback behind the existing cc privacy setting `web_scraping.allow_external`, and drop the cookies.

**Recommendation: (a).** It is dead code that sends URLs (and cookies) off the box, against the private-by-default
principle. The webview already covers blocked sites.

### RQ9 [scope] Meal-plan preferences the server ignores

**Context.** The LLM planner's request accepts `allergens`, `diet`, `cuisines`, `max_prep_minutes`, `max_cook_minutes` and
`repeat` ("meal prep: same/similar ×N"). The server **ignores all of them**. `excluded_ingredients` matches recipe *titles* only.
The app currently sends only `days` (no preferences), so no user has noticed.

**Options.**

- (a) **Freeze: port as is** (accepted and ignored), and file the features as future work.
- (b) Implement allergens/excluded ingredients against ingredient text, and the time limits in the candidate query, as
  part of R9.

**Recommendation: (a).** No client sends them today, and this is a port, not a feature pass. It is listed so it isn't
mistaken for working behaviour.
