# Chat images and context compaction (2026-10-09)

**Status: built 2026-10-09 (branch `feat/chat-images`): §6 wire contract as written, details in §7. No DB migration.**
**Photo → action (§8): built 2026-10-10 on branch `feat/chat-image-actions`, pending review. No DB migration, no wire change.**

## 1. Goal

Send images with chat messages from the mobile app (camera or gallery). The live model answers
about them and can act through the existing tools. Long conversations stop failing at the context
limit: older turns are summarized ("compacted"), the way hosted chat assistants do.

## 2. What exists

- **Vision in the LLM layer:** a slot loads a vision projector when its model has one. Per-slot
  setting `llm.<slot>.mmproj` (`internal/modules/llm/engine/config.go`): empty = the model's own
  projector if installed, `none` = no vision, or an installed id/path. The endpoint reports
  `Vision`; `/v1/models` shows `supports_images`; requests with images to a non-vision endpoint are
  refused (`llm/service.go`). Qwen3.5-9B on the dev box: `supports_images: true`.
- **Recipes** already use background-slot vision for photo import (OCR fallback).
- **Mobile chat:** `POST /api/v0/mobile/chat` (`cc/mobile_chat.go`), text only (`chatMessageMax`
  5000), SSE replay; conversation cache with a sliding 10-minute idle TTL (`cc/convcache.go`).

## 3. Design so far

- **No new image setting.** The per-slot switch is `llm.<slot>.mmproj`; the admin shows it as an
  "Image input" on/off toggle per slot (on = `""`, off = `none`). Default: on wherever the model
  has a projector. Turning live off also frees the projector's VRAM.
- **Capability to the app:** the app shows the image picker only when the live endpoint reports
  vision. The server exposes that (e.g. in the household settings or a chat capabilities call).
- **App:** camera + gallery button by the chat input; images downscaled on the phone to ~1024 px
  JPEG before upload; a few images per message at most.
- **Server:** the chat turn accepts text plus images and sends OpenAI-style `image_url` content
  parts to the live slot.
- **History:** an image stays in the conversation only for the turn it was sent; afterwards it is
  replaced by a short description, so follow-ups work without re-paying the image's tokens.
  Images are not written to disk by default.
- **Compaction:** when a conversation's prompt nears the slot's context size, older turns are
  summarized into one message and the recent turns kept verbatim. The image-to-description swap
  is the same mechanism applied early.

## 4. Later (deep dives)

Photo → action through existing tools (flyer → calendar/reminder, fridge → shopping list, receipt
totals; "save as recipe" → recipes import and the shopping list are done, §8); share sheet into Jarvis; camera snapshots via go2rtc
("who's at the door?", also by voice); memories from images; camera-equipped nodes.

## 5. Decisions

| # | Date | Question | Decision |
|---|---|---|---|
| CI1 | 2026-10-09 | Image switch: new setting or existing? Default? | Per slot (live/background, user). Reuse `llm.<slot>.mmproj` as the switch, shown as a toggle; on by default where the model supports it. |
| CI2 | 2026-10-09 | App picker | Camera/gallery by the chat input, gated on live vision (user: "makes sense"). |
| CI3 | 2026-10-09 | Image lifetime in history | Only for the turn sent, then a description (user: "exactly"). |
| CI4 | 2026-10-09 | Compaction | Yes: summarize older turns near the context limit (user idea). |
| CI5 | 2026-10-09 | Who writes image descriptions and compaction summaries | The background slot, as a job right after the reply (user). If background has no vision, live writes the description after replying. A turn arriving before the description is ready still sees the image. |
| CI6 | 2026-10-09 | Compaction thresholds | Async at 75 % of the live slot's context (setting; user found 70 % aggressive), synchronous at 90 %. Applies to every cc conversation (chat and voice share the cache). |
| CI7 | 2026-10-10 | Which tools can take a photo | Any tool: an `image` parameter type in jarvis-command-sdk (and server tools), so third-party/Pantry commands get photos too. The model passes the image number; the runtime swaps in the bytes, including in client `tool_call`s to nodes. Replaces the hard-coded `PhotoTools` gate; recipes becomes the first user (user: "so other 3rd party tools can take advantage"). |
| CI8 | 2026-10-10 | Photo with no instruction: act or offer | Offer: describe it and suggest the matching action; act only on an explicit request (user: "I don't like letting llm's implicitly make decisions that have repercussions without a confirmation"). Implies photos stay available to tools for the conversation's lifetime, so the follow-up "yes" works. |

## 6. Wire contract (app ↔ jarvisd)

- `GET /api/v0/mobile/chat/capabilities` → `{"images": bool, "max_images": 4, "max_image_bytes": 2097152}`.
  `images` is true only when the live endpoint reports vision. 404 (legacy server) = no images.
- `POST /api/v0/mobile/chat` gains optional `images: [{"mime": "image/jpeg", "data": "<base64>"}]`
  (at most `max_images`, each at most `max_image_bytes` decoded; jpeg/png/webp). `message` may be
  empty when images are present. Images while vision is off → 422
  `{"code": "images_unavailable", "detail": …}`; too many/too big/bad type → 422 with
  `code` `images_invalid`.
- The SSE stream is unchanged.
- Additive leniency (no client needs it): with images, `message` may also be absent; `data` may be a
  whole data URL. The declared `mime` is not trusted: the type is sniffed from the bytes.

## 7. As built (2026-10-09)

**Code:** `internal/modules/cc/chat_images.go` (capabilities, validation, description job),
`compaction.go`, plus `convcache.go` (message ids, `rebaseCommit`), `turn.go`, `continue.go`,
`mobile_chat.go`. Admin: `web/admin/src/components/models/LabelsEditor.tsx` (`ImageInputToggle`).

**Capabilities.** `images` = the live slot resolves and its endpoint reports `Vision` (a slot that
is loading or failed reports false). cc gets slot info through `Module.Endpoints` (the llm
resolver, wired in `cmd/jarvisd`).

**Chat turn.** Order: body (400s) → household/node gate (403/404) → images: live vision off → 422
`images_unavailable`; then count, base64, decoded size, sniffed type → 422 `images_invalid`
(`{"detail", "code"}` via `detailCode`). Only `POST /mobile/chat` gets the bigger body cap
(`httpx.MaxBody` + 4 × base64 of 2 MiB ≈ 12.2 MB); warmup keeps 1 MiB. The user message carries
the images in memory (`chatMsg.images`, as data URLs) and `toLLM` sends image parts first, then
the text, on every live call of the turn, tool-loop continues included. An empty message with
images skips the STT-noise prefilter. Traces and transcripts record `[image]` per image, never
data.

**Descriptions (CI3/CI5).** After the `done`/`error` event the turn queues `cc.chat_image_describe`
with `{conversation_id, message_id}` only. The job reads the images from the cached conversation,
asks background (if it has vision), then live (if it has), for a 1–3 sentence factual
description per image (max 200 tokens, thinking off); a failed call falls through to the next
slot; with neither, the text is `not described`. It then prepends `[image 1: …]` lines to that
message's text and drops its images, matching the message by id under the conversation lock.
Turns arriving earlier send the real image. After a restart the job finds nothing (the cache is
memory only). The log line has counts and the slot, never the description.

**Vision lost mid-conversation** (Image input switched off while a description is pending): the
live request gets `[image]` text instead of the parts the engine would refuse (`visionGuard`).

**Compaction (CI4/CI6).** Every live call records `usage.prompt_tokens` on the conversation.
After a finished turn (not while client tool calls are outstanding), prompt ≥
`cc.compaction.threshold` × the live endpoint's per-request context queues `cc.conv_compact`
(dedup per conversation). A turn arriving with prompt ≥ `cc.compaction.hard_threshold` × context
compacts first, synchronously (span `compaction`). Both summarize everything between
`messages[0]` and the last 2 turns: background slot first, live as fallback, max 400 tokens. The
result is `messages[0]` unchanged (byte-exact, so the warmed prefix cache stays valid), one
`system` summary message (`Summary of the earlier conversation …`; a later compaction folds it
into the next summary), then the last 2 turns verbatim. Turns split only at a user message that
opens a new exchange, so a tool call never loses its result (nor the text path's format prompt
its turn); per-turn transient blocks move with their turn. The async job re-checks the summarized
messages (ids and content) under the lock before splicing; anything changed → it gives up and
the next turn retriggers. After a compaction the prompt size is unknown (0) until the next live
call. Log: `cc: conversation compacted` with mode, message counts, turns summarized, prompt
tokens, context. `conversation.max_turns` (default 10) still trims first, so with defaults
compaction mostly matters for long tool outputs or a raised `max_turns`.

**Continue stream (voice).** The planned commit is rebased onto any description or compaction
that landed during the stream (`rebaseCommit`), so neither overwrites the other.

**Remote slots.** A remote endpoint's context is unknown unless the operator sets
`llm.<slot>.context` (now honoured for `engine=remote`, shown as "Context window" in the admin's
remote section); with 0 there is no compaction for that slot.

**Settings (cc, system scope).** `cc.compaction.threshold` Float 0.75 (0.3–0.95);
`cc.compaction.hard_threshold` Float 0.90 (0.5–0.98, never below the threshold at runtime).

**Admin.** Each local live/background card shows an "Image input" switch for `llm.<slot>.mmproj`
`""` (on) / `none` (off), whether the running engine has vision, and for background that off also
turns off LLM vision in recipe photo import. An explicit projector id/path keeps the raw select
under Advanced.

**Manual check (2026-10-09, this box, scratch home, dev build).** The system jarvisd's engine
needs its API key, so a CPU-only llama-server (`-ngl 0 -dev none`, qwen3.5-9b + mmproj, `-c 8192`)
served the scratch jarvisd's live and background slots as remotes. Capabilities `images: true`; a
flyer JPEG ("BAKE SALE / Saturday 10 AM", orange circle) got a correct answer; the background job
described it (`slot=background`); a text follow-up ("what day, what colour?") was answered from the
description; an empty message with two images worked; GIF bytes → 422 `images_invalid`. At
6698/8192 prompt tokens the async compaction ran (`turns_summarized=1`) while a second description
was pending, and both landed; the next turn still knew the conversation. Traces show `[image]`
markers, job payloads hold ids only, and the log has no image data. Not exercised live: the
synchronous path (unit-tested).

**Decided while building (not in §5):** the summary is a `system` message right after
`messages[0]`; the trigger is the last live call's `prompt_tokens`; the hard threshold is
validated separately and clamped to ≥ the async one; remote slots use `llm.<slot>.context` as their
window; descriptions are prepended to the user's
own text rather than replacing the whole message.


## 8. Photo → action (2026-10-10, pending review)

A photo in chat can now drive an action. First: **"save this as a recipe"** runs the recipes photo
import on the attached photo and saves the result. Also checked: a photo of a list → the existing
`shopping_list` tool, with no change.

### 8.1 Server tools see the turn's photos by number

- `servertools.Turn.Images` (`servertools/images.go`, interface `TurnImages`) resolves photos by
  their 1-based number in the message ("image 1", "image 2"). The model passes numbers, never
  data. `ImageNumbers(call, "images")` reads an optional `[1, 2]` argument (absent = all photos).
  `ResolveImages` returns the bytes and sniffed type.
- The engine fills `Turn.Images` for mobile chat conversations only (`chatUserID != 0`), per tool
  call, from the working history (`cc/chat_image_actions.go`, `turnImagesOf`). It walks back to
  the latest user message that ever had photos:
  - photos still attached → those (the current turn's, or an earlier turn's whose description
    hasn't landed yet: exactly what the model sees);
  - only the description left (`[image N: …]`, or visionGuard's `[image]`) →
    `ErrImagesExpired`;
  - none → `ErrNoImages`;
  - a number out of range → `*ImageIndexError` ("there is no image 3: only image 1 is attached").
- Bytes stay in memory. Tool results, traces, job payloads and logs never carry them (unit test,
  plus a leak check on the manual run).

### 8.2 `save_recipe_from_image`

- **Tool** (`cc/chat_image_actions.go`): args `{"images": [int]}` (optional). The description
  covers "save this recipe", "add this to my recipes", "keep this one", "import this recipe". Its
  `included_system_prompt_text` (tool guidance; native providers print it, ChatGPT's prompt has
  no guidance section):
  - call the tool every time the user asks, for a photo attached now or earlier;
  - never claim a save the tool didn't make;
  - don't transcribe the recipe, and don't use a list or note tool for it.
- **Offered** only in mobile chat, when the live slot reports vision at warmup (the only way a
  photo can arrive) and the recipes module is wired: `prompts.ToolGates.ChatPhotos`, on both the
  native and the text path. Voice conversations' prompt bytes are unchanged.
- **Refusals** the model relays:
  - `no_speaker` (no known user: voice);
  - `no_image`: ask the user to attach the photo;
  - `image_expired`: "That photo is no longer available … Ask the user to attach the photo
    again";
  - `invalid_image`: a bad number, or a photo the import refuses (too many, too big, not
    decodable);
  - `invalid_arguments`;
  - `import_failed`: a server error, logged.
- **Success** returns `{"success": true, "status": "processing", "images": n, "message": …}` and
  the model says it's being saved.
- **Recipes side** (`recipes/photo_chat.go`): `Module.ImportRecipePhotos(ctx, userID,
  householdID, photos)` is the in-process entry point. cc sees it through the `cc.RecipeImporter`
  interface, wired in `cmd/jarvisd/main.go`. It is `POST /recipes/from-image/jobs` without HTTP:
  - the same limits (1–8 photos, `image.max_bytes`) and `preparePhoto`;
  - the same `queuePhotoImport` (factored out of the route) → ingestion row + `recipes.image` job;
  - the same pipeline: every OCR engine in process, quality gate, P2/P3.
  - Several photos = the pages of one recipe, in the order the model named them (the import is
    multi-page already).
- **Ownership** is the route's: `resolve(authn.User{ID, HouseholdID: chat household})`. The user
  is the author; the write household is the chat's household while they're a member, else their
  first membership, else none (private). This is RD7, exactly as a token naming that household
  would get.
- **Saving.** The job data carries `"save": true, "origin": "chat"`. After `markComplete`,
  `saveDraft` turns the draft into a recipe, mapped as the recipes app's review screen does:
  - ingredient text = name, plus "— notes"; quantity and unit in their columns; a unit repeated
    at the start of the name is dropped ("cloves garlic" + unit "cloves" → "garlic");
  - steps numbered from 1; servings = the leading number;
  - `source_type: "image"`.
  The insert goes through the same `insertRecipe` as `POST /recipes`, and the job turns COMMITTED
  in the same transaction, with `recipe_id` in its result. A job canceled meanwhile is left alone.
  The app's own photo import is unchanged (the draft waits for review; tested).
- **Telling the user (async).** `notifySavedImport` creates an inbox item, then pushes to the user,
  the way deep research reports (category `recipe`, source `jarvis-recipes-server`):
  - saved: "Recipe saved: <title>", summary "N ingredients, M steps…", the recipe as the body;
    push data `{type: "recipe_saved", recipe_id, parse_job_id, inbox_item_id}`;
  - failed: "Recipe not saved" with the reason. The quality gate's own advice is used as is;
    other codes get a generic "try a straight-on photo…". Push type `recipe_import_failed`.
  - Mobile shows category `recipe` with the default inbox detail screen and colour; no app
    change.

### 8.3 Other photo → action through existing tools (manual, no code)

- **List photo → shopping list: works as is.** A photo of "Need from store: eggs, oat milk,
  bananas, coffee beans, dish soap" + "add these to my shopping list" → the model called the
  node's `shopping_list` `{"action": "add", "items": [all five]}`. No prompt change needed. The
  node round trip was not exercised: the scratch node was offline. With an offline node, the
  9B model retries until "Too many tool iterations". That happens for a plain text "add milk"
  too, so it is not image-specific (logged as a follow-up).
- **Flyer → reminder: not reliable yet, nothing changed.** "BOOK SWAP, Saturday, October 17,
  2:00 PM" + "remind me about this" → `reminder` `{action: set, text: "book swap at Maple Street
  Library", time: "14:00"}`: the right text and time, but `resolved_datetimes` was an ISO date for
  the next day, not October 17. The DT_KEYS vocabulary is relative ("tomorrow", "next_saturday");
  an absolute date on a flyer has no key. Fixing that is a date-resolution change, not a prompt
  tweak; left for a decision.

### 8.4 Manual check (2026-10-10, this box)

Throwaway jarvisd from the branch:
- scratch home, loopback only, ports 37xxx; the system service's unreadable
  `/etc/jarvisd/jarvisd.env` hidden by a bwrap tmpfs for that process only;
- a CPU-only llama-server (`-ngl 0 -dev none`, Qwen3.5-9B Q4_K_M + mmproj F16, `-c 12288`) as the
  live and background remotes, behind a small logging proxy that recorded tool calls, never
  image parts;
- tesseract the only OCR engine; a rendered 1000×1300 recipe card JPEG ("Lemon Garlic Roast
  Chicken", 8 ingredients, 6 steps).

Results:
- **"save this as a recipe"** → `save_recipe_from_image {}` → reply "Got it, I'm saving that Lemon
  Garlic Roast Chicken recipe for you…" (111 s on CPU, cold).
  - The job ran tesseract → P2 and saved recipe 1 for user 1 in their household: title, servings
    4, prep 15, cook 70, total 85, 8 ingredients, 6 steps verbatim; job COMMITTED with
    `recipe_id`.
  - Inbox item "Recipe saved: Lemon Garlic Roast Chicken" with the recipe as its body; the push
    was logged as `skipped` (no devices).
  - P3 timed out on CPU (30 s) and the draft was kept, as the import does.
- **"add this to my recipes"** → `save_recipe_from_image {"images":[1]}` → recipe 2.
- **Expired:** "what is this?" → "That's a recipe for Lemon Garlic Roast Chicken… Want me to save
  it?". After the description landed, "nice, save it as a recipe" → `image_expired` → "Hmm, the
  photo isn't available anymore. Want to attach it again…?".
- Before the guidance said "call it every time / never claim a save", a follow-up "save that
  recipe photo again" got "I'll save that… right away" with no tool call. The guidance was
  tightened, and the expired run above is from after that change.
- **Leaks:** no image data in the log (base64 probes, `data:image`), in the traces (`[image] save
  this as a recipe`), or in the job payloads (ids only).

### 8.5 Decided while building

- **Chat saves directly, no review step.** The user asked to save, so the job commits the draft
  instead of leaving it in the recipes app's import list. The app's own import still waits for
  review.
- **The outcome goes to the inbox plus a push** (deep research's pattern). The tool doesn't wait
  for the job: OCR + P2 take tens of seconds on a GPU and minutes on CPU.
- **Photos by number, resolved per call** from the working history, so tool-loop continues see
  the same photos as the model.
- **Expired = the latest photo message carries only its description.** Per spec, the tool doesn't
  keep pixels past the description (CI3).
- **Offered only in vision-capable mobile chats.** No prompt bytes change for voice or for chats
  without image input.

### 8.6 Open questions

1. **Keep the photo for tools a little longer?** The model itself offers "Want me to save it?"
   after describing a photo, but by the time the user says yes, the description has replaced
   the photo. **Recommendation:** keep the last photo message's bytes in memory, for tools only
   (not sent to the model), until the conversation expires (10-minute idle TTL, at most 4 × 2 MiB
   per conversation). Today's behaviour is the spec's (CI3: resend).
2. **Absolute dates for flyer → reminder/calendar** (§8.3). **Recommendation:** let
   `resolved_datetimes` accept an ISO calendar date the user's input (or the photo) states, or add
   date keys like `2026-10-17`, then retest. Out of scope here.
3. **Push deep link.** `type: "recipe_saved"` carries `recipe_id`, but the node app opens the
   inbox item; the recipes app is a separate app. **Recommendation:** keep the inbox item; teach
   the recipes app later if wanted.
