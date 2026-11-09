# Thumbnail cleanup recovery owner — Task2b3

The opt-in cache now exports `retryThumbCleanup(budget)` so a document can retry failed cleanup of an unpublished URL after the original mount has retired. One plain resource pool per budget retains the original running ticket, four-field allocation cell, standalone revoke capability and scalar counters/guard. A failed revoke keeps the original full reservation charged; successful cleanup removes resource links before settlement can admit more work.

The pool retains no cache, entry, owner, lease, task, API/provider object, DOM, Blob, Response or Error. Each retry takes one bounded snapshot and attempts eligible failed resources once; allocating or currently disposing resources are skipped. Nested passes are inert. Partial progress survives unrelated failures, including falsy thrown values. Published images retain their physical detach/revoke cleanup path.

Parent checks on x-boss, using one job, private synthetic homes and a physical owned temporary directory:

| Gate | Result |
| --- | --- |
| Genuine missing-feature RED | Missing named export prevented the seven new roots from registering; 52 existing controls passed; actual/bb exit1 |
| Current focused cleanup/cache/budget/legacy | 59 passed; zero failed, skipped or cancelled; actual/bb exit0 |
| Current full UI | 555 passed; zero failed, skipped or cancelled; actual/bb exit0 |
| Relevant current Go embedding checks | 7 passed; zero failed or skipped; actual/bb exit0 |
| Fresh independent explicit GPT-6.1-Sol source review | Spec PASS, quality PASS, C0/I0/M0 |

All 2,162 source entries match before/after/current checks. Raw hashes and two empty owned-process drains per gate were independently checked. The source review traced original-ticket ownership, callable captures, resource bounds, counter/guard sharing, reentry, snapshot order and settlement scheduling. Production model changes are +80/-30; the 362-line new test preserves all prior tests.

Raw SHA-256:

- Genuine RED: `dd77e04fbd27ea899e77e15d2899eb90c2a59cc8ae8b8759a3d2fce1dd905fce`.
- Focused GREEN: `4677095d9e7409fb1a60ae7fe3721d4420da986ae623ae7409baef7a7b5ee74e`.
- Full UI: `6f0df91c521945019226e6e5d39b392be90a4ab7f2f7f8f3eae3e8f0c6c6c53c`.
- Go embedding: `15e7f358d2f24c669ae121e64047a4268b730092833445b9c5a3344f9f0c09cb`.

Reviewed source SHA-256: model `444f9a5f897b98c7cc0c26bc364ff0e77d3770f34e33d62f9f6bdf56db6e49e5`; new tests `5819d2a6008a3a982841eb103fbca90e6e3596e37cb2bd73cfdc3f60a5adf518`.

This completes only the cleanup-owner dependency. Task3 must wire explicit new-mount Retry, visibility/viewer/focus and real physical lifecycle transitions. Native resource collection, measured RAM reduction, an installed caller, master integration, deployment and closure of issue #60 remain unproven. The existing DOM caller remains on legacy mode.
