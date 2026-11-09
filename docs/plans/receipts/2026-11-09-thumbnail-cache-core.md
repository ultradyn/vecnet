# Thumbnail cache core — 2026-11-09

Task2b1 of [the thumbnail memory plan](../2026-11-09-webui-thumbnail-memory.md) is a validated dependency. Supplying a shared budget opts into occurrence leases with acquire/drop/broken/release, bounded transport reservations, local payload ownership and retryable URL cleanup. Existing callers still use the unchanged legacy mode. Owner transfer/retry and demand-driven DOM/viewer integration remain pending.

The cache reserves compressed-byte, running, entry, queue and physical-source allowances before work. Retired requests retain their original tickets until actual settlement; late completions cannot affect a successor. Published cleanup detaches before revocation and releases accounting only after success. Bounded original-ticket cleanup obligations retain failed unpublished revocations for an explicit release retry, including allocation reentry. They contain no Blob, cache or observer backlink. Physical DOM detachment and native memory limits require the later browser integration.

| Parent-owned x-boss check | Result |
| --- | --- |
| Original core RED | 12 failures for missing lease behavior, 8 unchanged legacy passes |
| Cleanup RED | 7 unhandled-rejection regressions, 21 passes |
| Current focused core, cleanup and legacy | 28 passed, zero failed/skipped/cancelled; actual and build-box exit 0 |
| Full current UI suite | 542 passed, zero failed/skipped/cancelled; actual and build-box exit 0 |
| Current Go embedding/UI guard checks | 7 passed, zero failed/skipped; actual and build-box exit 0 |
| Fresh independent GPT-6.1-Sol full cache-core review | Spec PASS, quality PASS, C0/I0/M0 |
| Same reviewer's cap-only supplement | Spec PASS, quality PASS, C0/I0/M0 |

All gates ran with isolated homes and a physical owned temporary directory, one x-boss job each. Full source manifests were checked before and after execution; raw hashes and two empty owned-process drains per gate were independently verified. The focused/UI inputs differ from the final embedding inputs only by the separately reviewed Go asset-cap history/constant. No unchanged JavaScript tests were rerun for that metadata-only change.

The complete asset guard first failed honestly at 713,285 bytes against 688 KiB (704,512 bytes). R-M43 applies exactly the smallest 16 KiB step to 704 KiB (720,896 bytes), with a dated history entry; inventory, all-files summation and assertion stay intact. The prior failure and invalid runner setup attempts remain preserved separately.

Evidence SHA-256:

- cleanup-green: `6d1be6319f30f36eeee497779be9919f777b0e5818146c5888207fc3a615c6a9`.
- cleanup-ui: `3245267956f037896a8bfe217012d47660e16e186e0c755969548019bbf51077`.
- cleanup-go-ui2: `89683a9376196b429f59deee10c74e5677aa404944075c2762bda5f301861e15`.
- Cache model: `69502b753908c6311e06e335d1eb13746b928c96877f22cacdeee73ac7f68700`.
- Embedding guard: `cc1ed801b0826d2e50b68070fd3ee9a6c0aec430aba937f7bec283529c5bd3cf`.

This is source preparation on the isolated feature branch. No master merge, deployment, physical GC, decoded-image bound or resolution of the reported multi-GB incident is established. Move/retry, actual viewer access, visibility/focus preservation, combined browser measurements and final release gates remain required. Issue #60 remains open.
