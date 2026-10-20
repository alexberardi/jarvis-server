# Inbox attachments: upload first, then name the ids in the item

- **Date:** 2026-10-17 (agent, item #184; the user's ComfyUI plan asked for the piece and for this
  file. Held for the user's review: it adds a member route to the gateway's tier table.)

Inbox items can carry images and video, so a command package (jarvis-cmd-comfyui, #188) can
deliver what it made to the user through the inbox. This file is the wire contract for the
siblings: mobile (#185), the SDK's inbox API (#186) and the Python node (#187).

## Upload, then create (not multipart)

A producer uploads each file on its own, then creates the item with the ids:

- `POST /api/v0/inbox/attachments` on the notifications listener (app-to-app credentials), or
  `POST /api/v0/node/inbox-attachment` on cc (node credentials). The body is the raw file and
  `Content-Type` its type. Answer `201` with the attachment (below). `413` over the per-file cap,
  `415` for anything that is not an allowed type.
- `POST /api/v0/inbox` and `POST /api/v0/node/inbox-item` take an optional
  `"attachment_ids": ["…"]`, in display order (duplicates are ignored). `422` when an id is
  unknown, already used, someone else's upload or (for a node) another household's; `413` when
  the item's files together are over the per-item cap. Nothing is created then, and the uploads
  stay usable.

Why not multipart on the create routes: the JSON create contracts (the legacy shape the contract
suite pins, the node route's attention gate) stay exactly as they are; the SDK and the node can
stream a large file and then post a small JSON body; #187 only forwards ids; and one upload path
serves both creators. The cost is a second request and uploads that are never claimed, which
the sweep removes after an hour.

An upload belongs to its uploader (`app:<id>` or `node:<id>`) and only that uploader can claim
it. A node's upload is also bound to the node's household. The claim happens in the same
transaction as the item insert.

## Types and what is stored

Allowed: `image/png`, `image/jpeg`, `image/webp`, `image/gif`, `video/mp4`, `video/webm`. The
type comes from the content's magic bytes (`http.DetectContentType`, plus an ISO BMFF `ftyp`
check for MP4 that refuses HEIF/AVIF/QuickTime brands). A declared `Content-Type` other than
`application/octet-stream` must agree, so a mislabelled file is refused rather than guessed.
SVG is not allowed (it is a script vehicle). (Amended 2026-10-20: jarvisd's own errand spreadsheets may be `.xlsx`, in process only; see [2026-10-20-errand-spreadsheets](2026-10-20-errand-spreadsheets.md).)

Bytes go to jarvisd's blob store (the FS backend under the data dir, not MinIO),
content-addressed at `inbox/sha256/<ab>/<sha256>`. Identical files share one blob. Rows are in
`notifications_inbox_attachments` (`docs/schema/notifications.md`).

Caps are settings: `inbox.attachment_max_file_bytes` (default 25 MB) and
`inbox.attachment_max_item_bytes` (default 50 MB).

## The item JSON

Items with attachments gain `"attachments"`, in order:

```json
{"id": "uuid", "mime": "image/png", "size": 12345, "width": 1024, "height": 768, "duration_ms": null}
```

`width`/`height` come from the image header (png, jpeg, gif, webp) and are null for video.
`duration_ms` is always null for now, and there is no video thumbnail: both need a video parser
or ffmpeg, which jarvisd doesn't carry. The fields are there so a later change can fill them.
**Items without attachments omit the key** rather than sending `[]`. The contract suite matches
the item shape exactly against the legacy service, so an always-present key would break parity.
Clients treat a missing key as no attachments.

## Download

`GET /api/v0/inbox/{item_id}/attachments/{attachment_id}` (user JWT; through the admin gateway
at `/api/notifications/…`, tier user). It uses the same visibility rule as the item: the item's
user, or any member for a household-wide item. Anyone else gets 404. It serves the bytes with the
stored type, `X-Content-Type-Options: nosniff`, `Content-Disposition: inline`, a sandboxing CSP,
an ETag (the hash) and Range support (206) for video seeking. It does not mark the item read.

The web admin authenticates with a bearer header, so `<img src>` cannot point at the route. It
fetches each file with the token into a `blob:` URL. That is fine up to the caps but loses
streaming (a video downloads fully before it plays). Signed or query-token URLs would fix that,
but they are an auth change, so they are left for a later decision. Mobile can send the header on
its media requests and stream with Range.

## Deletion

Rows go with their item (`ON DELETE CASCADE`): single delete, bulk delete, `/me/data`, and the
user/household purges. Blobs are shared, so they are never deleted inline. The
`notifications.cleanup` job (daily) and `POST /api/v0/admin/cleanup` sweep uploads unclaimed
after an hour and every blob under `inbox/` that no row references and that is over an hour old.
The hour of grace covers an upload between writing its blob and its row. So a deleted item's
files leave the disk at the next cleanup run, up to a day later. There is no inbox retention
(nothing deletes old items); if one is added, its deletes get the same sweep for free.
