# Chat images and context compaction (2026-10-09)

**Status: built 2026-10-09 (branch `feat/chat-images`): §6 wire contract as written, details in §7. No DB migration.**

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
totals, "save as recipe" → recipes import); share sheet into Jarvis; camera snapshots via go2rtc
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

**Settings (cc, system scope).** `cc.compaction.threshold` Float 0.75 (0.3–0.95);
`cc.compaction.hard_threshold` Float 0.90 (0.5–0.98, never below the threshold at runtime).

**Admin.** Each local live/background card shows an "Image input" switch for `llm.<slot>.mmproj`
`""` (on) / `none` (off), whether the running engine has vision, and for background that off also
turns off LLM vision in recipe photo import. An explicit projector id/path keeps the raw select
under Advanced.

**Decided while building (not in §5):** the summary is a `system` message right after
`messages[0]`; the trigger is the last live call's `prompt_tokens`; the hard threshold is
validated separately and clamped to ≥ the async one; descriptions are prepended to the user's
own text rather than replacing the whole message.

