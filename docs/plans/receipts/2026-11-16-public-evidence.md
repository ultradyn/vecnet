# Public evidence acceptance — 2026-11-16

[Design](../2026-11-16-public-evidence-design.md) · [Plan](../2026-11-16-public-evidence.md) · [Operator guide](../../EVIDENCE.md)

## Deployed service

- Dedicated R2 Standard/APAC bucket: `vecnet-evidence`.
- Public HTTPS origin: `https://vecnet-evidence.xk.io`; custom-domain ownership and SSL active, minimum TLS 1.2. Managed `r2.dev` access disabled.
- Lifecycle readback: `runs/` objects deleted after 2,592,000 seconds (30 days); incomplete multipart uploads aborted after 86,400 seconds. No deletion rule matches `kept/`. This verifies configuration, not elapsed 30-day deletion.
- Upload transport: Node 22.23.3 with existing Wrangler 4.151.1 OAuth on `xsm`. No credentials copied into the repository or newly created. Downloads require no authentication.
- All objects use `Cache-Control: public, max-age=300, no-transform` to preserve bytes through the CDN. Cloudflare analytics injection initially changed HTML bytes; the final header and publisher were verified live.

## Live acceptance

[Canary JSON receipt](2026-11-16-public-evidence-canary.json) records publisher source commit `9b9fbd304145f0fb05804992e99777b6f381fcbb`, two reviewed safe images, generated manifest and gallery. The gallery link in that receipt is public until expected expiry **2026-12-16T06:38:38.315Z** (approximate).

The publisher exited successfully on `xsm`. An independent anonymous HTTPS reader on `x-boss` checked all four objects: sizes and SHA-256 matched, with `no-transform` on every response. The nested filename with a space was fetched successfully. Browser visual rendering was not checked because browser inventory was unavailable. Historical evidence from this private Git repository was not bulk-published.

## Repository verification

Final source at `50717f47` passed **94 tests, zero failures, zero skips**, through the build box:

```sh
BB_JOBS=2 just bb node --test --test-concurrency=2 \
  scripts/evidence/publish.test.mjs scripts/evidence/check.test.mjs \
  scripts/ui/shot-paths.test.mjs scripts/ui/recipes.test.mjs \
  scripts/ui/lib.test.mjs scripts/ui/closer.test.mjs \
  scripts/ui/capture-generation.test.mjs scripts/bb/shots-handoff.test.mjs
```

`node scripts/evidence/check.mjs --base origin/master` passed. The existing build-box physical-path/framing harness also passed all 19 assertions through `BB_JOBS=2 just bb bash scripts/bb/remote.test.sh`. Both task reviews and their scoped re-reviews passed after fixes. The [whole-branch review](2026-11-16-public-evidence-review.md) found three Important issues: stale captures/handoff, intermediate-commit evidence, and a missing partial-upload regression pin. The [fix report](2026-11-16-public-evidence-fix.md) and [scoped re-review](2026-11-16-public-evidence-rereview.md) close all three. No publisher transport/config changes followed the live canary. See the [decision ledger](2026-11-16-public-evidence-ledger.md).

## Preserved evidence and separate cleanup

Private backup directory: `~/.local/share/vecnet-repo-cleanup/2026-11-16` (mode 0700).

- `current-evidence.tar`: 773 current evidence files, 122,601,161 media bytes; every archived file re-read and SHA-256 checked. Archive SHA-256: `fab84e74d7cec1f5b776f1cb124db0b341853732c33fb90b17a0b9d5f93d5d01`.
- `original.bundle`: 1,986,430,200 bytes, `git bundle verify` passed. SHA-256: `362d6c99ce510aed1b36b41b72267624510cd401194d72b2b522b0e1c811048c`.
- Preliminary independent bare candidate: evidence media removed only under `.scratch/`, `docs/plans/`, `docs/prototypes/`, `docs/design/`. Packs fell from 1.79 GiB to 9.63 MiB (10,266,232 bytes including indexes). All 119 snapshot refs verified, zero refs deleted, non-evidence tip trees byte-identical, `git fsck --full` passed.

That candidate predates final feature commits and is a measurement, not a push target. Refresh the bundle, candidate and live ref inventory after integration before proposing shared replacement. A history purge changes commit IDs, requires coordinated branch/tag replacement and fresh clones or rebases, and removes old relative screenshot targets. Private backups preserve those bytes. R2 publishing and the guard do not reduce existing clone size. No shared-history rewrite, force push or backup deletion is authorized by this rollout.
