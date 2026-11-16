Reviewer: Fresh final reviewer (inherited controller model; requested aliases unavailable (ledger R2)), read-only. Method: this template.

### Passes

1. **Ledger, plan, Focus, task reviews.** Read the design, implementation plan, complete ledger, Task 1 review and scoped re-review in this worktree, and Task 2 review and scoped re-review in `../evidence-guard/.superpowers/sdd/2026-11-16-public-evidence/`. Read all **2,186 package lines**, including commits, stat and full contextual diff, in consecutive ranges. Verified target HEAD `f8cedeac6f096cd8cc5a65645cde3c9d8692fe33` and base `85661eba18217f8bfd7c8f89692e4168db5fc3e3`. Reviewed standards and specification axes personally; no subagents.

   Controller rulings, judged by user effect:

   - **R1: sound within the recorded authorization.** Reversible dedicated-bucket setup and routine repository changes implement the approved direction; shared-history replacement remains separate.
   - **R2: acceptable limitation.** Inherited-model reviewers retain some shared-model risk, but fresh contexts and explicit alias unavailability are preferable to falsely claiming another model was used. This review does not independently re-establish historical tool availability.
   - **R3: sound.** Wrangler avoids a new signing/credential implementation; its installation prerequisite is documented and the final invocation was exercised by the supplied live canary.
   - **R4: sound.** Preserving historical evidence prevents premature data/link loss. The new-history loophole in I2 does not justify deleting grandfathered evidence.
   - **R5: sound.** Disjoint worktrees preserved ownership, and the combined gate covers integration. The actual shared seams still require the findings below.
   - **R6: sound and implemented.** `media/` separates valid input directory names from generated indexes without breaking the CLI contract.
   - **R7: sound and implemented.** All-artifact `no-transform` addresses the observed CDN transformation without weakening integrity checks. Fresh anonymous GETs here confirm its present effect.

   **No ruling is wrong by its demonstrated effect on the user.** There are no ledger lines containing `minor (deferred)` or `parked`. The original Task 1 Minor namespace collision was fixed rather than deferred; both original Important findings and R7 have matching code and regressions. There is therefore no parked/deferred queue to waive before merge.

2. **Seam code, read in full.** Read `scripts/evidence/publish.mjs`, `scripts/evidence/config.json`, `scripts/evidence/check.mjs`, `scripts/ui/lib.mjs`, `scripts/ui/shot-paths.mjs`, `scripts/ui/shots-index.mjs`, `justfile`, `.github/workflows/evidence.yml`, `.gitignore`, `.dockerignore`, `scripts/evidence/publish.test.mjs`, `scripts/evidence/check.test.mjs`, `scripts/ui/shot-paths.test.mjs`, and `scripts/ui/recipes.test.mjs`. Also read the complete `scripts/bb/run.sh` and `scripts/bb/remote.sh` to check the documented capture handoff. The CLI-only task dependency, disjoint source ownership, sibling capture/staging defaults, native Node dependencies, and preserved historical media premises still hold. ET1–ET6's literal implementation is present; I1 and I2 expose deficiencies in workflow/gate guarantees, and I3 exposes an explicit Focus pin gap.

3. **README and documentation claims versus code.** Read README, the evidence guide, changed Operator/Buildbox/Changelog claims, both plan documents, acceptance and canary receipts, and the research addition. Checked **32 claims/workflow promises: 30 supported within the stated evidence boundaries; 2 materially unsafe as workflows**. Rows 30 and 32 are the two failures, not additional findings beyond I1/I2. Historical NAS/pricing statements are not counted as current operational claims checked here.

   | # | Claim or workflow promise checked | Result / evidence |
   |---|---|---|
   | 1 | Public account, bucket, URL and 30-day defaults match config | Supported: config and publisher validation |
   | 2 | Dedicated public service serves the accepted canary anonymously | Supported: four fresh GETs below |
   | 3 | No repository SDK or custom S3 signer is needed | Supported: complete publisher imports and transport |
   | 4 | Wrangler owns authentication; account ID goes through the child environment | Supported: `publish.mjs:98` |
   | 5 | Help and dry-run need no Wrangler credentials/network | Supported: CLI branch and offline tests |
   | 6 | Capture and index default to the same ignored directory | Supported: shared helper and path tests |
   | 7 | Relative SHOTS_DIR resolves from repository root | Supported: helper and subprocess fixture |
   | 8 | Nested selected media types are case insensitive | Supported: scan and selection tests |
   | 9 | Ordinary unsupported files are ignored | Supported: scan and selection tests |
   | 10 | Static symlinks and special inputs fail before upload | Supported: whole-tree scan and rejection tests |
   | 11 | Empty selected input fails | Supported: preflight and empty-input test |
   | 12 | Staging and receipts stay outside source; existing receipts survive | Supported: canonical-target checks and exclusive writes |
   | 13 | Generated indexes and input directory names cannot collide | Supported: media namespace regression |
   | 14 | Gallery references use encoded relative media paths | Supported by generated HTML/source; browser boundary below |
   | 15 | Default provenance is HEAD; explicit provenance is a full SHA, caller supplied | Supported: CLI, validator and guide qualification |
   | 16 | Public URL override changes URLs, not bucket | Supported: config validation and object construction |
   | 17 | HTTP, credentials, query and fragment delimiters are rejected | Supported: origin tests including bare delimiters |
   | 18 | Encoded query/fragment characters in base paths remain valid | Supported: positive base-path regression |
   | 19 | Timestamp plus random suffix separates runs | Supported: 12 random bytes and uniqueness test |
   | 20 | Labels are escaped and public media names encoded | Supported: hostile-name test and gallery code |
   | 21 | Media precedes indexes; upload concurrency is at most two per invocation | Supported: batch calls and concurrency test |
   | 22 | Subprocess diagnostics are suppressed | Supported: transport and CLI sentinel tests |
   | 23 | Every artifact requests no-transform; cache expiry is not deletion | Supported: argv tests and four fresh response headers |
   | 24 | Every public object's size/hash is checked before receipt creation | Supported: verifier, corruption tests and live GETs |
   | 25 | Receipt defaults to local run output and must not overwrite an existing file | Supported: exclusive write and documented copy-out workflow |
   | 26 | Manifest/receipt schema covers media and generated index hashes appropriately | Supported: serialization, receipt mapping and canary |
   | 27 | Routine/kept metadata distinguishes approximate expiry from no automatic expiry | Supported: ET4; provider readback supplied, future deletion not observed |
   | 28 | Staged guard and base-to-HEAD guard inspect their documented endpoints | Supported: real-Git fixtures |
   | 29 | Net evidence additions/modifications are blocked; deletions/assets/receipts pass | Supported: guard fixtures and destination-based rename handling |
   | 30 | Reusing the documented capture directory gives current captures suitable for publication | **Unsafe: I1.** Local gate accepts old generated output; build-box pullback merges stale files |
   | 31 | CI uses read-only full history and event-specific base SHAs; Git/Docker exclude generated evidence | Supported: workflow, shell fixture and ignore rules |
   | 32 | Guard prevents fresh evidence blobs entering history (`plan:8`) | **False: I2.** Added-then-deleted media remains reachable while CI passes |

### Verification run here

- **Did not rerun the suite.** Read `baseline.log` (original recipe tests 2/2), task review/re-review evidence, and `gate-combined.log`: **78 passed, 0 failed, 0 skipped** at `52570e00`. Existing two recipe tests and their assertions remain; added tests increase counts. The source/config gate inputs are unchanged since that gate. `git diff --exit-code 9b9fbd30 f8cedeac -- scripts/evidence/publish.mjs scripts/evidence/config.json` also confirms the live-tested publisher/config match this target. Those suite results are controller evidence, not a fresh suite claim.
- `git diff --check 85661eba f8cedeac` returned exit 0. Worktree status was clean before this report. No source, index, commit, service, credential store or cloud configuration was changed.
- Two bounded focused sessions used **`BB_JOBS=2 just bb ... node --input-type=module -e ...`**, `UV_THREADPOOL_SIZE=2`, sequential probes, synthetic temporary fixtures and no real uploads. Fixtures were removed. No test workers or subagents were launched; no retained probe file was added.
- **Stale gate/publisher probe:** ran the actual `ui-shots` recipe body with successful screen stubs and the real index validator in an isolated fixture. One old ignored `expected.png` existed; the run wrote **zero screenshots**, yet exited 0 and printed `ok - screenshot index lists all 1 generated PNGs exactly once`. Publishing that directory through injected upload/read seams wrote a success receipt under a new supplied source SHA. This establishes byte verification, not generation freshness; it reproduces I1 without running the long UI suite.
- **Pullback probe:** the exact local copy flags used by the wrapper, `rsync -rlt`, copied a fresh `current.png` onto a directory containing old `obsolete.png`; both remained. This is a local synthetic reproduction of the merge semantics, not a simulated network failure.
- **History probe:** real temporary Git repository, empty base commit, commit adding `docs/plans/temporary.PNG`, commit deleting it. Actual guard CLI with `--base BASE` exited **0**, while `git rev-list --objects BASE..HEAD` still named the newly reachable evidence blob. This reproduces I2 under the same endpoint comparison CI uses.
- **Partial upload probe:** `a.png` completed, `b.png` failed while `c.png` was held in flight. Publisher waited for `c.png`, returned the sanitized upload failure, started no indexes/reads, and wrote **no receipt**. This supports current runtime correctness but is not a retained regression pin for I3.
- **Fresh anonymous deployed GETs through the build box:** all four receipt URLs returned HTTP **200**, matching SHA-256 and exact sizes **33,781 / 6,839 / 1,281 / 1,468 bytes** for login PNG, nested-space PNG, manifest and gallery respectively. Every response carried **`public, max-age=300, no-transform`**. Only public HTTPS GETs were used, without credentials, cookies, redirects or mutations. This agrees with `independent-public-read-final.json` and the deployed acceptance receipt.

Behaviours set aside for controller ruling, listed before the verdict:

- Current provider-side upload authorization scope, bucket isolation settings, lifecycle configuration and disabled r2.dev state: accepted supplied readback; anonymous GETs cannot independently establish these controls, and credential/cloud-admin inspection was prohibited.
- Actual deletion after 30 days and indefinite retention of kept objects: future elapsed-time behaviour cannot be observed now; metadata and supplied prefix-policy readback were assessed.
- Browser rendering, offline file-origin CSP behaviour and video codec playback: source/URL/byte checks were performed; no browser rendering/playback probe was performed, and the acceptance receipt explicitly leaves visual rendering unchecked.
- Hostile concurrent replacement of ancestor directories after scanning: static symlinks were assessed; no lower-privilege concurrent filesystem writer boundary is established for this operator CLI. Same-user adversarial mutation requires an explicit threat-model ruling rather than an inferred guarantee from O_NOFOLLOW on leaf media.
- Current NAS/Forgejo/LFS operation and external pricing changes in the historical research: these are alternatives and historical observations, outside the selected R2 implementation; no alternate deployment was audited.

### Verdict

Ready to merge after the **3 Important findings** are fixed or ruled.

Core publishing, public integrity checks, error redaction, media namespacing, static hostile-input rejection, scoped ignores and literal staged/base guard behaviour hold. The live canary remains byte-correct. What fails is current-capture assurance in the combined documented workflow, prevention of evidence retained only in intermediate commits, and the durable partial-upload Focus pin. Counts: **0 Critical / 3 Important / 0 Minor**.

### Review Focus

- **Malformed source SHA or a symlink in the source tree fails before any upload — pinned:** `scripts/evidence/publish.test.mjs:193` covers malformed SHAs and root/file/directory/ignored symlinks, requiring validation-specific rejection and absent receipt; special files additionally at `:214`. Concurrent hostile ancestor mutation is explicitly declined above.
- **Hostile media names produce safe HTML and correct public URLs — pinned:** `scripts/evidence/publish.test.mjs:222` checks encoded public URLs and escaped hostile labels; base-path regression at `:314`, namespace integration at `:101`. This is a source/output pin, not browser rendering proof.
- **Partial upload/public corruption never yields a success receipt — half pinned:** public media/manifest/gallery corruption, wrong length and read failure are pinned at `scripts/evidence/publish.test.mjs:149`; its upload case at `:155` makes every upload fail, with no successful partial upload. Failure after some uploads succeed, including an index failure after media and a failing batch with another upload pending, is **unpinned** in retained tests: **I3**. The focused successful rejection here does not replace a regression test.
- **The output directory validator checks generated screenshots, not old committed files — pinned:** `scripts/ui/shot-paths.test.mjs:32` distinguishes ignored output from unchanged historical files and tests extra/missing output; `scripts/ui/recipes.test.mjs:87` checks final recipe placement. I1 is the separate, untested distinction between current-run and previous generated output.
- **Existing media/deletions permit unrelated changes, but new/changed evidence is rejected — half pinned at the history boundary:** existing/deleted evidence and net additions/modifications are pinned at `scripts/evidence/check.test.mjs:42`, with every root/extension at `:64` and renames at `:99`. New evidence introduced and removed/reverted before HEAD is **unpinned and accepted**: **I2**. The staged and endpoint halves themselves have real-Git coverage.

### Findings

**Critical:** None.

**Important:**

**I1 — Reused capture directories allow a green gate and public receipt for stale evidence.** `justfile:100`; `scripts/ui/shots-index.mjs:13`; documented entry point `docs/EVIDENCE.md:41`; handoff `scripts/bb/run.sh:137`.

**Scenario:** A previous run populated ignored `.scratch/evidence/ui`. A later screen check succeeds but no longer emits one expected shot, or a build-box run removes/renames a shot while the local destination still contains the old file. The local recipe neither clears nor records the current generation; the index checks names already present. The build-box branch validates fresh remote output, then merges it into the reused local destination using rsync without deletion. A transfer failure is also merely logged by `run.sh:139`, leaving the remote command's successful status intact. The guide then publishes that local directory under the current supplied/default SHA.

**Effect:** A missing current capture can pass locally, and stale or unrelated local media can be included in an otherwise correctly verified public run. The public byte hashes do not establish that the bytes came from the just-completed gate. This defeats the user-facing purpose of evidence even though the guide correctly disclaims proving a clean checkout. Both stale-gate acceptance and successful publication were reproduced; retained-old-file pullback semantics were separately reproduced. This is a cross-task integration defect, including pre-existing wrapper behaviour newly relied on by the publication workflow.

**Fix:** Make a capture generation explicit: run into a fresh run-specific directory, validate it, and hand that exact directory to the publisher. For stable defaults, use a lock and safely replace/promote complete generated output; do not broadly delete arbitrary SHOTS_DIR contents. Pull back to a fresh destination and propagate transfer failure before offering it for publication. Add a fixture prepopulated with old captures where current generation omits one, plus a two-generation build-box handoff fixture proving obsolete local files cannot enter publication. Preserve committed historical media.

**I2 — CI accepts new evidence blobs hidden by a later deletion or revert, preserving clone bloat.** `scripts/evidence/check.mjs:21`; `.github/workflows/evidence.yml:37`; contradicted architectural promise `docs/plans/2026-11-16-public-evidence.md:8`.

**Scenario:** A contributor accidentally commits a screenshot, notices the guard policy, and deletes it in a follow-up commit. The PR endpoint tree has no screenshot. The workflow calls `check.mjs --base BASE`, which compares only the base and HEAD trees. The same bypass occurs when a modified historical screenshot is restored before HEAD. Normal merge/rebase integration retains the intervening blobs.

**Effect:** CI reports success while fresh binary evidence enters shared history and remains in ordinary clones. The temporary-repository probe confirmed guard exit 0 with a newly reachable evidence blob. This matches the plan's literal endpoint algorithm but violates its stated history-prevention goal and a reasonable contributor's expectation of the CI protection. Squash-only integration would mitigate this particular trigger, but is not established or enforced by this branch.

**Fix:** Retain staged mode, and add a CI/history mode that examines evidence additions/modifications across newly introduced commits/objects relative to the event base, including merge history, rather than only net tree changes. Preserve unrelated grandfathered evidence and legitimate deletions. Add real-Git tests for add-then-delete and modify-then-restore branches, as well as ordinary grandfathered/deletion-only branches. Alternatively, the controller must explicitly rule that only endpoint protection is promised and establish an integration policy that prevents transient evidence commits reaching shared history; simply narrowing wording does not remove retained blobs under normal merges.

**I3 — Partial-upload failure has no retained regression pin despite explicit Review Focus.** `scripts/evidence/publish.test.mjs:149`, especially `:155`; protected implementation `scripts/evidence/publish.mjs:120`.

**Scenario:** The only upload-failure branch rejects every upload immediately. No test first accepts an object and then fails a later media/index upload, nor holds an upload pending when its sibling fails. Successful-batch concurrency coverage at `publish.test.mjs:259` does not exercise these transitions.

**Effect:** The promised partial-upload half of the Focus line is not pinned. Future changes could write a receipt after partial completion or return while an upload is still running without breaking the named test. Current implementation behaved correctly in the focused partial/in-flight probe; this is a required regression-coverage finding, not a claim that today's uploadBatch already emits a false receipt. Important severity follows the review's explicit unpinned-half rule.

**Fix:** Retain deterministic injected-transport tests that complete at least one upload before failing another, fail an index after media succeeds, and fail a batch while its sibling is pending. Assert no receipt/public verification, no inappropriate subsequent uploads, sanitized error, and settlement only after the in-flight upload ends. Keep public corruption cases intact.

**Minor:** None. No stylistic or speculative smell was promoted to a defect.

### Seams checked and found sound

- Canonical output/receipt containment, exclusive local writes, whole-tree preflight, leaf O_NOFOLLOW and descriptor checks, and staging all selected bytes before real uploads preserve static input safety. Ordinary capture changes after staging cannot change the bytes uploaded for that run.
- Publisher media/index namespaces align across local paths, keys, manifest, encoded URLs and gallery links. Quotes, markup delimiters, percent/query/fragment characters and reserved input directory names have direct regressions. Gallery CSP and HTML escaping do not introduce script execution in the inspected output.
- Successful media batch precedes indexes; both workers settle on failure. Every media/index length/hash check precedes receipt creation. Crashes before verification leave no success receipt; a crash during receipt write can leave invalid JSON, not a valid pre-verification receipt. A fully written receipt before stdout is safe because verification has completed.
- Random per-run directories/keys avoid shared publisher-instance collisions; `wx` preserves an existing custom receipt even if two invocations race. Retry intentionally creates a new run, not exactly-once remote publication. Partial routine and kept leftovers are documented, including kept cleanup responsibility. Uploads have a 300-second subprocess timeout, public reads a 30-second abort; no unbounded retry loop exists.
- The wrapper passes publisher arguments literally. Capture and publisher defaults are siblings, satisfying containment checks. The guard uses NUL paths and quoted diagnostics, rejects invalid bases, checks renamed destinations, preserves whitespace in repository roots, and fails closed on Git errors. The workflow has read-only permissions, no persisted checkout credentials, no secret interpolation and no privileged PR-target execution. Its remaining history limitation is I2.
- Public GET is intentionally anonymous; no application authentication route, attachment-storage contract, database schema, migration or running hub implementation changed. No hub restart is required to activate these scripts. The canary-tested publisher/config are byte-identical to the target; this reviewer host lacks Wrangler, so no local upload-installation claim is made.
- Branch changes neither delete historical media nor rewrite shared refs. Preservation and candidate-clone claims are supported by supplied acceptance/ledger evidence; the large private archive/bundle were not independently rehashed here. Their recorded candidate is explicitly preliminary, not an approved replacement target.

### Declined to judge

The complete five-item list appears before the verdict for controller ruling: current provider authorization/configuration controls; future elapsed expiry/indefinite retention; browser/offline-CSP/codec rendering; hostile concurrent ancestor replacement without an established writer boundary; and current alternate NAS/LFS/pricing state. Reasons are stated individually there. None is silently treated as verified, and none excuses I1–I3.

### Recommendations

- Fix or explicitly rule **I1–I3** before merge. The partial-failure probe already supplies a deterministic regression scenario; retain it in the publisher tests.
- Re-run only affected focused tests through `BB_JOBS=2 just bb` with at most two workers, then the required combined gate after fixes. A fresh cloud upload is needed only if the upload transport/public-object implementation changes; capture/guard regression fixes alone do not require live writes.
- Keep the existing private archive and deferred history-replacement approval boundary. Do not resolve the history guard issue by deleting grandfathered evidence or rewriting shared history in this rollout.

### Assessment

**Ready to merge? With fixes.** The publisher's core integrity path and current public deployment check out, but stale capture handoff and intermediate-commit evidence bypass undermine the workflow's purpose. Fix or rule those two Important defects and retain the missing partial-upload Focus regression before merge.

=== DONE ===
