# Public evidence implementation plan

> Execute with the per-task implementation/review/fix loop. No shared-history rewrite.

**Goal:** Public, expiring screenshots and clips outside Git.
**Architecture:** A dependency-free Node publisher drives Wrangler; a separate R2
bucket serves media and a generated gallery. Screenshot defaults become ignored,
and a Git-change guard prevents fresh evidence blobs entering history.
**Tech stack:** Node 22.23.3, Wrangler (existing operator installation), Cloudflare R2.
**Spec:** [Public evidence design](2026-11-16-public-evidence-design.md).
**Exit tests owned:** ET1–ET6.

## Global Constraints

- Node 22.23.3; no new repository SDK dependencies. Wrangler is an upload prerequisite.
- Tests run through `BB_JOBS=2 just bb`, with at most two concurrent workers.
- No secret values in argv, logs, receipts, manifests, test output or commits.
- No subagent reads credential stores or changes real Cloudflare resources.
- Preserve existing tracked evidence until an independently verified archive exists.
- Prepare history cleanup separately; no shared-history rewrite or force push here.

## Review Focus

- Malformed source SHA or a symlink in the source tree fails before any upload.
  Pinned in Task 1 (`rejects invalid source commits and symlink inputs before upload`).
- Hostile media names produce safe HTML and correct public URLs.
  Pinned in Task 1 (`encodes media URLs and escapes gallery labels`).
- Partial upload/public corruption never yields a success receipt.
  Pinned in Task 1 (`failed uploads and corrupt public bytes do not write success receipts`).
- The output directory validator checks generated screenshots, not old committed files.
  Pinned in Task 2 (`default screenshot paths and index checks use ignored output`).
- Existing media and deletions do not prevent unrelated changes, but new or changed
  evidence is rejected. Pinned in Task 2 (`evidence guard handles additions changes deletions and application assets`).

### Task 1: Publisher and operator guide

**Files:** Create `scripts/evidence/publish.mjs`, `scripts/evidence/publish.test.mjs`,
`scripts/evidence/config.json`, `docs/EVIDENCE.md`. No other code files.
**Produces:** CLI and manifest/receipt interfaces exactly as the spec; importable
functions for offline tests, with injected clock/upload/public-read seams.
**Consumes:** Nothing from Task 2. Runtime defaults `.scratch/evidence-publish/` for
generated local output. Config keys `accountId`, `bucket`, `publicUrl`, `retentionDays`.

- [ ] Write tests for ET1–ET4 and Review Focus, plus nested media and ignored ordinary
  files, empty inputs, 30-day versus kept expiry, at most two uploads, and subprocess
  failure output containing a sentinel credential. See expected no-success behavior.
- [ ] Run `BB_JOBS=2 just bb node --test --test-concurrency=2 scripts/evidence/publish.test.mjs`.
  Expect missing publisher export/module (RED); preserve evidence.
- [ ] Implement the CLI with native Node APIs and `execFile`/spawn argv for Wrangler
  `r2 object put --remote`. Capture subprocess output, report sanitized status only.
  No custom S3 signer or cloud credential loader. Upload only selected media, then
  manifest/gallery; verify public byte hashes before emitting a receipt. Generated
  assets should be available for dry-run inspection in an ignored run directory.
- [ ] Config: account `375a8abfb0c42fd0940edbef36fcd1e9`, bucket `vecnet-evidence`,
  public URL `https://vecnet-evidence.xk.io`, retentionDays 30. These are public IDs.
- [ ] Document local capture, dry-run, publish, kept evidence, expiry, installation,
  bucket isolation, credential handling and the separate history-cleanup procedure.
  Mark cloud resources as pending until controller supplies verified deployment.
- [ ] Run the complete evidence test file; commit explicit files as
  `feat(evidence): publish public R2 artifacts with retention receipts`.

### Task 2: Screenshot defaults and evidence guard

**Files:** Modify `scripts/ui/lib.mjs:156-161`, `scripts/ui/shots-index.mjs`,
`scripts/ui/recipes.test.mjs`, `.gitignore`, `.dockerignore`, `justfile`,
`README.md`, `docs/OPERATOR.md`, `docs/BUILDBOX.md`, `CHANGELOG.md`.
Create `scripts/evidence/check.mjs`, `scripts/evidence/check.test.mjs`,
`scripts/ui/shot-paths.mjs`, `scripts/ui/shot-paths.test.mjs`,
`.github/workflows/evidence.yml`. Do not modify Task 1 files.
**Consumes:** Task 1 CLI. **Produces:** `just evidence-publish DIRECTORY ...`,
`just check-evidence`, guard CLI `node scripts/evidence/check.mjs [--base REF]`.

- [ ] Test ET5–ET6 RED first. Use temporary Git repositories with real commits for
  the guard. Default guard inspects staged changes; `--base REF` compares REF to HEAD.
  Detect media (png,jpg,jpeg,gif,webp,mp4,webm,mov,avi,mkv) case-insensitively inside
  `.scratch/`, `docs/plans/`, `docs/prototypes/`, `docs/design/`. Handle renamed files
  using the new path; allow deletions and application/static assets.
- [ ] Add a small pure path module exporting `shotsDirectory(env = process.env)`
  resolved against repository root, default `.scratch/evidence/ui`. Share it between
  lib.mjs and shots-index.mjs. Preserve SHOTS_DIR and the exact PNG-name index contract.
- [ ] Keep the final index-completeness checks; add ignored default output tests
  and preserve the current recipe tests without relaxing screenshot assertions.
- [ ] Ignore generated media under the evidence paths and `.scratch/evidence*/`;
  keep all currently tracked files. Exclude ignored evidence output from Docker.
- [ ] Add a read-only PR/push workflow using checkout v5 (full history), Node 22,
  and `check.mjs --base` against the correct event base. Initial-branch pushes with
  an all-zero before SHA need a deliberate empty-tree base; no credentialed PR code.
- [ ] Recipes: `evidence-publish directory *ARGS` calls the Node CLI; `check-evidence`
  invokes the staged guard. Document ignored captures/public receipts. Update existing
  instructions that imply routine committed screenshot regeneration.
- [ ] Gate: `BB_JOBS=2 just bb node --test --test-concurrency=2 scripts/evidence/publish.test.mjs scripts/evidence/check.test.mjs scripts/ui/shot-paths.test.mjs scripts/ui/recipes.test.mjs`.
- [ ] Commit explicit paths as `fix(evidence): keep generated captures out of Git`.

### Controller live steps and closure

- [ ] Verify existing Cloudflare OAuth permissions on xsm without exposing tokens.
- [ ] Create/verify the dedicated bucket and runs-only 30-day lifecycle; verify kept
  prefix exclusion. Set custom domain and read back HTTPS/public access configuration.
- [ ] Publish a synthetic canary and verify public downloads, gallery and manifest.
- [ ] Export current tracked evidence to an ignored archive with a checksum manifest;
  verify every archived file before any future Git removal or rewrite.
- [ ] One final independent whole-branch review and one fix/re-review pass if needed.
- [ ] Document verified resources and exact commands. Prepare history-cleanup scope
  and measured result separately; seek shared-history replacement approval only when
  the backup, candidate clone and ref inventory are concrete and reviewable.

## Self-review notes

ET1–ET4 belong to Task 1; ET5–ET6 to Task 2; actual cloud configuration/public read
is controller-owned. Tasks share no source files; Task 2 consumes only the CLI.
No product behavior or attachment storage changes. No history loss in this plan.

--- SUMMARY ---
Two sequential implementation/review gates, controller-owned R2 deployment,
then one whole-branch review and preserved evidence archive.
