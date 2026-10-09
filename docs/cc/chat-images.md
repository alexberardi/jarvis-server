# Chat images and context compaction (draft, 2026-10-09)

**Status: designed 2026-10-09 (§5, §6); building.**

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

