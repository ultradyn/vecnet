# 20: Operator attachments in the web UI and TUI

Raised by Max, 2026-11-02: "as an operator, can I send a screenshot along with a message?"

## Today (operator-ui branch)

- Hub: full support. `PUT /v1/blobs` (25 MiB per blob, 500 MiB/day per peer), up to 16 `attachments` per message by sha256, `GET /v1/blobs/{sha}` for participants and the operator UI (PROTOCOL §2.7, §11).
- CLI: `vecnet send … --attach FILE` and `vecnet reply … --attach FILE`, repeatable.
- Web UI: shows attachment chips and previews text attachments up to 1 MiB. No upload in the composer or thread reply box, and images do not render inline.
- TUI: lists attachments and saves them to disk from the chat screen. No upload.

## Wanted

1. Web composer and thread reply: attach by file picker, drag-and-drop and clipboard paste (screenshots). Upload each to `PUT /v1/blobs` with progress, then send with `attachments: [{sha256, size, media_type, name}]`. Show 413 `blob_too_large` and 429 `upload_quota_exceeded` plainly.
2. Web thread: render `image/png|jpeg|gif|webp` attachments inline as thumbnails via a `blob:` URL from an authenticated fetch (the CSP's `img-src` must allow `blob:`; check `ui.CSP` and record the change). Never `image/svg+xml` inline. Click opens full size.
3. TUI composer: `:attach PATH` (repeatable) before send, shown as chips; same errors.
4. Screen shots in four states for each, as in the operator UI plan.

Follows the operator UI build; not part of it.
