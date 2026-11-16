# Public evidence storage

Approved direction: Cloudflare R2 public artifacts, 30-day routine retention,
explicitly kept artifacts, small receipts in Git. Approved by Max on 2026-11-16.

## Goal

Publish screenshots and clips outside Git with public URLs and bounded retention,
and make ordinary UI gates stop overwriting tracked evidence files.

## Interfaces

- Bucket `vecnet-evidence`, public domain `vecnet-evidence.xk.io`, standard storage.
- `runs/<full-source-commit>/<unique-run>/`: expire after 30 days.
- `kept/<full-source-commit>/<unique-run>/`: no automatic expiration.
- `node scripts/evidence/publish.mjs DIRECTORY [--commit SHA] [--keep]
  [--dry-run] [--receipt FILE] [--public-url HTTPS_URL]`.
- Wrangler is the upload transport. Existing operator OAuth or a separately
  supplied Cloudflare API credential authenticates uploads; public reads need none.
- A run contains media, `manifest.json` (relative names, keys, SHA-256, sizes,
  source commit, creation time and expected expiry), and an HTML gallery.
- Input media occupies `media/<original-relative-path>` inside the run; generated
  manifest/gallery occupy its root, avoiding collisions with input directory names.
- A small local receipt is emitted only after all published bytes are verified
  through the public HTTPS origin. The receipt names the gallery and manifest.
- Shared screenshot helpers and the index validator default to
  `.scratch/evidence/ui`; `SHOTS_DIR` continues to override both.
- `just evidence-publish DIRECTORY ...` invokes the publisher.
- `just check-evidence` rejects additions or modifications to evidence media in
  `.scratch/`, `docs/plans/`, `docs/prototypes/` and `docs/design/`.

## Constraints

- Node 22.23.3; no new repository SDK dependencies. Wrangler is an upload prerequisite.
- Tests run through `BB_JOBS=2 just bb`, with at most two concurrent workers.
- No secret values in argv, logs, receipts, manifests, test output or commits.
- No subagent reads credential stores or changes real Cloudflare resources.
- Publish only PNG, JPEG, GIF, WebP, MP4, WebM and MOV media; other ordinary files
  are ignored. Reject symlinks and unsupported special files before any upload.
- A unique timestamp plus cryptographically random suffix prevents run collisions.
- Media names are URL-encoded, HTML output escaped, and source SHA validated.
- `--dry-run` performs no uploads or external requests and needs no credentials.
- Limit upload concurrency to two. Check every public object's byte length and hash.
- Send `Cache-Control: public, max-age=300, no-transform` on every object; CDN
  transformations must not alter the bytes whose hashes appear in receipts.
- Never write a success receipt on an upload or verification failure.
- Routine expiry is expected, approximate and visible in the gallery/receipt.
- Keep application images and required test fixtures in Git.
- Preserve existing tracked evidence until an independently verified archive exists.
- Prepare history cleanup separately; no shared-history rewrite or force push here.

## Exit tests

- ET1: Dry-run a nested media directory: correct encoded keys, checksums and gallery,
  no network or uploader invocation; no input mutation.
- ET2: A real publish uploads selected media and its index/manifest, then public
  download verification succeeds before the receipt is written.
- ET3: Inject an upload failure or wrong public bytes: command fails, no success receipt.
- ET4: Routine runs report 30-day expiry; kept runs report no automatic expiry;
  the live lifecycle matches only `runs/` plus abandoned multipart cleanup.
- ET5: Screenshot output and index checks share the ignored default and still honor
  explicit SHOTS_DIR. The existing final screenshot completeness gate remains intact.
- ET6: Evidence-media additions/modifications fail the commit/CI guard; removals and
  application assets pass. Existing historical media does not block unrelated work.

## Decisions

| Option | Public downloads | Automatic expiry | Ordinary clones independent | Decision |
|---|---|---|---|---|
| R2 objects with Git receipts | Yes | Prefix lifecycle | Yes | Selected |
| Git LFS | Endpoint-dependent | Breaks historical hydration | Needs smudge policy | Rejected |
| NAS public-files tunnel | Yes | Owned cleanup job | Yes | Feasible alternative |
