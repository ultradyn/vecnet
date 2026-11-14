# PR55 modern launcher integration

Feature checkpoint: `c407a3fed14b9ef3e4bd45c09821ea941979caad`, parent PR72 `b4bb120974d2333f700e189cbc4ec403d0c5abeb`. Original PR55 feature source: `a7b46a6c4c46c05c531cb348f0c8ac05ae2eae44`; common ancestor with current master: `5a0735a3c2616fe26186cb55e72262d55eef7047`. This port does not merge or publish original branch ancestry; MAIN owns that integration.

## Retained behavior

- New native Codex and ACP backend plugins; canonical permission identity, exact owned launch/recovery/settlement, native turn correlation, worktree/project-root/temp workspace policy and explicit scratch aliases. ACP advertises no process-restart resume.
- Profile-scoped CLI setup/check/status and immutable content-addressed install snapshots; absolute runtime paths, private credential separation and unsafe snapshot refusal. Embedded modules/schemas/service template support direct Go installs. Npm stages and inventories all four dynamic builtins.
- Scoped capability discovery, web host setup and Launch job form, exact submitted-request retry/reload recovery. Current dashboard statistics, memory/pump behavior and Dot copy feedback remain intact.
- Owned smoke recovery journals, unit/workspace/provenance checks and conservative authority retention. Live provider smoke was not executed by this worker.

Modern `connectors/launcher/src/codex/` is unchanged from PR72, as are hookstate/watch/manual inbox/optout paths. `startCodexConnector` remains in daemon startup and shutdown with current generation authority and exact-root recovery. Historical hub-scoped auto-binding, takeover and delivery service changes were not ported. The explicit runtime-generation checks remain separate from job-backend ownership.

## Validation so far

All checks ran through `BB_HOST=x-boss BB_JOBS=2 BB_LANE=merge55 just bb`; no bare local build/test ran. First runs canonicalized lane TMPDIR. Final package/browser gate uses a dedicated private physical `/tmp/vecnet-merge55-gate-*` instead, preserving installer ancestor checks.

- Native/ACP focused Node: **199 passed, 0 failed**; strict launcher TypeScript passed.
- Full `internal/localruntime`: passed (47.453s), including MAIN's profile-unit conflict fix. Its preceding dedicated RED failed both active/inactive profile cases with `unexpected launcher/legacy service query response`.
- Hub launch discovery/idempotency test passed.
- All UI JavaScript plus native smoke harness unit tests: **626 passed, 0 failed**.
- Corrected CLI launcher and HTTP/UI Go checks passed. Their earlier failures exposed fixture-only writable build-lane ancestry and cross-filesystem renames; fixtures now use private physical `/tmp` homes and same-filesystem symlink-tamper staging. Production ownership validation was not relaxed.
- Asset budget RED measured **708,152 bytes > 688,128 bytes** before changing the bound. The next 16 KiB boundary is **704 KiB**; the unchanged all-files assertion then passed.
- Embedded-assets regression first failed `open : no such file or directory`; the embedded fallback then passed, including native/ACP, modern ownership and profile service assets.

Per-suite logs and exit codes: local `.scratch/merge55/`, mirrored from `x-boss:/home/xertrov/bb-src/vecnet4/merge55/src/.scratch/merge55/`. The second gate recorded launcher=1/runtime=0/hub=0/httpui=1/ui_harness=0; it must not be described as an all-green run. The subsequent corrected Go checks passed. The final gate recorded `corrected_go=0 npm_build=0 npm_pack=0 npm_test=0 demo_build=0 browser=0` and exited normally with status 0 in 46.7s.

- Npm **0.2.2** build/pack passed for x64 and arm64; 67 launcher runtime files and all four builtins were staged. Installed-package checks: **4 passed, 0 failed**. They exercise actual installed CLI bootstrap outside a checkout, real module loading, setup/install/check, private snapshot trust and tampered packaged source refusal.
- Candidate tarball SHA-256: `b630e80ea5fbd2f939a5af47b28adc97f581fe7405868f861f62873f1ba8a3e0`. This is a local candidate, not a published release artifact.
- Browser journey passed host setup, disposable real enrollment/discovery, unavailable-host recovery, native default selection, mobile, held navigation/reload retry and resulting job/thread. Nine PNGs are under `.scratch/merge55/shots/`; desktop ready-form and mobile launch-form images were visually inspected with no blocking layout defect. The journey uses the real disposable demo hub; it does not create a real provider model session.
- `freeze.txt` binds feature checkpoint `c407a3fe` and MAIN's dirty runtime files: runtime.go SHA-256 `887a1a6acd4555c77c2c1de2d9f9972a09cf6a0f54599763a4845c0f59085ac3`; runtime_test.go `b0e73338c1dbc9edb71e899a668e4e4c526905b50bbc6cd52b59c86a3ca6bba9`. These dirty files were tested but are not part of the feature checkpoint.

## Integration and acceptance boundaries

MAIN's `internal/localruntime/runtime.go` and `runtime_test.go` changes are deliberately excluded from feature commits. Aquinas owns managed profile registration and repair in its isolated branch, including the installer registration seam. New managed installs must join automatic software maintenance; customized/legacy/unsafe publications remain pending. No completion claim for that helper is made here.

PR54 owns display-proof/optout substrate; PR57 owns consumed inbox/footer behavior. RPC acceptance is not display proof. The current engine's `InflightRow.key` and `deliveries` can support their exact-receipt settlement integration, but this port does not change it.

Remaining acceptance: integrate/review managed repair and current controller fixes; reconcile recovered xsm harness patches; run required combined-source gates after integration; disposable actual native/ACP model checks and exact cancellation/teardown; controller deployment/release and current artifact integrity. Historical PR55 model turns, Fly cleanup and tarball receipts are historical evidence only and do not validate this new source. No real service, credential, binding or account was changed here.
