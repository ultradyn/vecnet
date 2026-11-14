# Operator Attachments Implementation Plan

> **For agentic workers:** executed by `docs/plans/vecnet-core-build.workflow.mjs` with `docs/plans/attachments.workflow-args.json`. Steps use checkbox (`- [ ]`) syntax.

**Goal:** The operator can attach files (picker, drag-and-drop, pasted screenshots) to messages from the web UI and the TUI, and sees image attachments inline in the web thread.

**Architecture:** The hub already does the work: `PUT /v1/blobs` takes the operator's bearer (charged to its operator peer), a send may carry attachments by sha256 with empty text, and `GET /v1/blobs/{sha}` serves any blob to the operator, sandboxed with `nosniff`. This plan is client work: one shared web upload module wired into the new-message form and the thread reply box; inline thumbnails fetched with the bearer and shown from `blob:` URLs; `:attach`/`:detach` in the TUI chat; and `opclient.Upload`.

**Tech Stack:** as the operator UI plan: Go 1.26 (`CGO_ENABLED=0`), vanilla ES modules in `internal/ui/static/`, Bubble Tea v2 TUI, Playwright and private tmux screen scripts.

**Spec:** `.scratch/vecnet/issues/20-operator-attachments.md`; PROTOCOL §2.2 (steps 6–7), §2.7, §11, §14.2; `docs/plans/2026-11-01-operator-ui.md` (its Global Constraints still apply except where this plan says otherwise). Reviewed against the code before the build: `.scratch/vecnet/build-attachments/plan-review.md`.

## Dependency map

```json
{"1":[],"2":[],"3":[2],"4":[1,2],"5":[1],"6":[3,4,5]}
```

Build settings: `taskPrefix: "att"`, worktree `.worktrees/attachments`, branch `attachments` off `master`, status files in `.scratch/vecnet/build-attachments/` (already gitignored).

## Global Constraints

- The operator UI plan's Global Constraints apply (build2 with two threads, no HTML sinks, peer strings through `h()`/`textContent` and `clean()`, dark palette, en-AU times, four dark PNGs per UI task, inspect every PNG with the Read tool), with one exception: **`ui.CSP` changes once**, in Task 1, to `img-src 'self' data: blob:`. Nothing else in the CSP changes.
- **Attachment limit:** at most **16** per send, or **15 when the text is over 4096 UTF-8 bytes**, because the hub appends the spilled `message.md` and refuses a 17th (`internal/hub/spill.go:108`: 413 `payload_too_large`, `details.field="attachments"`, `details.limit=15`). Adding a chip is refused past 16; sending checks the spill rule and refuses with "Remove one attachment or shorten the text: the long text needs an attachment slot." The hub's 413 with `details.field=attachments` shows the same text.
- **Blob limits** come from the hub: 25 MiB per blob (413 `blob_too_large`, `details.limit`); daily quota 429 `upload_quota_exceeded` with `details.retry_after_seconds`. Clients check count and size before uploading and still handle both hub errors. Retry times come from `details.retry_after_seconds`, shown in en-AU local 24-hour time. A missing `details.limit` reads as 25 MiB.
- A send may have empty text when it has at least one attachment (the schema has no `minLength`). Text-only sends keep today's rules.
- Uploads go to `PUT /v1/blobs?ttl=86400`, `Content-Type` = the file's declared type without sniffing, else `application/octet-stream`, with an exact `Content-Length`, so an abandoned upload expires after a day.
- **Names:** a picked or dropped file keeps its own name, cleaned and cut to 255 characters. A file taken from a `paste` event is always named `pasted-YYYYMMDD-HHMMSS.<ext>` (local time; ext from type: png, jpg, gif, webp, else bin), because browsers call every clipboard image `image.png`.
- **Idempotency:** a send's signature includes its attachments (sha256s joined with `,`, in order). Changing attachments mints a new key; an unchanged retry keeps it (PROTOCOL §5.3).
- **Inline images (web):** fetch only when the declared `media_type` essence (lowercased, before `;`) is `image/png`, `image/jpeg`, `image/gif` or `image/webp`, the declared `size` is at most 10 MiB, and the attachment has hosted bytes (no fetch for a reference-only `origin_url`). After the fetch, require the response `blob.type` essence to be in the same allowlist and `blob.size` ≤ 10 MiB, else show the chip. Show via `URL.createObjectURL`; revoke on unmount. SVG attachments are also eligible up to 2 MiB, with served `image/svg+xml` essence and size rechecked. Decode SVG only in a detached `Image`, draw to a canvas (longest side 480–2048 px, natural dimensions or inert XML root dimensions/viewBox, else 512×512) and encode PNG; only the PNG URL may reach the DOM/viewer. Revoke the private SVG URL immediately after drawing or failure. Decode/encoding failure, null PNG, tainted canvas or a five-second timeout falls back to a chip. Never `<iframe>`, `<object>`, `<embed>`; never a visible `<a href="blob:…">`. A decode `error` on the `<img>` falls back to the chip. `alt` is set to `clean(name)` explicitly (`h()` does not clean `alt`).
- Any object URL handed to a download is made from `new Blob([bytes], { type: 'application/octet-stream' })`. The bearer never appears in any URL.
- Tasks 3 and 4 never edit `attach.js`, `attach-model.js`, `attach.css`, `api.js`, `index.html` or `base.css`. A missing hook goes in deviations.
- Screen shots go to `docs/plans/shots/oui/`, named `att<NN>-<screen>-<state>.png`. Screen scripts build fixtures at run time (bytes from a literal in the script, large files written under the demo's temp dir); no fixture binaries are committed and the demo seed does not change. Synthetic input: picker via `setInputFiles` or the `filechooser` event; paste by dispatching `ClipboardEvent('paste', { clipboardData })` with a `DataTransfer` built in `page.evaluate`; drop by dispatching `dragenter`/`dragover`/`drop` with a `DataTransfer` handle. Modules must not filter on `isTrusted`.
- Web error shots fake hub errors with `page.route('**/v1/blobs*', r => r.fulfill({ status, contentType: 'application/json', body: JSON.stringify({ error: { code, message, details } }) }))`, because `withDemo`'s `fail()` sends empty details. Progress shots assert the chip's uploading state, not a percentage: an intercepted request shows no real upload progress.
- A peer's raw upload in a script is a plain `fetch(demo.url + '/v1/blobs?ttl=3600', { method: 'PUT', headers: { Authorization: 'Bearer ' + demo.peers.<name>.secret, 'Content-Type': type }, body })`; `asPeer` JSON-encodes bodies and cannot do it. The message is then posted with `asPeer`.
- Every merge runs the whole screen gate (`just ui-shots`), not only unit tests.

## Review Focus

1. **A pasted screenshot sends once.** Paste, send, the reply is lost, send again: one message, same key. Remove the chip and send: a new key.
2. **Hostile attachments.** A peer's attachment with a bidi-override, 150 × `é` name, an `image/svg+xml` file, and an `image/png`-typed blob holding `<html><script>`: names render cleaned and isolated (`unicode-bidi: isolate`), the SVG shows inline only as rasterized PNG and the fake PNG stays a chip; nothing executes, no CSP violation, and no SVG object URL enters the DOM. Include a benign viewBox-only SVG and verify nonzero dimensions.
3. **Upload failure mid-compose.** A 413, a 429 or a dropped connection on one of three uploads: that chip shows its error with Retry and Remove, the others stay, Send stays disabled until every chip is done or removed, and the text is never lost, including when a file is dropped just outside the drop target.
4. **Object URL lifetime.** Leaving a thread revokes its thumbnails' URLs; returning fetches again; redraws do not fetch again or leak URLs; a thumbnail whose fetch lands after two redraws still appears.
5. **TUI path input.** `:attach` with a missing file, a directory, a file over 25 MiB, a path with a space or `~/`: each a clear toast, nothing uploaded, the draft intact.

---

### Task 1: Hub-side checks, CSP and opclient upload

**Depends:** none

**Files:**
- Modify: `internal/ui/ui.go` (CSP and its comment), `internal/httpapi/ui_test.go`, `internal/opclient/client.go`, `internal/opclient/client_test.go`, `internal/hub/spill_test.go`

**Interfaces:**
- Produces: `ui.CSP` with `img-src 'self' data: blob:`; the comment above it says why (thumbnails are fetched with the bearer and shown from object URLs).
- Produces: `func (c *Client) Upload(ctx context.Context, r io.Reader, size int64, mediaType, name string) (envelope.Attachment, error)`. `PUT /v1/blobs?ttl=86400`, `req.ContentLength = size` (so the hub's size pre-check runs and the body is not chunked), `Content-Type` = mediaType (default `application/octet-stream`), the usual bearer. It cannot use `c.hc` (35 s timeout) or `request()` (JSON bodies): use a second `http.Client` with the same `CheckRedirect` and `Timeout: 0` (ctx bounds it), and factor `request()`'s error decoding into a helper both use, so hub errors come back as `*opclient.Error` with status, code and details. Returns `{SHA256, Size, MediaType, Name}`.

- [ ] **Step 1: Write the failing tests**

```go
func TestCSPAllowsBlobImagesOnly(t *testing.T)      // header img-src is exactly 'self' data: blob:; every other directive equals the previous constant
func TestOperatorAttachmentOnlySend(t *testing.T)    // spill_test.go: env.OperatorSecret uploads 3 bytes, sends {text:"", attachments:[it]} into a thread → 202; the recipient's delivery carries it with a blob_url
func TestFifteenAttachmentsPlusSpill(t *testing.T)   // spill_test.go: 15 attachments + 5000 bytes of text → 202; the message holds 16, the spill last (16 + spill is TestSpillNeedsRoomAndReplays: 413, limit 15)
func TestUploadSendsBytesAndType(t *testing.T)       // testhub: Upload returns sha256, size, type, name; GET /v1/blobs/{sha} returns the same bytes; the request carried Content-Length
func TestUploadTooLarge(t *testing.T)                // lower env.Hub.Blobs.MaxSize (as internal/httpapi/blobs_test.go:225 does) → *Error 413 blob_too_large with details.limit
```

- [ ] **Step 2:** `build2 go test -p 2 ./internal/httpapi/ ./internal/hub/ ./internal/opclient/ -run 'CSP|AttachmentOnly|FifteenAttachments|Upload'`. Expected: CSP and Upload fail; the two hub tests should already pass. If `TestOperatorAttachmentOnlySend` fails, the hub has a bug: fix it here with that test and record a deviation. `TestFifteenAttachmentsPlusSpill` must not be changed to 16.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `build2 go test -p 2 ./...` passes.
- [ ] **Step 5: Commit** `feat: blob images in the UI CSP, opclient.Upload`

---

### Task 2: Web upload module

**Depends:** none

**Files:**
- Create: `internal/ui/static/attach.js`, `attach-model.js`, `attach.css`, `internal/ui/jstest/attach.test.mjs`
- Modify: `internal/ui/static/api.js` (one method and one `createApi` option), `index.html` (one stylesheet link)

**Interfaces:**
- `api.upload(file, { onProgress, signal })` → `Promise<{sha256, size}>`, via `XMLHttpRequest` (fetch has no upload progress): `PUT /v1/blobs?ttl=86400`, `Authorization: Bearer`, `Content-Type` per Global Constraints. `onProgress(loaded, total)`. Errors are `APIError` built exactly as `request()` builds them (status 0 code `network` when unreached, `aborted` on abort; `details` from the error envelope), and a 401 fires `onUnauthorized` through the same once-flag. `createApi` takes an injectable `XHR` constructor (default `globalThis.XMLHttpRequest`).
- `attach-model.js` (pure, no DOM, the only part node tests touch): `attachmentName(file, now, { pasted })`, `mediaTypeOf(file)`, `canAdd(items, files)` → `{ files, refused: [{ name, reason }] }` (reasons `too_many` past 16, `too_large` over 25 MiB, `empty` for 0 bytes), `ready(items)` (no chip uploading or failed), `toAttachments(items)` (done chips, in order), `signature(items)` (sha256s joined with `,`), `sendRefusal(items, text)` (the spill-slot message from Global Constraints when the text is over 4096 bytes and 16 chips are done, else `''`), `errorText(err, nowMs)` for 413 `blob_too_large` (limit in MiB), 413 `payload_too_large` with `field=attachments` (the spill-slot message), 429 (time from `details.retry_after_seconds`), network, aborted and others.
- `attach.js`: `createAttachments({ api, onChange })` → `{ el, add(files, { pasted }), clear(), items(), ready(), toAttachments(), signature(), wire(dropTarget, textarea, pickerButton), destroy() }`. `el` is the chip row: cleaned name (bidi-isolated), size, an indeterminate bar until the first progress event and then a determinate one, ✓ when done, the error line with Retry and Remove on failure. Classes are prefixed `up-` (`.att` and `.chip` are already global in `base.css`). `wire` makes `pickerButton` (`type="button"`) open a hidden `<input type=file multiple>`, makes `dropTarget` accept files with a visible drop state, takes files from `paste` on `textarea` (only when the clipboard holds files; text paste untouched), and installs a `document` `dragover`/`drop` guard that stops a near-miss drop from opening the file as a page. At most two uploads run at once; Remove aborts its upload. `destroy()` aborts every upload, removes every listener including the guard, and drops `onChange`.

- [ ] **Step 1: Write the failing tests** (`attach.test.mjs`, node with a fake XHR):

```js
test('attachmentName: picked name cleaned and cut to 255 chars; pasted → pasted-YYYYMMDD-HHMMSS.png from local getters')
test('canAdd: refuses the 17th, a 25 MiB + 1 file and an empty file, each with its reason')
test('sendRefusal: 16 done chips and 5000-byte text → the spill-slot message; 15 chips → empty')
test('signature changes when a chip is removed or reordered, not when progress changes')
test('api.upload: PUT /v1/blobs?ttl=86400 with bearer and Content-Type, reports progress, resolves {sha256,size}')
test('api.upload: 413 → APIError blob_too_large with details.limit; abort → aborted; 401 fires onUnauthorized once')
test('errorText: 429 shows the en-AU time from retry_after_seconds; 413 without details.limit says 25 MiB')
test('ready is false while any chip uploads or failed')
```

- [ ] **Step 2:** `build2 node --test --test-concurrency=2 internal/ui/jstest/attach.test.mjs` fails.
- [ ] **Step 3:** Implement. The progress width goes through `el.style.setProperty('--p', …)`.
- [ ] **Step 4:** All jstest passes; the `internal/httpapi/ui_test.go` size cap, no-sinks and linked-stylesheet tests pass.
- [ ] **Step 5: Commit** `feat: web attachment uploads with progress, drop and paste`

---

### Task 3: Attachments in the new-message form

**Depends:** 2

**Files:** Modify `internal/ui/static/compose.js`, `compose-model.js`, `compose.css`, `internal/ui/jstest/compose.test.mjs`; Create `scripts/ui/att-compose.mjs`.

**Interfaces:**
- `form.attachments` (from `toAttachments()`) and `form.uploadsReady` (from `ready()`) reach the pure model. `buildSend(form, key)` includes `attachments` when non-empty and accepts empty text when attachments is non-empty (the error becomes "text or at least one attachment is required"). `canSend` requires recipients, text-or-attachments, `uploadsReady`, and an empty `sendRefusal`. `keyFor`'s signature includes attachments through `buildSend`.
- The form gains an Attach button (`type="button"`, inside the fieldset so it disables while sending) beside Send, the chip row under the textarea, drop onto the whole form, paste into the textarea. A successful send clears the chips; unmount calls `destroy()`.

- [ ] **Step 1: Failing tests:** `buildSend with only attachments → content.text "" and attachments`; `canSend false while uploads are not ready`; `canSend false with 16 attachments and 5000-byte text`; `keyFor: same text, different attachments → new key`.
- [ ] **Step 2–4:** red, implement, green, then `build2 node scripts/ui/att-compose.mjs`.
- **Shots:** `att03-compose-initial` (two chips done, one picked and one pasted); `att03-compose-progress` (one chip uploading, request held with `hold('**/v1/blobs*')`); `att03-compose-success` (the new thread opened, showing the attachment); `att03-compose-error` (a faked 413 `blob_too_large` with `details.limit` on one chip, Send disabled, text intact).
- [ ] **Step 5: Commit** `feat: attach files to a new message`

---

### Task 4: Thread reply attachments and inline images

**Depends:** 1, 2

**Files:** Modify `internal/ui/static/thread.js`, `thread-model.js`, `thread.css`, `internal/ui/jstest/thread.test.mjs`; Create `internal/ui/static/thumbs-model.js`, `thumbs.js`, `internal/ui/jstest/thumbs.test.mjs`, `scripts/ui/att-thread.mjs`.

**Interfaces:**
- `thread-model.js`: `replyKey(pending, { text, prio, replyTo, sig }, mint)` returns `pending` while all four match, else a fresh `{ key: mint(), text, prio, replyTo, sig }`. `thread.js`'s `send()` uses it, with `sig` from the attachments' `signature()`.
- Reply box: Attach button (`type="button"`), chip row, drop onto the composer, paste into its textarea. `send()` refuses with `sendRefusal` and while `!ready()`. Empty text sends when attachments exist. A successful send clears the chips; a failure keeps text and chips; unmount calls `destroy()`.
- `thumbs-model.js` (pure): `createThumbCache({ api, URL, rasterize, onReady })` → `{ eligible(att), state(att) → { state: 'none'|'loading'|'ok'|'chip', url }, release(), stats }`. `eligible` applies the declared checks; `state` starts one fetch per sha256 for the life of the cache, applies the response checks, and calls `onReady()` once per settled fetch. `rasterize(blob)` is injected and returns PNG bytes; `release()` revokes every URL the cache created, including PNG conversions.
- `thumbs.js`: `createThumbs({ api, onReady })` → `{ render(att) → Node, open(att), release() }`. The thread rebuilds every message node on each store update (`thread.js:349`), so `render` builds from the cache state each time and `onReady` makes the thread re-render; it never mutates a node it returned earlier. Thumbnails sit in a fixed box (max 240×180, `object-fit: contain`) so loading never shifts the layout or breaks bottom pinning. An `<img>` `error` marks that sha256 `chip` in the cache and re-renders. `open` shows the image full size in an in-page dialog (Escape and click-outside close, focus returns to the thumbnail). Non-image attachments keep today's chips; downloading stays in the thread side panel, which already does it safely.

- [ ] **Step 1: Failing tests:** `thumbs.test.mjs` (fake api, fake `URL`): `svg declared → eligible and fetched, rasterized PNG only`; `SVG declared/served size cap 2 MiB`; `SVG served type mismatch → chip`; `rasterize failure → chip without URL leak`; `release during rasterization → no URL`; `declared png but served text/html → chip`; `declared over 10 MiB → no fetch`; `origin_url only → no fetch`; `one fetch per sha256 across repeated state() calls`; `a fetch that settles after two state() calls ends ok and calls onReady once`; `release revokes every URL created`. `thread.test.mjs`: `replyKey keeps the key for unchanged text, prio, replyTo and sig; a changed sig mints a new one`.
- [ ] **Step 2–4:** red, implement, green, then `build2 node scripts/ui/att-thread.mjs`. Before opening the thread, the script uploads as one demo peer (raw `fetch`, Global Constraints) a PNG, an SVG, an `image/png`-typed `<html><script>` blob and a file named with U+202E inside 150 × `é`, posts them in one message with `asPeer`, then asserts: the PNG and SVG render inline, the SVG thumbnail and viewer use only PNG URLs, the fake PNG and named text are chips, no alert/dialog fires, no CSP violation, no console error. Also upload a benign viewBox-only SVG and verify nonzero size. It also drops a file just outside the composer and asserts the page did not navigate and the text is intact.
- **Shots:** `att04-thread-initial` (peer's PNG and two rasterized SVGs inline, fake PNG/text as chips); `att04-thread-progress` (reply chip uploading, held); `att04-thread-success` (operator reply with a pasted screenshot inline, full-size dialog open); `att04-thread-error` (a faked 429 `upload_quota_exceeded` with `retry_after_seconds` on the reply chip, retry time shown).
- [ ] **Step 5: Commit** `feat: reply attachments and inline images in the thread`

---

### Task 5: TUI `:attach` and `:detach`

**Depends:** 1

**Files:** Modify `internal/tui/screen_chat.go`, `internal/tui/screen_chat_test.go`, `internal/tui/tui.go` (the `API` interface), `internal/tui/fake_test.go` (`fakeAPI.Upload`); Create `scripts/ui/att-tui.mjs`.

**Interfaces:**
- `tui.API` gains `Upload(ctx context.Context, r io.Reader, size int64, mediaType, name string) (envelope.Attachment, error)`; `*opclient.Client` satisfies it (`fake_test.go` pins that). The TUI calls `m.API.Upload`.
- Register `attach` and `detach` in `chatScreen.register` (`screen_chat.go`, beside `msg`); help comes from `m.Commands`. `:attach PATH`: the path is `strings.Join(args, " ")` with a leading `~/` expanded; runs of whitespace inside a path are not kept, and the help text says so. It adds a chip to the open thread's draft and uploads at once. `:attach` alone lists the draft's attachments in a toast. `:detach N` removes the Nth (1-based) and cancels its upload; `:detach` removes all.
- Media type: copy `cli.mediaTypeOf`'s rule (`mime.TypeByExtension` without parameters, else `application/octet-stream`); `tui` cannot import `cli`. Never sniff content.
- `chatDraft` gains `atts []chatAtt` (`id`, name, size, `sha`, state, `err`, a cancel func, and an `atomic.Int64` of bytes read). `chatAttempt` gains `shas string` (sha256s joined with `,`), keeping the struct comparable. Upload completions carry `(thread, chip id)`, never an index. Progress is read from the counter on the existing `chatPoll` 250 ms tick.
- A chip row sits above the one-line composer, only when the draft has attachments, in ASCII so cell widths stay exact: `[+] name 1.2 MB`, `[..] name 40%`, `[x] name: reason`; it truncates with `+N more`, and the log area loses that row. Names pass through `Clean`.
- Send refuses with a toast while a chip is uploading or failed, and with the spill-slot message (Global Constraints). Empty text with attachments sends. `onSent` clears attachments only when `shas` is unchanged, as it already does for text.
- Refusals before upload (toast, nothing uploaded, draft intact): no open thread, missing file, a directory, over 25 MiB, a 17th attachment. Hub errors show on the chip: 413 `blob_too_large` with the limit, 429 `upload_quota_exceeded` with the retry time from `details.retry_after_seconds` (as `chatSendWhy` formats `rate_limited`).

- [ ] **Step 1: Failing tests:** `TestAttachMissingFile`, `TestAttachDirectory`, `TestAttachTooLarge` (sparse temp file over 25 MiB; the fake sees no upload), `TestAttachPathWithSpaceAndTilde`, `TestAttachRetryKeepsKey`, `TestDetachMintsNewKey`, `TestDetachByIDAfterShift` (detach 1 while 2 uploads; 2's completion lands on the right chip), `TestAttachmentOnlySend`, `TestSendRefusedWhileUploading`, `TestSendRefusedSpillSlot`, `TestAttachQuotaExceededShowsRetry`, `TestAttachTooLargeFromHub`.
- [ ] **Step 2–4:** red, implement, green, then `build2 node scripts/ui/att-tui.mjs`. The TUI harness has no per-route hold: for `att05-tui-progress`, write a ~20 MiB file under the demo's temp dir, `stall('60s')`, `:attach` it, wait for `[..]` with a percent under 100, shoot, then `clear('stall')`. Assert the marker, not an exact percent.
- **Shots:** `att05-tui-initial` (draft with one chip done); `att05-tui-progress` (as above); `att05-tui-success` (sent; the message's attachment row in the thread); `att05-tui-error` (`:attach` of a missing file, toast shown, draft intact).
- [ ] **Step 5: Commit** `feat: :attach and :detach in the TUI chat`

---

### Task 6: Journey, docs, gate

**Depends:** 3, 4, 5

**Files:** Create `scripts/ui/att-journey.mjs`; Modify `docs/OPERATOR.md` (an Attachments section: web picker, drop, paste, inline images, the side panel for downloads; TUI `:attach`/`:detach` and the whitespace note; limits including the spill slot; and a line recording the `img-src blob:` CSP change and why), `docs/plans/shots/oui/README.md` (one line per new PNG), `README.md` (one line), `.scratch/vecnet/issues/20-operator-attachments.md` (link the plan, mark done).

- [ ] **Step 1: Journey** (takes no shots): web and TUI on one demo hub (`withTui({ env: ctx })`, as `journey.mjs` does). Paste a screenshot into the web reply and send → the TUI thread shows the attachment row. `:attach` a PNG in the TUI and send → the web thread shows it inline without a reload.
- [ ] **Step 2: Gate:** `build2 go test -p 2 ./...`, `build2 node --test --test-concurrency=2 internal/ui/jstest/`, `build2 go vet ./...`, `gofmt -l .`, `CGO_ENABLED=0 build2 go build -p 2 -o bin/ ./cmd/...`, `just isa-check`, `just validate-protocol`, `SHOTS_DIR=.scratch/vecnet/build-attachments/gate-shots just ui-shots` (this runs the journey too). Expected: all pass; every PNG in the README exists and every PNG in the directory is listed.
- [ ] **Step 3: Commit** `docs: operator attachments guide and journey`

---

--- SUMMARY ---

- **What:** operator attachments end to end: web picker, drag-and-drop and clipboard paste in the new-message form and the thread reply box; inline image thumbnails with a full-size view in the web thread; `:attach`/`:detach` in the TUI chat. Downloads stay in the thread side panel, which already does them safely.
- **No hub feature work:** the hub already takes operator uploads and attachment-only sends and serves blobs sandboxed. Task 1 proves those with tests, opens `img-src` to `blob:`, and adds `opclient.Upload` (its own HTTP client, exact Content-Length).
- **Limits:** 16 attachments, or 15 when the text spills (the hub needs a slot for `message.md`); 25 MiB per blob; daily quota; every surface checks before uploading and handles the hub's 413 and 429.
- **Shape:** six tasks. 1 (Go, Sol) and 2 (web module, Opus) start at once; 3 (compose) after 2; 4 (thread, thumbnails) after 1 and 2; 5 (TUI) after 1; 6 (journey, docs, gate) last.
- **Safety:** thumbnails only for png/jpeg/gif/webp, checked by declared type and size before fetching and by served type and size after, at most 10 MiB; SVG up to 2 MiB is decoded off-DOM and converted to PNG before display, private SVG URLs revoked after drawing/failure and PNG URLs on unmount, hostile names cleaned and bidi-isolated, CSP changes only `img-src`, download blobs typed octet-stream, the bearer never in a URL.
- **Correctness:** the idempotency signature includes attachments on all three surfaces; TUI chips are keyed by id so `:detach` cannot misroute a completion; thumbnails survive the thread's full redraws.
- **Process:** reviewed against the code by an independent Opus pass (25 findings folded in); every merge runs the whole screen gate.
- **Open:** none.
