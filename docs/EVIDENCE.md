# Public evidence

Screenshots and clips can be published outside Git with a small integrity receipt.
Application images and required test fixtures still belong in Git. Existing tracked
evidence stays in place until an independently verified archive is available.

The publisher uses the dedicated **vecnet-evidence** R2 bucket in account
`375a8abfb0c42fd0940edbef36fcd1e9`, with public origin
`https://vecnet-evidence.xk.io`. These identifiers in
[`scripts/evidence/config.json`](../scripts/evidence/config.json) are public.
**Deployment verified on 2026-11-16.** The bucket and custom domain are active,
anonymous downloads passed all-object checksum checks, and the lifecycle was read
back from Cloudflare. See the [acceptance receipt](plans/receipts/2026-11-16-public-evidence.md).

## Installation and authentication

Use Node **22.23.3** and an existing operator installation of **Wrangler 4.151.1**:

```sh
npm install --global wrangler@4.151.1
wrangler --version
```

There are no repository SDK dependencies and no custom S3 signing code. Wrangler
owns authentication: use the operator's existing OAuth login, or supply a scoped
Cloudflare credential to Wrangler through its supported environment mechanism.
Do not put credentials in CLI arguments, the checked-in config, receipts or
manifests. No root Wrangler config needs provisioning. The publisher supplies the
public account ID through `CLOUDFLARE_ACCOUNT_ID` in the child environment.

The operator credential should be restricted to the dedicated evidence bucket.
Keep it separate from application attachment storage and other production buckets.
Public downloads use unauthenticated HTTPS GET requests. Dry-run and help need no
Wrangler installation, credentials or network access.

## Capture and inspect locally

Run these commands from the repository root. Capture to an ignored directory:

```sh
BB_JOBS=2 just bb SHOTS_DIR=.scratch/evidence/ui just ui-shots
node scripts/evidence/publish.mjs .scratch/evidence/ui --dry-run
```

The screenshot helpers and index validator default to `.scratch/evidence/ui`;
`SHOTS_DIR` overrides both.
The publisher stages each run under `.scratch/evidence-publish/` relative to the
working directory. Its printed run directory contains copied media under
`media/<original-relative-path>`, with `index.html` and `manifest.json` at the run
root. This keeps valid input directories such as `index.html/shot.png` and
`manifest.json/shot.png` separate from the generated files. Open `index.html` for
offline inspection. Relative gallery links point into `media/` and work both
locally and at the public origin. Labels retain the original input paths. Dry-run
emits no success receipt and never uploads or downloads anything.

Only PNG, JPG/JPEG, GIF, WebP, MP4, WebM and MOV are selected, case insensitively,
including nested files. Ordinary files such as notes are ignored. Symlinks anywhere
in the input tree (even ignored files), and non-regular special files, fail before
uploads. Inputs with no selected media fail. The source tree is read without
modification; generated output and receipts must be outside it.

## Publish and retain the receipt

```sh
node scripts/evidence/publish.mjs .scratch/evidence/ui \
  --commit "$(git rev-parse HEAD)" \
  --receipt .scratch/evidence-publish/ui-receipt.json
```

`just evidence-publish DIRECTORY ...` wraps this CLI. Omitting `--commit` uses `git rev-parse HEAD` in the current working
directory. An explicit commit must be a full 40-character hexadecimal source SHA.
It records provenance supplied by the caller; it does not prove that captures were
generated from a clean checkout. `--public-url HTTPS_URL` overrides the public
origin/base path for a separate deployment; credentials, query strings and fragments
(including empty `?` and `#` delimiters) are rejected before any upload or public
read. Percent-encoded path characters such as `%3F` and `%23` remain valid. This
option changes verification URLs, not the configured bucket.

Each run uses `runs/<source-commit>/<timestamp>-<random-suffix>/`. Names are
URL-encoded for public links and HTML-escaped in the gallery. The timestamp and
cryptographically random suffix avoid reuse of an existing run's keys. Public
media keys use `runs/<source-commit>/<run>/media/<original-relative-path>` (or the
corresponding `kept/` prefix); generated indexes occupy the run root.

The publisher uploads staged media first, then `manifest.json` and `index.html`,
with at most two concurrent uploads. Wrangler receives argv without a shell:

```text
wrangler r2 object put BUCKET/KEY --remote --file PATH --content-type TYPE --cache-control 'public, max-age=300, no-transform' --force
```

`--force` suppresses the Basin Catalog prompt; it is not overwrite protection.
Every uploaded artifact, including media, manifest and HTML, receives
`Cache-Control: public, max-age=300, no-transform`. The `no-transform` directive
requests byte preservation through the CDN, preventing transformations such as
analytics injection from invalidating recorded hashes. Public hash verification
remains mandatory; the [live canary receipt](plans/receipts/2026-11-16-public-evidence-canary.json)
records a successful check of every object through this domain. The five-minute cache policy is independent of object retention.
`--expires` is deliberately unused: it controls cache expiry, not object deletion.
Subprocess stdout/stderr is captured and suppressed on both success and failure, because
Wrangler diagnostics may contain credentials. Errors report a sanitized stage.

Before writing a success receipt, the publisher downloads **every** uploaded
object from the public HTTPS origin, including the manifest and HTML gallery, and
checks both byte length and SHA-256. Redirects and non-success HTTP responses fail
verification. A failed upload, unreadable public object or integrity mismatch
causes a nonzero exit without a success receipt. Generated local assets remain
available for inspection. Partial remote uploads may remain; routine ones are
covered by the lifecycle. Retry with a new run rather than treating partial output
as published evidence.

On success the receipt is printed and saved to `--receipt FILE`, or to
`receipt.json` inside the generated run directory by default. A receipt path must
be new; existing files are preserved. Copy only the small successful receipt into
a suitable document/PR, or commit it as JSON. Do not add generated media or the
staging directory to Git.

## Keep selected evidence and understand expiry

```sh
node scripts/evidence/publish.mjs .scratch/evidence/ui --keep \
  --receipt .scratch/evidence-publish/kept-ui-receipt.json
```

Routine runs record expected expiry 30 days after their creation timestamp.
Deletion is approximate and controlled by the bucket lifecycle, not by the CLI
or local clock. The gallery, manifest and receipt make expected expiry visible.
Links in historical receipts will stop working after deletion.

`--keep` selects `kept/<source-commit>/<timestamp>-<random-suffix>/` and records
`expiresAt: null`: no automatic expiry. Use it deliberately for evidence that
should remain available. A partially uploaded kept run also has no automatic
expiry and may need operator cleanup after inspection.

The deployed lifecycle expires only the **`runs/` prefix** after 30 days and
aborts abandoned multipart uploads after one day. No deletion rule matches
`kept/`. Preserve this prefix scope when changing the lifecycle; do not apply
whole-bucket expiration. The acceptance receipt records configuration readback;
actual deletion after 30 days has not yet been observed.

## Manifest and receipt interfaces

Both JSON formats have `version: 1`, `sourceCommit`, ISO-8601 `createdAt`,
`expiresAt` (ISO-8601 or `null`), and `kept` (boolean).

The manifest adds `prefix` and `files`. Each media entry has `name` (relative to
the input, unchanged), `key` (bucket object key with the per-run `media/` prefix),
`url` (including that namespace), `sha256` (lowercase hex), `size` (bytes), and
`contentType`. Index/manifest hashes cannot be embedded in their own
manifest; they are recorded in the receipt instead.

The receipt adds `galleryUrl`, `manifestUrl` and `objects`. Each object entry has
`key`, `url`, `sha256` and `size`, covering media **and both generated indexes**.
It contains public metadata only; it does not contain local paths or credentials.

For offline consumers, `publish.mjs` exports `publishEvidence(options)` with
`directory`, `commit`, `keep`, `dryRun`, `receipt`, `publicUrl`, `config` and
`outputRoot` options. Inject `clock() -> Date`, `upload(object)` and
`readPublic(url) -> Uint8Array` to avoid external services. Upload objects include
`accountId`, `bucket`, `key`, `path` and `contentType`. Results include
`runDirectory`, `manifest`, `galleryUrl`, `manifestUrl`, `receipt`, `receiptPath`;
the last two are `null` in dry-run. `createWranglerUploader` is the default
transport and accepts isolated executable/environment overrides for offline tests.

```sh
BB_JOBS=2 just bb node --test --test-concurrency=2 scripts/evidence/publish.test.mjs
```

## Separate history cleanup

Publishing new evidence does not remove old Git blobs. Preserve existing tracked
media and historical links during this rollout. Before proposing removals or a
history rewrite, export all tracked evidence to an ignored archive with a checksum
manifest, independently verify every file, inventory affected refs/links, and
prepare a candidate clone plus measured size results. Review that concrete plan
with the operator before replacing shared history. No history rewrite, force push
or archive deletion is part of this publisher task.
